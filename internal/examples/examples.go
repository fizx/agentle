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
        msg = recv(timeout=300)   # suspend up to 5 min for the next message
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
