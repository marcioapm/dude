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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marciomartins/dude/runner/internal/client"
	"github.com/marciomartins/dude/runner/internal/harness"
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

// executeRun takes one Run from lease to terminal state.
func (d *daemon) executeRun(ctx context.Context, r client.Run) error {
	log := d.logger.With("run", r.ID, "attempt", r.Attempt)
	log.Info("run claimed", "repos", len(r.Repositories))

	// Renew the lease for as long as this Run is executing, so the control
	// plane can distinguish "still working" from "worker disappeared".
	leaseCtx, stopLease := context.WithCancel(ctx)
	defer stopLease()
	go d.leaseLoop(leaseCtx, r.ID)

	if err := d.api.UpdateRun(ctx, r.ID, protocol.RunStarting, "", ""); err != nil {
		return fmt.Errorf("mark starting: %w", err)
	}

	repos := make([]workspace.Repository, 0, len(r.Repositories))
	for _, repo := range r.Repositories {
		repos = append(repos, workspace.Repository{
			Name:          repo.Name,
			URL:           repo.URL,
			DefaultBranch: repo.DefaultBranch,
		})
	}

	started := time.Now()
	wsPath, materialized, err := d.ws.Create(ctx, r.ProjectID, r.ID, repos)
	if err != nil {
		return fmt.Errorf("materialize workspace: %w", err)
	}
	log.Info("workspace ready", "path", wsPath, "took", time.Since(started))

	events := []client.Event{{
		EventType:  protocol.EventWorkspaceCreated,
		RunID:      r.ID,
		ProjectID:  r.ProjectID,
		WorkItemID: r.WorkItemID,
		ActorType:  "system",
		ActorID:    d.worker.ID,
		Payload: map[string]any{
			"path":       wsPath,
			"repos":      materialized,
			"durationMs": time.Since(started).Milliseconds(),
		},
	}}
	if err := d.api.SendEvents(ctx, events); err != nil {
		log.Warn("event ingest failed", "error", err)
	}

	// An untrusted repository gets no network at all (plan §47).
	network := protocol.NetworkBridge
	for _, repo := range r.Repositories {
		if repo.Trust == protocol.TrustExternal {
			network = protocol.NetworkIsolated
			break
		}
	}

	if err := d.api.ReportRuntime(ctx, r.ID, "", "", protocol.RuntimeCreating); err != nil {
		log.Warn("runtime report failed", "error", err)
	}

	created, err := d.docker.Create(ctx, dockerruntime.Spec{
		RunID:          r.ID,
		OrganizationID: r.ProjectID,
		Image:          r.RuntimeImage,
		HostWorkspace:  wsPath,
		Limits:         dockerruntime.DefaultLimits(),
		NetworkMode:    network,
	})
	if err != nil {
		_ = d.api.ReportRuntime(context.WithoutCancel(ctx), r.ID, "", "", protocol.RuntimeFailed)
		return fmt.Errorf("create runtime: %w", err)
	}
	log.Info("container started", "container", created.ContainerID[:12], "network", network)

	if err := d.docker.WaitHealthy(ctx, created.ContainerID, 60*time.Second); err != nil {
		_ = d.api.ReportRuntime(context.WithoutCancel(ctx), r.ID, created.ContainerID, "", protocol.RuntimeFailed)
		return fmt.Errorf("runtime unhealthy: %w", err)
	}

	if err := d.api.ReportRuntime(ctx, r.ID, created.ContainerID, created.ImageDigest, protocol.RuntimeRunning); err != nil {
		log.Warn("runtime report failed", "error", err)
	}
	if err := d.api.UpdateRun(ctx, r.ID, protocol.RunRunning, "", wsPath); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	// Drive the agent. This is where the Run stops being plumbing and starts
	// being work: the harness gets the task, the workspace and a scoped set of
	// credentials, and its progress streams to the ledger as it happens.
	agentErr := d.runAgent(ctx, r, created.ContainerID, materialized, log)

	// Capture what changed before the container goes away. The workspace
	// outlives it, but reading the diff here keeps the Run's record complete
	// even if the workspace is later reaped.
	changes := d.collectChanges(ctx, r, created.ContainerID, materialized, log)
	if len(changes) > 0 {
		if err := d.api.SendEvents(ctx, changes); err != nil {
			log.Warn("change event ingest failed", "error", err)
		}
	}

	// Tear the runtime down *before* reporting the Run terminal.
	//
	// Reporting completion first makes "the Run is done" and "its container is
	// gone" observably inconsistent: anything reacting to the terminal status
	// — the UI, a test, a scheduler counting capacity — can still see a live
	// container. Cleanup uses a context detached from the Run's so an aborted
	// Run is reaped too.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := d.docker.Stop(cleanup, r.ID); err != nil {
		log.Warn("container cleanup failed", "error", err)
	}
	if err := d.api.ReportRuntime(cleanup, r.ID, created.ContainerID, "", protocol.RuntimeDestroyed); err != nil {
		log.Warn("runtime report failed", "error", err)
	}

	if agentErr != nil {
		if err := d.api.UpdateRun(cleanup, r.ID, protocol.RunFailed, agentErr.Error(), wsPath); err != nil {
			log.Warn("status update failed", "error", err)
		}
		return fmt.Errorf("agent: %w", agentErr)
	}

	if err := d.api.UpdateRun(cleanup, r.ID, protocol.RunCompleted, "", wsPath); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	log.Info("run completed", "took", time.Since(started))
	return nil
}

