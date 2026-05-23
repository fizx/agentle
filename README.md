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
  **tool configs** (secret-bound capability instances), **triggers**
  (cron schedules and webhook URLs), and **API tokens** for the REST API.

## Programmatic REST API

Beyond the dashboard's header-authenticated control plane, there's a stable,
token-authenticated REST API at `/v1`. Mint a token under **Settings → API
tokens** (the secret is shown once); it carries the issuing user's RBAC.

```sh
# run a script and get its execution (completed | failed | suspended)
curl -X POST $ORIGIN/v1/scripts/<script-id>/runs \
  -H "Authorization: Bearer agtl_..." \
  -d '{"input": {"name": "kyle"}, "version": 0}'

curl $ORIGIN/v1/runs/<exec-id>        -H "Authorization: Bearer agtl_..."
curl $ORIGIN/v1/runs/<exec-id>/trace  -H "Authorization: Bearer agtl_..."
curl $ORIGIN/v1/scripts               -H "Authorization: Bearer agtl_..."
```

Tokens are stored only as SHA-256 hashes. (The dashboard control plane stays on
the dev `X-Agentle-User` header — real session/OAuth auth is still deferred.)

## MCP (Model Context Protocol)

Scripts can use MCP tools two ways: **directly** (`mcp_call("add", {...})`) and as
**LLM tools** (`llm(messages, tools=mcp_list_tools())` — the model returns
`tool_calls`, the script runs each via `mcp_call`, and feeds results back). Grant
an `mcp` tool config; with an empty `endpoint` it uses an in-process mock server
(echo/add/upper) so it's playable offline, and a real JSON-RPC MCP server is also
served at `/mcp`. See the `mcp_direct` and `mcp_agent` examples.

## Script API

`main(input)` is the entry point; its return value is the execution output.
Capabilities (all memoized RPCs unless noted):

| builtin | capability | notes |
|---|---|---|
| `log(*args)` | system | appears in the trace |
| `now()`, `sleep(seconds)` | system | deterministic time |
| `rand()`, `rand_int(n)` | system | seeded per execution |
| `store(k,v)`, `fetch(k)`, `keys(prefix)` | system | per-**workspace** durable store (`load` is a reserved Starlark keyword, hence `fetch`) |
| `send(workspace, data)`, `recv(timeout=)` | system | actor messaging; `recv` is the blocking "yield" point |
| `http_get(url, headers={})`, `http_post(url, body, headers={})` | `http` grant | SSRF-guarded, domain allowlist |
| `llm(messages, model=, temperature=, tools=)` | `llm` grant | OpenAI chat format; `tools=` enables tool use (accepts MCP tool defs) |
| `mcp_list_tools()`, `mcp_call(tool, args={})` | `mcp` grant | Model Context Protocol client (direct calls + LLM tool use) |
| `shell(argv, dir=, env=)` | `shell` grant | runs in a per-workspace sandbox home dir |
| `parallel_map(fn, items, max_concurrency=4)` | system | structured concurrency, replay-safe |

`recv()` is a **durable suspension point**: with an empty inbox the execution
parks (status `suspended`) — no goroutine blocks — and the engine resumes it by
replay when a message arrives at its workspace or its `timeout` deadline passes.

The full stdlib catalog (with one-line docs) is served at `/api/capabilities` and
drives the editor's autocomplete. A capability the script's version hasn't been
**granted** fails with `capability not granted` — grants are the security boundary.

`main(input)` receives a **structured event**: `{id, kind, trigger_id, workspace, data}`,
where `data` is the run input (or a webhook body). `kind` is `dashboard`, `webhook`,
or `cron`.

```python
def main(input):
    name = (input.get("data") or {}).get("name", "world")
    log("greeting", name, "via", input["kind"])
    seen = fetch("seen:" + name) or 0
    store("seen:" + name, seen + 1)
    reply = llm([{"role": "user", "content": "Greet " + name}])
    return {"greeting": reply["content"], "times_seen": seen + 1}
```

### Workspaces, actors & triggers

The `store`/`fetch`/inbox namespace is the **workspace** (an actor instance), not
the script. Manual (dashboard) runs and unbound trigger runs are *anonymous* — a
unique workspace per execution, so they share no state. A trigger can bind a
**named workspace** with a mustache template over the event, e.g.
`webhook-{{event.id}}`, so all events for the same id share durable state and an
inbox. `send()`/`recv()` pass messages between workspaces; `recv()` is the
**durable yield point** that lets one run process many messages in an agent loop.

