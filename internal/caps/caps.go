// Package caps provides the bound tool instances (Executors) that back script
// capabilities. Secrets are closed over here in Go and never reach the VM.
//
// Layout: one file per capability, each exposing a constructor that returns an
// engine.Executor. To add a capability: create caps/<name>.go with a constructor,
// register a Starlark builtin for it in internal/vm/std_*.go, and wire it into
// the environment in internal/platform/platform.go (system caps in assembleEnv,
// or granted caps in buildExecutor).
//
//	log.go    -> "log"   (always on)
//	time.go   -> "time"  (always on: now/sleep)
//	rand.go   -> "rand"  (always on: seeded)
//	kv.go     -> "kv"    (always on, per-workspace)
//	inbox.go  -> "inbox" (always on, per-workspace: send/recv)
//	http.go   -> "http"  (granted: SSRF guard + allowlist)
//	llm.go    -> "llm"   (granted: OpenAI chat format)
package caps
