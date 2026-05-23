I'm building an agent platform in golang.  I'd prefer to keep the code tight and readable by humans, using interfaces and good tests.  Prefer correctness, conciseness, then features.  Feel free to use as many libraries as reasonble.  This prioritizes security, correctness, scalability and functionality over visual editing, bells & whistles. 

The core of the agent platform is scripts written in Starlark.  This platform should have the feel of something like Google App Script, where editing, script management, secret management, triggers, history, traces are visible from an online control panel.  Please use codemirror to edit.  The dashboard can be vite+react, or another choice.

The execution model is as follows: The script runs until it hits an RPC, which are usually built-ins, LLM calls, tool calls, etc.  Then the RPC happens, potentially with retries, timeouts, etc, and then is memoized for next time.  Then the script runs again and makes more progress.  This happens repeatedly until completion.

This is also an actor model, because expensive state can be attached to a running workflow (e.g. git clone big-repo).  Trigger (cron, webhook) handlers are anonymous actors, but can dispatch to named actors.

The security model is capabilities-oriented hierarchically (i.e. admin > user > script), on both RBAC per-user and per-script.  Which tools and RPC you are allowed to use, along with their configuration, are limited.  e.g. you might have access to an http client, but only for certain domains.

Callables (i.e. both from starlark directly, and tool calls from embedded LLM/agents) should include:
- LLM calls (OAI format)
- MCP
- HTTP client
- structured concurrency (e.g. parallel map)
- some reasonable small stdlib (e.g. consistent, seeded random, sleep/wait, logging, etc)
- shell access (per-actor)
- kv store (per-actor)

Triggers include:
- cron
- webhook

Shell access is through a pluggable sandbox framework.  There should be implementations that work on both mac and linux+k8s+(kata or gvisor).  Shell access is to a per-actor linux vm with python, javascript, golang installed, inheriting network restrictions.  The shell acess should be into a home directory, which is saved to object storage on teardown and re-initialized later if the same actor resumes.  This enables LRUing the sandboxes.  This implementation should strive to minimize the cold start problem, perhaps by keeping standby sandboxes.  

The overall data model should be stored in a postgres database.  Runtime state, e.g. kv store, actor inboxes should be stored in a redis cluster.  Heavy state (i.e. filesystems) should be in object storage.

There also should be an OTel-compatible tracing, with a viewer in the dashboard.

---

# Design decisions

These resolve the load-bearing architectural questions behind the vision above. See INTERFACES.md for the Go-level contracts.

## Execution model
- Durable execution via deterministic replay (Temporal/Cadence-style). A script runs top-to-bottom; every capability call (RPC) is memoized in an append-only, per-execution event log. On resume or crash, we restore state and replay the log, actually performing only the RPCs that have no recorded result.
- Re-running from the top is O(n²) in RPC count; accepted, because memoized calls are near-free and n is small in practice (≲ ~100 RPCs/execution). Sticky in-memory caching of interpreter state keeps replay off the hot path — replay is the cold/recovery path.
- **Determinism is a hard invariant.** The Starlark VM has zero ambient authority and zero ambient nondeterminism. Time, randomness (seeded), iteration order, etc. are available only through memoized RPCs or deterministic stdlib.

## Event log
- Append-only, ordered, **single-writer-per-execution**. Default backend: Redis Cluster + AOF. Pluggable behind an interface so a more durable tier (e.g. Postgres) can be swapped in later without changing semantics.
- Single-writer is enforced by compare-and-set on the expected seq plus a **fencing token** from an execution lease, guarding against split-brain after a failover.
- **Durability is per-RPC, not per-backend.** Idempotent RPCs (LLM, HTTP GET) tolerate best-effort writes — worst case is re-spending on replay. Non-idempotent RPCs (POST, fs mutation) are **write-ahead**: durably record intent + idempotency key *before* executing, result after.
- Replay verifies each call's (capability, method, args-hash) against the recorded event; a mismatch is a nondeterminism / version-drift error and fails loudly.

## Filesystem ↔ log consistency (crash safety)
- The actor home dir (object storage) and the event log must not drift. **fs-mutating RPCs force an fs snapshot barrier on commit**; the log records the snapshot key + seq.
- Recovery: restore fs from the latest barrier snapshot, replay the log from that seq forward. Post-barrier RPCs re-execute, so they must be idempotent or themselves barriered.
- Home dir is overlay/CoW so snapshots are cheap enough to take per fs-mutating RPC.

