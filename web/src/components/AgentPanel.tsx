import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { Chat, RunUI } from '../types'
import { ChatBubble, Thinking } from './UIPanel'

// AgentPanel docks an autonomous coding assistant beside the editor. Each tab is
// a durable chat — an execution of the seeded harness script (PLAYTEST5 backend
// (A)) bound to chat:{script}:{chat}. Every turn carries the live editor buffer
// as `source`, so the agent reasons over the current code; switching tabs loads
// that chat's transcript and reopening resumes it.
export function AgentPanel({ scriptId, getSource, onClose }: {
  scriptId: string
  getSource: () => string
  onClose: () => void
}) {
  const [chats, setChats] = useState<Chat[]>([])
  const [activeId, setActiveId] = useState<string>('')
  const [ui, setUI] = useState<RunUI | null>(null)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [renaming, setRenaming] = useState<string>('')
  const bottom = useRef<HTMLDivElement>(null)

  const active = chats.find((c) => c.id === activeId) || null

  const loadChats = useCallback(async () => {
    const cs = (await api.listChats(scriptId)) || []
    setChats(cs)
    return cs
  }, [scriptId])

  // On script change: load this script's chats; select the first, or open one.
  useEffect(() => {
    let cancelled = false
    ;(async () => {
      const cs = await loadChats()
      if (cancelled) return
      if (cs.length) {
        setActiveId((cur) => (cs.some((c) => c.id === cur) ? cur : cs[0].id))
      } else {
        const c = await api.createChat(scriptId)
        if (!cancelled) { setChats([c]); setActiveId(c.id) }
      }
    })()
    return () => { cancelled = true }
  }, [scriptId, loadChats])

  // Poll the active chat's UI projection (transcript + status).
  const poll = useCallback(async () => {
    if (!active) return
    try { setUI(await api.getUI(active.exec_id)) } catch { /* transient */ }
  }, [active?.exec_id])
  useEffect(() => {
    setUI(null); poll()
    const t = setInterval(poll, 900)
    return () => clearInterval(t)
  }, [poll])
  useEffect(() => { bottom.current?.scrollIntoView({ behavior: 'smooth' }) }, [ui?.transcript.length, ui?.awaiting, activeId])

  const done = ui ? ui.status === 1 || ui.status === 2 : false // completed | failed
  const thinking = busy || (!!ui && !done && !ui.awaiting)

  const send = async () => {
    if (!text.trim() || busy || !active || done) return
    const t = text; setText(''); setBusy(true)
    const firstTurn = (ui?.transcript.length ?? 0) === 0
    try {
      await api.postMessage(active.exec_id, { text: t, source: getSource() })
      // Auto-title the chat from its first message.
      if (firstTurn && (!active.title || active.title === 'New chat')) {
        await api.renameChat(active.id, t.length > 40 ? t.slice(0, 40) + '…' : t)
        loadChats()
      }
      await poll()
    } finally { setBusy(false) }
  }

  // Stop ends the chat's harness loop (it breaks on /quit once the current turn
  // returns to recv). The durable run stays in history.
  const stop = async () => { if (active) { await api.postMessage(active.exec_id, { text: '/quit' }); await poll() } }

  const newChat = async () => {
    const c = await api.createChat(scriptId)
    setChats((cs) => [...cs, c]); setActiveId(c.id)
  }
  const closeChat = async (id: string) => {
    await api.deleteChat(id)
    const cs = await loadChats()
    if (id === activeId) setActiveId(cs[0]?.id || '')
  }
  const commitRename = async (id: string, title: string) => {
    setRenaming('')
    if (title.trim()) { await api.renameChat(id, title.trim()); loadChats() }
  }

  return (
    <div className="agent-panel">
      <div className="agent-tabs">
        {chats.map((c) => (
          <div key={c.id} className={'agent-tab' + (c.id === activeId ? ' active' : '')} onClick={() => setActiveId(c.id)}>
            {renaming === c.id ? (
              <input autoFocus className="agent-tab-edit" defaultValue={c.title}
                onClick={(e) => e.stopPropagation()}
                onBlur={(e) => commitRename(c.id, e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') commitRename(c.id, (e.target as HTMLInputElement).value)
                  if (e.key === 'Escape') setRenaming('')
                }} />
            ) : (
              <span className="agent-tab-name" onDoubleClick={() => setRenaming(c.id)} title="double-click to rename">
                {c.title || 'chat'}
              </span>
            )}
            <a className="agent-tab-x" title="Close chat" onClick={(e) => { e.stopPropagation(); closeChat(c.id) }}>×</a>
          </div>
        ))}
        <button className="agent-tab-add" onClick={newChat} title="New chat">+</button>
        <div style={{ flex: 1 }} />
        <button className="agent-close" onClick={onClose} title="Hide assistant">✕</button>
      </div>

      <div className="agent-body chat-log">
        {(ui?.transcript.length ?? 0) === 0 && !thinking && (
          <div className="muted agent-empty">
            Ask the assistant to write or explain this script. The current editor contents are sent with every message.
          </div>
        )}
        {(ui?.transcript || []).map((m, i) => <ChatBubble key={i} m={m} />)}
        {thinking && <Thinking />}
        {done && <div className="muted agent-ended">This chat ended. Open a new one with +.</div>}
        <div ref={bottom} />
      </div>

      <div className="agent-composer">
        <input
          placeholder={done ? 'Chat ended — open a new one (+)' : ui?.awaiting ? 'Message the assistant…' : 'Assistant is working…'}
          value={text} onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') send() }}
          disabled={done || (!ui?.awaiting && !text)} />
        {thinking
          ? <button onClick={stop} title="End this chat">Stop</button>
          : <button className="primary" onClick={send} disabled={busy || done || !ui?.awaiting}>Send</button>}
      </div>
    </div>
  )
}
