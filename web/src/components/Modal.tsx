import { useState, type ReactNode } from 'react'

// Modal is a simple centered dialog with a backdrop. Click outside or × to close.
export function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="row spread" style={{ marginBottom: 10 }}>
          <h3 style={{ margin: 0 }}>{title}</h3>
          <button onClick={onClose} aria-label="close">×</button>
        </div>
        {children}
      </div>
    </div>
  )
}

// PromptModal replaces window.prompt with an in-app dialog (single text input).
export function PromptModal({
  title, label, initial = '', confirmLabel = 'OK', onSubmit, onCancel,
}: {
  title: string; label: string; initial?: string; confirmLabel?: string
  onSubmit: (value: string) => void; onCancel: () => void
}) {
  const [value, setValue] = useState(initial)
  const submit = () => { if (value.trim()) onSubmit(value.trim()) }
  return (
    <Modal title={title} onClose={onCancel}>
      <label>{label}</label>
      <input
        autoFocus
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') onCancel() }}
        style={{ width: '100%' }}
      />
      <div className="row" style={{ justifyContent: 'flex-end', marginTop: 12, gap: 8 }}>
        <button onClick={onCancel}>Cancel</button>
        <button className="primary" onClick={submit} disabled={!value.trim()}>{confirmLabel}</button>
      </div>
    </Modal>
  )
}