// leaseLoop renews the Run lease at half the lease interval, which tolerates
// one lost renewal without the control plane reclaiming the Run.
func (d *daemon) leaseLoop(ctx context.Context, runID string) {
	interval := time.Duration(d.worker.LeaseSeconds) * time.Second / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.api.RenewLease(ctx, runID); err != nil && ctx.Err() == nil {
				d.logger.Warn("lease renewal failed", "run", runID, "error", err)
			}
		}
	}
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

// runAgent drives the harness for one Run.
//
// The task comes from the control plane; the credentials come from this
// runner's environment and are passed per-exec rather than baked into the
// image, so a Session's access ends when its container does.
func (d *daemon) runAgent(
	ctx context.Context,
	r client.Run,
	containerID string,
	repos []workspace.MaterializedRepo,
	log *slog.Logger,
) error {
	if len(repos) == 0 {
		return fmt.Errorf("no repository materialized for run %s", r.ID)
	}

	// Work in the first repository. Multi-repo Runs (plan §14) will need the
	// orchestrator to choose, but the workspace root is never a git repo.
	workDir := dockerruntime.ContainerWorkspacePath + "/" + workspace.DirRepos + "/" + repos[0].Name

	prompt := r.Prompt
	if prompt == "" {
		return fmt.Errorf("run %s has no prompt", r.ID)
	}

	model := r.Model
	if model == "" {
		return fmt.Errorf("run %s has no model configured", r.ID)
	}

	log.Info("agent starting", "model", model, "repo", repos[0].Name)

	// Batch events rather than posting one HTTP request per tool call: a busy
	// turn emits hundreds, and the ledger cares about order, not latency.
	var pending []client.Event
	flush := func() {
		if len(pending) == 0 {
			return
		}
		if err := d.api.SendEvents(ctx, pending); err != nil {
			log.Warn("event ingest failed", "error", err, "dropped", len(pending))
		}
		pending = nil
	}

	turnCtx, cancel := context.WithTimeout(ctx, harness.DefaultTimeout)
	defer cancel()

	agent := harness.NewOpenCode(d.docker)
	result, err := agent.Run(turnCtx, harness.Spec{
		ContainerID: containerID,
		WorkDir:     workDir,
		Model:       model,
		Agent:       r.Role,
		Prompt:      prompt,
		Env:         d.agentEnv(),
	}, func(ev harness.Event) {
		pending = append(pending, client.Event{
			EventType:  ev.Type,
			RunID:      r.ID,
			ProjectID:  r.ProjectID,
			WorkItemID: r.WorkItemID,
			ActorType:  "agent",
			ActorID:    r.ID,
			Payload:    ev.Payload,
		})
		if len(pending) >= 50 {
			flush()
		}
	})
	flush()

	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		// The harness output is the only explanation of why; keep the tail.
		return fmt.Errorf("harness exited %d: %s", result.ExitCode, tail(result.Output, 2000))
	}

	log.Info("agent finished", "events", len(result.Events))
	return nil
}