**Durable suspend.** When a run calls `recv()` and its inbox is empty, it does not
block a goroutine — it durably suspends (status `suspended`). A background
dispatcher resumes it (by replaying the event log) when a message is sent to its
workspace, or when the `recv(timeout=...)` deadline passes (the deadline is
anchored on a memoized `now()`, so it survives the suspend). On restart the
dispatcher recovers any parked runs whose wake condition is already met. See the
`request_reply` and `agent_loop` examples.

Triggers, per-script secrets, and run history are managed on each script's page;
global secrets, tool configs, and users live under Settings / Users. RBAC is
admin > user > script: users see and manage only their own scripts/runs; admins
see everything and can "act as" any user via the top-right selector. Identity is
dev-mode (a header; no passwords yet). New scripts can start from the **example
gallery** (`/api/examples`), and execution traces have a **timeline** view.

New to the codebase? See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the
folder map and how to add a capability, builtin, trigger, or example.

## Architecture

```
cmd/agentle         all-in-one server (API + engine + scheduler + dashboard)
internal/engine     durable execution: event log, CAS+fencing lease, Mediator
                    (memoizes RPCs by deterministic call-key), fs barriers
internal/vm         Starlark runner + stdlib catalog (std_*.go, one per category)
internal/caps       bound tool executors, one file per capability (no ambient authority)
internal/sandbox    local subprocess sandbox + tar home-dir snapshots (dev tier)
internal/store      SQLite data model + durable event log + KV + inbox
internal/platform   resolves capability env from grants; runs; projects traces
internal/api        chi control-plane REST + public /v1 API + webhook routes + SPA
internal/mcp        minimal Model Context Protocol server (JSON-RPC) + demo tools
internal/trigger    trigger-kind registry + cron scheduler
internal/examples   starter-script catalog (gallery + seeding)
web                 vite + react + codemirror dashboard in TypeScript (embedded)
```

The platform also runs a **dispatcher** (in `internal/platform`) that resumes
durably-suspended executions when their wake condition is met.

The pieces are wired behind interfaces (`engine.Log`, `Leaser`, `SandboxPool`,
`caps.KVStore`) so the dev backends here (SQLite, in-memory lease, local sandbox)
can be swapped for the prod tiers described in PLAN.md (Redis+AOF, kata+Firecracker)
without changing semantics.

## Develop

```sh
make test                        # backend tests (go test ./...)
make race                        # tests with the race detector
make cover                       # enforce the coverage threshold (COVERAGE_MIN=50)
make web                         # rebuild the embedded dashboard
make run                         # run the server against ./data
cd web && npm run dev            # dashboard dev server (proxies /api to :8080)
```

CI (`.github/workflows/ci.yml`) runs build + vet + race tests + the coverage gate.

### Trace timeline

The trace viewer is a small purpose-built **span waterfall** (`web/src/components/
TraceTimeline.tsx`), not a third-party timeline library. General timeline libs
(vis-timeline, react-calendar-timeline, react-chrono) model *event/date* timelines,
not *duration* waterfalls with nested spans, and would fight our data model while
adding a heavy dependency — the same reason Jaeger/Tempo ship their own waterfall.
Ours is ~80 lines with zero deps: it reads the event log directly (intent→result
bars, call-key depth for nested `parallel_map` branches) and now has a time axis,
gridlines, and richer hover. If we later want flame-graph density or virtualized
rendering for very large traces, `react-flame-graph` or `perfetto`-style rendering
would be the upgrade path.

## MVP scope / not yet built

This is a playable MVP. Deliberately deferred from the full vision: prod
kata+Firecracker sandbox, Redis/Postgres event-log tiers, the egress proxy
(Path B), OTLP export to an external collector, and **real authentication** — the
dashboard identity is a trusted `X-Agentle-User` header (RBAC on top is real;
the `/v1` API uses real bearer tokens, but passwords/OAuth/sessions for the
dashboard are not built yet). Durable `recv()` suspension and its timer deadlines
are implemented; the resume dispatcher is an in-process ticker (a distributed
deployment would drive it from the event/timer tiers).
