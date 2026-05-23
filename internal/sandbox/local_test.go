package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

func TestLocalSandboxExecAndSnapshotRestore(t *testing.T) {
	pool, err := NewLocalPool(t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	sb, err := pool.Acquire(ctx, "exec1", "img", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Write a file in the home dir.
	if _, err := sb.Exec(ctx, engine.Command{Argv: []string{"sh", "-c", "echo hello > note.txt"}}); err != nil {
		t.Fatal(err)
	}
	r, err := sb.Exec(ctx, engine.Command{Argv: []string{"cat", "note.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(r.Stdout)) != "hello" {
		t.Fatalf("stdout = %q", r.Stdout)
	}

	// Snapshot, then release with persistence.
	key, err := sb.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Release(ctx, sb, false); err != nil {
		t.Fatal(err)
	}

	// Re-acquire from the snapshot; the file must come back.
	sb2, err := pool.Acquire(ctx, "exec1", "img", &key)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := sb2.Exec(ctx, engine.Command{Argv: []string{"cat", "note.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(r2.Stdout)) != "hello" {
		t.Fatalf("restored stdout = %q", r2.Stdout)
	}
}

func TestLocalSandboxExitCode(t *testing.T) {
	pool, _ := NewLocalPool(t.TempDir(), 10*time.Second)
	sb, _ := pool.Acquire(context.Background(), "e", "img", nil)
	r, err := sb.Exec(context.Background(), engine.Command{Argv: []string{"sh", "-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != 3 {
		t.Fatalf("expected exit 3, got %d", r.Code)
	}
}
