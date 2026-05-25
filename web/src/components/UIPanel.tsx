import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { RunUI, UIBlock, UIField, UIMessage, UIPanelDesc } from '../types'
import { Modal } from './Modal'

// UIPanel renders an interactive run's panel stack: it polls the run's UI
// projection, sends user input/form submissions to the run's workspace, and
// re-polls. Closes when the run completes (or the user dismisses it).
//
// Panels are a stack: a base chat with forms modal over it (ui_form), matching
// the script's ui_chat/ui_form calls. Two presentations: the default modal (from
// the Scripts editor) and a full-bleed "app mode" (from the Apps tab).
export function UIPanel({ execId, onClose, appMode = false }: { execId: string; onClose: () => void; appMode?: boolean }) {
  const [ui, setUI] = useState<RunUI | null>(null)
  const [text, setText] = useState('')
  const [form, setForm] = useState<Record<string, unknown>>({})
  const [busy, setBusy] = useState(false)
  const bottom = useRef<HTMLDivElement>(null)

  const poll = useCallback(async () => {
    try { setUI(await api.getUI(execId)) } catch { /* transient */ }
  }, [execId])

  useEffect(() => {
    poll()
    const t = setInterval(poll, 700)
    return () => clearInterval(t)
  }, [poll])
  useEffect(() => { bottom.current?.scrollIntoView({ behavior: 'smooth' }) }, [ui?.transcript.length, ui?.awaiting])

  const done = ui ? ui.status === 1 || ui.status === 2 : false // completed | failed
  const failed = ui?.status === 2

  const stack: UIPanelDesc[] = ui?.panels ?? []
  const chat = stack.find((p) => p.kind === 'chat')
  const top = stack.length ? stack[stack.length - 1] : undefined
  const activeForm = top?.kind === 'form' ? top : undefined // form modal over the chat
  const soloForm = !chat && activeForm ? activeForm : undefined // a form with no chat beneath
  // The script has our input and is working (parked on neither chat nor form recv).
  const thinking = !!ui && !done && !ui.awaiting

  const sendChat = async () => {
    if (!text.trim() || busy) return
    const t = text; setText(''); setBusy(true)
    try { await api.postMessage(execId, { text: t }); await poll() } finally { setBusy(false) }
  }
  const submitForm = async () => {
    setBusy(true)
    try { await api.postMessage(execId, form); setForm({}); await poll() } finally { setBusy(false) }
  }

  const title = chat?.title || top?.title || (soloForm ? 'Form' : 'Chat')

  const transcript = (
    <div className={appMode ? 'app-log' : 'chat-log'}>
      {(ui?.transcript.length ?? 0) === 0 && !thinking && (
        <div className="muted app-empty">{soloForm ? 'Fill in the form below.' : 'Say hello to get started.'}</div>
      )}
      {(ui?.transcript || []).map((m, i) => <ChatBubble key={i} m={m} />)}
      {thinking && !activeForm && chat && <Thinking />}
      <div ref={bottom} />
    </div>
  )

  // Chat composer (only when a chat panel exists). Disabled while a form is modal
  // over it, since the form holds the active suspension.
  const chatInput = chat && !done && (
    <div className={appMode ? 'app-composer' : 'row'} style={appMode ? undefined : { marginTop: 8, gap: 6 }}>
      <input
        autoFocus placeholder={activeForm ? 'Finish the form above…' : ui?.awaiting ? 'Type a message…' : 'Assistant is thinking…'}
        value={text} onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') sendChat() }} style={{ flex: 1 }}
        disabled={!!activeForm || (!ui?.awaiting && !text)}
      />
      <button className="primary" onClick={sendChat} disabled={busy || !ui?.awaiting || !!activeForm}>Send</button>
    </div>
  )

  const formFields = (panel: UIPanelDesc) => (
    <FormFields fields={panel.fields || []} values={form} onChange={setForm} onSubmit={submitForm} busy={busy || !ui?.awaiting} />
  )

  const ended = done && (
    <div className={'app-ended ' + (failed ? 'err' : 'muted')}>
      {failed ? 'This session ended with an error.' : 'Session ended.'} <a onClick={onClose}>close</a>
    </div>
  )

  // A form modal over the chat (the "stack" UX). Rendered for app + modal mode.
  const formOverlay = activeForm && chat && !done && (
    <div className="ui-overlay">
      <div className="ui-overlay-card">
        <div className="app-title" style={{ marginBottom: 8 }}>{activeForm.title || 'Form'}</div>
        {activeForm.intro && <div className="muted" style={{ marginBottom: 8 }}>{activeForm.intro}</div>}
        {formFields(activeForm)}
      </div>
    </div>
  )

  if (appMode) {
    return (
      <div className="app-shell">
        <div className="app-header">
          <div>
            <div className="app-title">{title}</div>
            {(chat?.intro || (soloForm && soloForm.intro)) && <div className="muted app-intro">{chat?.intro || soloForm?.intro}</div>}
          </div>
          <div className="row" style={{ gap: 10 }}>
            <StatusDot done={done} failed={failed} awaiting={!!ui?.awaiting} thinking={thinking} />
            <button onClick={onClose}>Close</button>
          </div>
        </div>
        <div className="app-body">
          {(chat || (ui?.transcript.length ?? 0) > 0) && transcript}
          {soloForm && !done && formFields(soloForm)}
          {ended}
        </div>
        {chatInput}
        {formOverlay}
      </div>
    )
  }

  return (
    <Modal title={title} onClose={onClose}>
      {(chat?.intro || soloForm?.intro) && <div className="muted" style={{ marginBottom: 8 }}>{chat?.intro || soloForm?.intro}</div>}
      {(ui?.transcript.length ?? 0) > 0 && (
        <div className="chat-log">
          {ui!.transcript.map((m, i) => <ChatBubble key={i} m={m} />)}
          {thinking && !activeForm && chat && <Thinking />}
          <div ref={bottom} />
        </div>
      )}
      {chatInput}
      {soloForm && !done && formFields(soloForm)}
      {ended}
      {formOverlay}
    </Modal>
  )
}

