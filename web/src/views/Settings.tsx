import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { ApiToken, ToolConfig } from '../types'

const CONFIG_TEMPLATES: Record<string, string> = {
  llm: '{\n  "base_url": "",\n  "model": "gpt-4o-mini"\n}',
  http: '{\n  "allow": ["api.github.com", "*.example.com"],\n  "auth_header": "Authorization"\n}',
  mcp: '{\n  "endpoint": ""\n}',
  shell: '{}',
}

export default function Settings() {
  const [secrets, setSecrets] = useState<string[]>([])
  const [configs, setConfigs] = useState<ToolConfig[]>([])
  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')

  const refresh = useCallback(async () => {
    setErr('')
    try { setSecrets((await api.listSecrets()).names || []) }
    catch (e) { setErr((e as Error).message) }
    setConfigs((await api.listConfigs()) || [])
  }, [])
  useEffect(() => { refresh() }, [refresh])

  const toast = (m: string) => { setMsg(m); setTimeout(() => setMsg(''), 2500) }

  const [sName, setSName] = useState('')
  const [sVal, setSVal] = useState('')
  const addSecret = async () => {
    if (!sName) return
    try { await api.putSecret(sName, sVal); setSName(''); setSVal(''); toast('secret saved'); refresh() }
    catch (e) { toast((e as Error).message) }
  }
  const delSecret = async (n: string) => { await api.deleteSecret(n); refresh() }

  const [cId, setCId] = useState('')
  const [cCap, setCCap] = useState('llm')
  const [cCfg, setCCfg] = useState(CONFIG_TEMPLATES.llm)
  const [cSecret, setCSecret] = useState('')
  const addConfig = async () => {
    if (!cId) return toast('config id required')
    let config: unknown
    try { config = JSON.parse(cCfg) } catch { return toast('config JSON invalid') }
    await api.putConfig({ id: cId, capability: cCap, config, secret_ref: cSecret })
    setCId(''); toast('config saved'); refresh()
  }
  const onCapChange = (cap: string) => { setCCap(cap); setCCfg(CONFIG_TEMPLATES[cap] || '{}') }
  const editConfig = (c: ToolConfig) => {
    setCId(c.id); setCCap(c.capability); setCSecret(c.secret_ref || '')
    setCCfg(JSON.stringify(c.config ?? {}, null, 2))
  }
  const delConfig = async (id: string) => {
    if (!confirm(`Delete config "${id}"? Scripts granting it will fail until re-granted.`)) return
    await api.deleteConfig(id); toast('config deleted'); refresh()
  }

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
                <option value="mcp">mcp</option>
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
          <thead><tr><th>id</th><th>capability</th><th>secret</th><th /></tr></thead>
          <tbody>
            {configs.map((c) => (
              <tr key={c.id}>
                <td className="mono">{c.id}</td>
                <td>{c.capability}</td>
                <td className="muted mono">
                  {c.secret_ref
                    ? <>{c.secret_ref}{c.secret_present === false && <span className="err" style={{ marginLeft: 6, fontSize: 11 }}>● not set</span>}</>
                    : '—'}
                </td>
                <td><a onClick={() => editConfig(c)}>edit</a> · <a onClick={() => delConfig(c.id)}>delete</a></td>
              </tr>
            ))}
            {configs.length === 0 && <tr><td colSpan={4} className="muted">none</td></tr>}
          </tbody>
        </table>
      </div>

      <ApiTokens toast={toast} />

      <div className="muted" style={{ fontSize: 12 }}>Triggers are managed per-script on the Scripts page.</div>
      {msg && <div className="toast">{msg}</div>}
    </div>
  )
}

// ApiTokens manages bearer tokens for the programmatic REST API (/v1). A new
// token's secret is shown once, here, and never again.
function ApiTokens({ toast }: { toast: (m: string) => void }) {
  const [tokens, setTokens] = useState<ApiToken[]>([])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState<ApiToken | null>(null)
  const refresh = useCallback(async () => { setTokens((await api.listTokens()) || []) }, [])
  useEffect(() => { refresh() }, [refresh])

  const create = async () => {
    try {
      const tok = await api.createToken(name || 'token')
      setFresh(tok); setName(''); refresh()
    } catch (e) { toast((e as Error).message) }
  }
  const del = async (id: string) => { await api.deleteToken(id); if (fresh?.id === id) setFresh(null); refresh() }

  return (
    <div className="card">
      <h2>API tokens</h2>
      <div className="muted" style={{ marginBottom: 8 }}>
        Bearer tokens for the programmatic REST API (<span className="mono">/v1</span>). A token carries
        your role; use it as <span className="mono">Authorization: Bearer &lt;token&gt;</span>. The secret is shown once.
      </div>
      <div className="row" style={{ marginBottom: 10 }}>
        <input placeholder="token name (e.g. ci)" value={name} onChange={(e) => setName(e.target.value)} />
        <button onClick={create}>Create token</button>
      </div>
      {fresh?.token && (
        <div className="card" style={{ marginBottom: 10 }}>
          <div className="muted" style={{ fontSize: 12, marginBottom: 4 }}>Copy this now — it won't be shown again:</div>
          <code className="mono" style={{ cursor: 'pointer', wordBreak: 'break-all' }}
            title="copy" onClick={() => { navigator.clipboard?.writeText(fresh.token || ''); toast('token copied') }}>
            {fresh.token}
          </code>
          <pre className="muted" style={{ fontSize: 11, marginTop: 8, whiteSpace: 'pre-wrap' }}>
{`curl -s -X POST $ORIGIN/v1/scripts/<id>/runs \\
  -H "Authorization: Bearer ${fresh.token}" \\
  -d '{"input": {"name": "kyle"}}'`}
          </pre>
        </div>
      )}
      <table>
        <thead><tr><th>name</th><th>id</th><th>last used</th><th /></tr></thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              <td>{t.name || '—'}</td>
              <td className="mono muted">{t.id.slice(0, 12)}</td>
              <td className="muted">{t.last_used_at ? new Date(t.last_used_at / 1e6).toLocaleString() : 'never'}</td>
              <td><button onClick={() => del(t.id)}>revoke</button></td>
            </tr>
          ))}
          {tokens.length === 0 && <tr><td colSpan={4} className="muted">no tokens</td></tr>}
        </tbody>
      </table>
    </div>
  )
}
