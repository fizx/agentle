import React, { useState } from 'react'
import Scripts from './views/Scripts.jsx'
import Runs from './views/Runs.jsx'
import Settings from './views/Settings.jsx'

export default function App() {
  const [tab, setTab] = useState('scripts')
  const [focusExec, setFocusExec] = useState(null)

  const openRun = (execId) => { setFocusExec(execId); setTab('runs') }

  return (
    <>
      <div className="topbar">
        <span className="brand">agentle</span>
        <div className="tabs">
          {['scripts', 'runs', 'settings'].map((t) => (
            <button key={t} className={tab === t ? 'active' : ''} onClick={() => setTab(t)}>
              {t[0].toUpperCase() + t.slice(1)}
            </button>
          ))}
        </div>
        <span className="muted" style={{ marginLeft: 'auto', fontSize: 12 }}>
          Starlark agent platform · durable replay
        </span>
      </div>
      {tab === 'scripts' && <Scripts onOpenRun={openRun} />}
      {tab === 'runs' && <Runs focusExec={focusExec} clearFocus={() => setFocusExec(null)} />}
      {tab === 'settings' && <Settings />}
    </>
  )
}
