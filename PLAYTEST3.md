- If tools/capabilities expect secrets, make sure those keys are populated on the secrets page(s).  while secrets are write-only, existance or need of the keys should be obvious
- secrets should probably be a pluggable impl
- running a script should probably imply a save
- the timeline probably needs a better sense of scale.  three different sub-milli duration segments have wildly different lengths.
- can we track token count (input/output/cache), attach it to the otel traces, and correlate with openrouter pricing on the related model to effectively track spend
- do an html prompt modal rather than a native prompt modal to pick the name of the script
- the llms should default to the full tool list implied by the capabilities
- Error in mcp_list_tools: mcp.list_tools: capability not granted.  should always be able to list tools.  this list may be limited tho.  in the zero permission case, you'd get the empty list.
- need to be able to edit and delete tool configs once created.
- i really don't want recv to take a timeout.  i think you could do a separate primitive, e.g. timeout(300, lambda: recv) # adapt to my bad python :D
- i'm very interested in simple ui, e.g. a form or a chat that uses recv to implement itself.  like there's a simple dsl for the form or chat details, and if you run the script from the dashboard, it pops the form or chat and lets you interact.  Not sure if this needs a plan.  if its clear, prototype it.  if not, ask for help.
- i'm very interested in a plugin system.  Not sure if this needs a plan.  for example, you could implement a mcp tool via python that runs in the typical sandbox.  Would be managed by agentle.

---

# Plan (decided 2026-05-23)

Decisions from the planning Q&A are folded in below. Items are grouped into phases
(quick wins → primitives → big features). Determinism (D) and security (S) notes
flag the things that are easy to get wrong.

## Decisions at a glance
- **#5 cost:** live OpenRouter pricing (fetched + cached), not a static table.
- **#10 timeout:** a block-scoped deadline, not a recv arg. Starlark has no `with`,
  so it's `deadline(secs, fn)` — a thunk that bounds *every* suspend inside it.
- **#7/#8 tools:** support multiple MCP servers; `mcp_list_tools()` returns the
  union and never errors (empty when none granted); `llm()` auto-defaults to that union.
- **#11 form/chat:** one small declarative DSL covering both forms and chat.
- **#12 plugins:** a generic capability-plugin interface (add any capability /
  executor), with a sandboxed MCP tool as the first concrete plugin kind.
- **#2 secrets:** extract a `SecretStore` interface; add a Vault provider first.
- **#4 timeline:** keep linear time but enforce a min bar width + sharper ticks + zoom.
- **#3 run=save:** Run auto-saves a new version only when the editor buffer differs.

## Phase 1 — quick wins (UI + small backend)
- **#3 Run implies save.** Scripts.tsx `run()` first saves a version *iff* `source`
  differs from the latest saved version, then runs that version. Show the version it
  ran. (Avoids the "edited, hit Run, ran the old code" trap.)
- **#6 HTML name modal.** Replace `window.prompt` in the example gallery with an
  in-app modal component (name + Create/Cancel). Reusable for other prompts.
- **#9 Edit/delete tool configs.** Add `DELETE /api/configs/{id}`; Settings: click a
  config row to load it into the form (edit = upsert by id), plus a delete button.
  Deleting a granted config leaves a clear "config not found" run error (already the
  case); the grants UI will warn about dangling grants. (S: deletes are admin-only.)
- **#1 Secret visibility.** Per tool config, show whether its `secret_ref` is set
  *and present* in scope (ok / "secret not set" / none); warn on the script grants
  list when a granted config references a missing secret. New store check:
  `SecretExists(name, scope)`. (S: still names-only, never values.)
- **#4 Timeline scale.** Enforce a readable min bar width, add finer/zoomable ticks
  and a zoom (drag-select or +/-) so sub-milli spans are visible without distorting
  durations. (Linear time kept; log-scale rejected for now.)

## Phase 2 — execution primitives (engine/VM)
- **#10 `deadline(secs, fn)`.** A combinator establishing an ambient absolute
  deadline for the dynamic extent of `fn`; `recv()` (no longer taking a timeout)
  reads the innermost ambient deadline, suspending until a message *or* that
  deadline. `recv()` with no surrounding deadline suspends indefinitely.
  - D: the absolute deadline = memoized `now()` + secs, pushed on a VM-thread stack;
    replay reproduces it exactly. Remove recv's `timeout` arg + the deadline math in
    `bRecv`; thread the ambient deadline into the inbox `recv` call args instead.
  - On expiry `recv()` returns `None` (so `deadline(5, lambda: recv())` → value|None).
