- browser history # integration throughout so the back button works
- keep default as scripts
- do ui elements supercede each other?  how does it work when i call chat then form?  is this a stack?  to me, a stack makes sense (i.e. form modals over the chat), and then there needs to be some way to clear/pop.
- rather than mocking LLMs, can we integrate with ollama

---

> **Status (2026-05-24).**
>
> - **Browser history — done.** The active tab (and the focused run, e.g.
>   `#runs/ex_123`) lives in the URL hash; back/forward navigate between views and
>   within the Runs list. (`web/src/App.tsx`, `web/src/views/Runs.tsx`)
> - **Default = scripts — done.** Landing tab is `scripts` again (Apps kept in the
>   tab bar). (`web/src/App.tsx`)
> - **UI element stacking — done.** Panels are a stack: `ui_chat` pushes a base
>   chat, `ui_form` pushes a form *modal over it* and auto-pops on submit; added
>   `ui_pop()` / `ui_clear()`. Projection returns the full `panels[]` stack; the
>   dashboard renders the top form as a modal over the chat. New `stacked_ui`
>   example + tests. (`internal/vm/std_ui.go`, `internal/platform/ui.go`,
>   `web/src/components/UIPanel.tsx`)
> - **Coding agent — Phase 1 built; real LLM via an OpenAI-compatible backend.**
>   The harness is a real agentle script (`coding_agent` example) with the system
>   prompt baked in. The old blocker — "no real LLM offline, the mock only echoes,
>   so it can't author code" — is **resolved by pointing the `llm` capability at
>   any OpenAI-compatible server**: a local Ollama (no API key; models already
>   pulled here incl. `qwen2.5-coder:32b`) for offline/testing, or real OpenAI /
>   another hosted provider otherwise (`internal/caps/llm.go` now uses the real
>   client whenever `base_url` is set). **Phase 1 of the editor agent is built and
>   verified end-to-end against Ollama:** a docked split-pane Assistant in the
>   Scripts editor with an N-chats-per-script **tab strip** (create / switch /
>   double-click rename / auto-title / close), each tab a durable harness execution
>   bound to `chat:{script}:{chat}`; every turn carries the live buffer as
>   `source`; a working indicator + Stop. Seeded `ollama` config + `coding-assistant`
>   harness (hidden from the script list). (`internal/store/chat.go`,
>   `internal/api/chats.go`, `cmd/agentle/seed.go`, `web/src/components/AgentPanel.tsx`,
>   `web/src/views/Scripts.tsx`) Remaining (later phases): the agent *applying*
>   edits as inline diffs (apply/undo) + a `run` editor tool + run-result cards —
>   these need the harness to emit structured editor-tool calls (the
>   `read_source`/`apply_edit`/`run` vocabulary), not just chat replies.

---

# Design: a self-hosted coding agent in the script editor

**Goal.** An autonomous chat/coding-agent loop docked next to the CodeMirror
editor in the Scripts view. CodeMirror stays a native component; the *agent
harness* (system prompt, tool set, loop policy) is defined as an **agentle
script** — that's the "somewhat self-hosting" angle.

**Decided:** autonomous (not propose-only), and a nice streamed UI.

## Core idea

A coding agent is paced like a conversation, not a keystroke, so it fits the
durable model. The harness is essentially the existing `mcp_agent` example with
**editor tools** instead of MCP tools, plus `ui_chat`:

```
ui_chat()  +  durable recv loop  +  llm(tools=[editor tools])
```

Self-hosting payoff: the harness is forkable, and because it's just an execution
it shows up in Runs/Spend/trace like any other run — agent cost tracking for free.

## Chats: N per script (tabbed)

A script has **many** persistent agent chats, shown as a tab strip with a "+"
(per-session is just "start a new chat"). Each chat is its own durable execution
bound to a workspace like `chat:{script_id}:{chat_id}`; switching tabs loads that
chat's transcript, reopening the editor resumes them. Chats need create / switch /
rename (auto-title from the first message) / archive-delete; track them as a small
registry or derive from executions whose workspace matches `chat:{script_id}:*`.

**One buffer, N chats — the coherence rule.** All chats edit a single CodeMirror
source, so:
- only the **active** chat holds the edit/run lock (the buffer follows the focused
  tab); background chats may keep reasoning but cannot touch the buffer or run.
- every chat **re-reads the live buffer each turn** (`read_source`), so its
  transcript is *advisory history*, not a promise the file still matches — another
  chat may have rewritten it. (This is the Cursor model; accept it, state it.)
