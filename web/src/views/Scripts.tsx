import { useCallback, useEffect, useState } from 'react'
import { api, type GrantRefInput } from '../api'
import type { Example, Execution, ScriptDetail, Script, ToolConfig, Trigger } from '../types'
import { CRON_PRESETS } from '../types'
import { StatusBadge } from '../components/Badge'
import { Json } from '../components/Json'
import { CodeEditor } from '../components/CodeEditor'

type Sub = 'editor' | 'runs' | 'triggers' | 'secrets'

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

  const refresh = useCallback(async () => {
    setScripts((await api.listScripts(limit, 0)) || [])
    setConfigs((await api.listConfigs()) || [])
    setNames((await api.capabilities()).map((c) => c.name))
  }, [limit])
  useEffect(() => { refresh() }, [refresh])

  const select = async (id: string) => {
    setSel(id); setResult(null); setErr(''); setSub('editor'); setPicking(false)
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

  const save = async () => {
    if (!sel) return
    setBusy(true); setErr('')
    try {
      const grantRefs: GrantRefInput[] = grants
        .map((cid) => configs.find((c) => c.id === cid))
        .filter((c): c is ToolConfig => !!c)
        .map((c) => ({ capability: c.capability, config_id: c.id }))
      await api.saveVersion(sel, source, grantRefs)
      await select(sel)
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }

  const run = async () => {
    if (!sel) return
    setBusy(true); setErr(''); setResult(null)
    let parsed: unknown
    try { parsed = input.trim() ? JSON.parse(input) : null }
    catch (e) { setErr('input is not valid JSON: ' + (e as Error).message); setBusy(false); return }
    try { setResult(await api.run(sel, parsed)) }
    catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }

  const del = async () => {
    if (!sel || !confirm('Delete this script and its versions/triggers?')) return
    await api.deleteScript(sel)
    setSel(null); setDetail(null); refresh()
  }

  const restore = async (v: number) => { if (sel) { await api.restoreVersion(sel, v); await select(sel) } }
  const toggleGrant = (cid: string) =>
    setGrants((g) => g.includes(cid) ? g.filter((x) => x !== cid) : [...g, cid])

  const subTabs: Sub[] = ['editor', 'runs', 'triggers', 'secrets']

  return (
    <div className="layout">
      <div className="sidebar">
        <div className="row spread" style={{ marginBottom: 8 }}>
          <strong>Scripts</strong>
          <button onClick={() => { setPicking(true); setSel(null); setDetail(null) }}>+ New</button>
        </div>
        {scripts.map((s) => (
          <div key={s.id} className={'list-item' + (s.id === sel ? ' active' : '')} onClick={() => select(s.id)}>
            <div>{s.name}</div>
            <div className="muted mono" style={{ fontSize: 11 }}>v{s.current_version} · {s.owner || '—'}</div>
          </div>
        ))}
        {scripts.length >= limit && <button style={{ marginTop: 8 }} onClick={() => setLimit(limit + 20)}>Load more</button>}
        {scripts.length === 0 && <div className="muted">No scripts yet.</div>}
      </div>

      <div className="main">
        {picking && <ExampleGallery onCreate={createFrom} onCancel={() => setPicking(false)} />}
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
              <>
                <div className="row spread" style={{ marginBottom: 8 }}>
                  <span className="muted">Ctrl-Space for capability autocomplete</span>
                  <div className="row">
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
                      <a onClick={() => onOpenRun(result.id)}>view trace →</a>
                    </div>
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
              </>
            )}

            {sub === 'runs' && <ScriptRuns scriptId={sel} onOpenRun={onOpenRun} />}
            {sub === 'triggers' && <ScriptTriggers scriptId={sel} />}
            {sub === 'secrets' && <ScriptSecrets scriptId={sel} />}
          </>
        )}
      </div>
    </div>
  )
}

function ExampleGallery({ onCreate, onCancel }: { onCreate: (name: string, src: string) => void; onCancel: () => void }) {
  const [examples, setExamples] = useState<Example[]>([])
  useEffect(() => { api.examples().then(setExamples) }, [])
  const pick = (title: string, source: string) => {
    const name = prompt('Script name?', title)
    if (name) onCreate(name, source)
  }
  return (
    <div className="card">
      <div className="row spread"><h2>Start from an example</h2><button onClick={onCancel}>cancel</button></div>
      <div className="muted" style={{ marginBottom: 10 }}>Pick a template, or start blank. Capabilities listed need a matching grant under Settings.</div>
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

function ScriptRuns({ scriptId, onOpenRun }: { scriptId: string; onOpenRun: (id: string) => void }) {
  const [runs, setRuns] = useState<Execution[]>([])
  const [limit, setLimit] = useState(20)
  useEffect(() => { api.listExecutions(scriptId, limit, 0).then((r) => setRuns(r || [])) }, [scriptId, limit])
  return (
    <div className="card">
      <h3>Runs for this script</h3>
      <table>
        <thead><tr><th>id</th><th>status</th><th>trigger</th><th>workspace</th><th>when</th></tr></thead>
        <tbody>
          {runs.map((e) => (
            <tr key={e.id} style={{ cursor: 'pointer' }} onClick={() => onOpenRun(e.id)}>
              <td className="mono">{e.id.slice(3, 11)}</td>
              <td><StatusBadge status={e.status} /></td>
              <td className="muted">{e.trigger}</td>
              <td className="mono muted">{e.workspace}</td>
              <td className="muted">{new Date(e.created_at / 1e6).toLocaleString()}</td>
            </tr>
          ))}
          {runs.length === 0 && <tr><td colSpan={5} className="muted">no runs yet</td></tr>}
        </tbody>
      </table>
      {runs.length >= limit && <button style={{ marginTop: 8 }} onClick={() => setLimit(limit + 20)}>Load more</button>}
    </div>
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