- **#7/#8 Multi-MCP + auto-default tools.**
  - Make `mcp_list_tools()` an always-available builtin that fans out to *all*
    granted MCP servers (empty list, no error, when none granted). Tools are tagged
    with their server so `mcp_call` can route. (D: each `tools/list` is a memoized RPC.)
  - Support several MCP grants: replace the single `"mcp"` env slot with an MCP
    *router* keyed by config id/name; `mcp_call(tool, args, server=?)` resolves the
    server (unambiguous tool name → infer server; else require `server=`).
  - `llm(messages)` with no `tools=` defaults to the union from `mcp_list_tools()`
    (+ future plugin tools). Explicit `tools=[]` opts out. (D: the implicit list call
    is memoized; document the behavior so it isn't surprising.)

## Phase 3 — cost tracking (#5)
- Capture `usage` (input/output/cache tokens) from each `llm()` result — already in
  the memoized result, so it's in the event log (D: deterministic).
- A Go-land `pricing` service fetches OpenRouter model prices (cached w/ TTL,
  refreshed out-of-band) — **never** an in-VM RPC, so replay is unaffected.
- Trace projection attaches `tokens_*` and computed `cost_usd` as span attributes;
  add a per-run cost total and an aggregate spend view. When OTLP export lands, emit
  the same as OTel span attributes. (S: model id only, no secrets.)
- **Rollups at every level:** per run, per script, per workspace, per user, per
  model, and a global total (with a time window). One spend query keyed by
  dimension; the dashboard exposes each cut.

## Phase 4 — pluggable secrets (#2)
- Extract `SecretStore` (Resolve/Put/Delete/ListNames/Exists, scope-aware) from the
  SQLite store; keep SQLite as the default provider.
- Add a **Vault** provider (KV v2) as the first external backend; provider chosen by
  config/env. (S: provider creds come from process env/role, not the DB.)
- Tool configs keep referencing secrets by name; only resolution changes.

## Phase 5 — generic capability plugins (#12)
- A **plugin** declares the capabilities/executors it provides and how it runs.
  Plugin kinds, behind one registry/interface:
  1. **Sandboxed subprocess plugin (first):** a program (e.g. Python) run in the
     standard sandbox that speaks MCP/JSON-RPC; agentle manages its lifecycle and
     exposes it as an MCP server → tools (composes with Phase 2's multi-MCP). Your
     "Python MCP tool" example is exactly this.
  2. **In-process Go plugin:** registers an `engine.Executor` directly (built-in /
     compiled extensions).
- Manifest (name, version, provided capabilities, sandbox image, health), install/
  enable/disable, and a registry the platform consults in `buildExecutor`.
- S: plugins are capabilities → bound by grants + RBAC + sandbox egress like any
  other; a plugin cannot grant itself authority. D: plugin calls are memoized RPCs.
- Big surface — likely its own design doc before building; Phase 5 starts with the
  manifest + registry + the sandboxed-MCP kind.

## Phase 6 — form/chat DSL (#11)
- A script declares a UI via a small DSL, e.g. `ui_chat(title=...)` or
  `ui_form([field(...), ...])`; the declaration is returned/registered so the
  dashboard knows to render a panel when you run from the UI.
- Interaction maps onto the actor model: the dashboard panel `send()`s user
  input/field values into the run's workspace; the script `recv()`s them (using the
  Phase-2 `deadline`), and `send()`s messages/needs back for the panel to render.
- Run lifecycle: a UI run suspends on `recv()` (already durable) and the panel
  resumes it on each submit; closing the panel/timeout ends the session.
- Needs its own mini-spec (field types, validation, chat message schema) — draft
  the DSL, get your sign-off, then build chat first, forms second.

## Suggested sequencing
Phase 1 (independent, fast) → Phase 2 (primitives others build on) → Phase 3 →
Phase 4 in parallel → then the two big features (5, 6), each with a short spec PR
before implementation.

## Resolved
- Spend rollups: **every level** (run/script/workspace/user/model + global).
- `deadline(secs, fn)` thunk shape: **approved** (Starlark has no `with`).
- Phases 5 (plugins) & 6 (form/chat): **designed** — full notes below, all
  sub-questions resolved (transport, scope, fields/validation, message richness).

## Phase 5 design — generic capability plugins

**Decisions:** per-call subprocess for v1 + an LRU-pooled long-lived kind as the
scaling path; plugin code is DB-stored source + manifest; a plugin is just a
grantable capability (RBAC + sandbox egress apply).

**Seam.** `platform.buildExecutor` consults a plugin registry for capability names
it doesn't handle natively. A plugin registers one or more capability names →
a factory that builds an `engine.Executor` from the tool config (+ secret).

**Plugin kinds (one interface):**
1. **Subprocess (sandboxed), per-call — v1 default.** Each capability call is one
   `sandbox.Exec` of the plugin entrypoint with a subcommand + JSON over
   stdin/stdout: `entrypoint list` → tools JSON; `entrypoint call <tool>` reads args
   on stdin, writes result JSON. Reuses the batch sandbox + warm pool; each call is
   already a memoized RPC, so replay/crash-safety are free. Stateless between calls
   (durable state goes to kv / the home dir).
2. **Subprocess, persistent (LRU) — opt-in, later.** The plugin runs an MCP server
   on a **local port inside the sandbox**; the engine connects over a controlled
   loopback/forward (MCP over HTTP) and the pool is **LRU-evicted** with idle timeout
   + health checks. Requires a new `Sandbox` primitive (a long-lived process + a
   reachable port) that the prod tier must also implement — hence deferred. Note:
   in-process plugin state can't survive replay, so persistent is a latency
   optimization, not a state mechanism.
   - **Scope = shared multi-tenant pool** (max reuse). Consequence: persistent
     plugins **MUST be declared stateless / multi-tenant-safe**; per-actor secrets +
     egress allowlist are injected **per request** (auth header / request context),
     never baked into the shared process. A plugin that needs per-actor state or
     holds a secret in memory must use **per-call mode** (fresh isolated sandbox)
     instead. (Per-call stays the safe default; persistent is the vetted fast path.)
3. **In-process Go plugin.** Registers an `engine.Executor` directly (compiled
   extensions / built-ins).

**Manifest (DB):** `{name, version, runtime (python|node|…), entrypoint,
mode (per_call|persistent), provides:[{capability, kind:"mcp"}], image?, egress}`.
Admin uploads source + manifest; enable/disable; the platform builds executors on
demand.

**Composition with Phase 2:** a subprocess MCP plugin registers as another MCP
server in the multi-MCP router, so its tools automatically join the
`mcp_list_tools()` union and `llm()` defaults.

- D: plugin tool calls are memoized RPCs; non-idempotent by default (write-ahead).
- S: admin-only install; plugin runs under the actor's grants + sandbox egress; it
  cannot self-escalate. Code review of uploaded plugins is the operator's job.
  Persistent (shared) plugins additionally must not retain per-tenant state in
  memory — see kind 2.

## Phase 6 design — form/chat DSL

**Decisions:** a `ui` declaration + the existing actor (recv/send) loop; inbox +
polling transport. Chat first, then forms (shared descriptor + transport).

**Builtins (a `ui` capability):**
- `ui_chat(title=, intro=)` — declare a chat panel (records a UI descriptor on the
  run so the dashboard opens a chat).
- `ui_say(text, role="assistant")` — append a message to the run's UI transcript
  (shown in the panel). Rendered as **markdown + typed blocks**: prose is sanitized
  markdown, and `ui_say` accepts optional structured blocks (`code`, `table`,
  `image`) via a small block schema for richer panels.
- `recv()` — receive the user's next message (durably suspends; the panel resumes
  the run on submit). Bound waits with `deadline(secs, fn)` from Phase 2.