// agentEnv is the credential set handed to a Session.
//
// Deliberately an allowlist rather than passing the runner's environment
// through: that environment holds this runner's control-plane key, which an
// agent must never see. Nothing here is baked into the image, so a Session's
// access ends when its container does.
func (d *daemon) agentEnv() map[string]string {
	env := map[string]string{}
	for _, key := range []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_BASE_URL",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		// Lets a deployment point the harness at a gateway or a private
		// model catalogue without rebuilding the runtime image.
		"OPENCODE_CONFIG_CONTENT",
	} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	return env
}

// collectChanges records what the agent actually did to each repository.
//
// A Run whose agent edited nothing looks identical to one that failed
// silently, so the diffstat and the commits are part of the Run's record.
func (d *daemon) collectChanges(
	ctx context.Context,
	r client.Run,
	containerID string,
	repos []workspace.MaterializedRepo,
	log *slog.Logger,
) []client.Event {
	var events []client.Event

	for _, repo := range repos {
		dir := dockerruntime.ContainerWorkspacePath + "/" + workspace.DirRepos + "/" + repo.Name

		head := d.execCapture(ctx, containerID, dir, "git rev-parse HEAD")
		status := d.execCapture(ctx, containerID, dir, "git status --porcelain")
		diffstat := d.execCapture(ctx, containerID, dir, "git diff --stat HEAD")

		// New commits since the workspace was materialized.
		commits := ""
		if head != "" && head != repo.HeadSHA {
			commits = d.execCapture(ctx, containerID, dir,
				fmt.Sprintf("git log --oneline %s..HEAD", repo.HeadSHA))
		}

		if commits == "" && status == "" && diffstat == "" {
			continue
		}

		// Count lines, not separators: a single commit has no trailing
		// newline once trimmed, so counting "\n" reports zero for it.
		commitCount := 0
		if commits != "" {
			commitCount = len(strings.Split(commits, "\n"))
		}

		log.Info("repository changed", "repo", repo.Name,
			"commits", commitCount, "dirty", status != "")

		events = append(events, client.Event{
			EventType: protocol.EventGitCommitCreated,
			// Scoped to the Run: without these the event is unreachable from
			// a Run's timeline, which is where anyone would look for it.
			RunID:      r.ID,
			ProjectID:  r.ProjectID,
			WorkItemID: r.WorkItemID,
			ActorType:  "agent",
			ActorID:    "harness",
			Payload: map[string]any{
				"repo":        repo.Name,
				"baseSha":     repo.HeadSHA,
				"headSha":     head,
				"commits":     commits,
				"diffstat":    diffstat,
				"uncommitted": status,
			},
		})
	}
	return events
}

// execCapture runs a shell command in the container and returns trimmed
// stdout, or "" if it fails — this is diagnostic collection, not control flow.
func (d *daemon) execCapture(ctx context.Context, containerID, dir, command string) string {
	var out strings.Builder
	_, err := d.docker.ExecStream(ctx, containerID,
		[]string{"sh", "-c", fmt.Sprintf("cd %q && %s", dir, command)}, nil,
		func(line string) {
			out.WriteString(line)
			out.WriteByte('\n')
		})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// tail returns the last n characters, for error messages that must stay
// readable without discarding the part that explains the failure.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
