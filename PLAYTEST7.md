# PLAYTEST7: Evals, Golden Datasets, and Replay-Based Scoring

## Goal

Let a script author (a) promote real runs into a per-script golden dataset, labeled
success/failure, and (b) re-run *new versions* of the script against those goldens to
answer two questions: "did we regress below the bar?" (gate) and "did we get better?"
(promotion signal). Default scoring is LLM-as-judge; custom code/script judges are also
supported.

## Core idea: fix the human inputs, run the agent live, manage external side effects

"Replay" in the durable engine means deterministic replay of *all* recorded effects.
That tests nothing about a new version — it just checks the prompts are byte-identical.
For an eval we hold a different line:

- **Fixed:** the user's side of the conversation (recvs), served by a user simulator
  seeded from the golden run (see persona.md).
- **Live:** the agent's own logic and LLM calls.
- **Replayed-or-gated:** external HTTP, via the cassette proxy below.

The hard problems are the two non-deterministic, non-rollback-able surfaces: mid-run user
input (recv) and external writes. Internal state is not a problem — an eval runs in a
**throwaway sandbox booted fresh from a snapshot** (`sandbox.Acquire` from a snapshot key),
so anything it does locally is discarded by dropping the sandbox. Two facts about the
current engine shape this: recovery is restore-latest-then-replay, *not* roll-back-in-place,
and snapshots are debounced (~60s). So the eval must **force a snapshot at run start** to
get a clean fork point. Only *external* side effects escape the discard, so only they need
real management.

**Prerequisite (tracked separately):** the safety story rests on 100% of egress being
forced through the single HTTP proxy — no subprocess, static binary, raw socket, or
out-of-band DNS bypass. That proxy is the in-progress "Path B"; this plan assumes it lands.

## The HTTP proxy as a record/replay cassette

The proxy records every external request/response on the golden run into a cassette keyed
by request. On eval re-run:

|            | read (pure)        | write (default)                      |
|------------|--------------------|--------------------------------------|
| **hit**    | replay (safe, free)| replay response — write never escapes (safe, free) |
| **miss**   | go live (costs $, safe-ish) | **DANGER: real side effect — gate** |

A write *hit* is safe: we serve the recorded response, the agent sees what it would have
seen, no real mutation happens. The dangerous cell is the **write miss** — a new request
the new version invented, with no recorded response, where proceeding means actually
issuing it.

**Evaluable segment = start → first cache-miss write.** Read-misses and write-hits run
straight through. Only a write-miss forces a decision; policy is per-eval:
**`fail` (default)** | `go_live` (explicitly allowed, e.g. sandbox/staging target) |
`flag_for_human`.

