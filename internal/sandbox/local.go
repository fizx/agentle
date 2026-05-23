// Package sandbox provides Sandbox implementations. Local is a subprocess-backed
// dev sandbox: it gives each actor a private home dir persisted to a local
// object-store directory via tar snapshots. It is NOT a security boundary — the
// prod tier (kata + Firecracker) implements the same interfaces.
package sandbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/engine"
)

// LocalPool is an engine.SandboxPool backed by local subprocesses. Home dirs live
// under base/home/<exec>; snapshots are tar.gz blobs under base/snapshots.
type LocalPool struct {
	base    string
	maxWall time.Duration
}

// NewLocalPool roots all state under base. cmdTimeout caps each shell command.
func NewLocalPool(base string, cmdTimeout time.Duration) (*LocalPool, error) {
	for _, d := range []string{filepath.Join(base, "home"), filepath.Join(base, "snapshots")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if cmdTimeout == 0 {
		cmdTimeout = 60 * time.Second
	}
	return &LocalPool{base: base, maxWall: cmdTimeout}, nil
}

func (p *LocalPool) Acquire(ctx context.Context, exec engine.ExecutionID, _ string, restore *engine.SnapshotKey) (engine.Sandbox, error) {
	home := filepath.Join(p.base, "home", safe(string(exec)))
	if err := os.RemoveAll(home); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	if restore != nil {
		if err := untar(filepath.Join(p.base, "snapshots", string(*restore)), home); err != nil {
			return nil, fmt.Errorf("restore snapshot: %w", err)
		}
	}
	return &localSandbox{home: home, snapDir: filepath.Join(p.base, "snapshots"), timeout: p.maxWall}, nil
}

func (p *LocalPool) Release(ctx context.Context, sb engine.Sandbox, persist bool) error {
	ls, ok := sb.(*localSandbox)
	if !ok {
		return nil
	}
	if persist {
		if _, err := ls.Snapshot(ctx); err != nil {
			return err
		}
	}
	return os.RemoveAll(ls.home)
}

type localSandbox struct {
	home    string
	snapDir string
	timeout time.Duration
}

func (s *localSandbox) Exec(ctx context.Context, cmd engine.Command) (engine.ExecResult, error) {
	if len(cmd.Argv) == 0 {
		return engine.ExecResult{}, fmt.Errorf("shell: empty argv")
	}
	cctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	c := exec.CommandContext(cctx, cmd.Argv[0], cmd.Argv[1:]...)
	dir := s.home
	if cmd.Dir != "" {
		dir = filepath.Join(s.home, cmd.Dir)
	}
	c.Dir = dir
	c.Env = append(os.Environ(), "HOME="+s.home)
	for k, v := range cmd.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	res := engine.ExecResult{Code: 0, Stdout: []byte(stdout.String()), Stderr: []byte(stderr.String())}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Code = ee.ExitCode()
		} else {
			return res, err
		}
	}
	return res, nil
}

func (s *localSandbox) Snapshot(_ context.Context) (engine.SnapshotKey, error) {
	key := uuid.NewString() + ".tar.gz"
	if err := tarDir(s.home, filepath.Join(s.snapDir, key)); err != nil {
		return "", err
	}
	return engine.SnapshotKey(key), nil
}

func tarDir(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
}

func untar(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, hdr.Name)
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry escapes destination: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // dev sandbox, bounded inputs
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
