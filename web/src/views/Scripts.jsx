import React, { useEffect, useState, useCallback } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, STATUS } from '../api.js'

const STARTER = `# main(input) is the entry point. Capabilities are memoized RPCs.
def main(input):
    log("hello from", input.get("name", "world"))
    reply = llm([{"role": "user", "content": "Say hi to " + input.get("name", "world")}])
    return {"reply": reply["content"]}
`

export default function Scripts({ onOpenRun }) {
  const [scripts, setScripts] = useState([])
  const [sel, setSel] = useState(null)
  const [detail, setDetail] = useState(null)
  const [configs, setConfigs] = useState([])
  const [source, setSource] = useState('')
  const [grants, setGrants] = useState([]) // config ids
  const [input, setInput] = useState('{\n  "name": "kyle"\n}')
  const [result, setResult] = useState(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const refresh = useCallback(async () => {
    setScripts(await api.listScripts() || [])
    setConfigs(await api.listConfigs() || [])
  }, [])
  useEffect(() => { refresh() }, [refresh])

  const select = async (id) => {
    setSel(id); setResult(null); setErr('')
    const d = await api.getScript(id)
    setDetail(d)
    const latest = d.versions && d.versions[0]
    setSource(latest ? latest.source : STARTER)
    setGrants(latest ? (latest.grants || []).map((g) => g.config_id) : [])
  }

  const newScript = async () => {
    const name = prompt('Script name?')
    if (!name) return
    const sc = await api.createScript(name, STARTER)
    await refresh()
    select(sc.id)
  }

  const save = async () => {
    setBusy(true); setErr('')
    try {
      const grantRefs = grants
        .map((cid) => configs.find((c) => c.id === cid))
        .filter(Boolean)
        .map((c) => ({ capability: c.capability, config_id: c.id }))
      await api.saveVersion(sel, source, grantRefs)
      await select(sel)
    } catch (e) { setErr(e.message) } finally { setBusy(false) }
  }

  const run = async () => {
    setBusy(true); setErr(''); setResult(null)
    let parsed
    try { parsed = input.trim() ? JSON.parse(input) : null }
    catch (e) { setErr('input is not valid JSON: ' + e.message); setBusy(false); return }
    try {
      const exe = await api.run(sel, parsed)
      setResult(exe)
    } catch (e) { setErr(e.message) } finally { setBusy(false) }
  }

  const toggleGrant = (cid) => {
    setGrants((g) => g.includes(cid) ? g.filter((x) => x !== cid) : [...g, cid])
  }

  return (
    <div className="layout">
      <div className="sidebar">
        <div className="row spread" style={{ marginBottom: 8 }}>
          <strong>Scripts</strong>
          <button onClick={newScript}>+ New</button>
        </div>
        {scripts.map((s) => (
          <div key={s.id} className={'list-item' + (s.id === sel ? ' active' : '')} onClick={() => select(s.id)}>
            <div>{s.name}</div>
            <div className="muted mono" style={{ fontSize: 11 }}>v{s.current_version}</div>
          </div>
        ))}
        {scripts.length === 0 && <div className="muted">No scripts yet.</div>}
      </div>

      <div className="main">
        {!sel && <div className="muted">Select or create a script to begin.</div>}
        {sel && detail && (
          <>
            <div className="row spread">
              <h2>{detail.script.name}</h2>
              <div className="row">
                <button onClick={save} disabled={busy}>Save version</button>
                <button className="primary" onClick={run} disabled={busy}>Run</button>
              </div>
            </div>

            <CodeMirror
              value={source}
              height="360px"
              theme="dark"
              extensions={[python()]}
              onChange={setSource}
            />

            <div className="grid2" style={{ marginTop: 14 }}>
              <div className="card">
                <h3>Granted capabilities</h3>
                {configs.length === 0 && <div className="muted">No tool configs. Add some under Settings.</div>}
                {configs.map((c) => (
                  <label key={c.id} className="row" style={{ marginBottom: 4 }}>
                    <input type="checkbox" checked={grants.includes(c.id)} onChange={() => toggleGrant(c.id)} style={{ width: 'auto' }} />
                    <span className="mono">{c.id}</span>
                    <span className="muted">({c.capability})</span>
                  </label>
                ))}
                <div className="muted" style={{ marginTop: 6, fontSize: 12 }}>
                  log, time, rand and kv are always available.
                </div>
              </div>

              <div className="card">
                <h3>Run input (JSON)</h3>
                <textarea value={input} onChange={(e) => setInput(e.target.value)} rows={6} style={{ width: '100%' }} />
              </div>
            </div>

            {err && <div className="card err">{err}</div>}

            {result && (
              <div className="card">
                <div className="row spread">
                  <h3>
                    Result <span className={'badge ' + STATUS[result.status]}>{STATUS[result.status]}</span>
                  </h3>
                  <a onClick={() => onOpenRun(result.id)}>view trace →</a>
                </div>
                {result.error && <pre className="err">{result.error}</pre>}
                {result.output && <pre>{pretty(result.output)}</pre>}
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
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function pretty(raw) {
  try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return String(raw) }
}
