// Thin wrapper over the control-plane API.
async function req(method, path, body) {
  const opts = { method, headers: {} }
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

export const api = {
  listScripts: () => req('GET', '/scripts'),
  createScript: (name, source) => req('POST', '/scripts', { name, source }),
  getScript: (id) => req('GET', `/scripts/${id}`),
  saveVersion: (id, source, grants, image) =>
    req('POST', `/scripts/${id}/versions`, { source, grants, image }),
  run: (id, input, version) => req('POST', `/scripts/${id}/run`, { input, version }),
  listExecutions: (script) => req('GET', '/executions' + (script ? `?script=${script}` : '')),
  getExecution: (id) => req('GET', `/executions/${id}`),
  getTrace: (id) => req('GET', `/executions/${id}/trace`),
  listConfigs: () => req('GET', '/configs'),
  putConfig: (c) => req('PUT', '/configs', c),
  listSecrets: () => req('GET', '/secrets'),
  putSecret: (name, value) => req('PUT', '/secrets', { name, value }),
  listTriggers: () => req('GET', '/triggers'),
  putTrigger: (t) => req('PUT', '/triggers', t),
  deleteTrigger: (id) => req('DELETE', `/triggers/${id}`),
}

export const STATUS = ['running', 'completed', 'failed', 'suspended']
