import { useState } from 'react'
import type { Span, Trace } from '../types'

// TraceTimeline renders the event log as a gantt-style span waterfall: each RPC
// result is a bar from its intent (or the prior event) to its result on a shared
// time axis, with call-key depth indenting nested calls (e.g. parallel_map
// branches). This is a purpose-built waterfall rather than a generic timeline
// library — see the README note ("Trace timeline") for why.
const TICKS = 4

export function TraceTimeline({ trace }: { trace: Trace }) {
  const [zoom, setZoom] = useState(1)
  const spans = trace.spans
  if (spans.length === 0) return <div className="muted">No events.</div>

  const times = spans.map((s) => s.wall_time)
  const t0 = Math.min(...times)
  const t1 = Math.max(...times)
  const span = Math.max(1, t1 - t0)
  const totalMs = span / 1e6

  // intent start times by call key (for non-idempotent calls).
  const intentAt: Record<string, number> = {}
  for (const s of spans) if (s.kind === 'intent' && s.call_key) intentAt[s.call_key] = s.wall_time

  const rows = spans.filter((s) => s.kind === 'result' || s.kind === 'barrier')
  let prevEnd = t0

  return (
    <>
      <div className="row" style={{ justifyContent: 'flex-end', gap: 6, marginBottom: 6 }}>
        <span className="muted" style={{ fontSize: 11 }}>zoom</span>
        <button onClick={() => setZoom((z) => Math.max(1, +(z / 1.5).toFixed(2)))} disabled={zoom <= 1}>−</button>
        <span className="mono muted" style={{ fontSize: 11, minWidth: 34, textAlign: 'center' }}>{zoom.toFixed(1)}×</span>
        <button onClick={() => setZoom((z) => Math.min(40, +(z * 1.5).toFixed(2)))}>+</button>
      </div>
      <div className="tl-scroll">
        <div className="timeline" style={{ width: zoom * 100 + '%' }}>
          <div className="tl-axis">
        <div className="tl-label muted" style={{ fontSize: 11 }}>{rows.length} spans · {fmtMs(totalMs)}</div>
        <div className="tl-axis-track">
          {Array.from({ length: TICKS + 1 }).map((_, i) => (
            <span className="tl-tick" key={i} style={{ left: (i / TICKS) * 100 + '%' }}>
              {fmtMs((totalMs * i) / TICKS)}
            </span>
          ))}
        </div>
        <div />
      </div>

      {rows.map((s) => {
        const end = s.wall_time
        const start = s.call_key && intentAt[s.call_key] !== undefined ? intentAt[s.call_key] : prevEnd
        prevEnd = end
        const leftPct = ((start - t0) / span) * 100
        const widthPct = Math.max(1.5, ((end - start) / span) * 100)
        const depth = s.call_key ? s.call_key.split('.').length - 1 : 0
        const ms = (end - start) / 1e6
        const detail = s.error ? ' — ' + s.error : s.result ? ' — ' + truncate(s.result, 120) : ''
        return (
          <div className="tl-row" key={s.seq}>
            <div className="tl-label mono" style={{ paddingLeft: depth * 14 }} title={label(s)}>
              {label(s)}
            </div>
            <div className="tl-track">
              <div
                className={'tl-bar' + (s.error ? ' err' : '') + (s.kind === 'barrier' ? ' barrier' : '')}
                style={{ left: leftPct + '%', width: widthPct + '%' }}
                title={`${label(s)} · ${fmtMs(ms)}${detail}`}
              />
            </div>
            <div className="tl-dur muted mono">{fmtMs(ms)}</div>
          </div>
        )
      })}
        </div>
      </div>
    </>
  )
}

function label(s: Span): string {
  if (s.snapshot) return 'fs-barrier'
  if (s.capability) return s.capability + '.' + (s.method || '')
  return s.kind
}

function fmtMs(ms: number): string {
  if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10000 ? 0 : 1) + 's'
  return ms.toFixed(ms < 10 ? 1 : 0) + 'ms'
}

function truncate(s: string, n: number): string { return s.length > n ? s.slice(0, n) + '…' : s }