- autonomy is bounded by the lock: a background autonomous chat can't silently
  edit/run behind the focused one.

## The brain: `llm()` against an OpenAI-compatible backend

The chat panel, streamed step cards, diff/apply, run-result cards, and the
permission/stop controls are designed **once** against an abstract "agent
session." Behind it is a single backend: an `llm()` tool-loop inside an agentle
harness script. It's self-hosting (the harness is itself an agentle script),
durable, replayable, and cost-tracked — the session shows up in Runs/Spend/trace
like any other run.

The only requirement is a real model behind `llm()`. The `llm` capability already
speaks the OpenAI chat-completions format, so **any OpenAI-compatible endpoint
works** — the same config shape, just a different `base_url`/`model`/key:

- **Local Ollama (offline / testing).** Ollama serves an OpenAI-compatible API at
  `http://localhost:11434/v1` and ignores the bearer token, so it needs no key.
  Point an `llm` tool config at it (`base_url: http://localhost:11434/v1`, e.g.
  `model: qwen2.5-coder:32b`) and grant it to the `coding_agent` harness — the
  agent is real and runs entirely offline. Cost tracking still works (unpriced
  local models simply price at $0).
- **Hosted OpenAI (or any OAI-compatible provider).** Same config with the real
  `base_url` + an API-key secret; cost tracking is already wired for priced models.

The mock only echoes and can't author code, so a real demo needs one of the above.
One small enabler: the `llm` cap should select the mock only when `base_url` is
empty, so an Ollama `base_url` with no key uses the real local provider (today it
also requires a key).

## Editor-agent protocol (the shared surface)

Tools / client-mediated ops:

- `read_source` → current CodeMirror buffer (buffer stays client-authoritative).
- `apply_edit` → diff rendered inline; **autonomous = auto-apply with undo**.
- `run` → execute the *edited* source in the sandbox; return result + trace.
- `read_result` / `read_trace` → last run's output + spans.
- `list_capabilities` / `grant` (later) → wire tool grants from chat.

The agent never silently writes the version store; edits land as undoable diffs.

## Tool execution locus (hybrid)

- **reads + run: server-side.** The client syncs its buffer into the run's
  workspace; `read_source` is then a pure server read. `run` is a **scoped**
  capability that runs *the script being edited* with the editing user's grants +
  identity — never arbitrary code. Replayable (memoized like any capability call).
- **edit application: client-side.** Apply to CodeMirror, render the diff, keep an
  undo stack (the gesture and the live buffer are inherently UI).

## Autonomous loop + guardrails

- Bounded turns (existing `deadline()` / loop caps) + a run-cost ceiling + a hard
  **Stop** (a control message + deadline cancels the run).
- Per-action stream so the user sees every tool call as it happens; an undo stack
  for edits and a "revert all changes this session" escape hatch.
- Autonomy policy: auto-allow edits/runs within bounds; escalate destructive or
  out-of-scope actions to an explicit Accept gate.

## Nice UI

- A resizable split: CodeMirror on one side, the agent panel on the other (reuses
  the app-mode chat styling).
- A **chat tab strip** atop the agent panel: switch/create/rename/close the
  script's chats; the focused tab owns the edit/run lock (see "Chats: N per
  script").
- Streamed step cards: thoughts (muted), tool-call cards (read/edit/run + status),
  inline diffs (apply/undo), run-result cards (status + output + trace link), and
  the final assistant message.
- Working indicator + Stop; a turn/cost counter.

## System prompt (the harness brain)

The single highest-leverage artifact: it must stop the model from treating this
as a Python project. `{{CATALOG}}` is filled from `vm.Catalog()`, `{{GRANTS}}`
from the script's current grants, `{{CONTEXT}}` is appended each turn (current
source + last run summary). Draft:

