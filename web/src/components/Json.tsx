// pretty renders any value as formatted JSON. Crucially it handles objects (the
// API returns parsed JSON, so output is often an object) — previously these
// rendered as "[object Object]" because JSON.parse was called on a non-string.
export function pretty(value: unknown): string {
  if (value === null || value === undefined) return '∅'
  if (typeof value === 'string') {
    try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value }
  }
  try { return JSON.stringify(value, null, 2) } catch { return String(value) }
}

export function Json({ value, className }: { value: unknown; className?: string }) {
  return <pre className={className}>{pretty(value)}</pre>
}
