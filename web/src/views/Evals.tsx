import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { CalibrationStats, EvalResult, EvalSuite, Golden, Script, ToolPolicy } from '../types'

// Evals: per-script golden datasets and replay-based scoring. Promote runs to
// goldens from the Runs tab, then re-run new versions here to see how far they
// cover the golden trajectory and where they stop (write-miss / recv / budget).
export default function Evals({ onOpenRun }: { onOpenRun: (id: string) => void }) {
  const [scripts, setScripts] = useState<Script[]>([])
  const [sel, setSel] = useState<string | null>(null)

  useEffect(() => { (async () => setScripts((await api.listScripts(500, 0)) || []))() }, [])

  return (
    <div className="layout">
      <div className="sidebar">
        <strong style={{ marginBottom: 8, display: 'block' }}>Scripts</strong>
        {scripts.map((s) => (
          <div key={s.id} className={'list-item' + (s.id === sel ? ' active' : '')} onClick={() => setSel(s.id)}>
            <div>{s.name}</div>
            <div className="muted" style={{ fontSize: 11 }}>v{s.current_version}</div>
          </div>
        ))}
        {scripts.length === 0 && <div className="muted">No scripts.</div>}
      </div>

      <div className="main">
        {!sel && <div className="muted">Select a script to see its golden dataset.</div>}
        {sel && <EvalsPanel scriptId={sel} onOpenRun={onOpenRun} />}
      </div>
    </div>
  )
}

// EvalsPanel is the per-script evals surface (calibration, egress policy, and the
// golden dataset). It is shared by the global Evals tab and a script's own Evals
// sub-tab so both render identically from one implementation.
export function EvalsPanel({ scriptId, onOpenRun }: { scriptId: string; onOpenRun: (id: string) => void }) {
  const [goldens, setGoldens] = useState<Golden[]>([])
  const loadGoldens = useCallback(async (id: string) => { setGoldens((await api.listGoldens(id)) || []) }, [])
  useEffect(() => { loadGoldens(scriptId) }, [scriptId, loadGoldens])

  return (
    <>
      <Calibration scriptId={scriptId} />
      <ToolPolicyEditor />
      {goldens.length === 0 && (
        <div className="muted">No goldens yet. Open a run (here or in the Runs tab) and click ⭐ Promote to add one.</div>
      )}
      {goldens.map((g) => (
        <GoldenCard key={g.id} golden={g} onDeleted={() => loadGoldens(scriptId)} onOpenRun={onOpenRun} />
      ))}
    </>
  )
}

