# Architecture & contributor guide

How the pieces fit, and exactly where to add the things you're most likely to add:
a capability, a stdlib builtin, a trigger, or an example.

## Folder map

```
cmd/agentle/         all-in-one server entrypoint + first-run seeding
internal/
  engine/            durable execution: event log, lease (CAS+fencing),
                     Mediator (memoizes RPCs by deterministic call-key),
                     fs barriers. The replay invariant lives here.
  vm/                Starlark runtime. The stdlib is a catalog assembled from
                     std_*.go files (one category per file).
  caps/              capability executors — ONE FILE PER CAPABILITY. Secrets are
                     bound here and never reach the VM.
  sandbox/           Sandbox implementations (local subprocess for dev).
  store/             SQLite data model + durable event log + kv + inbox + suspensions + tokens.
  platform/          integration hub: resolves a run's capability environment
                     from grants, builds the event envelope, runs, projects traces.
                     resume.go is the dispatcher that wakes durably-suspended runs.
  mcp/               minimal Model Context Protocol server (JSON-RPC) + demo tools.
  pricing/           OpenRouter price table (cached) for LLM cost tracking.
  secrets/           pluggable SecretStore: SQLite default + HashiCorp Vault.
  trigger/           trigger kinds (registry.go) + the cron scheduler.
  examples/          starter-script catalog (the dashboard gallery + seeding).
  api/               control-plane HTTP + public /v1 token API + UI/messages +
                     spend + plugins + webhook routes + SPA.
web/                 TypeScript + React dashboard (embedded into the binary).
```

## Add a capability (a "callable")

A capability is a bound tool the script calls as a memoized RPC.

1. **Executor** — add `internal/caps/<name>.go` with a constructor returning an
   `engine.Executor` (see `caps/llm.go` for a config+secret example, or
   `caps/log.go` for an always-on one).
2. **Builtin** — expose it to scripts: add an entry to the relevant group slice
   in `internal/vm/std_*.go` (or a new `std_<name>.go`) and write the thin
   `bXxx` wrapper that calls `callCap(...)`. The catalog drives autocomplete and
   `/api/capabilities` automatically.
3. **Wire it** in `internal/platform/platform.go`:
   - always-on cap → add to the env map in `assembleEnv`.
   - granted cap (needs a tool config / secret) → add a case to `buildExecutor`.

Idempotency: pass `idempotent=false` for calls with side effects (they get a
write-ahead intent + a stable `IdemKey`); `mutatesFS=true` if it writes the
sandbox home dir (forces a snapshot barrier).

## Add a stdlib builtin (no new capability)

Add a `Builtin` entry + `bXxx` function to the matching `internal/vm/std_*.go`
group. That's it — `predeclared()`, `Names()` (autocomplete) and the
`/api/capabilities` endpoint all derive from the catalog.

## Add a trigger

1. Append a `Kind` to `internal/trigger/registry.go` (set `Creatable`).
2. Implement dispatch: cron-like → extend `trigger.Scheduler`; inbound HTTP →
   add a route in `internal/api/server.go`. Build a `platform.RunRequest`
   (set `Kind`, `Data`, and optionally `ActorTemplate` to bind a named workspace).

## Add an example

Append an `Example` to `internal/examples/examples.go`. It shows up in the New
Script gallery and at `/api/examples`.

## The replay invariant (don't break this)

The Starlark VM has zero ambient authority and zero ambient nondeterminism. Time,
randomness, I/O, and the filesystem are reachable only through memoized capability
RPCs. A run can be replayed from the event log and must reproduce identically;
that's what makes crash recovery and the `recv()` yield point safe.

## Durable suspension

`recv()` is the durable yield point. When its inbox is empty the executor returns
`engine.SuspendError`; the Mediator records *nothing* for that call (so it
re-executes on resume), the runner recovers the typed error from under Starlark's
backtrace via a thread-local, and `engine.Run` parks the execution
(`Resolver.Suspend`) instead of failing it. `platform/resume.go` is an in-process
dispatcher: it resumes a parked run (by replaying the log) when its workspace inbox
has a message or its `wake_at` deadline passes, and recovers parked runs at boot.
A `recv(timeout=)` deadline is anchored on a memoized `now()` so it is stable
across suspend/resume. To keep a new capability suspend-capable, return
`*engine.SuspendError` and make the call idempotent (no write-ahead intent), so a
resume re-runs it cleanly.

## Interactive UI, plugins, cost (where things live)

- **Interactive UI** (`caps/ui.go`, `vm/std_ui.go`, `platform/ui.go`): the `ui`
  capability echoes its args into the (memoized) result; `platform.GetUI` projects
  those results + inbox recv results into a chat/form transcript. The panel posts
  to `POST /executions/{id}/messages` → `send` into the workspace → resume. UI runs
  are just durably-suspending actors with a rendered front end.
- **Capability plugins** (`store/plugin.go`, `caps/mcp.go` PluginSpec): a plugin is
  an MCP server backed by a sandboxed subprocess (`pluginArgv` builds the per-call
  command). Wired in via an `mcp` tool config with `plugin_id`.
- **Cost** (`pricing/`, `platform/cost.go`): token usage lives in the memoized llm
  result; cost is derived out-of-band (never an in-VM RPC) at completion (usage
  rows) and trace projection. Keep it that way — pricing must not affect replay.

## fs snapshot policy

fs-mutating RPCs mark the home dir dirty; the Mediator snapshots to object storage
at most once per debounce window (default 60s) and once more on teardown
(`FlushFS`, e.g. before a suspension). This bounds object-storage writes; crash
recovery replays from the latest barrier and re-executes fs-mutating RPCs after it,
so those must be idempotent. Pass `WithDebounce(0)` for strict per-RPC barriers.
