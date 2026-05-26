import { useCallback, useEffect, useState } from 'react'
import { api, type GrantRefInput } from '../api'
import type { Example, Execution, ScriptDetail, Script, ToolConfig, Trigger } from '../types'
import { CRON_PRESETS } from '../types'
import { StatusBadge } from '../components/Badge'
import { Json } from '../components/Json'
import { CodeEditor } from '../components/CodeEditor'
import { PromptModal } from '../components/Modal'
import { UIPanel } from '../components/UIPanel'
import { AgentPanel } from '../components/AgentPanel'
import { RunDetail } from './Runs'
import { EvalsPanel } from './Evals'

// The seeded coding-assistant harness backs the in-editor agent panel; hide it
// from the script list so it reads as a feature, not user content.
const HARNESS_SCRIPT = 'sc_coding_assistant'

type Sub = 'editor' | 'runs' | 'evals' | 'triggers' | 'secrets'

export default function Scripts({ onOpenRun }: { onOpenRun: (id: string) => void }) {
  const [scripts, setScripts] = useState<Script[]>([])
  const [sel, setSel] = useState<string | null>(null)
  const [detail, setDetail] = useState<ScriptDetail | null>(null)
  const [configs, setConfigs] = useState<ToolConfig[]>([])
  const [names, setNames] = useState<string[]>([])
  const [source, setSource] = useState('')
  const [grants, setGrants] = useState<string[]>([])
  const [input, setInput] = useState('{\n  "name": "kyle"\n}')
  const [result, setResult] = useState<Execution | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [sub, setSub] = useState<Sub>('editor')
  const [limit, setLimit] = useState(20)
  const [picking, setPicking] = useState(false)
  const [uiExec, setUiExec] = useState<string | null>(null)
  const [assistant, setAssistant] = useState(true)
  // Docked-assistant width, drag-resized (see startResize) and persisted.
  const [agentWidth, setAgentWidth] = useState(() => {
    const v = Number(localStorage.getItem('agentWidth'))
    return v >= 300 && v <= 820 ? v : 420
  })
  const [resizing, setResizing] = useState(false)
  // A prompt to hand the assistant as the first message of a freshly created
  // script (set by "describe what to build" on the new-script screen).
  const [seedPrompt, setSeedPrompt] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setScripts((await api.listScripts(limit, 0)) || [])
    setConfigs((await api.listConfigs()) || [])
    setNames((await api.capabilities()).map((c) => c.name))
  }, [limit])
  useEffect(() => { refresh() }, [refresh])

  // A UI run starts out suspended (parked on recv/form). Poll while it's
  // non-terminal so the Result card reflects completion + output once the user
  // finishes the chat/form, instead of being stuck showing "suspended".
  useEffect(() => {
    if (!result || (result.status !== 0 && result.status !== 3)) return
    const id = result.id
    const t = setInterval(async () => {
      try {
        const e = await api.getExecution(id)
        setResult((cur) => (cur && cur.id === e.id ? e : cur))
      } catch { /* transient */ }
    }, 1000)
    return () => clearInterval(t)
  }, [result?.id, result?.status])

  const select = async (id: string) => {
    setSel(id); setResult(null); setErr(''); setSub('editor'); setPicking(false); setSeedPrompt(null)
    const d = await api.getScript(id)
    setDetail(d)
    const latest = d.versions?.[0]
    setSource(latest ? latest.source : '')
    setGrants(latest ? (latest.grants || []).map((g) => g.config_id) : [])
  }

  const createFrom = async (name: string, src: string) => {
    const sc = await api.createScript(name, src)
    setPicking(false)
    await refresh(); select(sc.id)
  }

  // Create a blank script from a free-text prompt, then open the assistant and
  // hand it the prompt as the first message so it writes the script for you.
  const createFromPrompt = async (prompt: string) => {
    const name = prompt.replace(/\s+/g, ' ').trim().slice(0, 40) || 'New script'
    const sc = await api.createScript(name, 'def main(input):\n    return {}\n')
    setPicking(false)
    await refresh()
    await select(sc.id)
    setAssistant(true)
    setSeedPrompt(prompt.trim())
  }

  const grantRefs = (): GrantRefInput[] => grants
    .map((cid) => configs.find((c) => c.id === cid))
    .filter((c): c is ToolConfig => !!c)
    .map((c) => ({ capability: c.capability, config_id: c.id }))

  const save = async () => {
    if (!sel) return
    setBusy(true); setErr('')
    try {
      await api.saveVersion(sel, source, grantRefs())
      await select(sel)
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }

  const run = async () => {
    if (!sel) return
    setBusy(true); setErr(''); setResult(null)
    let parsed: unknown
    try { parsed = input.trim() ? JSON.parse(input) : null }
    catch (e) { setErr('input is not valid JSON: ' + (e as Error).message); setBusy(false); return }
    try {
      // Running implies a save: persist a new version iff the editor differs from
      // the latest saved one, then run (so you never accidentally run stale code).
      const latest = detail?.versions?.[0]
      if (!latest || latest.source !== source) {
        await api.saveVersion(sel, source, grantRefs())
        await select(sel)
      }
      const exe = await api.run(sel, parsed)
      setResult(exe)
      // If the run declared an interactive UI, open its panel.
      try { if ((await api.getUI(exe.id)).kind) setUiExec(exe.id) } catch { /* no UI */ }
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }

  // runForAgent backs the assistant's `run` editor tool: persist the live buffer
  // (so we run exactly what's shown), run it as this script with its grants, wait
  // for a terminal status, surface it in the Result card, and return a compact
  // summary (status + output/error + trace) for the model.
  const runForAgent = async (rawInput: unknown): Promise<string> => {
    if (!sel) return 'no script selected'
    const input = rawInput && typeof rawInput === 'object' ? rawInput : null
    try {
      const latest = detail?.versions?.[0]
      if (!latest || latest.source !== source) await api.saveVersion(sel, source, grantRefs())
    } catch (e) { return 'save failed: ' + (e as Error).message }
    let exe: Execution
    try { exe = await api.run(sel, input) } catch (e) { return 'run failed: ' + (e as Error).message }
    for (let i = 0; i < 60 && exe.status === 0; i++) {
      await new Promise((r) => setTimeout(r, 400))
      try { exe = await api.getExecution(exe.id) } catch { break }
    }
    setResult(exe)
    try { setDetail(await api.getScript(sel)) } catch { /* keep prior detail */ }
    const statusName = ['running', 'completed', 'failed', 'suspended'][exe.status] ?? String(exe.status)
    let trace = ''
    try {
      const t = await api.getTrace(exe.id)
      const caps = Array.from(new Set(t.spans.filter((s) => s.capability).map((s) => s.capability)))
      const errs = t.spans.filter((s) => s.error).map((s) => s.error)
      trace = `trace: ${t.spans.length} spans` + (caps.length ? ' (' + caps.join(', ') + ')' : '') +
        (errs.length ? '; errors: ' + errs.join('; ') : '')
    } catch { /* no trace */ }
    const body = exe.error ? 'error: ' + exe.error : 'output: ' + JSON.stringify(exe.output)
    return `status=${statusName}\n${body}${trace ? '\n' + trace : ''}`
  }

  const del = async () => {
    if (!sel || !confirm('Delete this script and its versions/triggers?')) return
    await api.deleteScript(sel)
    setSel(null); setDetail(null); refresh()
  }

  const restore = async (v: number) => { if (sel) { await api.restoreVersion(sel, v); await select(sel) } }
  const toggleGrant = (cid: string) =>
    setGrants((g) => g.includes(cid) ? g.filter((x) => x !== cid) : [...g, cid])

  // Drag the divider to resize the docked assistant. The panel hugs the right
  // edge, so dragging left (smaller clientX) widens it.
  const startResize = (e: React.MouseEvent) => {
    e.preventDefault()
    setResizing(true)
    const startX = e.clientX, startW = agentWidth
    let latest = startW
    const onMove = (ev: MouseEvent) => {
      latest = Math.min(Math.max(startW + (startX - ev.clientX), 300), 820)
      setAgentWidth(latest)
    }
    const onUp = () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      setResizing(false)
      localStorage.setItem('agentWidth', String(latest))
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  const subTabs: Sub[] = ['editor', 'runs', 'evals', 'triggers', 'secrets']

  return (
    <div className="layout">
      <div className="sidebar">
        <div className="row spread" style={{ marginBottom: 8 }}>
          <strong>Scripts</strong>
          <button onClick={() => { setPicking(true); setSel(null); setDetail(null) }}>+ New</button>
        </div>
        {scripts.filter((s) => s.id !== HARNESS_SCRIPT).map((s) => (
          <div key={s.id} className={'list-item' + (s.id === sel ? ' active' : '')} onClick={() => select(s.id)}>
            <div>{s.name}</div>
            <div className="muted mono" style={{ fontSize: 11 }}>v{s.current_version} · {s.owner || '—'}</div>
          </div>
        ))}
        {scripts.length >= limit && <button style={{ marginTop: 8 }} onClick={() => setLimit(limit + 20)}>Load more</button>}
        {scripts.length === 0 && <div className="muted">No scripts yet.</div>}
      </div>

      <div className={'main' + (assistant && sub === 'editor' && sel && detail ? ' main-fixed' : '')}>
        {uiExec && <UIPanel execId={uiExec} onClose={() => setUiExec(null)} />}
        {picking && <ExampleGallery onCreate={createFrom} onPrompt={createFromPrompt} onCancel={() => setPicking(false)} />}
        {!sel && !picking && <div className="muted">Select a script, or click + New to start from an example.</div>}
        {sel && detail && (
          <>
            <div className="row spread">
              <h2>{detail.script.name}</h2>
              <button onClick={del}>Delete script</button>
            </div>
            <div className="tabs" style={{ marginBottom: 12 }}>
              {subTabs.map((t) => (
                <button key={t} className={sub === t ? 'active' : ''} onClick={() => setSub(t)}>
                  {t[0].toUpperCase() + t.slice(1)}
                </button>
              ))}
            </div>

            {sub === 'editor' && (
              <div className={'editor-split' + (assistant ? ' with-agent' : '')}>
                <div className="editor-main">
                <div className="row spread" style={{ marginBottom: 8 }}>
                  <span className="muted">Ctrl-Space for capability autocomplete</span>
                  <div className="row">
                    <button className={assistant ? 'active' : ''} onClick={() => setAssistant((a) => !a)}>
                      {assistant ? 'Hide assistant' : '✨ Assistant'}
                    </button>
                    <button onClick={save} disabled={busy}>Save version</button>
                    <button className="primary" onClick={run} disabled={busy}>Run</button>
                  </div>
                </div>
                <CodeEditor value={source} onChange={setSource} names={names} />

                <div className="grid2" style={{ marginTop: 14 }}>
                  <div className="card">
                    <h3>Granted capabilities</h3>
                    {configs.length === 0 && <div className="muted">No tool configs. Add some under Settings.</div>}
                    {configs.map((c) => (
                      <label key={c.id} className="row" style={{ marginBottom: 4 }}>
                        <input type="checkbox" checked={grants.includes(c.id)} onChange={() => toggleGrant(c.id)} style={{ width: 'auto' }} />
                        <span className="mono">{c.id}</span><span className="muted">({c.capability})</span>
                      </label>
                    ))}
                    <div className="muted" style={{ marginTop: 6, fontSize: 12 }}>log, time, rand, store/fetch and send/recv are always available.</div>
                  </div>
                  <div className="card">
                    <h3>Run input → event.data (JSON)</h3>
                    <textarea value={input} onChange={(e) => setInput(e.target.value)} rows={6} style={{ width: '100%' }} />
                  </div>
                </div>

                {err && <div className="card err">{err}</div>}
                {result && (
                  <div className="card">
                    <div className="row spread">
                      <h3>Result <StatusBadge status={result.status} /></h3>
                      <div className="row" style={{ gap: 10 }}>
                        <a onClick={() => setUiExec(result.id)}>open panel ↔</a>
                        <a onClick={() => onOpenRun(result.id)}>view trace →</a>
                      </div>
                    </div>
                    <div className="row spread" style={{ marginBottom: 6 }}>
                      <span className="muted" style={{ fontSize: 12 }}>
                        execution <span className="mono" title="copy" style={{ cursor: 'pointer' }}
                          onClick={() => navigator.clipboard?.writeText(result.id)}>{result.id}</span>
                        <span className="mono" style={{ marginLeft: 8 }}>workspace {result.workspace}</span>
                      </span>
                    </div>
                    {result.status === 3 && (
                      <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>
                        Suspended at a recv() — it resumes automatically when a message arrives or its timeout fires.
                      </div>
                    )}
                    {result.error && <pre className="err">{result.error}</pre>}
                    {result.output !== undefined && <Json value={result.output} />}
                  </div>
                )}

                <div className="card">
                  <h3>Versions</h3>
                  <table>
                    <tbody>
                      {(detail.versions || []).map((v) => (
                        <tr key={v.version}>
                          <td className="mono">v{v.version}</td>
                          <td className="muted">{(v.grants || []).map((g) => g.capability).join(', ') || 'no grants'}</td>
                          <td className="muted">{new Date(v.created_at / 1e6).toLocaleString()}</td>
                          <td><button onClick={() => restore(v.version)}>restore</button></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                </div>
                {assistant && (
                  <>
                    <div className={'agent-resizer' + (resizing ? ' dragging' : '')}
                      onMouseDown={startResize} title="Drag to resize the assistant" />
                    <AgentPanel
                      width={agentWidth}
                      scriptId={sel}
                      getSource={() => source}
                      onApply={setSource}
                      onRun={runForAgent}
                      onClose={() => setAssistant(false)}
                      initialPrompt={seedPrompt}
                      onSeeded={() => setSeedPrompt(null)}
                    />
                  </>
                )}
              </div>
            )}

            {sub === 'runs' && <ScriptRuns scriptId={sel} />}
            {sub === 'evals' && <EvalsPanel scriptId={sel} onOpenRun={onOpenRun} />}
            {sub === 'triggers' && <ScriptTriggers scriptId={sel} />}
            {sub === 'secrets' && <ScriptSecrets scriptId={sel} />}
          </>
        )}
      </div>
    </div>
  )
}

function ExampleGallery({ onCreate, onPrompt, onCancel }: { onCreate: (name: string, src: string) => void; onPrompt: (prompt: string) => void; onCancel: () => void }) {
  const [examples, setExamples] = useState<Example[]>([])
  const [naming, setNaming] = useState<{ title: string; source: string } | null>(null)
  const [prompt, setPrompt] = useState('')
  useEffect(() => { api.examples().then(setExamples) }, [])
  const pick = (title: string, source: string) => setNaming({ title, source })
  const submitPrompt = () => { if (prompt.trim()) onPrompt(prompt.trim()) }
  return (
    <div className="card">
      {naming && (
        <PromptModal
          title="New script" label="Script name" initial={naming.title} confirmLabel="Create"
          onSubmit={(name) => { setNaming(null); onCreate(name, naming.source) }}
          onCancel={() => setNaming(null)}
        />
      )}
      <div className="row spread"><h2>New script</h2><button onClick={onCancel}>cancel</button></div>
      <div className="prompt-create">
        <h3>✨ Describe what you want to build</h3>
        <div className="muted" style={{ marginBottom: 8, fontSize: 12 }}>The assistant will scaffold it for you — you can refine it in the editor.</div>
        <div className="row" style={{ alignItems: 'flex-start' }}>
          <textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={2}
            placeholder="e.g. Every morning, fetch open PRs from GitHub and post a summary to Slack"
            style={{ flex: 1 }}
            onKeyDown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) submitPrompt() }} />
          <button className="primary" disabled={!prompt.trim()} onClick={submitPrompt}>Create with assistant</button>
        </div>
      </div>
      <h3 style={{ marginTop: 18 }}>or start from a template</h3>
      <div className="muted" style={{ marginBottom: 10, fontSize: 12 }}>Pick a template, or start blank. Capabilities listed need a matching grant under Settings.</div>
      <div className="example-grid">
        <div className="example" onClick={() => pick('blank', 'def main(input):\n    return {}\n')}>
          <strong>Blank</strong>
          <div className="muted" style={{ fontSize: 12 }}>An empty main(input).</div>
        </div>
        {examples.map((ex) => (
          <div key={ex.id} className="example" onClick={() => pick(ex.title, ex.source)}>
            <strong>{ex.title}</strong>
            <div className="muted" style={{ fontSize: 12 }}>{ex.description}</div>
            {ex.capabilities.length > 0 && (
              <div style={{ marginTop: 6 }}>{ex.capabilities.map((c) => <span key={c} className="badge suspended mono" style={{ marginRight: 4 }}>{c}</span>)}</div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

// ScriptRuns lists this script's executions and opens the selected one inline in
// the shared RunDetail — so Promote / feedback / the trace are all available here,
// not only in the global Runs tab.
function ScriptRuns({ scriptId }: { scriptId: string }) {
  const [runs, setRuns] = useState<Execution[]>([])
  const [limit, setLimit] = useState(20)
  const [sel, setSel] = useState<string | null>(null)
  const refresh = useCallback(() => { api.listExecutions(scriptId, limit, 0).then((r) => setRuns(r || [])) }, [scriptId, limit])
  useEffect(() => { refresh() }, [refresh])
  // Keep the list's feedback indicator in sync with edits made in the detail.
  const onChanged = (e: Execution) => setRuns((xs) => xs.map((x) => (x.id === e.id ? { ...x, feedback: e.feedback } : x)))
  return (
    <>
      <div className="card">
        <h3>Runs for this script</h3>
        <table>
          <thead><tr><th>id</th><th>status</th><th /><th>trigger</th><th>workspace</th><th>when</th></tr></thead>
          <tbody>
            {runs.map((e) => (
              <tr key={e.id} className={e.id === sel ? 'active' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(e.id)}>
                <td className="mono">{e.id.slice(3, 11)}</td>
                <td><StatusBadge status={e.status} /></td>
                <td>{e.feedback === 'up' ? '👍' : e.feedback === 'down' ? '👎' : ''}</td>
                <td className="muted">{e.trigger}</td>
                <td className="mono muted">{e.workspace}</td>
                <td className="muted">{new Date(e.created_at / 1e6).toLocaleString()}</td>
              </tr>
            ))}
            {runs.length === 0 && <tr><td colSpan={6} className="muted">no runs yet</td></tr>}
          </tbody>
        </table>
        {runs.length >= limit && <button style={{ marginTop: 8 }} onClick={() => setLimit(limit + 20)}>Load more</button>}
      </div>
      {sel && <RunDetail execId={sel} onChanged={onChanged} />}
    </>
  )
}

function ScriptTriggers({ scriptId }: { scriptId: string }) {
  const [triggers, setTriggers] = useState<Trigger[]>([])
  const [kind, setKind] = useState('cron')
  const [spec, setSpec] = useState('0 * * * *')
  const [actor, setActor] = useState('')
  const refresh = useCallback(() => { api.listTriggers(scriptId).then((t) => setTriggers(t || [])) }, [scriptId])
  useEffect(() => { refresh() }, [refresh])

  const add = async () => {
    await api.putTrigger({ script_id: scriptId, kind, spec: kind === 'cron' ? spec : '', actor_template: actor })
    refresh()
  }
  const del = async (id: string) => { await api.deleteTrigger(id); refresh() }
  const hookURL = (token: string) => `${location.origin}/api/hooks/${token}`

  return (
    <div className="card">
      <h3>Triggers</h3>
      <div className="row" style={{ marginBottom: 8, flexWrap: 'wrap' }}>
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="cron">cron</option>
          <option value="webhook">webhook</option>
        </select>
        {kind === 'cron' && (
          <select value={spec} onChange={(e) => setSpec(e.target.value)}>
            {CRON_PRESETS.map((p) => <option key={p.spec} value={p.spec}>{p.label}</option>)}
          </select>
        )}
        {kind === 'cron' && <input value={spec} onChange={(e) => setSpec(e.target.value)} placeholder="cron expr" style={{ width: 130 }} />}
        <input value={actor} onChange={(e) => setActor(e.target.value)} placeholder="workspace template (optional)" style={{ width: 220 }} />
        <button onClick={add}>Add</button>
      </div>
      <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>
        workspace template binds runs to a named workspace for shared state, e.g. <span className="mono">webhook-{'{{event.id}}'}</span>. Empty = anonymous.
      </div>
      <table>
        <thead><tr><th>kind</th><th>spec / url</th><th>workspace</th><th /></tr></thead>
        <tbody>
          {triggers.map((t) => (
            <tr key={t.id}>
              <td>{t.kind}{t.enabled ? '' : ' (off)'}</td>
              <td className="mono" style={{ fontSize: 12 }}>
                {t.kind === 'webhook' ? <a href={hookURL(t.spec)}>{hookURL(t.spec)}</a> : t.spec}
              </td>
              <td className="mono muted">{t.actor_template || '—'}</td>
              <td><button onClick={() => del(t.id)}>delete</button></td>
            </tr>
          ))}
          {triggers.length === 0 && <tr><td colSpan={4} className="muted">none</td></tr>}
        </tbody>
      </table>
    </div>
  )
}

function ScriptSecrets({ scriptId }: { scriptId: string }) {
  const [names, setNames] = useState<string[]>([])
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const refresh = useCallback(() => { api.listSecrets(scriptId).then((r) => setNames(r.names || [])) }, [scriptId])
  useEffect(() => { refresh() }, [refresh])
  const add = async () => { if (!name) return; await api.putSecret(name, value, scriptId); setName(''); setValue(''); refresh() }
  const del = async (n: string) => { await api.deleteSecret(n, scriptId); refresh() }
  return (
    <div className="card">
      <h3>Per-script secrets</h3>
      <div className="muted" style={{ marginBottom: 8 }}>Override globals for this script. Write-only; never returned to scripts or traces.</div>
      <div className="row" style={{ marginBottom: 10 }}>
        <input placeholder="NAME" value={name} onChange={(e) => setName(e.target.value)} />
        <input placeholder="value" type="password" value={value} onChange={(e) => setValue(e.target.value)} />
        <button onClick={add}>Save</button>
      </div>
      <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
        {names.map((n) => (
          <span key={n} className="badge suspended mono">{n} <a onClick={() => del(n)} style={{ marginLeft: 4 }}>×</a></span>
        ))}
        {names.length === 0 && <span className="muted">none</span>}
      </div>
    </div>
  )
}