## Sandboxes
- Pluggable sandbox framework, one `Sandbox`/`SandboxPool` interface across all tiers. The sandbox template is an **OCI image** (`Version.Image`), so the *same artifact* runs in dev and prod — no behavior divergence.
- **Prod: kata-containers + Firecracker VMM via a k8s RuntimeClass** (microVM isolation + snapshot/restore).
- **Dev: Docker / docker-compose.** Containers run Linux/OCI images matching prod, with clean primitives that map 1:1 to our model — cgroup limits (`--cpus`/`--memory`/`--pids-limit`), a volume/bind-mount for the home-dir overlay, and volume export for `Snapshot()`. compose models the local stack (engine, postgres, redis, object-store stand-in, egress proxy) and the network topology below. Shared host (VM) kernel — fine for dev/CI, **not** the prod multi-tenant boundary.

### Network egress (no ambient network)
Sandboxes get **no ambient network**. Two controlled egress paths share one per-actor allowlist (from the tool-config / grant), enforced at two points:
- **Path A — `http` capability (fine-grained, memoized).** The *script* calls `http`; it's a memoized RPC run by the Go-land executor in the engine process (scoped network). Durable, replayable, traced. SSRF guards live here.
- **Path B — egress proxy (coarse, in-shell, not memoized).** In-sandbox tooling that makes its own calls — `pip`/`npm`/`go mod`, `git`, agent-written subprocesses — reaches the network only through a bespoke egress proxy. The traffic is part of the enclosing `shell` RPC (one memoized unit), so it isn't individually memoized — exactly like a Temporal activity's I/O. Use Path A when a call must be durable on its own; bake heavy deps into the warm template to minimize Path B.

Proxy enforcement (do **not** trust `HTTP_PROXY` alone — untrusted code can ignore it):
- Sandbox joins a Docker **`internal` network** (no internet route); the proxy is dual-homed (internal + egress) and is the *only* way out — the hard boundary. compose declares this.
- Belt-and-suspenders: set `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` so well-behaved tools work; optionally an iptables transparent-redirect to catch processes that bypass the env.
- Policy: **CONNECT/SNI domain-allowlisting, no MITM by default** (control *who*, tunnel the bytes). **Optional MITM** (proxy CA baked into the image trust store) per tool-config for path-level rules + full egress auditing in traces.
- **Per-actor identity** at the proxy (per-container token or per-container IP) → applies that actor's allowlist and attributes egress to the execution in OTel.
- Implementation: bespoke Go proxy (e.g. `elazarl/goproxy`, or `net/http` + CONNECT) for dev + unified policy; **Envoy** as the prod-grade swap behind the same allowlist config.
- Cold start: Firecracker snapshot (prod) / warm container pool (Docker dev) = a warm per-language-version template; per-actor home dir = overlay restored from object storage on top.

## Capabilities & secrets
- A capability is a **pre-configured, secret-bound tool instance**, not a bare permission bit. The data model separates **tool configurations** (endpoint + secret-ref + limits) from **grants** (which principal/script gets which configured instance). The RBAC hierarchy (admin > user > script) selects granted instances.
- Secrets live in Go-land tools and are consumed at the RPC boundary; **they never materialize as values in the Starlark VM**, so they cannot leak into traces. A script that must compute with a secret gets a narrow operation (e.g. `sign(req)`), never raw bytes.
- Limits everywhere: Starlark execution-step limits, per-actor CPU/mem quotas, parallel-map fan-out caps, RPC timeouts/retries.

## Versioning
- Scripts are versioned; each save is an immutable version. An **execution is pinned to one version for its entire life** so replay stays stable across edits — in-flight runs finish on their pinned version, new edits create a new version.

## Observability
- Each memoized RPC maps to an OTel span; the event log and the trace share structure, so the user-facing trace viewer comes largely for free. Secret-refs are redacted by construction (never present).
- **Two trace consumers:** (1) *user-facing execution traces* — served by the dashboard from our **own span storage derived from the event log**, tied to execution/version, with secret redaction; the product feature, depends on no external backend. (2) *platform/engine observability* — standard OTel for debugging the platform itself.
- Wiring: app emits **OTLP → OTel Collector** (the swappable seam) → backends. App code is identical across environments; only the collector's exporters change.
- **Mac local demo:** backends are just containers in the compose stack — Docker Desktop runs them identically to Linux, so there's no Mac-specific concern. Use `grafana/otel-lgtm` (single container: Grafana UI + Tempo traces + Prometheus metrics + Loki logs, built-in OTLP) or **Jaeger all-in-one** for traces-only. Ephemeral/in-memory storage is fine for a demo.
- **Prod (linux):** same OTLP→Collector seam, exporting to Tempo/Jaeger (traces) + Prometheus/Mimir (metrics). Note: **Prometheus is metrics-only** — traces live in Tempo/Jaeger, not Prom.