// StatusDot is the live indicator in the app header.
function StatusDot({ done, failed, awaiting, thinking }: { done: boolean; failed: boolean; awaiting: boolean; thinking: boolean }) {
  const [cls, label] = done
    ? (failed ? ['failed', 'ended'] : ['ended', 'ended'])
    : thinking ? ['thinking', 'working…'] : awaiting ? ['live', 'ready'] : ['live', 'live']
  return <span className={'app-status ' + cls}><i /> {label}</span>
}

// Thinking is the animated "assistant is typing" bubble.
export function Thinking() {
  return (
    <div className="chat-msg them thinking-msg">
      <div className="typing"><span /><span /><span /></div>
    </div>
  )
}

export function ChatBubble({ m }: { m: UIMessage }) {
  const mine = m.role === 'user'
  return (
    <div className={'chat-msg ' + (mine ? 'me' : 'them')}>
      <div className="chat-role muted">{m.role}</div>
      <div className="chat-text" dangerouslySetInnerHTML={{ __html: mdInline(m.text || '') }} />
      {(m.blocks || []).map((b, i) => <Block key={i} b={b} />)}
    </div>
  )
}

function Block({ b }: { b: UIBlock }) {
  if (b.type === 'code') return <pre className="mono">{b.text}</pre>
  if (b.type === 'image' && b.url) return <img src={b.url} alt={b.alt || ''} style={{ maxWidth: '100%' }} />
  if (b.type === 'table') return (
    <table>
      <thead><tr>{(b.columns || []).map((c, i) => <th key={i}>{c}</th>)}</tr></thead>
      <tbody>{(b.rows || []).map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>)}</tbody>
    </table>
  )
  return null
}

function FormFields({ fields, values, onChange, onSubmit, busy }: {
  fields: UIField[]; values: Record<string, unknown>
  onChange: (v: Record<string, unknown>) => void; onSubmit: () => void; busy: boolean
}) {
  const set = (k: string, v: unknown) => onChange({ ...values, [k]: v })
  const missingRequired = fields.some((f) => f.required && !values[f.name])
  return (
    <div className="app-form" style={{ marginTop: 8 }}>
      {fields.map((f) => (
        <div key={f.name} style={{ marginBottom: 8 }}>
          <label>{f.label || f.name}{f.required ? ' *' : ''}</label>
          {f.type === 'textarea' ? (
            <textarea value={String(values[f.name] ?? '')} onChange={(e) => set(f.name, e.target.value)} style={{ width: '100%' }} />
          ) : f.type === 'select' ? (
            <select value={String(values[f.name] ?? '')} onChange={(e) => set(f.name, e.target.value)} style={{ width: '100%' }}>
              <option value="">— choose —</option>
              {(f.options || []).map((o) => <option key={o} value={o}>{o}</option>)}
            </select>
          ) : f.type === 'checkbox' ? (
            <input type="checkbox" checked={!!values[f.name]} onChange={(e) => set(f.name, e.target.checked)} style={{ width: 'auto' }} />
          ) : (
            <input type={f.type === 'number' ? 'number' : 'text'}
              value={String(values[f.name] ?? '')}
              onChange={(e) => set(f.name, f.type === 'number' ? Number(e.target.value) : e.target.value)}
              style={{ width: '100%' }} />
          )}
        </div>
      ))}
      <button className="primary" onClick={onSubmit} disabled={busy || missingRequired}>Submit</button>
    </div>
  )
}

// mdInline is a tiny, safe inline-markdown renderer: it HTML-escapes first, then
// applies **bold**, `code`, [text](url), and newlines. No raw HTML passes through.
function mdInline(s: string): string {
  const esc = s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  return esc
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
    .replace(/\n/g, '<br/>')
}
