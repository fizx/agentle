import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import type { Capability, Example } from '../types'

// Docs is a single-page manual with a sticky table of contents. The prose
// (quickstart, concepts, workflows) is hand-written; the Capabilities and
// Examples sections are generated from the live catalogs (/api/capabilities and
// /api/examples) so the reference can't drift from what the runtime actually
// ships. The TOC shows one level of subheaders, expanded under the active
// section, and a scroll-spy keeps the active entry in sync as you scroll.
type Sub = { id: string; title: string }
type Section = { id: string; title: string; subs?: Sub[] }

const SECTIONS: Section[] = [
  { id: 'intro', title: 'Introduction' },
  { id: 'quickstart', title: 'Quickstart' },
  {
    id: 'concepts', title: 'Core concepts', subs: [
      { id: 'concepts-scripts', title: 'Scripts & versions' },
      { id: 'concepts-caps', title: 'Capabilities & grants' },
      { id: 'concepts-durable', title: 'Durable execution' },
      { id: 'concepts-workspaces', title: 'Workspaces' },
    ],
  },
  { id: 'writing', title: 'Writing a script' },
  { id: 'tabs', title: 'The dashboard' },
  { id: 'capabilities', title: 'Capability reference' }, // subs injected from the live catalog
  { id: 'examples', title: 'Examples' },
  { id: 'evals', title: 'Evals & goldens' },
  {
    id: 'plugins', title: 'Plugins', subs: [
      { id: 'plugins-script', title: 'Script plugins' },
      { id: 'plugins-native', title: 'Native plugins' },
    ],
  },
  {
    id: 'triggers', title: 'Triggers & API', subs: [
      { id: 'triggers-cron', title: 'Triggers' },
      { id: 'triggers-api', title: 'Programmatic API' },
      { id: 'triggers-roles', title: 'Roles' },
    ],
  },
]

const slug = (s: string) => 'cap-' + s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

