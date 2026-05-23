// Thin wrapper over the control-plane API.

// Dev-mode identity: the chosen user id is sent in a header on every request.
const USER_KEY = 'agentle.user'
export function getUserId() {
  return localStorage.getItem(USER_KEY) || ''
}
export function setUserId(id) {
  if (id) localStorage.setItem(USER_KEY, id)
  else localStorage.removeItem(USER_KEY)
}

async function req(method, path, body) {
  const opts = { method, headers: {} }
  const uid = getUserId()
  if (uid) opts.headers['X-Agentle-User'] = uid
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const resp = await fetch('/api' + path, opts)
  const text = await resp.text()
  const data = text ? JSON.parse(text) : null
  if (!resp.ok) {
    throw new Error((data && data.error) || resp.statusText)
  }
  return data
}

const qs = (params) => {
  const p = Object.entries(params || {}).filter(([, v]) => v !== undefined && v !== '' && v !== null)
  return p.length ? '?' + p.map(([k, v]) => `${k}=${encodeURIComponent(v)}`).join('&') : ''
}

export const api = {
  me: () => req('GET', '/me'),
  listUsers: () => req('GET', '/users'),
  putUser: (u) => req('PUT', '/users', u),
  deleteUser: (id) => req('DELETE', `/users/${id}`),

  listScripts: (limit, offset) => req('GET', '/scripts' + qs({ limit, offset })),
  createScript: (name, source) => req('POST', '/scripts', { name, source }),
  getScript: (id) => req('GET', `/scripts/${id}`),
  deleteScript: (id) => req('DELETE', `/scripts/${id}`),
  saveVersion: (id, source, grants, image) => req('POST', `/scripts/${id}/versions`, { source, grants, image }),
  restoreVersion: (id, v) => req('POST', `/scripts/${id}/versions/${v}/restore`),
  run: (id, input, version) => req('POST', `/scripts/${id}/run`, { input, version }),

  listExecutions: (script, limit, offset) => req('GET', '/executions' + qs({ script, limit, offset })),
  getExecution: (id) => req('GET', `/executions/${id}`),
  getTrace: (id) => req('GET', `/executions/${id}/trace`),

  listConfigs: () => req('GET', '/configs'),
  putConfig: (c) => req('PUT', '/configs', c),

  // scope: omit for global (admin), or pass a scriptId for per-script secrets.
  listSecrets: (script) => req('GET', '/secrets' + qs({ script })),
  putSecret: (name, value, script) => req('PUT', '/secrets' + qs({ script }), { name, value }),
  deleteSecret: (name, script) => req('DELETE', `/secrets/${encodeURIComponent(name)}` + qs({ script })),

  listTriggers: (script) => req('GET', '/triggers' + qs({ script })),
  putTrigger: (t) => req('PUT', '/triggers', t),
  deleteTrigger: (id) => req('DELETE', `/triggers/${id}`),
}

export const STATUS = ['running', 'completed', 'failed', 'suspended']

export const BUILTINS = [
  'log', 'now', 'sleep', 'rand', 'rand_int',
  'store', 'fetch', 'keys',
  'http_get', 'http_post', 'llm', 'shell', 'parallel_map',
]

// Common cron presets surfaced in the UI alongside raw cron entry.
export const CRON_PRESETS = [
  { label: 'Every minute', spec: '* * * * *' },
  { label: 'Every 5 minutes', spec: '*/5 * * * *' },
  { label: 'Every 15 minutes', spec: '*/15 * * * *' },
  { label: 'Hourly', spec: '0 * * * *' },
  { label: 'Daily (midnight)', spec: '0 0 * * *' },
  { label: 'Weekly (Sun)', spec: '0 0 * * 0' },
]