**Script convention: write-late.** Scripts are expected to do their reads/planning before
any external write, so the read-prefix is the substantive part of the trajectory and the
prefix-only MVP is worth shipping on its own. This is a convention the eval leans on, not
an enforced invariant — so **report coverage** ("eval covered N% of the trajectory before
the first write-miss"). A write-early script then shows visibly thin coverage instead of
silently passing on almost nothing. Consider linting write-before-read later if drift
becomes a problem.

This subsumes the older "stop at first write" idea — that was the special case where the
cassette is empty. It also unifies recv divergence with HTTP divergence: a missing recv
is just a cache miss on the user channel.

**Engine-side: a per-kind replay policy.** Today the mediator memoizes/replays *every* RPC
by `CallKey` (all-or-nothing). Eval needs a **selective mediator** whose replay decision is
a per-RPC-kind table:

| kind | eval behavior |
|------|---------------|
| `llm` | **live** — it's the thing under test |
| `http` | cassette: replay-on-hit, gate-on-write-miss, live-on-read-miss |
| `recv` | simulator (or replay the recorded answer on an exact match) |
| `shell` | live, in the throwaway sandbox |
| clock / random | pin to recorded values, else they add noise to judge comparisons |

`llm` and `http` both leave the box as HTTP egress through the same proxy, yet get opposite
treatment — so the split must happen at the *kind* layer (which the mediator already
distinguishes; cost tracking reads `llm` results out of the log), not at the proxy. The
position-based `CallKey` gives cache-miss detection for free, so this is a new mediator
*mode*, not a new keying scheme.

**Cassette match key.** Don't key on raw request bytes — real requests carry volatile
fields (nonces, timestamps, idempotency keys, auth, signatures, multipart boundaries), so
byte-keying yields a false miss on every replay. Define a canonicalization: significant
headers/body fields vs an ignored set, volatile fields ignored by default, per-endpoint
overrides where matching needs to be tighter.

## tool_policy: read vs write classification

- **Default: every external call is a write** (fail-safe). The expensive consequence of a
  wrong "read" tag is a real side effect; the cheap consequence of a wrong "write" tag is
  an unnecessary gate. Bias toward gates.
- Seed read/write hints from MCP tool annotations (`readOnlyHint`, `destructiveHint`,
  `idempotentHint`). These are **advisory, not guarantees** — honor them automatically
  only for first-party/vetted servers; for untrusted MCPs require operator opt-in.
- Annotations are not widely supported yet, and purity is often per-*call* not per-*tool*.
  So don't depend on the ecosystem: maintain a local `tool_policy` table keyed by
  `server/tool`, seeded from annotations where present, operator-overridable. You're
  tagging the handful of tools you actually use.
- Missing annotation → write-by-default → gated. Lack of support costs *unattended
  throughput*, never *safety*. The tag only matters on a **miss** — it decides whether a
  novel call can run unattended (pure) or must gate (write). It is not a correctness
  mechanism; hits are safe regardless.

## The user simulator and persona.md

The simulator replaces the recorded recvs. Matching recv *count* is the wrong invariant:
recvs are bound to the question that elicited them, so replaying "$500" at version B's
"what's your destination?" is garbage. Instead, a persona-seeded simulator answers version
B's *actual* questions.

### persona.md — an explicit, UI-editable artifact

The persona is authored, not an inferred shadow. This is what decouples a golden from any
one version's trajectory (and makes evals debuggable). Frontmatter carries the
machine-load-bearing knobs; prose is freely human-editable.

```markdown
---
on_unknown: refuse        # or: improvise_consistent (seeded RNG, append-as-fact)
style: naive              # or: goal_locked
context: surface          # or: oracle (sees internals — capability-ceiling mode only)
# extends: ./personas/impatient-budget.md   # (future) reusable traits
---
You're a budget-conscious traveler, mild time pressure, US passport.
You stated: destination Tokyo, ~$800 ceiling, prefer morning departures.
You did NOT specify: hotel, return date, seat class.
```

- **`on_unknown` is non-negotiable structure.** A persona inferred from version A's
  transcript is only as complete as the questions A asked. The moment B asks something new,
  an unconstrained sim *hallucinates* an answer and you score B against fabricated ground
  truth. **`refuse` (default)** = honest about coverage; `improvise_consistent` = seeded
  fill, recorded as a new fact so it stays consistent for the rest of the run. Never silent
  inconsistent invention.
- **`style`**: `naive` (realistic — may accept a subtly-wrong answer and stop) vs
  `goal_locked` (pushes until the criterion is met). Default `naive`; the success criterion
  lives in the *judge*, not the persona, so the sim can be fooled (realistic) while scoring
  stays objective.
- **Autofill button**: drafts persona.md from the golden transcript. Annotates what it
  inferred (and from which turn) vs what it's guessing, so the human knows where to look.
  Draft-into-diff / fill-when-empty — never silently overwrites human edits on re-run.
- **`extends` seam (not v1):** don't weld persona to golden 1:1. The *who* (style, risk
  tolerance, expertise) is reusable; the *what* (this task's goal + stated facts) is
  per-golden. v1 can be 1:1 but leave the structure so traits can later be factored out.

### Simulator context: user-visible surface only

The simulator sees **persona + transcript-so-far + the rendered recv request** — exactly
what a real user would have seen. It does **not** see raw internal variable state. Handing
it the agent's internals makes it omniscient: it answers using info the agent never
surfaced, papering over UX failures and inflating the pass rate (the worst eval failure
mode). What the user can know becomes an *authored decision* — what the script renders into
the recv prompt. `context: oracle` exists for deliberate capability-ceiling measurement
only; default is `surface`.

### Self-consistency gate

Before *any* persona goes active — autofilled or hand-written — replay the **origin
version** through it and confirm it reproduces the golden outcome. A human can write a
plausible-but-wrong persona too; authorship does not bypass validation. Show pass/fail in
the UI next to the save button. This is the gate that makes trusting the artifact OK.

## The judge

Separate, UI-editable artifact (e.g. `criteria.md`), independent of the persona.

- **Asymmetry with the simulator:** the simulator is constrained to realism (surface
  only); the judge is *unconstrained* — full trajectory, internals included — because it
  evaluates rather than role-plays.
- **Explicit rubric, not "infer as you go."** Success is task-specific; a structured
  verdict (criterion → pass/fail → evidence) is stable and comparable across versions.
  Freeform verdicts drift.
- **Calibrate against the up/downvote labels.** Those are ground truth — measure
  judge/human agreement (accuracy, κ) before trusting verdicts. Pin model version, temp 0,
  fixed prompt.
- **Failure-tagged goldens invert the semantics:** "eval success" means the new version
  *avoided* the recorded failure (or fixed it = improvement). Define this in the criterion.
- **Judging mode: `prefix` vs `full`.** A read-prefix eval stops at the first write-miss,
  so the task is *not complete* — judging it as "did the task succeed?" marks every prefix
  run as fail. Make it an explicit eval mode passed to the judge: `prefix` asks "did the
  agent correctly reach the right action?", `full` asks "did it succeed?". The criterion is
  written for the mode (or carries both).
- Custom judges (code/script) are first-class for cases where correctness is checkable
  programmatically; reserve the LLM judge for fuzzy outcomes (cost + latency at scale).

## Feedback labels (build first)

None of this exists yet — there is no upvote/downvote, rating, or success/failure column on
runs today (`store.Execution` has status but no human label). Build it first and standalone:
it's cheap, independent of the proxy/cassette work, and the calibration corpus needs
**wall-clock time** to accumulate. If the judge ships before labels exist, it lands
uncalibratable and stays that way for weeks.

- **Pointwise label on a run:** up/down (= success/fail). Stored on the execution (new column
  or a small `run_feedback` table keyed by execution id).
- One signal feeds two things: the golden's `label`, and judge calibration (measure
  agreement before trusting the judge).

