import { useCallback, useEffect, useState } from 'react'
import Scripts from './views/Scripts'
import Runs from './views/Runs'
import Settings from './views/Settings'
import Spend from './views/Spend'
import Users from './views/Users'
import { api, setUserId } from './api'
import type { User } from './types'

type Tab = 'scripts' | 'runs' | 'spend' | 'settings' | 'users'

export default function App() {
  const [tab, setTab] = useState<Tab>('scripts')
  const [focusExec, setFocusExec] = useState<string | null>(null)
  const [me, setMe] = useState<User | null>(null)
  const [users, setUsers] = useState<User[]>([])

  const loadIdentity = useCallback(async () => {
    try {
      setMe(await api.me())
      setUsers((await api.listUsers()) || [])
    } catch { /* server may be starting */ }
  }, [])
  useEffect(() => { loadIdentity() }, [loadIdentity])

  const openRun = (execId: string) => { setFocusExec(execId); setTab('runs') }
  const switchUser = (id: string) => { setUserId(id); window.location.reload() }

  const isAdmin = me?.role === 'admin'
  const tabs: Tab[] = isAdmin
    ? ['scripts', 'runs', 'spend', 'settings', 'users']
    : ['scripts', 'runs', 'spend', 'settings']

  return (
    <>
      <div className="topbar">
        <span className="brand">agentle</span>
        <div className="tabs">
          {tabs.map((t) => (
            <button key={t} className={tab === t ? 'active' : ''} onClick={() => setTab(t)}>
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
      {tab === 'scripts' && <Scripts onOpenRun={openRun} />}
      {tab === 'runs' && <Runs focusExec={focusExec} clearFocus={() => setFocusExec(null)} />}
      {tab === 'spend' && <Spend />}
      {tab === 'settings' && <Settings />}
      {tab === 'users' && <Users onChange={loadIdentity} me={me} />}
    </>
  )
}
