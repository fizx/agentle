import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { Plugin } from '../types'
import { CodeEditor } from '../components/CodeEditor'

const RUNTIMES = ['python', 'node', 'bash']

const STARTER = `import sys, json
cmd = sys.argv[1] if len(sys.argv) > 1 else ""
if cmd == "list":
    print(json.dumps([
        {"name": "reverse", "description": "reverse text",
         "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}},
    ]))
elif cmd == "call":
    args = json.loads(sys.argv[3]) if len(sys.argv) > 3 else {}
    print(args.get("text", "")[::-1])
`

// Plugins is the admin view for managing capability plugins: small sandboxed
// programs that provide MCP tools. Grant one to a script via an mcp tool config
// with {"plugin_id": "<id>"}.
export default function Plugins() {
  const [plugins, setPlugins] = useState<Plugin[]>([])
  const [sel, setSel] = useState<Plugin | null>(null)
  const [name, setName] = useState('')
  const [runtime, setRuntime] = useState('python')
  const [source, setSource] = useState(STARTER)
  const [msg, setMsg] = useState('')

  const refresh = useCallback(async () => { setPlugins((await api.listPlugins()) || []) }, [])
  useEffect(() => { refresh() }, [refresh])
  const toast = (m: string) => { setMsg(m); setTimeout(() => setMsg(''), 2500) }

  const edit = (p: Plugin) => { setSel(p); setName(p.name); setRuntime(p.runtime); setSource(p.source) }
  const reset = () => { setSel(null); setName(''); setRuntime('python'); setSource(STARTER) }

  const save = async () => {
    if (!name) return toast('name required')
    const p = await api.putPlugin({ id: sel?.id, name, runtime, source, enabled: sel ? sel.enabled : true })
    setSel(p); toast('plugin saved'); refresh()
  }
  const toggle = async (p: Plugin) => { await api.putPlugin({ ...p, enabled: !p.enabled }); refresh() }
  const del = async (p: Plugin) => {
    if (!confirm(`Delete plugin "${p.name}"?`)) return
    await api.deletePlugin(p.id); if (sel?.id === p.id) reset(); refresh()
  }

  return (
    <div className="layout">
      <div className="sidebar">
        <div className="row spread" style={{ marginBottom: 8 }}>
          <strong>Plugins</strong>
          <button onClick={reset}>+ New</button>
        </div>
        {plugins.map((p) => (
          <div key={p.id} className={'list-item' + (p.id === sel?.id ? ' active' : '')} onClick={() => edit(p)}>
            <div>{p.name}{p.enabled ? '' : ' (off)'}</div>
            <div className="muted mono" style={{ fontSize: 11 }}>{p.runtime} · {p.id.slice(0, 10)}</div>
          </div>
        ))}
        {plugins.length === 0 && <div className="muted">No plugins yet.</div>}
      </div>

      <div className="main">
        <div className="card">
          <div className="muted" style={{ marginBottom: 8 }}>
            A plugin is a sandboxed program providing MCP tools. Convention: <span className="mono">argv[1]="list"</span> prints
            the tool catalog as JSON; <span className="mono">"call"</span> with <span className="mono">argv[2]=tool</span> and
            <span className="mono"> argv[3]=args-JSON</span> prints the result. Grant it to a script with an <span className="mono">mcp</span> tool
            config whose JSON is <span className="mono">{'{'}"plugin_id": "&lt;id&gt;"{'}'}</span>.
          </div>
          <div className="row" style={{ gap: 8, marginBottom: 8 }}>
            <input placeholder="plugin name" value={name} onChange={(e) => setName(e.target.value)} />
            <select value={runtime} onChange={(e) => setRuntime(e.target.value)}>
              {RUNTIMES.map((r) => <option key={r} value={r}>{r}</option>)}
            </select>
            <button className="primary" onClick={save}>{sel ? 'Save' : 'Create'}</button>
            {sel && <span className="mono muted" style={{ alignSelf: 'center' }}>{sel.id}</span>}
          </div>
          <CodeEditor value={source} onChange={setSource} names={[]} />
        </div>

        {plugins.length > 0 && (
          <div className="card">
            <h3>All plugins</h3>
            <table>
              <thead><tr><th>name</th><th>runtime</th><th>id</th><th>enabled</th><th /></tr></thead>
              <tbody>
                {plugins.map((p) => (
                  <tr key={p.id}>
                    <td>{p.name}</td>
                    <td>{p.runtime}</td>
                    <td className="mono muted">{p.id}</td>
                    <td><a onClick={() => toggle(p)}>{p.enabled ? 'on' : 'off'}</a></td>
                    <td><a onClick={() => edit(p)}>edit</a> · <a onClick={() => del(p)}>delete</a></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {msg && <div className="toast">{msg}</div>}
      </div>
    </div>
  )
}
