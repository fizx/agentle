import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { RunUI, UIBlock, UIField, UIMessage } from '../types'
import { Modal } from './Modal'

// UIPanel renders an interactive run's chat/form panel: it polls the run's UI
// projection, sends user input/form submissions to the run's workspace, and
// re-polls. Closes when the run completes (or the user dismisses it).
export function UIPanel({ execId, onClose }: { execId: string; onClose: () => void }) {
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
  useEffect(() => { bottom.current?.scrollIntoView({ behavior: 'smooth' }) }, [ui?.transcript.length])

  const done = ui ? ui.status === 1 || ui.status === 2 : false // completed | failed

  const sendChat = async () => {
    if (!text.trim() || busy) return
    const t = text; setText(''); setBusy(true)
    try { await api.postMessage(execId, { text: t }); await poll() } finally { setBusy(false) }
  }
  const submitForm = async () => {
    setBusy(true)
    try { await api.postMessage(execId, form); await poll() } finally { setBusy(false) }
  }

  const title = ui?.title || (ui?.kind === 'form' ? 'Form' : 'Chat')
  return (
    <Modal title={title} onClose={onClose}>
      {ui?.intro && <div className="muted" style={{ marginBottom: 8 }}>{ui.intro}</div>}

      {/* transcript */}
      {(ui?.transcript.length ?? 0) > 0 && (
        <div className="chat-log">
          {ui!.transcript.map((m, i) => <ChatBubble key={i} m={m} />)}
          <div ref={bottom} />
        </div>
      )}

      {/* chat input */}
      {ui?.kind === 'chat' && !done && (
        <div className="row" style={{ marginTop: 8, gap: 6 }}>
          <input
            autoFocus placeholder={ui.awaiting ? 'Type a message…' : 'thinking…'}
            value={text} onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') sendChat() }} style={{ flex: 1 }}
          />
          <button className="primary" onClick={sendChat} disabled={busy || !ui.awaiting}>Send</button>
        </div>
      )}

      {/* form */}
      {ui?.kind === 'form' && !done && (
        <FormFields fields={ui.fields || []} values={form} onChange={setForm} onSubmit={submitForm} busy={busy || !ui.awaiting} />
      )}

      {done && <div className="muted" style={{ marginTop: 10 }}>Session ended. <a onClick={onClose}>close</a></div>}
    </Modal>
  )
}

function ChatBubble({ m }: { m: UIMessage }) {
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
    <div style={{ marginTop: 8 }}>
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