export default function Docs() {
  const [caps, setCaps] = useState<Capability[]>([])
  const [examples, setExamples] = useState<Example[]>([])
  const [active, setActive] = useState('intro')
  const mainRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    api.capabilities().then((c) => setCaps(c || [])).catch(() => {})
    api.examples().then((e) => setExamples(e || [])).catch(() => {})
  }, [])

  // Capabilities grouped by their catalog group, preserving first-seen order.
  const capGroups = useMemo(() => {
    const order: string[] = []
    const by: Record<string, Capability[]> = {}
    for (const c of caps) {
      if (!by[c.group]) { by[c.group] = []; order.push(c.group) }
      by[c.group].push(c)
    }
    return order.map((g) => ({ group: g, items: by[g] }))
  }, [caps])

  // The TOC: static sections, with the capability reference's subs filled in from
  // the live catalog groups.
  const sections = useMemo<Section[]>(() => SECTIONS.map((s) =>
    s.id === 'capabilities'
      ? { ...s, subs: capGroups.map((g) => ({ id: slug(g.group), title: g.group })) }
      : s,
  ), [capGroups])

  // Scroll-spy over sections AND their subheadings: highlight whichever is nearest
  // the top of the scroll container.
  useEffect(() => {
    const root = mainRef.current
    if (!root) return
    const obs = new IntersectionObserver(
      (entries) => {
        const vis = entries.filter((e) => e.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)
        if (vis[0]) setActive(vis[0].target.id)
      },
      { root, rootMargin: '0px 0px -70% 0px', threshold: 0 },
    )
    root.querySelectorAll('section[id], .docs-sub[id]').forEach((s) => obs.observe(s))
    return () => obs.disconnect()
  }, [sections])

  const go = (id: string) => {
    mainRef.current?.querySelector('#' + CSS.escape(id))?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  return (
    <div className="layout">
      <div className="sidebar docs-toc">
        <strong style={{ display: 'block', marginBottom: 8 }}>Contents</strong>
        {sections.map((s) => {
          const childActive = !!s.subs?.some((x) => x.id === active)
          const expanded = active === s.id || childActive
          return (
            <div key={s.id}>
              <span className={'toc-link' + (active === s.id || childActive ? ' active' : '')} onClick={() => go(s.id)}>{s.title}</span>
              {expanded && s.subs?.map((x) => (
                <span key={x.id} className={'toc-link toc-sub' + (active === x.id ? ' active' : '')} onClick={() => go(x.id)}>{x.title}</span>
              ))}
            </div>
          )
        })}
      </div>

      <div className="main docs-main" ref={mainRef}>
        <div className="docs-body">
          <section id="intro">
            <h1>agentle documentation</h1>
            <p className="docs-lead">
              agentle runs small <strong>Starlark</strong> scripts as durable agents. A script is plain code with a
              <code> main(input)</code> entry point; the platform gives it capabilities (LLM, HTTP, shell, MCP tools,
              durable storage and messaging), records every external effect, and can replay a run deterministically.
            </p>
            <p>
              Everything here is self-contained: SQLite for state, a local subprocess sandbox for shell/plugins, and an
              offline mock LLM so the examples run with no credentials. Point an <code>llm</code> config at a real model
              when you're ready.
            </p>
          </section>

          <section id="quickstart">
            <h2>Quickstart</h2>
            <ol>
              <li>Open the <strong>Scripts</strong> tab and click <code>+ New</code>. Start from a template (or describe what you want and let the assistant scaffold it).</li>
              <li>Edit the code. Press <kbd>Ctrl-Space</kbd> for capability autocomplete. Tick the capability grants the script needs.</li>
              <li>Put some JSON in <em>Run input → event.data</em> and click <strong>Run</strong>. Running auto-saves a new version first.</li>
              <li>Inspect the result inline, or open the <strong>Runs</strong> tab (or the script's <em>Runs</em> sub-tab) to see the full trace.</li>
              <li>Happy with a run? Click <strong>⭐ Promote</strong> to add it to the script's golden dataset, then re-run it as an eval from the <strong>Evals</strong> tab.</li>
            </ol>
            <p className="muted">A fresh install ships sample scripts and eval fixtures (<code>eval: greeter</code>, <code>eval: chat assistant</code>) so the Evals tab is populated from the start.</p>
          </section>

          <section id="concepts">
            <h2>Core concepts</h2>
            <h3 id="concepts-scripts" className="docs-sub">Scripts & versions</h3>
            <p>A script is a named program; every save is an immutable <strong>version</strong> with its own source and capability grants. Executions pin to a version, and you can restore any older version.</p>
            <h3 id="concepts-caps" className="docs-sub">Capabilities & grants</h3>
            <p>Scripts can't reach the outside world except through granted capabilities. A <strong>tool config</strong> (Settings tab) is a pre-configured capability instance — an endpoint, an allowlist, an optional secret-ref. You grant configs to a script version. <code>log</code>, <code>time</code>, <code>rand</code>, <code>store</code>/<code>fetch</code> and <code>send</code>/<code>recv</code> are always available.</p>
            <h3 id="concepts-durable" className="docs-sub">Durable execution</h3>
            <p>Every external effect is written to an event log. <code>recv()</code> with an empty inbox doesn't block a thread — the run <em>suspends</em> (status “suspended”) and the engine resumes it by replay when a message arrives or a deadline fires. This is what lets an agent live across days without holding resources.</p>
            <h3 id="concepts-workspaces" className="docs-sub">Workspaces (actors)</h3>
            <p>Each run belongs to a <strong>workspace</strong> — the namespace for its <code>store</code>/<code>fetch</code> state and its <code>send</code>/<code>recv</code> inbox. Triggers can bind runs to a named workspace (e.g. <code>agent-{'{{event.id}}'}</code>) so messages for the same id reach the same long-lived agent.</p>
          </section>

          <section id="writing">
            <h2>Writing a script</h2>
            <p>Starlark is a deterministic Python dialect. Define <code>main(input)</code>, where <code>input</code> is the event envelope <code>{'{id, kind, workspace, data}'}</code>:</p>
            <pre><code>{`def main(input):
    name = (input.get("data") or {}).get("name", "world")
    seen = fetch("seen:" + name) or 0     # durable per-workspace storage
    store("seen:" + name, seen + 1)
    reply = llm([{"role": "user", "content": "Greet " + name}])
    return {"greeting": reply["content"], "times_seen": seen + 1}`}</code></pre>
            <p className="muted">No <code>import</code>, classes, <code>while</code>, recursion, <code>try/except</code>, <code>open()</code> or raw network — reach the world only through capabilities. The engine replays runs, so determinism matters: clock and randomness come from <code>time</code>/<code>rand</code>, which are recorded.</p>
          </section>

          <section id="tabs">
            <h2>The dashboard</h2>
            <ul>
              <li><strong>Scripts</strong> — author, run, and inspect a script. Sub-tabs: <em>Editor</em> (with the ✨ assistant), <em>Runs</em>, <em>Evals</em>, <em>Triggers</em>, <em>Secrets</em>.</li>
              <li><strong>Apps</strong> — launch scripts that declare an interactive UI (<code>ui_chat</code>/<code>ui_form</code>) as full-screen apps.</li>
              <li><strong>Runs</strong> — every execution, with trace timeline/events, cost, human feedback (👍/👎) and ⭐ Promote.</li>
              <li><strong>Evals</strong> — golden datasets, replay-based coverage, the LLM judge, calibration, the persona simulator, and pass@k. (Mirrored under each script's <em>Evals</em> sub-tab.)</li>
              <li><strong>Spend</strong> — token usage and cost, rolled up by script / workspace / user / model.</li>
              <li><strong>Plugins</strong> — manage capability plugins (admin).</li>
              <li><strong>Settings</strong> — tool configs, global secrets, API tokens.</li>
            </ul>
          </section>

          <section id="capabilities">
            <h2>Capability reference</h2>
            <p className="muted">Generated from the live stdlib catalog — the same functions the editor autocompletes.</p>
            {capGroups.map((g) => (
              <div key={g.group}>
                <h3 id={slug(g.group)} className="docs-sub">{g.group}</h3>
                {g.items.map((c) => (
                  <div key={c.name} className="docs-cap">
                    <span className="mono">{c.name}</span>
                    <div className="muted" style={{ fontSize: 13, marginTop: 2 }}>{c.doc}</div>
                  </div>
                ))}
              </div>
            ))}
            {caps.length === 0 && <p className="muted">Catalog unavailable.</p>}
          </section>

          <section id="examples">
            <h2>Examples</h2>
            <p className="muted">The starter gallery (New script → template). Capability badges show the grants each one needs.</p>
            {examples.map((ex) => (
              <div key={ex.id} className="card" style={{ marginBottom: 12 }}>
                <div className="row spread">
                  <strong>{ex.title}</strong>
                  <span>{ex.capabilities.map((c) => <span key={c} className="badge suspended mono" style={{ marginRight: 4 }}>{c}</span>)}</span>
                </div>
                <div className="muted" style={{ fontSize: 13, margin: '4px 0 8px' }}>{ex.description}</div>
                <pre><code>{ex.source}</code></pre>
              </div>
            ))}
            {examples.length === 0 && <p className="muted">No examples available.</p>}
          </section>

          <section id="evals">
            <h2>Evals & goldens</h2>
            <p>A <strong>golden</strong> is a promoted run kept as a reference. Its external surface (HTTP responses, recorded user replies, clock, randomness) is rebuilt from the origin run's event log, so it doesn't rot when you change the script.</p>
            <ol>
              <li><strong>Promote</strong> a good (or instructive bad) run from the Runs tab or a script's Runs sub-tab. Its 👍/👎 label becomes the golden's success/failure label.</li>
              <li>Re-run a new version against the golden: HTTP and user replies are replayed, the LLM runs <em>live</em>, and the run stops at the first novel write (read-prefix). You get a <strong>coverage</strong> score and where it stopped.</li>
              <li>Add a <strong>rubric</strong> (criteria.md) to have the LLM <strong>judge</strong> score the trajectory. Run <strong>Calibrate</strong> first to check the judge agrees with your human labels before trusting verdicts.</li>
              <li>Add a <strong>persona</strong> (persona.md) to swap recorded replies for a live <em>simulator</em> that answers the new version's actual questions. <strong>Check consistency</strong> confirms the persona reproduces the golden's outcome on its own version before you rely on it.</li>
              <li>Set <strong>samples &gt; 1</strong> for <strong>pass@k</strong> over the non-deterministic LLM — a single replay isn't a verdict; the pass-rate and a flakiness flag are.</li>
            </ol>
            <p className="muted">The egress policy table classifies each external tool as read (runs live on a cassette miss) or write (gates). Unlisted ⇒ write, fail-safe.</p>
          </section>

          <section id="plugins">
            <h2>Plugins</h2>
            <p>A plugin provides MCP tools to scripts. Grant one with an <code>mcp</code> tool config whose JSON is <code>{'{'}"plugin_id": "&lt;id&gt;"{'}'}</code>; its tools then appear in <code>mcp_list_tools()</code>.</p>
            <h3 id="plugins-script" className="docs-sub">Script plugins</h3>
            <p>A small program (python / node / bash) run per-call in the sandbox. Convention: <code>argv[1]="list"</code> prints the tool catalog as JSON; <code>"call"</code> with <code>argv[2]=tool</code> and <code>argv[3]=args-JSON</code> prints the result. Script plugins are <strong>versioned</strong> — each save is a snapshot you can roll back to.</p>
            <h3 id="plugins-native" className="docs-sub">Native plugins</h3>
            <p>Plugins implemented in Go and run in-process. They appear in the Plugins list (marked <span className="docs-pill">native</span>) and are grantable just like script plugins, but their source lives in code, so they aren't editable or deletable from the UI.</p>
          </section>

          <section id="triggers">
            <h2>Triggers & API</h2>
            <h3 id="triggers-cron" className="docs-sub">Triggers</h3>
            <p>From a script's <em>Triggers</em> sub-tab, add a <strong>cron</strong> schedule or a <strong>webhook</strong>. A webhook gives you a URL; POSTing to it runs the script with the body at <code>event.data</code>. An optional workspace template binds runs to a named workspace for shared state.</p>
            <h3 id="triggers-api" className="docs-sub">Programmatic API</h3>
            <p>The <code>/v1</code> REST API is authenticated by a Bearer token (create one under Settings → API tokens, carrying your RBAC):</p>
            <pre><code>{`curl -X POST https://<host>/v1/scripts/<id>/runs \\
  -H "Authorization: Bearer <token>" \\
  -H "Content-Type: application/json" \\
  -d '{"input": {"name": "Ada"}}'

# then poll the run + its trace
curl https://<host>/v1/runs/<run_id>      -H "Authorization: Bearer <token>"
curl https://<host>/v1/runs/<run_id>/trace -H "Authorization: Bearer <token>"`}</code></pre>
            <h3 id="triggers-roles" className="docs-sub">Roles</h3>
            <p><strong>admin</strong> sees and manages everything (users, plugins, global secrets, all scripts/runs). A <strong>user</strong> sees only their own scripts and runs.</p>
          </section>
        </div>
      </div>
    </div>
  )
}
