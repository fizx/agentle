import React, { useEffect, useState, useCallback } from 'react'
import { api } from '../api.js'

const CONFIG_TEMPLATES = {
  llm: '{\n  "base_url": "",\n  "model": "gpt-4o-mini"\n}',
  http: '{\n  "allow": ["api.github.com", "*.example.com"],\n  "auth_header": "Authorization"\n}',
  shell: '{}',
}

export default function Settings() {
  const [secrets, setSecrets] = useState([])
  const [configs, setConfigs] = useState([])
  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')

  const refresh = useCallback(async () => {
    setErr('')
    try {
      const s = await api.listSecrets() // global scope (admin)
      setSecrets(s.names || [])
    } catch (e) { setErr(e.message) }
    setConfigs(await api.listConfigs() || [])
  }, [])
  useEffect(() => { refresh() }, [refresh])

  const toast = (m) => { setMsg(m); setTimeout(() => setMsg(''), 2500) }

  const [sName, setSName] = useState('')
  const [sVal, setSVal] = useState('')
  const addSecret = async () => {
    if (!sName) return
    try { await api.putSecret(sName, sVal); setSName(''); setSVal(''); toast('secret saved'); refresh() }
    catch (e) { toast(e.message) }
  }
  const delSecret = async (n) => { await api.deleteSecret(n); refresh() }

  const [cId, setCId] = useState('')
  const [cCap, setCCap] = useState('llm')
  const [cCfg, setCCfg] = useState(CONFIG_TEMPLATES.llm)
  const [cSecret, setCSecret] = useState('')
  const addConfig = async () => {
    if (!cId) return toast('config id required')
    let config
    try { config = JSON.parse(cCfg) } catch { return toast('config JSON invalid') }
    await api.putConfig({ id: cId, capability: cCap, config, secret_ref: cSecret })
    setCId(''); toast('config saved'); refresh()
  }
  const onCapChange = (cap) => { setCCap(cap); setCCfg(CONFIG_TEMPLATES[cap] || '{}') }

  return (
    <div className="main" style={{ maxWidth: 980, margin: '0 auto' }}>
      <div className="card">
        <h2>Global secrets</h2>
        <div className="muted" style={{ marginBottom: 8 }}>
          Admin-only. Write-only — values bind to tool configs and never reach scripts or traces.
          Per-script overrides live on each script's Secrets tab.
        </div>
        {err && <div className="err" style={{ marginBottom: 8 }}>{err}</div>}
        <div className="row" style={{ marginBottom: 10 }}>
          <input placeholder="NAME" value={sName} onChange={(e) => setSName(e.target.value)} />
          <input placeholder="value" type="password" value={sVal} onChange={(e) => setSVal(e.target.value)} />
          <button onClick={addSecret}>Save</button>
        </div>
        <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
          {secrets.map((n) => (
            <span key={n} className="badge suspended mono">{n} <a onClick={() => delSecret(n)} style={{ marginLeft: 4 }}>×</a></span>
          ))}
          {secrets.length === 0 && <span className="muted">none</span>}
        </div>
      </div>

      <div className="card">
        <h2>Tool configs</h2>
        <div className="muted" style={{ marginBottom: 8 }}>A configured, secret-bound capability instance. Scripts grant these by id.</div>
        <div className="grid2">
          <div className="col">
            <div><label>Config id</label><input value={cId} onChange={(e) => setCId(e.target.value)} placeholder="openai-prod" style={{ width: '100%' }} /></div>
            <div><label>Capability</label>
              <select value={cCap} onChange={(e) => onCapChange(e.target.value)} style={{ width: '100%' }}>
                <option value="llm">llm</option>
                <option value="http">http</option>
                <option value="shell">shell</option>
              </select>
            </div>
            <div><label>Secret ref (optional)</label>
              <select value={cSecret} onChange={(e) => setCSecret(e.target.value)} style={{ width: '100%' }}>
                <option value="">— none —</option>
                {secrets.map((n) => <option key={n} value={n}>{n}</option>)}
              </select>
            </div>
            <button onClick={addConfig}>Save config</button>
          </div>
          <div><label>Config JSON</label>
            <textarea value={cCfg} onChange={(e) => setCCfg(e.target.value)} rows={7} style={{ width: '100%' }} />
          </div>
        </div>
        <table style={{ marginTop: 12 }}>
          <thead><tr><th>id</th><th>capability</th><th>secret</th></tr></thead>
          <tbody>
            {configs.map((c) => (
              <tr key={c.id}><td className="mono">{c.id}</td><td>{c.capability}</td><td className="muted mono">{c.secret_ref || '—'}</td></tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="muted" style={{ fontSize: 12 }}>Triggers are managed per-script on the Scripts page.</div>
      {msg && <div className="toast">{msg}</div>}
    </div>
  )
}
