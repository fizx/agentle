package eval

import (
	"encoding/json"
	"net/url"
	"strings"
)

// RPCMode is the eval-time replay decision for one capability kind. The split is
// at the kind layer, not the proxy: llm and http both leave the box as HTTP
// egress yet get opposite treatment.
type RPCMode int

const (
	// ModeLive executes the real bound executor. This is the thing under test for
	// llm; for shell/kv/log/ui it is safe because the sandbox is thrown away.
	ModeLive RPCMode = iota
	// ModeCassette replays HTTP from the tape: hit → recorded response; read-miss
	// → live; write-miss → the WriteMissPolicy decides.
	ModeCassette
	// ModePinned returns a deterministic value for clock/random so they don't add
	// noise to judge comparisons across samples.
	ModePinned
	// ModeSimulate serves the user channel (recv): replay the recorded answer
	// (PLAYTEST7 phase 1) or, later, a persona-seeded simulator (phase 4).
	ModeSimulate
)

// DefaultPolicy is the per-kind replay table from the plan. Capabilities absent
// from the table default to ModeLive (local, discarded with the sandbox).
func DefaultPolicy() map[string]RPCMode {
	return map[string]RPCMode{
		"llm":   ModeLive,
		"http":  ModeCassette,
		"shell": ModeLive,
		"time":  ModePinned,
		"rand":  ModePinned,
		"inbox": ModeSimulate,
	}
}

// WriteMissPolicy decides what happens when the new version invents an external
// write with no recorded response — the one dangerous cell, where proceeding
// means actually issuing it.
type WriteMissPolicy int

const (
	// MissFail is the default: fail closed at the write-miss (the evaluable
	// segment ends here).
	MissFail WriteMissPolicy = iota
	// MissGoLive issues the request for real. Only safe against a sandbox/staging
	// target the operator has explicitly allowed.
	MissGoLive
	// MissFlag parks the run for human review rather than failing or issuing.
	MissFlag
)

// Classifier decides read vs write for an HTTP call on a cassette miss. The tag
// only matters on a miss — it gates whether a novel call may run unattended
// (read) or must stop (write). It is not a correctness mechanism; hits are safe
// regardless.
type Classifier interface {
	IsWrite(method string, args json.RawMessage) bool
}

// writeAll is the fail-safe default: every external call is a write. The
// expensive consequence of a wrong "read" tag is a real side effect; the cheap
// consequence of a wrong "write" tag is an unnecessary gate. Bias toward gates.
type writeAll struct{}

func (writeAll) IsWrite(string, json.RawMessage) bool { return true }

// DefaultClassifier tags every call a write (fail-safe).
func DefaultClassifier() Classifier { return writeAll{} }

// MethodClassifier treats GET/HEAD as reads and everything else as a write. This
// is an opt-in convenience for evals whose endpoints follow REST conventions; it
// trusts the verb, which the plan warns against for untrusted tools, so it is not
// the default.
type MethodClassifier struct{}

func (MethodClassifier) IsWrite(method string, _ json.RawMessage) bool {
	switch method {
	case "get", "head", "GET", "HEAD":
		return false
	default:
		return true
	}
}

// HostMethodClassifier consults an operator-maintained policy table keyed by
// (host, method) — the tool_policy table — falling back to write-by-default. An
// optional MethodFallback treats unmatched GET/HEAD as reads (the AllowReads
// convenience). A wrong "read" risks a real side effect, so unknown tools gate.
type HostMethodClassifier struct {
	// Table maps host + "\x00" + UPPER(method) -> isWrite. Absent = unknown.
	Table          map[string]bool
	MethodFallback bool
}

// PolicyKey is the tool_policy lookup key for an HTTP call.
func PolicyKey(host, method string) string {
	return strings.ToLower(host) + "\x00" + strings.ToUpper(method)
}

func (c HostMethodClassifier) IsWrite(method string, args json.RawMessage) bool {
	host := hostOf(args)
	if w, ok := c.Table[PolicyKey(host, method)]; ok {
		return w
	}
	if c.MethodFallback {
		switch strings.ToLower(method) {
		case "get", "head":
			return false
		}
	}
	return true // unknown => write (fail-safe)
}

func hostOf(args json.RawMessage) string {
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(args, &a)
	if u, err := url.Parse(a.URL); err == nil {
		return u.Hostname()
	}
	return ""
}

// AnnotationWrite maps MCP tool hints to a write classification. Hints are
// advisory (honor automatically only for vetted servers), so the bool is paired
// with a "known" flag — an absent/contradictory hint set leaves it unknown
// (caller defaults to write). readOnlyHint=true => read; destructiveHint=true or
// idempotentHint=false => write.
func AnnotationWrite(readOnlyHint, destructiveHint, idempotentHint *bool) (isWrite, known bool) {
	if readOnlyHint != nil && *readOnlyHint {
		return false, true
	}
	if destructiveHint != nil && *destructiveHint {
		return true, true
	}
	if idempotentHint != nil {
		return !*idempotentHint, true
	}
	return true, false
}
