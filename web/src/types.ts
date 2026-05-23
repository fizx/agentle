export type Role = 'admin' | 'user'

export interface User {
  id: string
  name: string
  role: Role
  created_at: number
}

export interface GrantRef {
  capability: string
  config_id: string
}

export interface Script {
  id: string
  name: string
  owner: string
  current_version: number
  created_at: number
}

export interface Version {
  script_id: string
  version: number
  source: string
  image: string
  grants: GrantRef[]
  created_at: number
}

export interface ScriptDetail {
  script: Script
  versions: Version[]
}

export interface ToolConfig {
  id: string
  capability: string
  config: unknown
  secret_ref?: string
  created_at: number
}

export interface Trigger {
  id: string
  script_id: string
  kind: string
  spec: string
  actor_template: string
  enabled: boolean
  created_at: number
}

export interface Execution {
  id: string
  script_id: string
  version: number
  workspace: string
  status: number
  input?: unknown
  output?: unknown
  error?: string
  trigger: string
  created_at: number
  updated_at: number
}

export interface Span {
  seq: number
  kind: string
  capability?: string
  method?: string
  call_key?: string
  wall_time: number
  args?: string
  result?: string
  error?: string
  snapshot?: string
}

export interface Trace {
  execution: string
  spans: Span[]
}

export interface Capability {
  name: string
  group: string
  doc: string
}

export interface ApiToken {
  id: string
  name: string
  user_id: string
  created_at: number
  last_used_at: number
  token?: string // plaintext, returned only at creation
}

export interface Example {
  id: string
  title: string
  description: string
  capabilities: string[]
  source: string
}

export const STATUS = ['running', 'completed', 'failed', 'suspended'] as const

export const CRON_PRESETS: { label: string; spec: string }[] = [
  { label: 'Every minute', spec: '* * * * *' },
  { label: 'Every 5 minutes', spec: '*/5 * * * *' },
  { label: 'Every 15 minutes', spec: '*/15 * * * *' },
  { label: 'Hourly', spec: '0 * * * *' },
  { label: 'Daily (midnight)', spec: '0 0 * * *' },
  { label: 'Weekly (Sun)', spec: '0 0 * * 0' },
]
