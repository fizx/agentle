# agentle

A durable agent platform: write **Starlark** scripts that call capabilities
(LLM, HTTP, KV, shell, …), and the engine runs them with **deterministic replay** —
every capability call is a memoized RPC recorded in an append-only event log, so a
crashed or resumed execution replays from the log instead of re-spending calls.
Feels like Google Apps Script: edit, run, manage secrets/triggers, and inspect
traces from a web dashboard.

See [PLAN.md](PLAN.md) for the vision and design decisions, and
[INTERFACES.md](INTERFACES.md) for the core Go contracts.

## Quick start (no Docker/Postgres/Redis needed)

```sh
go run ./cmd/agentle            # serves the API + dashboard on :8080
# then open http://localhost:8080
```

State lives under `./data` (SQLite db, sandbox home dirs, fs snapshots). A sample
`hello-agent` script is seeded on first run. With no API key it uses an **offline
mock LLM**, so it's playable immediately. To use a real model:

```sh
OPENAI_API_KEY=sk-... OPENAI_MODEL=gpt-4o-mini go run ./cmd/agentle
```

Flags: `--addr :8080`, `--data ./data`.

## What you can do in the dashboard

- **Scripts** — edit Starlark in CodeMirror, grant capabilities (tool configs),
  save an immutable version, and run with a JSON input. Output + a link to the
  trace appear inline.
- **Runs** — execution history with status; click any run to see its trace
  (every memoized RPC as a span, derived from the durable event log).
- **Settings** — write-only **secrets** (never returned to scripts or traces),
  **tool configs** (secret-bound capability instances), and **triggers**
  (cron schedules and webhook URLs).

## Script API

`main(input)` is the entry point; its return value is the execution output.
Capabilities (all memoized RPCs unless noted):

| builtin | capability | notes |
|---|---|---|
| `log(*args)` | system | appears in the trace |
| `now()`, `sleep(seconds)` | system | deterministic time |
| `rand()`, `rand_int(n)` | system | seeded per execution |
| `kv_get(k)`, `kv_set(k,v)`, `kv_list(prefix)` | system | per-script durable store |
| `http_get(url, headers={})`, `http_post(url, body, headers={})` | `http` grant | SSRF-guarded, domain allowlist |
| `llm(messages, model=, temperature=)` | `llm` grant | OpenAI chat format |
| `shell(argv, dir=, env=)` | `shell` grant | runs in a per-actor sandbox home dir |
| `parallel_map(fn, items, max_concurrency=4)` | system | structured concurrency, replay-safe |

A capability the script's version hasn't been **granted** fails with
`capability not granted` — grants are the security boundary.

```python
def main(input):
    log("greeting", input["name"])
    seen = kv_get("seen:" + input["name"]) or 0
    kv_set("seen:" + input["name"], seen + 1)
    reply = llm([{"role": "user", "content": "Greet " + input["name"]}])
    return {"greeting": reply["content"], "times_seen": seen + 1}
```

## Architecture

```
cmd/agentle         all-in-one server (API + engine + scheduler + dashboard)
internal/engine     durable execution: event log, CAS+fencing lease, Mediator
                    (memoizes RPCs by deterministic call-key), fs barriers
internal/vm         Starlark runner + capability builtins (no ambient authority)
internal/caps       bound tool executors: http (SSRF guard), llm, kv, system
internal/sandbox    local subprocess sandbox + tar home-dir snapshots (dev tier)
internal/store      SQLite data model + durable event log + KV
internal/platform   resolves capability env from grants; runs; projects traces
internal/api        chi control-plane REST + webhook routes + SPA hosting
internal/trigger    cron scheduler
web                 vite + react + codemirror dashboard (embedded into the binary)
```

The pieces are wired behind interfaces (`engine.Log`, `Leaser`, `SandboxPool`,
`caps.KVStore`) so the dev backends here (SQLite, in-memory lease, local sandbox)
can be swapped for the prod tiers described in PLAN.md (Redis+AOF, kata+Firecracker)
without changing semantics.

## Develop

```sh
go test ./...                    # backend tests
go test -race ./internal/...     # incl. parallel_map concurrency
cd web && npm install && npm run build   # rebuild the embedded dashboard
cd web && npm run dev            # dashboard dev server (proxies /api to :8080)
```

## MVP scope / not yet built

This is a playable MVP. Deliberately deferred from the full vision: prod
kata+Firecracker sandbox, Redis/Postgres event-log tiers, the egress proxy
(Path B), OTLP export to an external collector, durable timers for long
suspensions, and multi-tenant RBAC beyond per-version grants.
