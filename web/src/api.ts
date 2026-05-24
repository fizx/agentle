import type {
  ApiToken, Capability, Example, Execution, Plugin, Script, ScriptDetail, Spend, ToolConfig, Trace, Trigger, User, Version,
} from './types'

const USER_KEY = 'agentle.user'
export function getUserId(): string {
  return localStorage.getItem(USER_KEY) || ''
}
export function setUserId(id: string): void {
  if (id) localStorage.setItem(USER_KEY, id)
  else localStorage.removeItem(USER_KEY)
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const uid = getUserId()
  if (uid) headers['X-Agentle-User'] = uid
  const opts: RequestInit = { method, headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const resp = await fetch('/api' + path, opts)
  const text = await resp.text()
  const data = text ? JSON.parse(text) : null
  if (!resp.ok) throw new Error((data && data.error) || resp.statusText)
  return data as T
}

const qs = (params: Record<string, string | number | undefined>): string => {
  const p = Object.entries(params).filter(([, v]) => v !== undefined && v !== '' && v !== null)
  return p.length ? '?' + p.map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join('&') : ''
}

export const api = {
  me: () => req<User>('GET', '/me'),
  capabilities: () => req<Capability[]>('GET', '/capabilities'),
  examples: () => req<Example[]>('GET', '/examples'),

  listUsers: () => req<User[]>('GET', '/users'),
  putUser: (u: Partial<User>) => req<User>('PUT', '/users', u),
  deleteUser: (id: string) => req<void>('DELETE', `/users/${id}`),

  listScripts: (limit?: number, offset?: number) => req<Script[]>('GET', '/scripts' + qs({ limit, offset })),
  createScript: (name: string, source: string) => req<Script>('POST', '/scripts', { name, source }),
  getScript: (id: string) => req<ScriptDetail>('GET', `/scripts/${id}`),
  deleteScript: (id: string) => req<void>('DELETE', `/scripts/${id}`),
  saveVersion: (id: string, source: string, grants: GrantRefInput[], image?: string) =>
    req<Version>('POST', `/scripts/${id}/versions`, { source, grants, image }),
  restoreVersion: (id: string, v: number) => req<Version>('POST', `/scripts/${id}/versions/${v}/restore`),
  run: (id: string, input: unknown, version?: number) => req<Execution>('POST', `/scripts/${id}/run`, { input, version }),

  listExecutions: (script?: string, limit?: number, offset?: number) =>
    req<Execution[]>('GET', '/executions' + qs({ script, limit, offset })),
  getExecution: (id: string) => req<Execution>('GET', `/executions/${id}`),
  getTrace: (id: string) => req<Trace>('GET', `/executions/${id}/trace`),
  spend: (by: string, since?: number) => req<Spend>('GET', '/spend' + qs({ by, since })),

  listConfigs: () => req<ToolConfig[]>('GET', '/configs'),
  putConfig: (c: Partial<ToolConfig>) => req<{ id: string }>('PUT', '/configs', c),
  deleteConfig: (id: string) => req<void>('DELETE', `/configs/${id}`),

  listSecrets: (script?: string) => req<{ names: string[]; scope: string }>('GET', '/secrets' + qs({ script })),
  putSecret: (name: string, value: string, script?: string) =>
    req<{ name: string }>('PUT', '/secrets' + qs({ script }), { name, value }),
  deleteSecret: (name: string, script?: string) =>
    req<void>('DELETE', `/secrets/${encodeURIComponent(name)}` + qs({ script })),

  listTriggers: (script?: string) => req<Trigger[]>('GET', '/triggers' + qs({ script })),
  putTrigger: (t: Partial<Trigger>) => req<Trigger>('PUT', '/triggers', t),
  deleteTrigger: (id: string) => req<void>('DELETE', `/triggers/${id}`),

  listTokens: () => req<ApiToken[]>('GET', '/tokens'),
  createToken: (name: string) => req<ApiToken>('POST', '/tokens', { name }),
  deleteToken: (id: string) => req<void>('DELETE', `/tokens/${id}`),

  listPlugins: () => req<Plugin[]>('GET', '/plugins'),
  putPlugin: (p: Partial<Plugin>) => req<Plugin>('PUT', '/plugins', p),
  deletePlugin: (id: string) => req<void>('DELETE', `/plugins/${id}`),
}

export interface GrantRefInput {
  capability: string
  config_id: string
}
