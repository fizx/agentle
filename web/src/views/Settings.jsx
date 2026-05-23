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
  const [triggers, setTriggers] = useState([])
  const [scripts, setScripts] = useState([])
  const [msg, setMsg] = useState('')

  const refresh = useCallback(async () => {
    const s = await api.listSecrets()
    setSecrets(s.names || [])
    setConfigs(await api.listConfigs() || [])
    setTriggers(await api.listTriggers() || [])
    setScripts(await api.listScripts() || [])
  }, [])
  useEffect(() => { refresh() }, [refresh])

  const toast = (m) => { setMsg(m); setTimeout(() => setMsg(''), 2500) }

  // secret form
  const [sName, setSName] = useState('')
  const [sVal, setSVal] = useState('')
  const addSecret = async () => {
    if (!sName) return
    await api.putSecret(sName, sVal); setSName(''); setSVal(''); toast('secret saved'); refresh()
  }

  // config form
  const [cId, setCId] = useState('')
  const [cCap, setCCap] = useState('llm')
  const [cCfg, setCCfg] = useState(CONFIG_TEMPLATES.llm)
  const [cSecret, setCSecret] = useState('')
  const addConfig = async () => {
    if (!cId) return toast('config id required')
    let config
    try { config = JSON.parse(cCfg) } catch (e) { return toast('config JSON invalid') }
    await api.putConfig({ id: cId, capability: cCap, config, secret_ref: cSecret })
    setCId(''); toast('config saved'); refresh()
  }
  const onCapChange = (cap) => { setCCap(cap); setCCfg(CONFIG_TEMPLATES[cap] || '{}') }

  // trigger form
  const [tScript, setTScript] = useState('')
  const [tKind, setTKind] = useState('cron')
  const [tSpec, setTSpec] = useState('*/5 * * * *')
  const addTrigger = async () => {
    if (!tScript) return toast('pick a script')
    await api.putTrigger({ script_id: tScript, kind: tKind, spec: tKind === 'cron' ? tSpec : '' })
    toast('trigger saved'); refresh()
  }
  const delTrigger = async (id) => { await api.deleteTrigger(id); refresh() }

  const hookURL = (token) => `${location.origin}/api/hooks/${token}`

  return (
    <div className="main" style={{ maxWidth: 980, margin: '0 auto' }}>
      <div className="card">
        <h2>Secrets</h2>
        <div className="muted" style={{ marginBottom: 8 }}>Values are write-only — they bind to tool configs and never reach scripts or traces.</div>
        <div className="row" style={{ marginBottom: 10 }}>
          <input placeholder="NAME" value={sName} onChange={(e) => setSName(e.target.value)} />
          <input placeholder="value" type="password" value={sVal} onChange={(e) => setSVal(e.target.value)} />
          <button onClick={addSecret}>Save</button>
        </div>
        <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
          {secrets.map((n) => <span key={n} className="badge suspended mono">{n}</span>)}
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

      <div className="card">
        <h2>Triggers</h2>
        <div className="row" style={{ marginBottom: 10, flexWrap: 'wrap' }}>
          <select value={tScript} onChange={(e) => setTScript(e.target.value)}>
            <option value="">— script —</option>
            {scripts.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
          <select value={tKind} onChange={(e) => setTKind(e.target.value)}>
            <option value="cron">cron</option>
            <option value="webhook">webhook</option>
          </select>
          {tKind === 'cron' && <input value={tSpec} onChange={(e) => setTSpec(e.target.value)} placeholder="*/5 * * * *" />}
          <button onClick={addTrigger}>Add</button>
        </div>
        <table>
          <thead><tr><th>script</th><th>kind</th><th>spec / url</th><th></th></tr></thead>
          <tbody>
            {triggers.map((t) => (
              <tr key={t.id}>
                <td>{scriptName(scripts, t.script_id)}</td>
                <td>{t.kind}{t.enabled ? '' : ' (off)'}</td>
                <td className="mono" style={{ fontSize: 12 }}>
                  {t.kind === 'webhook' ? <a href={hookURL(t.spec)}>{hookURL(t.spec)}</a> : t.spec}
                </td>
                <td><button onClick={() => delTrigger(t.id)}>delete</button></td>
              </tr>
            ))}
            {triggers.length === 0 && <tr><td colSpan={4} className="muted">none</td></tr>}
          </tbody>
        </table>
      </div>

      {msg && <div className="toast">{msg}</div>}
    </div>
  )
}

function scriptName(scripts, id) {
  const s = scripts.find((x) => x.id === id)
  return s ? s.name : id
}
