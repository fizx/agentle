import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { Chat, RunUI, ToolCall, UIMessage } from '../types'
import { ChatBubble, Thinking } from './UIPanel'

// AgentPanel docks an autonomous coding assistant beside the editor. Each tab is
// a durable chat — an execution of the seeded harness script bound to
// chat:{script}:{chat}. Every turn carries the live editor buffer as `source`.
//
// The agent has editor tools (read_source / apply_edit / run): the harness emits
// a tool batch (ui.pending_tools), this panel executes it client-side — reading
// the buffer, applying edits via onApply, running via onRun — and posts the
// results back, so the round-trip is durable + replay-safe. See PLAYTEST5.md.
export function AgentPanel({ scriptId, getSource, onApply, onRun, onClose, width, initialPrompt, onSeeded }: {
  scriptId: string
  getSource: () => string
  onApply: (source: string) => void
  onRun: (input: unknown) => Promise<string>
  onClose: () => void
  width?: number
  initialPrompt?: string | null
  onSeeded?: () => void
}) {
  const [chats, setChats] = useState<Chat[]>([])
  const [activeId, setActiveId] = useState<string>('')
  const [ui, setUI] = useState<RunUI | null>(null)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [renaming, setRenaming] = useState<string>('')
  const bottom = useRef<HTMLDivElement>(null)
  const handledBatch = useRef<string>('') // dedup: tool batches this panel already ran
  const runningTools = useRef(false)

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
  // Batches are numbered per-execution, so reset the dedup marker on tab switch.
  useEffect(() => { handledBatch.current = ''; runningTools.current = false }, [active?.exec_id])

  // Execute a pending editor-tool batch client-side, then post the results back.
  useEffect(() => {
    const pt = ui?.pending_tools
    if (!active || !pt || pt.batch === handledBatch.current || runningTools.current) return
    runningTools.current = true
    const execId = active.exec_id
    ;(async () => {
      const results = []
      for (const c of pt.calls) {
        results.push({ id: c.id, name: c.name, content: await runTool(c) })
      }
      handledBatch.current = pt.batch
      try { await api.postMessage(execId, { tool_results: results, batch: pt.batch }) } finally {
        runningTools.current = false
        poll()
      }
    })()
  }, [ui?.pending_tools?.batch, active?.exec_id]) // eslint-disable-line react-hooks/exhaustive-deps

  // runTool fulfils one editor tool call in the browser and returns its result
  // string (fed back to the model as the tool message content).
  const runTool = async (c: ToolCall): Promise<string> => {
    try {
      if (c.name === 'read_source') return getSource()
      if (c.name === 'apply_edit') {
        const src = String((c.arguments?.source as string) ?? '')
        if (!src.trim()) return 'error: apply_edit needs a non-empty "source" (the complete new file)'
        onApply(src)
        return 'applied — buffer is now ' + src.split('\n').length + ' lines'
      }
      if (c.name === 'run') return await onRun(c.arguments?.input ?? null)
      return 'error: unknown tool ' + c.name
    } catch (e) { return 'error: ' + (e as Error).message }
  }

  const done = ui ? ui.status === 1 || ui.status === 2 : false // completed | failed
  const runningTool = !!ui?.pending_tools
  const thinking = busy || runningTool || (!!ui && !done && !ui.awaiting)

  const send = async (override?: string) => {
    const t = (override ?? text).trim()
    if (!t || busy || !active || done) return
    if (override === undefined) setText('')
    setBusy(true)
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

  // Seed: when a script is created from a "describe what to build" prompt, send
  // it as the first message once the fresh chat is parked on recv (awaiting).
  const seeded = useRef(false)
  useEffect(() => { seeded.current = false }, [initialPrompt])
  useEffect(() => {
    if (!initialPrompt || seeded.current || busy || !active || done) return
    if (!ui?.awaiting || (ui?.transcript.length ?? 0) !== 0) return
    seeded.current = true
    onSeeded?.()
    send(initialPrompt)
  }, [initialPrompt, active?.exec_id, ui?.awaiting, ui?.transcript.length]) // eslint-disable-line react-hooks/exhaustive-deps

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
    <div className="agent-panel" style={width ? { width } : undefined}>
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
        {(ui?.transcript || []).map((m, i) =>
          m.role === 'tool'
            ? <ToolCard key={i} m={m} pendingBatch={ui?.pending_tools?.batch} />
            : <ChatBubble key={i} m={m} />)}
        {thinking && <Thinking />}
        {done && <div className="muted agent-ended">This chat ended. Open a new one with +.</div>}
        <div ref={bottom} />
      </div>

      <div className="agent-composer">
        <input
          placeholder={done ? 'Chat ended — open a new one (+)' : thinking ? 'Assistant is working…' : 'Message the assistant…'}
          value={text} onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') send() }}
          disabled={done || thinking} />
        {thinking
          ? <button onClick={stop} title="End this chat">Stop</button>
          : <button className="primary" onClick={() => send()} disabled={busy || done || !text.trim()}>Send</button>}
      </div>
    </div>
  )
}

// ToolCard renders an editor-tool batch (read_source / apply_edit / run) the agent
// requested. The batch still in ui.pending_tools is "running"; older ones are done.
const TOOL_ICON: Record<string, string> = { read_source: '👁', apply_edit: '✏️', run: '▶️' }
function ToolCard({ m, pendingBatch }: { m: UIMessage; pendingBatch?: string }) {
  const block = (m.blocks || []).find((b) => b.type === 'tool_calls')
  const calls: ToolCall[] = (block?.calls as ToolCall[]) || []
  const running = !!block?.batch && block.batch === pendingBatch
  return (
    <div className="agent-tool-card">
      {calls.map((c, i) => (
        <div key={i} className="agent-tool-row">
          <span className="agent-tool-name">{TOOL_ICON[c.name] || '🔧'} {c.name}</span>
          {c.name === 'apply_edit' && <span className="muted agent-tool-arg">edit buffer</span>}
          <span className={'agent-tool-status' + (running ? ' running' : '')}>{running ? '…' : '✓'}</span>
        </div>
      ))}
    </div>
  )
}
