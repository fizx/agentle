import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { AppInfo } from '../types'
import { UIPanel } from '../components/UIPanel'

// Apps is the friendly launcher: it lists scripts that render an interactive UI
// (auto-detected from ui_chat/ui_form) and runs one with a single click, opening
// its panel in full app mode. No editor, no JSON — just pick an app and use it.
export default function Apps() {
  const [apps, setApps] = useState<AppInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [launching, setLaunching] = useState<string | null>(null)
  const [running, setRunning] = useState<{ execId: string; name: string } | null>(null)
  const [err, setErr] = useState('')

  const refresh = useCallback(async () => {
    setLoading(true)
    try { setApps((await api.apps()) || []) } catch (e) { setErr((e as Error).message) } finally { setLoading(false) }
  }, [])
  useEffect(() => { refresh() }, [refresh])

  const launch = async (app: AppInfo) => {
    setLaunching(app.script_id); setErr('')
    try {
      const exe = await api.run(app.script_id, null)
      setRunning({ execId: exe.id, name: app.title || app.name })
    } catch (e) { setErr((e as Error).message) } finally { setLaunching(null) }
  }

  if (running) {
    return (
      <div className="main app-main">
        <UIPanel execId={running.execId} appMode onClose={() => { setRunning(null); refresh() }} />
      </div>
    )
  }

  return (
    <div className="main">
      <div className="row spread" style={{ marginBottom: 12 }}>
        <h2>Apps</h2>
        <button onClick={refresh}>Refresh</button>
      </div>
      {err && <div className="card err">{err}</div>}
      {loading && <div className="muted">Loading…</div>}
      {!loading && apps.length === 0 && (
        <div className="card">
          <div className="muted">No apps yet.</div>
          <div className="muted" style={{ marginTop: 6, fontSize: 13 }}>
            Any script that calls <span className="mono">ui_chat()</span> or <span className="mono">ui_form()</span> shows up here.
            Create one from the <strong>Chat assistant</strong> or <strong>Form UI</strong> example under <strong>Scripts → + New</strong>.
          </div>
        </div>
      )}
      <div className="app-grid">
        {apps.map((a) => (
          <div key={a.script_id} className="app-card">
            <div className="app-card-icon">{a.kind === 'form' ? '📝' : '💬'}</div>
            <div className="app-card-body">
              <strong>{a.title || a.name}</strong>
              <div className="muted" style={{ fontSize: 12, marginTop: 2 }}>
                {a.intro || (a.kind === 'form' ? 'A form to fill in.' : 'An interactive chat.')}
              </div>
              <div className="row" style={{ marginTop: 4, gap: 6 }}>
                <span className="badge suspended mono">{a.kind}</span>
                <span className="muted" style={{ fontSize: 11 }}>{a.name} · v{a.version}</span>
              </div>
            </div>
            <button className="primary" disabled={launching === a.script_id} onClick={() => launch(a)}>
              {launching === a.script_id ? 'Opening…' : 'Open'}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
