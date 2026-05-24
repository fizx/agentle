import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { SpendRow } from '../types'

type Dim = 'script' | 'workspace' | 'user' | 'model' | 'exec'
const DIMS: Dim[] = ['script', 'workspace', 'user', 'model', 'exec']

const WINDOWS: { label: string; ms: number }[] = [
  { label: 'All time', ms: 0 },
  { label: 'Last 24h', ms: 24 * 3600e3 },
  { label: 'Last 7d', ms: 7 * 24 * 3600e3 },
  { label: 'Last 30d', ms: 30 * 24 * 3600e3 },
]

export function fmtUSD(n: number): string {
  if (n === 0) return '$0'
  if (n < 0.01) return '$' + n.toFixed(4)
  return '$' + n.toFixed(2)
}

export default function Spend() {
  const [by, setBy] = useState<Dim>('script')
  const [winMs, setWinMs] = useState(0)
  const [rows, setRows] = useState<SpendRow[]>([])
  const [total, setTotal] = useState(0)

  const refresh = useCallback(async () => {
    const since = winMs ? (Date.now() - winMs) * 1e6 : 0 // unix nanos
    const s = await api.spend(by, since)
    setRows(s.rows || [])
    setTotal(s.total_cost_usd || 0)
  }, [by, winMs])
  useEffect(() => { refresh() }, [refresh])

  return (
    <div className="main" style={{ maxWidth: 980, margin: '0 auto' }}>
      <div className="card">
        <div className="row spread">
          <h2>Spend</h2>
          <div className="row" style={{ gap: 8 }}>
            <select value={by} onChange={(e) => setBy(e.target.value as Dim)}>
              {DIMS.map((d) => <option key={d} value={d}>by {d}</option>)}
            </select>
            <select value={winMs} onChange={(e) => setWinMs(Number(e.target.value))}>
              {WINDOWS.map((w) => <option key={w.label} value={w.ms}>{w.label}</option>)}
            </select>
            <button onClick={refresh}>↻</button>
          </div>
        </div>
        <div className="muted" style={{ marginBottom: 10 }}>
          Token usage + cost from LLM calls, priced from OpenRouter. Total <strong>{fmtUSD(total)}</strong>.
          Non-admins see only their own scripts.
        </div>
        <table>
          <thead><tr><th>{by}</th><th>calls</th><th>input tok</th><th>output tok</th><th>cost</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key}>
                <td className="mono">{by === 'exec' ? r.key.slice(0, 14) : (r.key || '—')}</td>
                <td>{r.calls}</td>
                <td className="muted">{r.input_tokens.toLocaleString()}</td>
                <td className="muted">{r.output_tokens.toLocaleString()}</td>
                <td className="mono">{fmtUSD(r.cost_usd)}</td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={5} className="muted">no usage recorded yet</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}