function GoldenCard({ golden, onDeleted, onOpenRun }: { golden: Golden; onDeleted: () => void; onOpenRun: (id: string) => void }) {
  const [version, setVersion] = useState('')
  const [allowReads, setAllowReads] = useState(false)
  const [miss, setMiss] = useState('fail')
  const [judge, setJudge] = useState(true)
  const [samples, setSamples] = useState(1)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<EvalResult | null>(null)
  const [suite, setSuite] = useState<EvalSuite | null>(null)
  const [err, setErr] = useState<string | null>(null)

  // Authored artifacts (saved together on blur, since they share one endpoint).
  const [criteria, setCriteria] = useState(golden.criteria || '')
  const [persona, setPersona] = useState(golden.persona || '')
  const saveArtifacts = async () => {
    if (persona === (golden.persona || '') && criteria === (golden.criteria || '')) return
    await api.updateGoldenArtifacts(golden.id, persona, criteria)
    golden.persona = persona; golden.criteria = criteria
  }
  const [drafting, setDrafting] = useState(false)
  const autofill = async () => {
    setDrafting(true)
    try { const { persona: p } = await api.draftPersona(golden.id); setPersona(p) }
    catch (e) { alert(String(e instanceof Error ? e.message : e)) }
    finally { setDrafting(false) }
  }
  const [consistency, setConsistency] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const check = async () => {
    setChecking(true); setConsistency(null)
    try {
      await saveArtifacts()
      const cr = await api.checkConsistency(golden.id)
      setConsistency((cr.consistent ? '✓ consistent — ' : '✗ inconsistent — ') + cr.detail)
    } catch (e) { setConsistency('error: ' + String(e instanceof Error ? e.message : e)) }
    finally { setChecking(false) }
  }

  const run = async () => {
    setBusy(true); setErr(null); setResult(null); setSuite(null)
    const opts = { version: version ? Number(version) : undefined, allowReads, miss, judge }
    try {
      if (samples > 1) setSuite(await api.runEvalSuite(golden.id, samples, opts))
      else setResult(await api.runEval(golden.id, opts))
    } catch (e) {
      setErr(String(e instanceof Error ? e.message : e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <div className="row spread">
        <h3 className="row" style={{ gap: 8 }}>
          <span className={'badge ' + (golden.label === 'failure' ? 'failed' : 'completed')}>{golden.label}</span>
          <span className="mono" style={{ fontSize: 13 }}>{golden.id.slice(5, 13)}</span>
        </h3>
        <button onClick={async () => { if (confirm('Delete this golden?')) { await api.deleteGolden(golden.id); onDeleted() } }}>Delete</button>
      </div>
      <div className="muted" style={{ fontSize: 12, marginBottom: 10 }}>
        origin v{golden.origin_version} ·{' '}
        <a className="mono" style={{ cursor: 'pointer' }} onClick={() => onOpenRun(golden.origin_exec)}>{golden.origin_exec.slice(0, 11)}</a>
        {' · '}{new Date(golden.created_at / 1e6).toLocaleString()}
      </div>

      <div className="row spread" style={{ marginBottom: 4 }}>
        <label className="muted" style={{ fontSize: 12 }}>Persona (persona.md) — the user simulator. Leave blank to replay recorded answers.</label>
        <span className="row" style={{ gap: 6 }}>
          <button onClick={autofill} disabled={drafting} title="Draft from the golden transcript">{drafting ? 'Drafting…' : 'Autofill'}</button>
          <button onClick={check} disabled={checking} title="Replay the origin version through this persona and confirm it reproduces the golden outcome">{checking ? 'Checking…' : 'Check consistency'}</button>
        </span>
      </div>
      <textarea
        value={persona} onChange={(e) => setPersona(e.target.value)} onBlur={saveArtifacts}
        placeholder={'---\non_unknown: refuse\nstyle: naive\n---\nYou are a budget-conscious traveler who wants to visit Tokyo.'}
        style={{ width: '100%', minHeight: 72, marginBottom: 6, fontFamily: 'monospace', fontSize: 12 }}
      />
      {consistency && <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>{consistency}</div>}

      <label className="muted" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>Rubric (criteria.md) — what the judge scores:</label>
      <textarea
        value={criteria} onChange={(e) => setCriteria(e.target.value)} onBlur={saveArtifacts}
        placeholder="e.g. The agent must identify the destination and stay under budget before booking."
        style={{ width: '100%', minHeight: 56, marginBottom: 10, fontFamily: 'inherit', fontSize: 13 }}
      />

      <div className="row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <input style={{ width: 130 }} placeholder="version (current)" value={version} onChange={(e) => setVersion(e.target.value)} />
        <label className="row muted" style={{ gap: 4, fontSize: 12 }}>
          <input type="checkbox" checked={allowReads} onChange={(e) => setAllowReads(e.target.checked)} /> reads live on miss
        </label>
        <label className="row muted" style={{ gap: 4, fontSize: 12 }}>
          <input type="checkbox" checked={judge} onChange={(e) => setJudge(e.target.checked)} /> LLM judge
        </label>
        <label className="row muted" style={{ gap: 4, fontSize: 12 }} title="pass@k over the non-deterministic live LLM">
          samples <input type="number" min={1} max={20} value={samples} onChange={(e) => setSamples(Math.max(1, Number(e.target.value)))} style={{ width: 52 }} />
        </label>
        <select value={miss} onChange={(e) => setMiss(e.target.value)} title="what a write-miss does">
          <option value="fail">write-miss: fail</option>
          <option value="go_live">write-miss: go live</option>
          <option value="flag">write-miss: flag</option>
        </select>
        <button onClick={run} disabled={busy}>{busy ? 'Running…' : 'Run eval'}</button>
      </div>

      {err && <pre className="err" style={{ marginTop: 10 }}>{err}</pre>}
      {result && <EvalResultView r={result} />}
      {suite && <SuiteView s={suite} />}
    </div>
  )
}

// SuiteView shows pass@k aggregate: a single replay isn't a verdict over the
// non-deterministic LLM, so the pass-rate + flakiness flag is the real signal.
function SuiteView({ s }: { s: EvalSuite }) {
  return (
    <div style={{ marginTop: 12 }}>
      <div className="row" style={{ gap: 18 }}>
        <Stat label="pass@k" value={`${s.passes}/${s.k}`} />
        <Stat label="pass rate" value={(s.pass_rate * 100).toFixed(0) + '%'} />
        <Stat label="mean coverage" value={(s.mean_coverage * 100).toFixed(0) + '%'} />
        {s.flaky && <span className="badge running" title="some samples passed, some failed — unstable">flaky</span>}
      </div>
      <div className="row" style={{ gap: 4, marginTop: 8, flexWrap: 'wrap' }}>
        {s.samples.map((r, i) => {
          const ok = r.verdict ? r.verdict.pass : (r.completed && !r.error)
          return <span key={i} className={'badge ' + (ok ? 'completed' : 'failed')} style={{ fontSize: 11 }} title={r.stop_kind || r.error || 'pass'}>#{i + 1}</span>
        })}
      </div>
    </div>
  )
}

function EvalResultView({ r }: { r: EvalResult }) {
  const pct = Math.round(r.coverage * 100)
  const verdict = r.completed ? 'completed' : (r.error ? 'errored' : `stopped: ${r.stop_kind}`)
  return (
    <div style={{ marginTop: 12 }}>
      <div className="row spread" style={{ marginBottom: 4 }}>
        <span className={r.completed ? 'badge completed' : 'badge failed'}>{verdict}</span>
        <span className="muted" style={{ fontSize: 12 }}>{r.executed}/{r.golden_len} RPCs · v{r.version}</span>
      </div>
      {/* Coverage bar: how much of the golden trajectory the new version reached. */}
      <div style={{ background: 'rgba(255,255,255,.08)', borderRadius: 4, height: 8, overflow: 'hidden' }}>
        <div style={{ width: pct + '%', height: '100%', background: r.completed ? 'var(--green)' : 'var(--yellow)' }} />
      </div>
      <div className="muted" style={{ fontSize: 11, marginTop: 4 }}>{pct}% coverage before {r.completed ? 'completion' : 'stop'}</div>
      {r.stop_msg && <div className="muted" style={{ fontSize: 12, marginTop: 6 }}>{r.stop_msg}</div>}
      {r.error && <pre className="err" style={{ marginTop: 6 }}>{r.error}</pre>}

      {r.judge_error && <div className="muted" style={{ fontSize: 12, marginTop: 8 }}>judge unavailable: {r.judge_error}</div>}
      {r.verdict && (
        <div className="card" style={{ marginTop: 10 }}>
          <div className="row spread">
            <strong>Judge verdict <span className="muted" style={{ fontWeight: 400, fontSize: 12 }}>({r.verdict.mode} mode)</span></strong>
            <span className={r.verdict.pass ? 'badge completed' : 'badge failed'}>{r.verdict.pass ? 'PASS' : 'FAIL'}</span>
          </div>
          {r.verdict.reasoning && <div className="muted" style={{ fontSize: 13, marginTop: 6 }}>{r.verdict.reasoning}</div>}
          {(r.verdict.criteria || []).map((c, i) => (
            <div key={i} className="row" style={{ gap: 6, marginTop: 4, fontSize: 13 }}>
              <span>{c.pass ? '✓' : '✗'}</span>
              <span>{c.criterion}{c.evidence ? <span className="muted"> — {c.evidence}</span> : null}</span>
            </div>
          ))}
        </div>
      )}

      {r.output !== undefined && r.output !== null && (
        <pre className="mono" style={{ marginTop: 6, fontSize: 12, maxHeight: 160, overflow: 'auto' }}>{JSON.stringify(r.output, null, 2)}</pre>
      )}
    </div>
  )
}

// Calibration: judge↔human agreement over the script's labeled goldens. Accuracy
// alone misleads on imbalanced labels, so kappa (chance-corrected) is shown too.
function Calibration({ scriptId }: { scriptId: string }) {
  const [stats, setStats] = useState<CalibrationStats | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const run = async () => {
    setBusy(true); setErr(null)
    try { setStats(await api.calibrate(scriptId)) }
    catch (e) { setErr(String(e instanceof Error ? e.message : e)) }
    finally { setBusy(false) }
  }
  return (
    <div className="card">
      <div className="row spread">
        <h3>Judge calibration</h3>
        <button onClick={run} disabled={busy}>{busy ? 'Judging…' : 'Calibrate'}</button>
      </div>
      <div className="muted" style={{ fontSize: 12 }}>Agreement between the LLM judge and the human success/fail labels. Trust verdicts only once this clears.</div>
      {err && <pre className="err">{err}</pre>}
      {stats && (stats.n === 0
        ? <div className="muted" style={{ marginTop: 8 }}>No goldens with a rubric yet — add criteria to goldens, then calibrate.</div>
        : (
          <div className="row" style={{ gap: 18, marginTop: 8 }}>
            <Stat label="accuracy" value={(stats.accuracy * 100).toFixed(0) + '%'} />
            <Stat label="κ (kappa)" value={stats.kappa.toFixed(2)} />
            <Stat label="n" value={String(stats.n)} />
            <Stat label="agree" value={`${stats.agreements}/${stats.n}`} />
            <Stat label="FP / FN" value={`${stats.fp} / ${stats.fn}`} />
          </div>
        ))}
    </div>
  )
}

// ToolPolicyEditor manages the operator's read/write classification table (global,
// not per-script). It decides whether a NOVEL external call on a cassette miss may
// run unattended (read) or must gate (write); default is write (fail-safe).
function ToolPolicyEditor() {
  const [rows, setRows] = useState<ToolPolicy[]>([])
  const [open, setOpen] = useState(false)
  const [server, setServer] = useState('')
  const [tool, setTool] = useState('GET')
  const [isWrite, setIsWrite] = useState(false)
  const load = useCallback(async () => setRows((await api.listToolPolicies()) || []), [])
  useEffect(() => { if (open) load() }, [open, load])

  const add = async () => {
    if (!server || !tool) return
    await api.putToolPolicy({ server, tool, is_write: isWrite, source: 'operator' })
    setServer('')
    load()
  }
  return (
    <div className="card">
      <div className="row spread">
        <h3>Egress policy <span className="muted" style={{ fontWeight: 400, fontSize: 12 }}>(read/write classification)</span></h3>
        <button onClick={() => setOpen(!open)}>{open ? 'Hide' : 'Show'}</button>
      </div>
      {open && (
        <>
          <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>
            On a cassette miss, a tool tagged <b>read</b> runs live; <b>write</b> gates. Unlisted ⇒ write (fail-safe). For HTTP: server = host, tool = method.
          </div>
          <table>
            <thead><tr><th>server</th><th>tool</th><th>class</th><th>source</th><th /></tr></thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.server + '\x00' + p.tool}>
                  <td className="mono">{p.server}</td>
                  <td className="mono">{p.tool}</td>
                  <td><span className={'badge ' + (p.is_write ? 'failed' : 'completed')}>{p.is_write ? 'write' : 'read'}</span></td>
                  <td className="muted">{p.source}</td>
                  <td><button onClick={async () => { await api.deleteToolPolicy(p.server, p.tool); load() }}>✕</button></td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="row" style={{ gap: 8, marginTop: 8, alignItems: 'center' }}>
            <input placeholder="host (api.example.com)" value={server} onChange={(e) => setServer(e.target.value)} />
            <input placeholder="method" value={tool} onChange={(e) => setTool(e.target.value)} style={{ width: 90 }} />
            <select value={isWrite ? 'write' : 'read'} onChange={(e) => setIsWrite(e.target.value === 'write')}>
              <option value="read">read</option>
              <option value="write">write</option>
            </select>
            <button onClick={add}>Add</button>
          </div>
        </>
      )}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div style={{ fontSize: 18, fontWeight: 600 }}>{value}</div>
      <div className="muted" style={{ fontSize: 11 }}>{label}</div>
    </div>
  )
}
