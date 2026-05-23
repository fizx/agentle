import { STATUS } from '../types'

export function StatusBadge({ status }: { status: number }) {
  const s = STATUS[status] ?? 'unknown'
  return <span className={'badge ' + s}>{s}</span>
}
