import React, { useState, useEffect, useCallback } from 'react'
import Scripts from './views/Scripts.jsx'
import Runs from './views/Runs.jsx'
import Settings from './views/Settings.jsx'
import Users from './views/Users.jsx'
import { api, setUserId } from './api.js'

export default function App() {
  const [tab, setTab] = useState('scripts')
  const [focusExec, setFocusExec] = useState(null)
  const [me, setMe] = useState(null)
  const [users, setUsers] = useState([])

  const loadIdentity = useCallback(async () => {
    try {
      setMe(await api.me())
      setUsers(await api.listUsers() || [])
    } catch { /* server may be starting */ }
  }, [])
  useEffect(() => { loadIdentity() }, [loadIdentity])

  const openRun = (execId) => { setFocusExec(execId); setTab('runs') }
  const switchUser = (id) => { setUserId(id); window.location.reload() }

  const isAdmin = me && me.role === 'admin'
  const tabs = ['scripts', 'runs', 'settings']
  if (isAdmin) tabs.push('users')

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
          <select value={me ? me.id : ''} onChange={(e) => switchUser(e.target.value)}>
            {me && !users.find((u) => u.id === me.id) && <option value={me.id}>{me.name} ({me.role})</option>}
            {users.map((u) => <option key={u.id} value={u.id}>{u.name} ({u.role})</option>)}
          </select>
        </div>
      </div>
      {tab === 'scripts' && <Scripts onOpenRun={openRun} me={me} />}
      {tab === 'runs' && <Runs focusExec={focusExec} clearFocus={() => setFocusExec(null)} />}
      {tab === 'settings' && <Settings />}
      {tab === 'users' && <Users onChange={loadIdentity} />}
    </>
  )
}