```
You are the coding assistant inside agentle, a platform for DURABLE AGENTS. You
help the user write and debug ONE agentle script, `main.star`, open in their
editor. Make precise edits and verify them by running.

## What agentle is — read this; it is NOT a normal Python project
- A script is Starlark (a deterministic Python dialect), executed server-side by
  a REPLAY ENGINE, not a Python interpreter. There is no pip, no filesystem, no
  threads, and no network except through granted capabilities (below).
- The engine memoizes every capability call and REPLAYS the script to resume
  after a suspend. So the script must be deterministic: every side effect and
  every source of nondeterminism (time, randomness, I/O, model calls) must go
  through a capability. Never reach outside them.
- Entry point: `def main(input):`. `input` is the event envelope
  {"id","kind","workspace","data"}. Read runtime data from input["data"] (a dict,
  may be None — use `(input.get("data") or {})`). Whatever main returns is the
  run's JSON output.

## The file
Edit only `main.star`, and keep it valid Starlark with a `main(input)` function
at all times. Any other files in the folder are scratch and are ignored.

## Starlark, not Python
DO use: def, lambda (single expression), bounded `for ... in range(n)`,
if/elif/else, lists/dicts/tuples, comprehensions, string methods, and
len/range/enumerate/sorted/min/max/sum.
DON'T use: import, class, while, recursion, try/except, open(), set literals,
f-strings, or global mutation. There are no modules — only the builtins below.

## Capabilities — the only outside world
Always available: log, now, sleep, rand, rand_int, store, fetch, keys, send,
recv, deadline, parallel_map, ui_chat, ui_say, ui_form, field.
Granted per script (a call fails if its grant is missing): llm, http_get,
http_post, shell, mcp_call. (mcp_list_tools is always callable; empty with no grant.)

Signatures:
{{CATALOG}}

This script's current grants: {{GRANTS}}. If a change needs an ungranted
capability, say so and propose the grant — do not just call it.

## Suspends are durable yield points
recv(), ui_form(...), and deadline(secs, lambda: recv()) suspend the run durably
(it may sleep for days and resume by replay). Bare recv() waits forever; bound it
with deadline(secs, lambda: recv()) (returns None on timeout). Loop chat/agent
turns with a bounded `for _ in range(N)` — never `while True`.

## Running
To execute, use the `/run` command — do NOT use a terminal; there is no
interpreter. `/run` runs the current main.star with the script's grants and an
input envelope and returns the output + an execution trace. After any non-trivial
edit, run it and read the trace before claiming success. To pass input, set the
run input (JSON placed at input["data"]).

## Edits
Your edits apply as diffs to the user's live buffer (auto-applied; user can undo).
Re-read main.star before editing — it may have changed since your last turn (the
user or another chat can edit it). Prefer minimal diffs; don't reformat unrelated
code.

## Style
Be concise: state the plan in a sentence, make the edit, run to verify, report the
result (with the trace if it failed). Don't restate the whole file for a small diff.

## Canonical shape
def main(input):
    data = input.get("data") or {}
    name = data.get("name", "world")
    log("greeting", name)
    reply = llm([{"role": "user", "content": "Greet " + name}])
    return {"greeting": reply["content"]}

{{CONTEXT}}
```

Notes:
- `{{CATALOG}}` reuses the same `vm.Catalog()` we feed editor autocomplete, so the
  agent and the editor always agree on what exists.
- `{{GRANTS}}` keeps the agent honest about llm/http/shell/mcp — the #1 way the
  model goes wrong is calling an ungranted capability.
- The canonical-shape block + the "Starlark, not Python" do/don't list are what
  actually move output quality, since the model will otherwise reach for `import`,
  `while`, and `open()`.

## Determinism / security

- The harness memoizes each `llm` + tool call → the whole session is replay-safe
  and shows up in the trace + Spend like any other run.
- The run-the-edited-source capability is scoped to this script + this user's
  grants; it never runs arbitrary code, only the buffer being edited.

## Phased plan

1. **Client surface + protocol.** Split editor pane; send `{text, source}` per
   turn; render streamed step cards + diffs (apply/undo) + run-result cards +
   Stop. Drive it with the `coding_agent` harness so it's playable offline.
2. **Wire the harness + an OpenAI-compatible model.** Seed a forkable
   `coding_agent` script (`ui_chat` + recv loop + `llm(tools=editor tools)`); add
   the scoped run capability; wire the same surface. Configure `llm` against a
   local Ollama server (no key, offline) or a hosted OpenAI-compatible provider.
   Enabler: the `llm` cap selects the mock only when `base_url` is empty (so a
   base_url with no key uses the real local provider).

## Open questions

- ~~Per session or per script?~~ **Resolved: N chats per script, tabbed** (see
  "Chats: N per script"). Persistent, one durable execution each; per-session is
  just a new tab.
- ~~Multi-file?~~ **Resolved:** one `main.star` per script for now — the editor
  binds a single buffer. If scripts become multi-file later, the editor and the
  `read_source`/`apply_edit` ops generalize to a file list.

### Sources
- Ollama — OpenAI compatibility: https://github.com/ollama/ollama/blob/main/docs/openai.md