## Judgment: pointwise success/fail

One axis: did the run succeed? (matches up/downvote.) This is the **ship gate** — did
version B still pass the goldens A passed? Preference/ranking ("is B *better* than A") is
explicitly out of scope: the gate only asks whether B clears the bar each golden sets, not
how runs compare to one another.

## Determinism and variance

Even with recvs fixed and HTTP replayed, the live LLM is non-deterministic. A single
replay is not a verdict — score over N samples (pass@k / pass-rate) with a flakiness
threshold, or the eval will be noisy and lose trust. Pin the judge separately.

## Eval-run guardrails

Eval runs the LLM live, multiplied by pass@k × goldens × versions — a real cost surface —
and a buggy new version can loop. Wire the existing cost accounting as a **hard per-eval
budget ceiling**, plus **step and wall-clock caps**, so a divergent or looping version
**fails closed** rather than burning unbounded money and time.

## Golden dataset schema

A golden is decoupled from any version's internal trajectory (so it doesn't rot when the
script changes):

```
golden = {
  task_setup,            # initial state / inputs
  persona_ref,           # persona.md
  criterion_ref,         # criteria.md (judge)
  cassette_ref,          # recorded HTTP request/response tape
  label,                 # success | failure  (correctness, from up/downvote)
  origin_version,        # for the self-consistency gate
}
```

Upvote/downvote = the pointwise success/failure label.

## Sequencing

The durable replay engine, `recv`, run storage, immutable script versioning, pinnable LLM,
and cost tracking already exist — this is a new layer on top. Two things must exist before
any eval scoring: **feedback labels** (build now, the corpus needs wall-clock time) and the
**egress proxy** (Path B, tracked separately). Then:

0. **Feedback labels** — up/downvote on runs. Independent of everything else; ship first so
   the calibration corpus grows while the rest is built.
1. **Cassette + selective mediator** — record request/response on golden runs; on eval
   re-run replay HTTP/recv from the cassette, LLM live, cache-miss detection. (Needs the
   proxy.)
2. **Eval runner** — force a start-snapshot, boot a throwaway sandbox, run the new version
   against the cassette, **read-prefix only** (to first write-miss), report coverage.
3. **LLM judge** with explicit rubric — now calibratable against the accumulated labels.
4. **User simulator + persona.md** + autofill + self-consistency gate — unlocks
   conversational scripts and full-length evals.
5. **Multi-sample variance** (pass@k), `tool_policy` table, `extends` persona factoring.

## Later (operational maturity)

Deferred, but cheaper to design for than to retrofit:

- **Golden health.** Goldens rot — a recorded API's real behavior drifts, or a golden stops
  reproducing even for its origin version. Periodically re-run the self-consistency gate,
  store last-validated + status, quarantine failures; else the dataset degrades silently and
  verdicts become noise.
- **Eval runner on the durable engine.** The runner is long (N samples × goldens) — make it
  just another durable run so a crash resumes instead of restarting from zero.
- **Fast-subset vs full-set.** Running every golden × pass@k on every save is too slow to
  stay in the loop. Fast subset on save, full set nightly / on-promote.

---

## Appendix: original sketch

- I'd like to be able to do evals.
- I'd like to be able to promote runs to golden dataset per script, with success or failure tags.  Upvote/downvote?
- Other versions of the same script can attempt replay on those runs.
- Default judge is LLM-as-a-judge, where the LLM asks whether the task succeeded or failed, inferring context as it goes.
- Also accept custom judges, implemented as code or script.
- Agents trajectories have two hard problems: mid-run user input via recv, and writes that would be unsafe to replay.
- Given checkpointing of the shell fs, and given single http proxy for all network traffic, all effective writes are via http (can always roll back rm -rf)
- I think this means we should assume the same number and content of recvs (replay), and diverge/fail if there are more/less of them.
- So the evaluable segment is either from start to the first write, or from start to finish if we have some flag set to say writes are ok.
