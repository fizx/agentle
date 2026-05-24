// Command agentle is the all-in-one server: control-plane API, durable execution
// engine, capability executors, trigger scheduler, and the embedded dashboard.
// It runs self-contained against local SQLite + a local subprocess sandbox — no
// Docker, Postgres, or Redis required for playtesting.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kylemaxwell/agentle/internal/api"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/pricing"
	"github.com/kylemaxwell/agentle/internal/sandbox"
	"github.com/kylemaxwell/agentle/internal/secrets"
	"github.com/kylemaxwell/agentle/internal/store"
	"github.com/kylemaxwell/agentle/internal/trigger"
	"github.com/kylemaxwell/agentle/web"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dataDir := flag.String("data", "./data", "data directory (sqlite db, sandbox homes, snapshots)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(*addr, *dataDir, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, dataDir string, log *slog.Logger) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "agentle.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	pool, err := sandbox.NewLocalPool(filepath.Join(dataDir, "sandbox"), 60*time.Second)
	if err != nil {
		return err
	}

	leaser := engine.NewMemLeaser()
	sink := func(exec engine.ExecutionID, msg string) { log.Info("script.log", "exec", string(exec), "msg", msg) }
	svc := platform.New(st, st.EventLog(leaser), leaser, pool, st.KV(), st.Inbox(), sink, platform.Config{})
	prices := pricing.New()
	svc.Pricing = prices
	// Secret backend: Vault when VAULT_ADDR is set, else the default SQLite store.
	if addr := os.Getenv("VAULT_ADDR"); addr != "" {
		vault, err := secrets.Vault(secrets.VaultConfig{
			Addr:   addr,
			Token:  os.Getenv("VAULT_TOKEN"),
			Mount:  os.Getenv("VAULT_KV_MOUNT"),
			Prefix: os.Getenv("VAULT_PREFIX"),
		})
		if err != nil {
			return err
		}
		svc.Secrets = vault
		log.Info("using Vault secret backend", "addr", addr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := seed(ctx, st, log); err != nil {
		return err
	}

	// The dispatcher resumes durably-suspended executions when their inbox gets a
	// message or their wake deadline passes; it also recovers parked runs at boot.
	go svc.RunDispatcher(ctx, 500*time.Millisecond, log)
	// Keep the OpenRouter price table warm for cost tracking (best-effort/offline-safe).
	go prices.RefreshLoop(ctx)

	sched := trigger.NewScheduler(st, svc, log)
	if err := sched.Reload(ctx); err != nil {
		log.Warn("initial cron load failed", "err", err)
	}
	defer sched.Stop()

	static, err := web.FS()
	if err != nil {
		return err
	}
	srv := api.New(svc, sched, static, log)

	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		log.Info("agentle listening", "addr", addr, "dashboard", "http://localhost"+addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutCtx)
}
