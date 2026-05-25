// Package examples is the catalog of starter scripts shown in the dashboard's
// "New script" gallery and used to seed a fresh workspace. Add an example by
// appending an Example to All — it then appears in the UI and via /api/examples.
package examples

// Example is a named starter script template.
type Example struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"` // tool grants the script needs (besides always-on caps)
	Source       string   `json:"source"`
}

// All is the ordered example catalog.
var All = []Example{
	{
		ID:           "hello",
		Title:        "Hello agent",
		Description:  "Greets a name via the LLM and counts visits in per-workspace storage.",
		Capabilities: []string{"llm"},
		Source: `# main(input) receives the event envelope {id, kind, workspace, data}.
def main(input):
    name = (input.get("data") or {}).get("name", "world")
    log("greeting", name, "via", input["kind"])
    seen = fetch("seen:" + name) or 0
    store("seen:" + name, seen + 1)
    reply = llm([{"role": "user", "content": "Greet " + name + " in one sentence."}])
    return {"greeting": reply["content"], "times_seen": seen + 1}
`,
	},
	{
		ID:           "shell",
		Title:        "Shell sandbox",
		Description:  "Runs commands in the per-workspace sandbox; each fs mutation is snapshotted.",
		Capabilities: []string{"shell"},
		Source: `# The shell runs in a per-workspace sandbox home dir (see the trace barriers).
def main(input):
    msg = (input.get("data") or {}).get("message", "hello from the sandbox")
    shell(["sh", "-c", "echo '" + msg + "' > note.txt"])
    cat = shell(["cat", "note.txt"])
    return {"exit": cat["code"], "note": cat["stdout"].strip(), "uname": shell(["uname","-a"])["stdout"].strip()}
`,
	},
	{
		ID:           "parallel_map",
		Title:        "Parallel map (fan-out LLM)",
		Description:  "Fans out concurrent LLM calls with bounded concurrency; replay-safe.",
		Capabilities: []string{"llm"},
		Source: `def summarize(topic):
    return llm([{"role": "user", "content": "One sentence about " + topic}])["content"]

def main(input):
    topics = (input.get("data") or {}).get("topics", ["go", "starlark", "actors"])
    return parallel_map(summarize, topics, max_concurrency=3)
`,
	},
	{
		ID:           "http",
		Title:        "HTTP fetch",
		Description:  "Calls an allowlisted HTTP endpoint (grant an http config allowing api.github.com).",
		Capabilities: []string{"http"},
		Source: `def main(input):
    repo = (input.get("data") or {}).get("repo", "golang/go")
    r = http_get("https://api.github.com/repos/" + repo)
    return {"status": r["status"], "bytes": len(r["body"])}
`,
	},
	{
		ID:           "request_reply",
		Title:        "Actors: request / reply (durable suspend)",
		Description:  "Two workspaces hand off via send/recv; recv durably suspends the run until a message arrives — no goroutine blocks.",
		Capabilities: []string{},
		Source: `# Durable actors. recv() with an empty inbox does NOT block: the run suspends
# (status "suspended") and the engine resumes it by replay when a message arrives.
#
# Drive it with two dashboard runs (set the workspace via a trigger actor template,
# or just call this script twice with these inputs):
#   1) responder:  data = {"role": "responder"}        workspace = "svc"
#   2) requester:  data = {"role": "requester", "to": "svc"}   (any workspace)
# The requester sends to "svc" and suspends awaiting the reply; the responder
# (woken by the send) replies, which wakes the requester. Both then complete.
def main(input):
    data = input.get("data") or {}
    if data.get("role") == "requester":
        send(data["to"], {"q": "ping", "reply_to": input["workspace"]})
        reply = recv()            # suspend until the responder replies
        return {"got": reply}
    # responder
    msg = recv()                  # suspend until a request arrives
    send(msg["reply_to"], {"a": "pong", "re": msg["q"]})
    return {"handled": msg}
`,
	},
	{
		ID:           "agent_loop",
		Title:        "Durable agent loop (recv yield)",
		Description:  "A long-lived LLM agent: recv() is the durable yield point — the run suspends between messages and resumes on each new one.",
		Capabilities: []string{"llm"},
		Source: `# Bind a trigger with an actor template (e.g. agent-{{event.id}}) so messages for
# the same id reach this agent's workspace. recv() durably suspends between turns,
# so the agent can live indefinitely without holding a goroutine while idle.
def main(input):
    history = [{"role": "system", "content": "You are a helpful agent."}]
    turns = 0
    for _ in range(20):  # bounded
        msg = deadline(300, lambda: recv())   # suspend up to 5 min for the next message
        if msg == None:
            break                 # timed out: end the session
        history.append({"role": "user", "content": msg.get("text", "")})
        reply = llm(history)
        history.append({"role": "assistant", "content": reply["content"]})
        turns += 1
        if msg.get("reply_to"):
            send(msg["reply_to"], {"text": reply["content"]})
    return {"turns": turns}
`,
	},
	{
		ID:           "mcp_direct",
		Title:        "MCP: direct tool call",
		Description:  "Lists and calls Model Context Protocol tools directly (grant an mcp config; the demo server has echo/add/upper).",
		Capabilities: []string{"mcp"},
		Source: `# mcp_call invokes a tool on the granted MCP server. With no endpoint configured,
# an in-process mock server (echo/add/upper) makes this playable offline.
def main(input):
    names = [t["name"] for t in mcp_list_tools()]
    total = mcp_call("add", {"a": 2, "b": 3})
    echoed = mcp_call("echo", {"text": "hi from starlark"})
    return {"tools": names, "sum": total["text"], "echo": echoed["text"]}
`,
	},
	{
		ID:           "chat_ui",
		Title:        "Chat assistant (LLM)",
		Description:  "An LLM-backed chat app. ui_chat() only renders the panel — the script defines the behavior, so edit the system prompt or the loop to build any chat.",
		Capabilities: []string{"llm"},
		Source: `# A chat app. ui_chat() is NOT magic: it just opens the panel. The script drives
# the conversation — here by calling llm() each turn. Swap the system prompt
# (pass data.system) or the whole loop to build any chat you like.
def main(input):
    data = input.get("data") or {}
    system = data.get("system") or "You are a concise, friendly assistant."
    ui_chat(title=data.get("title") or "Assistant",
            intro="Ask me anything. Send /quit to end.")
    history = [{"role": "system", "content": system}]
    turns = 0
    for _ in range(100):
        msg = recv()                  # durably suspend until the user sends
        if msg == None:
            break
        text = (msg.get("text") or "").strip()
        if text == "/quit":
            ui_say("Bye! 👋")
            break
        history.append({"role": "user", "content": text})
        reply = llm(history)
        history.append({"role": "assistant", "content": reply["content"]})
        ui_say(reply["content"])      # append the reply to the transcript
        turns += 1
    return {"turns": turns}
`,
	},
	{
		ID:           "form_ui",
		Title:        "Form UI",
		Description:  "Running this from the dashboard pops a form; the script receives the submitted values.",
		Capabilities: []string{},
		Source: `# ui_form declares a form and suspends until you submit it.
def main(input):
    vals = ui_form([
        field("name", "Your name", required=True),
        field("color", "Favorite color", type="select", options=["red", "green", "blue"]),
        field("subscribe", "Subscribe?", type="checkbox"),
    ])
    ui_say("Thanks, " + (vals.get("name") or "stranger") + "! You picked " + str(vals.get("color")) + ".")
    return vals
`,
	},
	{
		ID:           "coding_agent",
		Title:        "Coding assistant (self-hosted)",
		Description:  "An autonomous coding agent with EDITOR TOOLS: it can read_source, apply_edit, and run the script you're editing. Powers the in-editor ✨ Assistant panel. Grant llm and point it at a tool-capable model (e.g. Ollama qwen2.5-coder:32b).",
		Capabilities: []string{"llm"},
		Source: `# A self-hosted coding assistant with EDITOR TOOLS. Each turn the editor sends
# your message + the live buffer (data.source); the model may call read_source /
# apply_edit / run, which the dashboard executes client-side and feeds back via the
# inbox (so the round-trip is durable + replay-safe). Point the llm grant at a
# tool-capable model. See PLAYTEST5.md for the full design.
SYSTEM = """You are the coding assistant inside agentle, helping write and debug ONE
agentle script, main.star. It is Starlark (a deterministic Python dialect) run by a
REPLAY engine — no imports, classes, while, recursion, try/except, open(), or network
except through granted capabilities (llm, http_get, shell, mcp_call; always-on: log,
now, rand, store, fetch, send, recv, deadline, parallel_map, ui_chat, ui_say, ui_form).
Entry point: def main(input): with input the event envelope {id,kind,workspace,data}.

You have EDITOR TOOLS. You MUST act through them — never paste code in your text reply:
- read_source(): the current main.star buffer.
- apply_edit(source): to CHANGE the file, CALL THIS with the COMPLETE new source (not
  a diff, not a snippet). This is the ONLY way to edit; do not write code in chat.
- run(input): run the current main.star; returns status, output, and a short trace.
Workflow: make the change with apply_edit, then call run to verify, then briefly (1-2
sentences, no code blocks) report what you did and the result. The current buffer is
provided below for reference. Keep main.star valid Starlark with a main(input) function."""

TOOLS = [
    {"name": "read_source",
     "description": "Return the current full contents of main.star (the editor buffer).",
     "inputSchema": {"type": "object", "properties": {}}},
    {"name": "apply_edit",
     "description": "Replace the entire main.star buffer with new full source. Provide the COMPLETE file, not a diff.",
     "inputSchema": {"type": "object",
                     "properties": {"source": {"type": "string", "description": "the complete new main.star"}},
                     "required": ["source"]}},
    {"name": "run",
     "description": "Run the current main.star and return its output and a short execution trace.",
     "inputSchema": {"type": "object",
                     "properties": {"input": {"type": "object", "description": "optional JSON placed at event.data"}}}},
]

def main(input):
    ui_chat(title="Coding assistant", intro="Ask me to write, fix, or run this script. I can read it, edit it, and run it. /quit to end.")
    history = [{"role": "system", "content": SYSTEM}]
    step = 0
    for _ in range(40):
        msg = recv()
        if msg == None:
            break
        text = (msg.get("text") or "").strip()
        if text == "/quit":
            break
        source = msg.get("source") or ""
        user = text
        if source:
            user += "\n\nCurrent main.star:\n<source>\n" + source + "\n</source>"
        history.append({"role": "user", "content": user})
        for _ in range(8):  # bounded tool loop within a turn
            reply = llm(history, tools=TOOLS)
            calls = reply.get("tool_calls")
            if not calls:
                ui_say(reply.get("content") or "(no reply)")
                break
            history.append({"role": "assistant", "content": reply.get("content", ""), "tool_calls": calls})
            step += 1
            ui_tools(calls, str(step))            # the dashboard runs the tools…
            res = deadline(120, lambda: recv())   # …and posts {tool_results, batch} back
            if res == None:
                ui_say("(timed out waiting for editor tools)")
                break
            for r in (res.get("tool_results") or []):
                history.append({"role": "tool", "tool_call_id": r.get("id"), "name": r.get("name", ""), "content": r.get("content", "")})
    return {"steps": step}
`,
	},
	{
		ID:           "stacked_ui",
		Title:        "Chat + form stack",
		Description:  "Shows the UI panel stack: a chat that opens a form modal over itself (/profile), which pops on submit.",
		Capabilities: []string{},
		Source: `# UI panels are a stack. ui_chat() pushes the base chat; ui_form() pushes a form
# *over* it (rendered modal), and pops automatically once you submit. ui_pop()/
# ui_clear() pop explicitly.
def main(input):
    ui_chat(title="Stacker", intro="Type /profile to open a form over this chat. /quit to end.")
    for _ in range(50):
        msg = recv()
        if msg == None:
            break
        text = (msg.get("text") or "").strip()
        if text == "/quit":
            break
        if text == "/profile":
            vals = ui_form([
                field("name", "Your name", required=True),
                field("role", "Role", type="select", options=["dev", "ops", "pm"]),
            ])
            ui_say("Nice to meet you, " + (vals.get("name") or "?") + " (" + str(vals.get("role")) + ").")
        else:
            ui_say("You said: " + text)
    return {"done": True}
`,
	},
	{
		ID:           "plugin_tool",
		Title:        "Capability plugin (sandboxed MCP tool)",
		Description:  "Calls a tool provided by an agentle-managed plugin running in the sandbox (grant the mcp-plugin config).",
		Capabilities: []string{"mcp"},
		Source: `# The granted mcp config points at a plugin (Python "text-tools" with a
# "reverse" tool). Plugin tools appear in mcp_list_tools() like any MCP server.
def main(input):
    text = (input.get("data") or {}).get("text", "hello world")
    return {"reversed": mcp_call("reverse", {"text": text})["text"]}
`,
	},
	{
		ID:           "mcp_agent",
		Title:        "MCP: LLM tool use",
		Description:  "The LLM is handed the MCP server's tools and drives them in a bounded tool-use loop (grant llm + mcp).",
		Capabilities: []string{"llm", "mcp"},
		Source: `# Pass MCP tools straight to llm(tools=...). When the model returns tool_calls,
# run each via mcp_call and feed the result back as a {"role":"tool"} message.
def main(input):
    question = (input.get("data") or {}).get("q", "Add 2 and 3 using the add tool.")
    tools = mcp_list_tools()
    msgs = [{"role": "user", "content": question}]
    for _ in range(5):  # bounded tool-use loop
        reply = llm(msgs, tools=tools)
        calls = reply.get("tool_calls")
        if not calls:
            return {"answer": reply.get("content")}
        msgs.append({"role": "assistant", "content": reply.get("content", ""), "tool_calls": calls})
        for c in calls:
            out = mcp_call(c["name"], c.get("arguments") or {})
            msgs.append({"role": "tool", "tool_call_id": c["id"], "name": c["name"], "content": out["text"]})
    return {"answer": "(tool loop did not converge)"}
`,
	},
}

// Find returns the example with the given id, or nil.
func Find(id string) *Example {
	for i := range All {
		if All[i].ID == id {
			return &All[i]
		}
	}
	return nil
}
