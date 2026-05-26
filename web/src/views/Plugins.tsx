import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { Plugin, PluginVersion } from '../types'
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

// Plugins is the admin view for managing capability plugins: small programs that
// provide MCP tools. A "script" plugin is sandboxed source you edit here; a
// "native" plugin is implemented in Go (listed but not editable). Grant either to
// a script via an mcp tool config with {"plugin_id": "<id>"}.
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

  const isNative = sel?.kind === 'native'

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
  const onRestored = (p: Plugin) => { edit(p); toast('restored as v' + p.current_version); refresh() }

  return (
    <div className="layout">
      <div className="sidebar">
        <div className="row spread" style={{ marginBottom: 8 }}>
          <strong>Plugins</strong>
          <button onClick={reset}>+ New</button>
        </div>
        {plugins.map((p) => (
          <div key={p.id} className={'list-item' + (p.id === sel?.id ? ' active' : '')} onClick={() => edit(p)}>
            <div className="row spread">
              <span>{p.name}{p.enabled ? '' : ' (off)'}</span>
              {p.kind === 'native' && <span className="badge running" style={{ fontSize: 10 }}>native</span>}
            </div>
            <div className="muted mono" style={{ fontSize: 11 }}>{p.runtime} · v{p.current_version} · {p.id.slice(0, 10)}</div>
          </div>
        ))}
        {plugins.length === 0 && <div className="muted">No plugins yet.</div>}
      </div>

      <div className="main">
        <div className="card">
          <div className="muted" style={{ marginBottom: 8 }}>
            A plugin is a program providing MCP tools. Script plugins follow a convention: <span className="mono">argv[1]="list"</span> prints
            the tool catalog as JSON; <span className="mono">"call"</span> with <span className="mono">argv[2]=tool</span> and
            <span className="mono"> argv[3]=args-JSON</span> prints the result. <b>Native</b> plugins are implemented in Go and run in-process —
            they appear here but aren't editable. Grant either to a script with an <span className="mono">mcp</span> tool
            config whose JSON is <span className="mono">{'{'}"plugin_id": "&lt;id&gt;"{'}'}</span>.
          </div>
          {isNative ? (
            <NativePluginView plugin={sel!} />
          ) : (
            <>
              <div className="row" style={{ gap: 8, marginBottom: 8 }}>
                <input placeholder="plugin name" value={name} onChange={(e) => setName(e.target.value)} />
                <select value={runtime} onChange={(e) => setRuntime(e.target.value)}>
                  {RUNTIMES.map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
                <button className="primary" onClick={save}>{sel ? 'Save' : 'Create'}</button>
                {sel && <span className="mono muted" style={{ alignSelf: 'center' }}>{sel.id} · v{sel.current_version}</span>}
              </div>
              <CodeEditor value={source} onChange={setSource} names={[]} />
              {sel && <PluginVersions plugin={sel} onRestored={onRestored} />}
            </>
          )}
        </div>

        {plugins.length > 0 && (
          <div className="card">
            <h3>All plugins</h3>
            <table>
              <thead><tr><th>name</th><th>kind</th><th>runtime</th><th>version</th><th>id</th><th>enabled</th><th /></tr></thead>
              <tbody>
                {plugins.map((p) => (
                  <tr key={p.id}>
                    <td>{p.name}</td>
                    <td>{p.kind === 'native' ? <span className="badge running">native</span> : 'script'}</td>
                    <td>{p.runtime}</td>
                    <td className="mono">v{p.current_version}</td>
                    <td className="mono muted">{p.id}</td>
                    <td><a onClick={() => toggle(p)}>{p.enabled ? 'on' : 'off'}</a></td>
                    <td>
                      <a onClick={() => edit(p)}>{p.kind === 'native' ? 'view' : 'edit'}</a>
                      {p.kind !== 'native' && <> · <a onClick={() => del(p)}>delete</a></>}
                    </td>
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

// PluginVersions shows the save history of a script plugin and lets you roll back
// — the same model as script versions.
function PluginVersions({ plugin, onRestored }: { plugin: Plugin; onRestored: (p: Plugin) => void }) {
  const [versions, setVersions] = useState<PluginVersion[]>([])
  const load = useCallback(() => { api.listPluginVersions(plugin.id).then((v) => setVersions(v || [])) }, [plugin.id, plugin.current_version])
  useEffect(() => { load() }, [load])
  const restore = async (v: number) => onRestored(await api.restorePluginVersion(plugin.id, v))
  if (versions.length === 0) return null
  return (
    <div style={{ marginTop: 14 }}>
      <h3>Versions</h3>
      <table>
        <tbody>
          {versions.map((v) => (
            <tr key={v.version}>
              <td className="mono">v{v.version}</td>
              <td className="muted">{v.runtime}</td>
              <td className="muted">{new Date(v.created_at / 1e6).toLocaleString()}</td>
              <td>{v.version !== plugin.current_version && <button onClick={() => restore(v.version)}>restore</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// NativePluginView is the read-only inspector for a Go native plugin: it has no
// editable source, so we just show its identity and a note that it lives in code.
function NativePluginView({ plugin }: { plugin: Plugin }) {
  return (
    <div>
      <div className="row" style={{ gap: 8, marginBottom: 8, alignItems: 'center' }}>
        <strong>{plugin.name}</strong>
        <span className="badge running">native</span>
        <span className="mono muted">{plugin.id} · v{plugin.current_version}</span>
      </div>
      <div className="muted" style={{ fontSize: 13 }}>
        This plugin is implemented in Go and runs in-process. Its tools are defined in code, so there's nothing to edit here.
        It's still grantable to scripts via its <span className="mono">mcp-{plugin.id}</span> tool config, and can be toggled on/off.
      </div>
    </div>
  )
}
