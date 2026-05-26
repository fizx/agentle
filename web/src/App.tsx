import { useCallback, useEffect, useState } from 'react'
import Apps from './views/Apps'
import Scripts from './views/Scripts'
import Runs from './views/Runs'
import Evals from './views/Evals'
import Settings from './views/Settings'
import Spend from './views/Spend'
import Plugins from './views/Plugins'
import Users from './views/Users'
import Docs from './views/Docs'
import { api, setUserId } from './api'
import type { User } from './types'

type Tab = 'scripts' | 'apps' | 'runs' | 'evals' | 'spend' | 'settings' | 'plugins' | 'users' | 'docs'
const ALL_TABS: Tab[] = ['scripts', 'apps', 'runs', 'evals', 'spend', 'settings', 'plugins', 'users', 'docs']

// The active tab (and, for runs, the focused execution) lives in the URL hash so
// the browser back/forward buttons navigate between views. e.g. "#runs/ex_123".
function parseHash(): { tab: Tab; exec: string | null } {
  const raw = location.hash.replace(/^#\/?/, '')
  const [seg, sub] = raw.split('/')
  const tab = (ALL_TABS as string[]).includes(seg) ? (seg as Tab) : 'scripts'
  return { tab, exec: tab === 'runs' && sub ? sub : null }
}

export default function App() {
  const [{ tab, exec }, setRoute] = useState(parseHash)
  const [me, setMe] = useState<User | null>(null)
  const [users, setUsers] = useState<User[]>([])

  const loadIdentity = useCallback(async () => {
    try {
      setMe(await api.me())
      setUsers((await api.listUsers()) || [])
    } catch { /* server may be starting */ }
  }, [])
  useEffect(() => { loadIdentity() }, [loadIdentity])

  useEffect(() => {
    if (!location.hash) history.replaceState(null, '', '#scripts') // default landing = scripts
    const onHash = () => setRoute(parseHash())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  // navigate pushes a history entry (setting location.hash fires hashchange).
  const go = (t: Tab, e?: string) => { location.hash = e ? `${t}/${e}` : t }
  const switchUser = (id: string) => { setUserId(id); window.location.reload() }

  const isAdmin = me?.role === 'admin'
  const tabs: Tab[] = isAdmin
    ? ['scripts', 'apps', 'runs', 'evals', 'spend', 'settings', 'plugins', 'users', 'docs']
    : ['scripts', 'apps', 'runs', 'evals', 'spend', 'settings', 'docs']

  return (
    <>
      <div className="topbar">
        <span className="brand">agentle</span>
        <div className="tabs">
          {tabs.map((t) => (
            <button key={t} className={tab === t ? 'active' : ''} onClick={() => go(t)}>
              {t[0].toUpperCase() + t.slice(1)}
            </button>
          ))}
        </div>
        <div className="row" style={{ marginLeft: 'auto', gap: 8 }}>
          <span className="muted" style={{ fontSize: 12 }}>acting as</span>
          <select value={me?.id ?? ''} onChange={(e) => switchUser(e.target.value)}>
            {me && !users.find((u) => u.id === me.id) && <option value={me.id}>{me.name} ({me.role})</option>}
            {users.map((u) => <option key={u.id} value={u.id}>{u.name} ({u.role})</option>)}
          </select>
        </div>
      </div>
      {tab === 'apps' && <Apps />}
      {tab === 'scripts' && <Scripts onOpenRun={(id) => go('runs', id)} />}
      {tab === 'runs' && <Runs focusExec={exec} onSelect={(id) => go('runs', id)} />}
      {tab === 'evals' && <Evals onOpenRun={(id) => go('runs', id)} />}
      {tab === 'spend' && <Spend />}
      {tab === 'settings' && <Settings />}
      {tab === 'plugins' && <Plugins />}
      {tab === 'users' && <Users onChange={loadIdentity} me={me} />}
      {tab === 'docs' && <Docs />}
    </>
  )
}
