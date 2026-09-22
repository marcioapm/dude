// Command factory-runner is the worker daemon.
//
// It registers with the control plane, reports capacity, leases Runs, and
// makes each one real on this node: materialize a Session Workspace, start a
// container against it, drive the harness, stream events back, clean up.
//
// It owns no workflow or business policy. The control plane decides what
// should happen and whether it is allowed; the runner makes it happen.
//
// The same binary is used for local development and for remote worker nodes,
// so development exercises registration, heartbeats, leases, Docker lifecycle
// and reconciliation rather than a shortcut path that only exists in dev
// (plan §60).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/marciomartins/dude/runner/internal/client"
	"github.com/marciomartins/dude/runner/internal/protocol"
	dockerruntime "github.com/marciomartins/dude/runner/internal/runtime"
	"github.com/marciomartins/dude/runner/internal/workspace"
)

type config struct {
	controlPlaneURL string
	apiKey          string
	name            string
	pool            string
	workspaceRoot   string
	maxRuns         int
	pollInterval    time.Duration
	heartbeat       time.Duration
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.controlPlaneURL, "control-plane", env("DUDE_CONTROL_PLANE", "http://localhost:3000"),
		"control plane base URL")
	flag.StringVar(&cfg.apiKey, "api-key", os.Getenv("DUDE_RUNNER_KEY"), "runner API key")
	flag.StringVar(&cfg.name, "name", env("DUDE_RUNNER_NAME", defaultName()), "worker name (stable across restarts)")
	flag.StringVar(&cfg.pool, "pool", env("DUDE_RUNNER_POOL", "local"), "worker pool")
	flag.StringVar(&cfg.workspaceRoot, "workspace-root", env("DUDE_WORKSPACE_ROOT", defaultWorkspaceRoot()),
		"directory for workspaces and the repository mirror cache")
	flag.IntVar(&cfg.maxRuns, "max-runs", 2, "maximum concurrent Runs on this node")
	flag.DurationVar(&cfg.pollInterval, "poll-interval", 2*time.Second, "how often to poll for work")
	flag.DurationVar(&cfg.heartbeat, "heartbeat", 15*time.Second, "heartbeat interval")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if cfg.apiKey == "" {
		logger.Error("a runner API key is required (--api-key or DUDE_RUNNER_KEY)")
		os.Exit(1)
	}

	// Signals cancel the root context, so shutdown unwinds through the same
	// path as any other cancellation.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("runner failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	docker, err := dockerruntime.NewManager()
	if err != nil {
		return err
	}
	defer docker.Close()

	// Fail at startup rather than on the first Run.
	if err := docker.Ping(ctx); err != nil {
		return err
	}

	ws := workspace.NewManager(cfg.workspaceRoot)
	api := client.New(cfg.controlPlaneURL, cfg.apiKey)

	worker, err := api.Register(ctx, client.RegisterRequest{
		Name:               cfg.name,
		Pool:               cfg.pool,
		CPUMillis:          runtime.NumCPU() * 1000,
		MemoryMB:           0, // reported as unknown; the scheduler treats 0 as unconstrained
		MaxRuns:            cfg.maxRuns,
		CachedImages:       []string{},
		CachedRepositories: ws.CachedRepositories(),
	})
	if err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	logger.Info("registered", "worker", worker.ID, "name", worker.Name, "pool", worker.Pool)

	d := &daemon{
		cfg:    cfg,
		api:    api,
		docker: docker,
		ws:     ws,
		worker: worker,
		logger: logger,
		active: make(map[string]context.CancelFunc),
	}

	// Adopt containers left behind by a previous process before taking new
	// work, so the node's real state matches what the control plane believes.
	d.reconcile(ctx)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.heartbeatLoop(ctx) }()
	go func() { defer wg.Done(); d.pollLoop(ctx) }()
	wg.Wait()

	d.shutdown()
	return ctx.Err()
}

type daemon struct {
	cfg    config
	api    *client.Client
	docker *dockerruntime.Manager
	ws     *workspace.Manager
	worker *client.Worker
	logger *slog.Logger

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func (d *daemon) activeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.active)
}

// reconcile removes containers for Runs this process no longer tracks. After
// a restart the runner has no in-memory state, so anything still running is
// orphaned and must not be left consuming the node.
func (d *daemon) reconcile(ctx context.Context) {
	managed, err := d.docker.ListManaged(ctx)
	if err != nil {
		d.logger.Warn("reconcile: cannot list containers", "error", err)
		return
	}
	for runID := range managed {
		d.logger.Info("reconcile: removing orphaned container", "run", runID)
		if err := d.docker.Stop(ctx, runID); err != nil {
			d.logger.Warn("reconcile: stop failed", "run", runID, "error", err)
			continue
		}
		// The control plane must learn the Run is not progressing, or it will
		// wait on a lease that nobody is renewing.
		if err := d.api.UpdateRun(ctx, runID, protocol.RunFailed, "runner restarted; container orphaned", ""); err != nil {
			d.logger.Warn("reconcile: status update failed", "run", runID, "error", err)
		}
	}
}

func (d *daemon) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := d.api.Heartbeat(ctx, d.worker.ID, client.HeartbeatRequest{
				ActiveRuns:         d.activeCount(),
				Status:             protocol.WorkerReady,
				CachedRepositories: d.ws.CachedRepositories(),
			})
			if err != nil && ctx.Err() == nil {
				d.logger.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

func (d *daemon) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			free := d.cfg.maxRuns - d.activeCount()
			if free <= 0 {
				continue
			}

			runs, err := d.api.ClaimRuns(ctx, d.worker.ID, free)
			if err != nil {
				if ctx.Err() == nil {
					d.logger.Warn("claim failed", "error", err)
				}
				continue
			}

			for _, r := range runs {
				d.startRun(ctx, r)
			}
		}
	}
}

// startRun launches a Run in its own goroutine with a cancellable context, so
// shutdown and abort both work through cancellation.
func (d *daemon) startRun(parent context.Context, r client.Run) {
	ctx, cancel := context.WithCancel(parent)

	d.mu.Lock()
	d.active[r.ID] = cancel
	d.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			d.mu.Lock()
			delete(d.active, r.ID)
			d.mu.Unlock()
		}()

		if err := d.executeRun(ctx, r); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			d.logger.Error("run failed", "run", r.ID, "error", err)
			// Best effort: the control plane must not wait forever on a Run
			// whose worker already gave up.
			_ = d.api.UpdateRun(context.WithoutCancel(ctx), r.ID, protocol.RunFailed, err.Error(), "")
		}
	}()
}

// shutdown stops in-flight Runs and their containers.
func (d *daemon) shutdown() {
	d.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(d.active))
	runIDs := make([]string, 0, len(d.active))
	for runID, cancel := range d.active {
		cancels = append(cancels, cancel)
		runIDs = append(runIDs, runID)
	}
	d.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, runID := range runIDs {
		d.logger.Info("stopping run for shutdown", "run", runID)
		if err := d.docker.Stop(ctx, runID); err != nil {
			d.logger.Warn("shutdown stop failed", "run", runID, "error", err)
		}
	}
	d.logger.Info("runner stopped")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultName is stable across restarts so the worker reclaims its identity
// and its repository-cache affinity.
func defaultName() string {
	host, err := os.Hostname()
	if err != nil {
		return "runner-local"
	}
	return "runner-" + host
}

func defaultWorkspaceRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.dude/runner"
	}
	return "/var/lib/dude/runner"
}
