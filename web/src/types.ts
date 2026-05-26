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
  secret_present?: boolean // false => referenced secret isn't set yet
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
  feedback?: string // pointwise human label: "up" | "down" | ""
  feedback_note?: string // reviewer note (detail view only)
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
  model?: string
  input_tokens?: number
  output_tokens?: number
  cost_usd?: number
}

export interface Trace {
  execution: string
  spans: Span[]
  cost_usd: number
}

export interface SpendRow {
  key: string
  calls: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
}

export interface Spend {
  by: string
  rows: SpendRow[]
  total_cost_usd: number
}

export interface Capability {
  name: string
  group: string
  doc: string
}

export interface UIMessage {
  role: string
  text: string
  blocks?: UIBlock[]
}

export interface UIBlock {
  type: string // code | table | image | tool_calls
  lang?: string
  text?: string
  columns?: string[]
  rows?: string[][]
  url?: string
  alt?: string
  batch?: string // tool_calls
  calls?: ToolCall[] // tool_calls
}

export interface ToolCall {
  id: string
  name: string
  arguments: Record<string, unknown>
}

export interface ToolBatch {
  batch: string
  calls: ToolCall[]
}

export interface UIField {
  name: string
  label?: string
  type: string // text | textarea | number | select | checkbox
  options?: string[]
  required?: boolean
  default?: unknown
}

export interface UIPanelDesc {
  kind: string // chat | form
  title?: string
  intro?: string
  fields?: UIField[]
}

export interface RunUI {
  kind: string // top-of-stack panel kind: chat | form | ''
  title?: string
  intro?: string
  fields?: UIField[]
  panels: UIPanelDesc[] // the full panel stack (bottom → top)
  transcript: UIMessage[]
  status: number
  awaiting: boolean
  pending_tools?: ToolBatch // editor tool calls awaiting client execution
}

export interface Golden {
  id: string
  script_id: string
  origin_exec: string
  origin_version: number
  label: string // success | failure
  persona?: string
  criteria?: string
  note?: string
  created_at: number
}

export interface CriterionResult {
  criterion: string
  pass: boolean
  evidence?: string
}

export interface Verdict {
  pass: boolean
  mode: string // prefix | full
  criteria?: CriterionResult[]
  reasoning?: string
  raw?: string
}

export interface EvalResult {
  golden_id: string
  script_id: string
  version: number
  label: string
  executed: number
  golden_len: number
  coverage: number
  completed: boolean
  stop_kind?: string // '' if completed; else write_miss | recv_exhausted | budget
  stop_msg?: string
  output?: unknown
  error?: string
  verdict?: Verdict
  judge_error?: string
}

export interface EvalOpts {
  version?: number
  allowReads?: boolean
  miss?: string // fail | go_live | flag
  maxSteps?: number
  judge?: boolean
  judgeModel?: string
  mode?: string // prefix | full
}

export interface EvalSuite {
  golden_id: string
  version: number
  k: number
  passes: number
  pass_rate: number
  flaky: boolean
  mean_coverage: number
  samples: EvalResult[]
}

export interface ToolPolicy {
  server: string
  tool: string
  is_write: boolean
  source: string // operator | annotation
  created_at?: number
}

export interface ConsistencyResult {
  consistent: boolean
  detail: string
  result?: EvalResult
}

export interface CalibrationStats {
  n: number
  agreements: number
  accuracy: number
  kappa: number
  tp: number
  tn: number
  fp: number
  fn: number
}

export interface AppInfo {
  script_id: string
  name: string
  owner: string
  version: number
  kind: string // chat | form
  title?: string
  intro?: string
}

export interface Plugin {
  id: string
  name: string
  kind: string // script | native
  runtime: string // python | node | bash | native
  source: string
  enabled: boolean
  current_version: number
  created_at: number
}

export interface PluginVersion {
  plugin_id: string
  version: number
  runtime: string
  source: string
  note?: string
  created_at: number
}

export interface ApiToken {
  id: string
  name: string
  user_id: string
  created_at: number
  last_used_at: number
  token?: string // plaintext, returned only at creation
}

export interface Chat {
  id: string
  script_id: string
  exec_id: string
  title: string
  archived: boolean
  created_at: number
  updated_at: number
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