- `ui_form(fields) -> values` — declare a form and suspend until submit; sugar over
  "declare descriptor + one `recv`". `field(name, label, type=, options=, required=,
  default=)`. **Field types (v1): text, textarea, number, select, checkbox**
  (extensible). **Validation:** `required` + type are checked client-side; all other
  rules are enforced by the **script re-emitting the form with error messages** (a
  recv loop) — keeps the DSL small and validation logic in the script.

**State & transport:**
- The `ui_*` declaration + `ui_say` are memoized RPCs; inbound user messages are
  `recv` results — so the descriptor + full transcript are a projection of the event
  log (replay-safe).
- `GET /api/executions/{id}/ui` → `{descriptor, transcript, status}` for the panel
  to render/poll (~sub-second, matching the dispatcher tick).
- `POST /api/executions/{id}/messages` (RBAC: can-see-exec) → `send()` into the
  run's workspace → the dispatcher resumes the parked run.

**Lifecycle:** dashboard run → script calls `ui_chat`/`ui_form` → suspends on
`recv` → dashboard sees the descriptor and opens the panel → user submits → message
delivered → run resumes → loop (chat) or return (form). Closing the panel or a
`deadline` expiry ends the session.

- D: user inputs and UI output are all in the event log → replay reproduces the
  session. S: only the exec's owner/admin can read the UI or post messages.
- Block schema (v1): `{type:"code", lang, text}`, `{type:"table", columns, rows}`,
  `{type:"image", url|data, alt}`; unknown block types degrade to text.

## Suggested build order
Phase 1 (independent, fast) and Phase 2 (primitives) are fully specced and ready to
build. Phases 3–4 follow. Phases 5 & 6 are specced above; each warrants a short
design-doc/PR confirming the new seams (plugin registry / `ui` capability) before
the bulk of the code.