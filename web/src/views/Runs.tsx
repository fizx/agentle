import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { Execution, Trace } from '../types'
import { StatusBadge } from '../components/Badge'
import { Json } from '../components/Json'
import { TraceTimeline } from '../components/TraceTimeline'
import { fmtUSD } from './Spend'

export default function Runs({ focusExec, onSelect }: { focusExec: string | null; onSelect: (id: string) => void }) {
  const [execs, setExecs] = useState<Execution[]>([])
  const [detail, setDetail] = useState<Execution | null>(null)
  const [trace, setTrace] = useState<Trace | null>(null)
  const [view, setView] = useState<'timeline' | 'events'>('timeline')
  const [limit, setLimit] = useState(30)
  const sel = focusExec // selection is the URL-driven focused execution

  const refresh = useCallback(async () => { setExecs((await api.listExecutions('', limit, 0)) || []) }, [limit])
  useEffect(() => { refresh() }, [refresh])

  // Load the focused execution's detail + trace whenever the URL selection changes.
  useEffect(() => {
    if (!focusExec) { setDetail(null); setTrace(null); return }
    let live = true
    ;(async () => {
      const d = await api.getExecution(focusExec); if (live) setDetail(d)
      const tr = await api.getTrace(focusExec); if (live) setTrace(tr)
    })()
    return () => { live = false }
  }, [focusExec])

  return (
    <div className="layout">
      <div className="sidebar">
        <div className="row spread" style={{ marginBottom: 8 }}>
          <strong>Executions</strong>
          <button onClick={refresh}>↻</button>
        </div>
        {execs.map((e) => (
          <div key={e.id} className={'list-item' + (e.id === sel ? ' active' : '')} onClick={() => onSelect(e.id)}>
            <div className="row spread">
              <span className="mono" style={{ fontSize: 12 }}>{e.id.slice(3, 11)}</span>
              <StatusBadge status={e.status} />
            </div>
            <div className="muted" style={{ fontSize: 11 }}>{e.trigger} · {new Date(e.created_at / 1e6).toLocaleTimeString()}</div>
          </div>
        ))}
        {execs.length >= limit && <button style={{ marginTop: 8 }} onClick={() => setLimit(limit + 30)}>Load more</button>}
        {execs.length === 0 && <div className="muted">No runs yet.</div>}
      </div>

      <div className="main">
        {!detail && <div className="muted">Select an execution to see its trace.</div>}
        {detail && (
          <>
            <div className="row spread">
              <h2 className="mono">{detail.id}</h2>
              <StatusBadge status={detail.status} />
            </div>
            <div className="muted" style={{ marginBottom: 12 }}>
              script {detail.script_id} · v{detail.version} · {detail.trigger} · workspace <span className="mono">{detail.workspace}</span>
            </div>

            {detail.error && <div className="card"><h3>Error</h3><pre className="err">{detail.error}</pre></div>}

            <div className="grid2">
              <div className="card"><h3>Input</h3><Json value={detail.input} /></div>
              <div className="card"><h3>Output</h3><Json value={detail.output} /></div>
            </div>

            <div className="card">
              <div className="row spread">
                <h3>
                  Trace ({trace ? trace.spans.length : 0} events)
                  {trace && trace.cost_usd > 0 && <span className="muted" style={{ marginLeft: 8, fontWeight: 400 }}>· {fmtUSD(trace.cost_usd)}</span>}
                </h3>
                <div className="tabs">
                  <button className={view === 'timeline' ? 'active' : ''} onClick={() => setView('timeline')}>Timeline</button>
                  <button className={view === 'events' ? 'active' : ''} onClick={() => setView('events')}>Events</button>
                </div>
              </div>
              {trace && view === 'timeline' && <TraceTimeline trace={trace} />}
              {trace && view === 'events' && (
                <table>
                  <thead>
                    <tr><th>#</th><th>kind</th><th>capability</th><th>call</th><th>result / error</th></tr>
                  </thead>
                  <tbody>
                    {trace.spans.map((sp) => (
                      <tr key={sp.seq}>
                        <td className="mono">{sp.seq}</td>
                        <td>{sp.kind}</td>
                        <td className="mono">{sp.capability ? sp.capability + '.' + sp.method : (sp.snapshot ? 'fs-barrier' : '')}</td>
                        <td className="mono muted">{sp.call_key}</td>
                        <td className="mono" style={{ maxWidth: 380 }}>
                          {sp.error
                            ? <span className="err">{sp.error}</span>
                            : <span className="muted">{truncate(sp.result || sp.snapshot || '', 160)}</span>}
                          {sp.capability === 'llm' && (sp.input_tokens || sp.output_tokens) ? (
                            <span className="muted" style={{ fontSize: 11 }}>
                              {' '}· {sp.input_tokens || 0}/{sp.output_tokens || 0} tok{sp.cost_usd ? ' · ' + fmtUSD(sp.cost_usd) : ''}
                            </span>
                          ) : null}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function truncate(s: string, n: number): string { return s.length > n ? s.slice(0, n) + '…' : s }
