// Package eval implements PLAYTEST7's replay-based scoring: it re-runs a new
// script version against a golden run's recorded external effects, holding the
// human inputs fixed while the agent's own logic and LLM calls run live.
//
// The two non-deterministic, non-rollbackable surfaces are external HTTP writes
// and mid-run user input (recv); everything else is discarded by running in a
// throwaway sandbox. The Cassette (here) handles HTTP record/replay; the
// selective Mediator (mediator.go) applies a per-RPC-kind replay policy.
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// httpArgs mirrors the "http" capability's argument shape (caps.httpArgs). The
// auth header injected from a secret is added by the executor at request time and
// never appears in these args, so a cassette built from the log carries no
// secrets.
type httpArgs struct {
	URL     string            `json:"url"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

// CanonOpts controls how a request is reduced to a match key. Real requests carry
// volatile fields (nonces, timestamps, idempotency keys, auth, signatures) that
// must be excluded or every replay false-misses. The defaults bias toward TIGHT
// matching (method + URL + body, headers ignored): a too-loose key serves the
// wrong recorded response (a false hit, silently wrong), whereas a too-tight key
// only over-gates (the cheap error, per the plan). Loosen per-endpoint via the
// ignore lists.
type CanonOpts struct {
	// IgnoreQuery drops these query-param names before matching (e.g. "ts",
	// "nonce", "sig"). Matching is case-insensitive on the name.
	IgnoreQuery []string `json:"ignore_query,omitempty"`
	// IncludeBody includes the request body in the key. Default true; set false
	// (via NewCanonOpts) only when the body is purely volatile.
	IncludeBody bool `json:"include_body"`
	// bodyIsSet distinguishes "IncludeBody intentionally false" from the zero
	// value; NewCanonOpts and DefaultCanon set it.
	bodyIsSet bool
}

// DefaultCanon returns the default canonicalization: match on method + URL (with
// sorted query) + body, ignore all headers.
func DefaultCanon() CanonOpts { return CanonOpts{IncludeBody: true, bodyIsSet: true} }

func (o CanonOpts) includeBody() bool {
	if !o.bodyIsSet {
		return true // zero-value CanonOpts still includes the body
	}
	return o.IncludeBody
}

// canonKey reduces a request to a stable match key. method is normalized to
// lowercase; the URL's scheme/host are lowercased, query params are sorted (minus
// the ignore set), and the fragment is dropped.
func (o CanonOpts) canonKey(method, rawURL, body string) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(method))))
	h.Write([]byte{0})
	h.Write([]byte(o.canonURL(rawURL)))
	h.Write([]byte{0})
	if o.includeBody() {
		h.Write([]byte(body))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (o CanonOpts) canonURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(raw)) // unparseable: fall back to raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if u.RawQuery != "" {
		ignore := map[string]bool{}
		for _, k := range o.IgnoreQuery {
			ignore[strings.ToLower(k)] = true
		}
		q := u.Query()
		for k := range q {
			if ignore[strings.ToLower(k)] {
				q.Del(k)
			}
		}
		// url.Values.Encode sorts by key, giving order-independent matching.
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Entry is one recorded request→response pair. Method/URL are retained for
// debugging and coverage reporting; matching is by Key.
type Entry struct {
	Key    string          `json:"key"`
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Result json.RawMessage `json:"result,omitempty"` // recorded response {status,body,headers}
	Err    string          `json:"err,omitempty"`    // recorded error, if the golden call failed
}

// Cassette is an HTTP record/replay tape extracted from a golden run. Lookups
// match on the canonical key; identical requests recorded multiple times (e.g.
// polling) replay in order, then stick on the last recorded response so a request
// we DID record never spuriously misses.
type Cassette struct {
	Opts    CanonOpts `json:"opts"`
	Entries []Entry   `json:"entries"`

	index  map[string][]int // key -> entry indices, in record order (lazily built)
	cursor map[string]int   // key -> next index to serve
}

// BuildCassette extracts the HTTP request/response pairs from a golden run's event
// log into a replay tape. It reads EventRPCResult records for the "http"
// capability, whose Args (recorded since PLAYTEST7) carry the request.
func BuildCassette(events []engine.Event, opts CanonOpts) *Cassette {
	c := &Cassette{Opts: opts}
	for i := range events {
		ev := events[i]
		if ev.Kind != engine.EventRPCResult || ev.RPC == nil || ev.RPC.Capability != "http" {
			continue
		}
		var a httpArgs
		if err := json.Unmarshal(ev.RPC.Args, &a); err != nil {
			continue // no usable request to key on
		}
		c.Entries = append(c.Entries, Entry{
			Key:    opts.canonKey(ev.RPC.Method, a.URL, a.Body),
			Method: ev.RPC.Method,
			URL:    a.URL,
			Result: ev.RPC.Result,
			Err:    ev.RPC.Err,
		})
	}
	return c
}

func (c *Cassette) buildIndex() {
	if c.index != nil {
		return
	}
	c.index = map[string][]int{}
	c.cursor = map[string]int{}
	for i := range c.Entries {
		k := c.Entries[i].Key
		c.index[k] = append(c.index[k], i)
	}
}

// Lookup returns the recorded entry matching the request, advancing the per-key
// cursor so repeated identical requests replay successive recordings (and stick on
// the last). The bool reports a hit; a miss means the request is novel.
func (c *Cassette) Lookup(method, rawURL, body string) (*Entry, bool) {
	c.buildIndex()
	key := c.Opts.canonKey(method, rawURL, body)
	idxs, ok := c.index[key]
	if !ok || len(idxs) == 0 {
		return nil, false
	}
	pos := c.cursor[key]
	if pos >= len(idxs) {
		pos = len(idxs) - 1 // exhausted: stick on the last recorded response
	} else {
		c.cursor[key] = pos + 1
	}
	return &c.Entries[idxs[pos]], true
}

// Len reports the number of recorded HTTP exchanges (a coverage input).
func (c *Cassette) Len() int { return len(c.Entries) }
