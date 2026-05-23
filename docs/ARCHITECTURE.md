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
  store/             SQLite data model + durable event log + kv + inbox.
  platform/          integration hub: resolves a run's capability environment
                     from grants, builds the event envelope, runs, projects traces.
  trigger/           trigger kinds (registry.go) + the cron scheduler.
  examples/          starter-script catalog (the dashboard gallery + seeding).
  api/               control-plane HTTP + webhook routes + SPA hosting.
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
