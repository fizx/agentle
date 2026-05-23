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
		ID:          "hello",
		Title:       "Hello agent",
		Description: "Greets a name via the LLM and counts visits in per-workspace storage.",
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
		ID:          "shell",
		Title:       "Shell sandbox",
		Description: "Runs commands in the per-workspace sandbox; each fs mutation is snapshotted.",
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
		ID:          "parallel_map",
		Title:       "Parallel map (fan-out LLM)",
		Description: "Fans out concurrent LLM calls with bounded concurrency; replay-safe.",
		Capabilities: []string{"llm"},
		Source: `def summarize(topic):
    return llm([{"role": "user", "content": "One sentence about " + topic}])["content"]

def main(input):
    topics = (input.get("data") or {}).get("topics", ["go", "starlark", "actors"])
    return parallel_map(summarize, topics, max_concurrency=3)
`,
	},
	{
		ID:          "http",
		Title:       "HTTP fetch",
		Description: "Calls an allowlisted HTTP endpoint (grant an http config allowing api.github.com).",
		Capabilities: []string{"http"},
		Source: `def main(input):
    repo = (input.get("data") or {}).get("repo", "golang/go")
    r = http_get("https://api.github.com/repos/" + repo)
    return {"status": r["status"], "bytes": len(r["body"])}
`,
	},
	{
		ID:          "message_passing",
		Title:       "Message passing",
		Description: "Sends a message to another workspace and awaits a reply (send/recv).",
		Capabilities: []string{},
		Source: `# Run one execution with data {"role":"ping","to":"<other workspace>"} and
# another (workspace = that 'to' value via a trigger actor template) as the pong.
def main(input):
    data = input.get("data") or {}
    if data.get("role") == "ping":
        send(data["to"], {"text": "ping", "reply_to": input["workspace"]})
        return {"reply": recv(timeout=5)}
    m = recv(timeout=5)
    if m and m.get("reply_to"):
        send(m["reply_to"], {"text": "pong"})
    return {"handled": m}
`,
	},
	{
		ID:          "agent_loop",
		Title:       "Yield-driven agent loop",
		Description: "An inner LLM agent loop: recv() yields the next inbox message mid-flow.",
		Capabilities: []string{"llm"},
		Source: `# Bind a trigger with an actor template (e.g. agent-{{event.id}}) so messages
# for the same id reach this running agent. recv() is the blocking yield point.
def main(input):
    history = [{"role": "system", "content": "You are a helpful agent."}]
    turns = 0
    for _ in range(20):  # bounded
        msg = recv(timeout=30)
        if msg == None:
            break
        history.append({"role": "user", "content": msg.get("text", "")})
        reply = llm(history)
        history.append({"role": "assistant", "content": reply["content"]})
        turns += 1
        if msg.get("reply_to"):
            send(msg["reply_to"], {"text": reply["content"]})
    return {"turns": turns}
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
