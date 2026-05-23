import type { Span, Trace } from '../types'

// TraceTimeline renders the event log as a gantt-style timeline. Each RPC result
// is a bar from its intent (or the prior event) to its result, positioned on a
// shared time axis; call-key depth indents nested calls (e.g. parallel_map
// branches), giving a dependency/structure view alongside timing.
export function TraceTimeline({ trace }: { trace: Trace }) {
  const spans = trace.spans
  if (spans.length === 0) return <div className="muted">No events.</div>

  const times = spans.map((s) => s.wall_time)
  const t0 = Math.min(...times)
  const t1 = Math.max(...times)
  const span = Math.max(1, t1 - t0)

  // intent start times by call key (for non-idempotent calls).
  const intentAt: Record<string, number> = {}
  for (const s of spans) if (s.kind === 'intent' && s.call_key) intentAt[s.call_key] = s.wall_time

  const rows = spans.filter((s) => s.kind === 'result' || s.kind === 'barrier')
  let prevEnd = t0

  return (
    <div className="timeline">
      {rows.map((s) => {
        const end = s.wall_time
        const start = s.call_key && intentAt[s.call_key] !== undefined ? intentAt[s.call_key] : prevEnd
        prevEnd = end
        const leftPct = ((start - t0) / span) * 100
        const widthPct = Math.max(1.5, ((end - start) / span) * 100)
        const depth = s.call_key ? s.call_key.split('.').length - 1 : 0
        const ms = ((end - start) / 1e6).toFixed(1)
        return (
          <div className="tl-row" key={s.seq}>
            <div className="tl-label mono" style={{ paddingLeft: depth * 14 }} title={label(s)}>
              {label(s)}
            </div>
            <div className="tl-track">
              <div
                className={'tl-bar' + (s.error ? ' err' : '') + (s.kind === 'barrier' ? ' barrier' : '')}
                style={{ left: leftPct + '%', width: widthPct + '%' }}
                title={`${ms}ms${s.error ? ' — ' + s.error : ''}`}
              />
            </div>
            <div className="tl-dur muted mono">{ms}ms</div>
          </div>
        )
      })}
    </div>
  )
}

function label(s: Span): string {
  if (s.snapshot) return 'fs-barrier'
  if (s.capability) return s.capability + '.' + (s.method || '')
  return s.kind
}
