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

	/*
	 * `runCtx` is what the agent runs under, so cancelling it interrupts the
	 * turn. A hard pause or an abort cancels it; a graceful pause sets a flag
	 * that is checked between phases, letting the current safe action finish
	 * (plan §24).
	 */
	runCtx, interrupt := context.WithCancel(ctx)
	defer interrupt()

	control := &runControl{}

	// Renew the lease for as long as this Run is executing, so the control
	// plane can distinguish "still working" from "worker disappeared". The
	// same call carries back anything a human has asked of this Run.
	leaseCtx, stopLease := context.WithCancel(ctx)
	defer stopLease()
	go d.leaseLoop(leaseCtx, r.ID,
		func(kind, reason string) {
			control.request(kind, reason)

			// Hard pause and abort stop the agent now; graceful waits for the
			// next phase boundary.
			if kind != protocol.ControlPauseHard && kind != protocol.ControlAbort {
				return
			}

			/*
			 * Cancelling the context is not enough. The agent runs inside a
			 * Docker exec, and cancelling the client-side call abandons the
			 * stream without killing the process on the other side — the
			 * model keeps generating, and the human who pressed stop keeps
			 * paying for it.
			 *
			 * Stopping the container is what actually ends the turn.
			 */
			interrupt()
			stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancelStop()
			if err := d.docker.Stop(stopCtx, r.ID); err != nil {
				log.Warn("stopping container for intervention failed", "error", err)
			}
		},
		func(directive client.Directive) { control.addDirective(directive) })

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
	d.sendEvents(ctx, log, events)

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
	// Stop before starting the agent if a pause or abort already arrived —
	// there is no point beginning a turn we are about to interrupt.
	if control.stopped() {
		cleanup, cancel := d.teardown(ctx, r, created, log)
		defer cancel()
		d.applyControl(cleanup, r, control, log)
		return nil
	}

	// Under runCtx, not ctx: cancelling it is how a hard pause or abort
	// interrupts the turn.
	agentErr := d.runAgent(runCtx, r, created.ContainerID, materialized, control, log)

	// Capture what changed before the container goes away. The workspace
	// outlives it, but reading the diff here keeps the Run's record complete
	// even if the workspace is later reaped.
	changes := d.collectChanges(ctx, r, created.ContainerID, materialized, log)
	d.sendEvents(ctx, log, changes)

	cleanup, cancel := d.teardown(ctx, r, created, log)
	defer cancel()

	// An interrupted turn is not a failure: the human asked for it, and
	// reporting it as failed would make deliberate control look like a bug.
	if terminal := d.applyControl(cleanup, r, control, log); terminal != "" {
		log.Info("run stopped by intervention", "terminal", terminal, "took", time.Since(started))
		return nil
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

/*
leaseLoop renews the Run lease and carries the control channel.

Renewal runs at half the lease interval, which tolerates one lost renewal
without the control plane reclaiming the Run. The response brings back
anything a human has asked of this Run since the last tick — a pause, an
abort, or steering directives — so intervention needs no second poll.

`onControl` is called for a pause or abort; `onDirective` for steering. Both
run on this goroutine, so they must not block for long.
*/
func (d *daemon) leaseLoop(
	ctx context.Context,
	runID string,
	onControl func(control, reason string),
	onDirective func(client.Directive),
) {
	/*
	 * Poll far more often than the lease requires.
	 *
	 * Renewal alone could run at half the lease — 45s — but this is also the
	 * control channel, and a human who aborts a run should not watch it keep
	 * working for the better part of a minute. The renewal is cheap; the
	 * responsiveness is what matters.
	 */
	interval := controlPollInterval
	if lease := time.Duration(d.worker.LeaseSeconds) * time.Second / 2; lease > 0 && lease < interval {
		interval = lease
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lease, err := d.api.RenewLease(ctx, runID)
			if err != nil {
				// The control plane no longer considers this Run ours —
				// aborted, or reclaimed after we went quiet. Either way the
				// work must stop now rather than run on unowned.
				if errors.Is(err, client.ErrRunNotLeasable) {
					d.logger.Info("run no longer leasable; stopping", "run", runID)
					if onControl != nil {
						onControl(protocol.ControlAbort, "run is no longer leasable")
					}
					return
				}
				if ctx.Err() == nil {
					d.logger.Warn("lease renewal failed", "run", runID, "error", err)
				}
				continue
			}

			for _, directive := range lease.Directives {
				d.logger.Info("directive received", "run", runID,
					"scope", directive.Scope, "text", directive.Text)
				if onDirective != nil {
					onDirective(directive)
				}
			}

			if lease.Control != "" && lease.Control != protocol.ControlNone && onControl != nil {
				d.logger.Info("control requested", "run", runID,
					"control", lease.Control, "reason", lease.ControlReason)
				onControl(lease.Control, lease.ControlReason)
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
	control *runControl,
	log *slog.Logger,
) error {
	if len(repos) == 0 {
		return fmt.Errorf("no repository materialized for run %s", r.ID)
	}

	// Work in the first repository. Multi-repo Runs (plan §14) will need the
	// orchestrator to choose, but the workspace root is never a git repo.
	workDir := repoDir(repos[0].Name)

	prompt := r.Prompt
	if prompt == "" {
		return fmt.Errorf("run %s has no prompt", r.ID)
	}

	// Directives issued before the turn started are appended to the task, so
	// a human who steers a queued Run is heard on its first turn rather than
	// having to wait for a second one.
	if directives := control.takeDirectives(); len(directives) > 0 {
		var b strings.Builder
		b.WriteString(prompt)
		b.WriteString("\n\nUpdated instructions from the operator:")
		for _, directive := range directives {
			b.WriteString("\n- ")
			b.WriteString(directive.Text)
		}
		prompt = b.String()
		log.Info("applied directives to prompt", "count", len(directives))
	}

	model := r.Model
	if model == "" {
		return fmt.Errorf("run %s has no model configured", r.ID)
	}

	/*
	 * The fake harness spends nothing and is deterministic, which is what the
	 * platform's own tests need: workflow transitions, lease handling, event
	 * ordering and container lifecycle are all independent of whether a real
	 * model wrote the text (plan §27.1, §34.15).
	 *
	 * Selected by model name rather than a runner flag, so a single runner can
	 * serve both real and fake Runs and the choice lives with the project
	 * configuration that made it.
	 */
	if strings.HasPrefix(model, FakeModelPrefix) {
		return d.runFakeAgent(ctx, r, containerID, repos[0], log)
	}

	log.Info("agent starting", "model", model, "repo", repos[0].Name)

	// Batch events rather than posting one HTTP request per tool call: a busy
	// turn emits hundreds, and the ledger cares about order, not latency.
	var pending []client.Event
	flush := func() {
		if len(pending) == 0 {
			return
		}
		d.sendEvents(ctx, log, pending)
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
		changes := d.inspectRepo(ctx, containerID, repo)
		if changes == nil {
			continue
		}

		log.Info("repository changed", "repo", repo.Name,
			"commits", len(changes.commits), "dirty", changes.uncommitted != "")

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
				"headSha":     changes.head,
				"commits":     strings.Join(changes.commits, "\n"),
				"diffstat":    changes.diffstat,
				"uncommitted": changes.uncommitted,
			},
		})
	}
	return events
}

// repoChanges is what one repository looked like after the agent finished.
type repoChanges struct {
	head        string
	commits     []string
	diffstat    string
	uncommitted string
}

// Separates the sections of the combined git output below. Chosen to be
// something git will never emit on its own.
const gitSectionSeparator = "---dude-section---"

/*
inspectRepo reads a repository's post-run state in a single container exec.

One exec rather than four: each is a full Docker exec-create, attach, demux
and inspect cycle, and they sit on the critical path between "the agent
finished" and "the Run is reported terminal". Four round trips per repository
is latency paid on every Run for information that one shell invocation can
gather at once.

Returns nil when the repository is unchanged, or when the commands fail —
this is diagnostic collection, not control flow, and a Run must not fail
because its summary could not be read.
*/
func (d *daemon) inspectRepo(
	ctx context.Context,
	containerID string,
	repo workspace.MaterializedRepo,
) *repoChanges {
	dir := repoDir(repo.Name)

	script := strings.Join([]string{
		"git rev-parse HEAD 2>/dev/null || true",
		"echo " + gitSectionSeparator,
		"git status --porcelain 2>/dev/null || true",
		"echo " + gitSectionSeparator,
		"git diff --stat HEAD 2>/dev/null || true",
		"echo " + gitSectionSeparator,
		// Empty when HEAD has not moved, which is the common case.
		fmt.Sprintf("git log --oneline %s..HEAD 2>/dev/null || true", repo.HeadSHA),
	}, "\n")

	var out strings.Builder
	_, err := d.docker.ExecStream(ctx, containerID,
		[]string{"sh", "-c", fmt.Sprintf("cd %q && { %s\n }", dir, script)}, nil,
		func(line string) {
			out.WriteString(line)
			out.WriteByte('\n')
		})
	if err != nil {
		return nil
	}

	sections := strings.Split(out.String(), gitSectionSeparator)
	section := func(i int) string {
		if i >= len(sections) {
			return ""
		}
		return strings.TrimSpace(sections[i])
	}

	changes := &repoChanges{
		head:        section(0),
		uncommitted: section(1),
		diffstat:    section(2),
	}
	if log := section(3); log != "" {
		changes.commits = strings.Split(log, "\n")
	}

	if len(changes.commits) == 0 && changes.uncommitted == "" && changes.diffstat == "" {
		return nil
	}
	return changes
}

/*
sendEvents ships execution-plane events to the ledger.

A dropped event is not worth failing a Run over — the Run's own status still
records the outcome — but it must be visible, so every failure is logged with
how many were lost rather than swallowed.
*/
func (d *daemon) sendEvents(ctx context.Context, log *slog.Logger, events []client.Event) {
	if len(events) == 0 {
		return
	}
	if err := d.api.SendEvents(ctx, events); err != nil {
		log.Warn("event ingest failed", "error", err, "dropped", len(events))
	}
}

// repoDir is where a materialized repository appears inside the container.
func repoDir(name string) string {
	return dockerruntime.ContainerWorkspacePath + "/" + workspace.DirRepos + "/" + name
}

// tail returns the last n characters, for error messages that must stay
// readable without discarding the part that explains the failure.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

/*
controlPollInterval is how often the runner asks for pending interventions.

Tight because it bounds how long an aborted agent keeps working after a
human said stop. The request is a single indexed UPDATE, so the cost of
polling is small next to the cost of ignoring a person.
*/
const controlPollInterval = 3 * time.Second

// FakeModelPrefix marks a Run that should execute without a model.
//
// Platform tests assert on lease handling, event ordering, container
// lifecycle and workspace materialization — none of which depend on a real
// model. Running one would make the suite slow, non-deterministic and
// expensive for no added coverage.
const FakeModelPrefix = "fake/"

// runFakeAgent performs a scripted change instead of calling a model.
//
// It writes a file and commits it, so the Run exercises exactly the same
// downstream path as a real agent: a dirty worktree, a new commit, and a
// git.commit_created event carrying both.
func (d *daemon) runFakeAgent(
	ctx context.Context,
	r client.Run,
	containerID string,
	repo workspace.MaterializedRepo,
	log *slog.Logger,
) error {
	log.Info("fake agent starting", "repo", repo.Name)

	dir := repoDir(repo.Name)
	script := strings.Join([]string{
		"set -e",
		// Content includes the Run id so concurrent Runs produce distinct
		// commits and cannot be confused for one another.
		fmt.Sprintf("printf '%%s\\n' 'Written by run %s' > FACTORY.md", r.ID),
		"git add FACTORY.md",
		fmt.Sprintf("git commit -q -m 'Add FACTORY.md for %s'", r.ID),
	}, "\n")

	var output strings.Builder
	exitCode, err := d.docker.ExecStream(ctx, containerID,
		[]string{"sh", "-c", fmt.Sprintf("cd %q && %s", dir, script)}, nil,
		func(line string) { output.WriteString(line + "\n") })
	if err != nil {
		return fmt.Errorf("fake agent: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("fake agent exited %d: %s", exitCode, tail(output.String(), 500))
	}

	events := []client.Event{{
		EventType:  protocol.EventAgentMessage,
		RunID:      r.ID,
		ProjectID:  r.ProjectID,
		WorkItemID: r.WorkItemID,
		ActorType:  "agent",
		ActorID:    r.ID,
		Payload:    map[string]any{"text": "fake agent wrote FACTORY.md", "fake": true},
	}}
	d.sendEvents(ctx, log, events)

	log.Info("fake agent finished")
	return nil
}

/*
teardown stops the Run's container and reports the runtime destroyed.

Called *before* the Run is reported terminal: reporting completion first
makes "the Run is done" and "its container is gone" observably inconsistent,
and anything reacting to the terminal status — the UI, a test, a scheduler
counting capacity — could still see a live container.

Returns a cleanup context detached from the Run's own, so an aborted or
paused Run is still reaped, and the cancel func the caller must defer.
*/
func (d *daemon) teardown(
	ctx context.Context,
	r client.Run,
	created *dockerruntime.Created,
	log *slog.Logger,
) (context.Context, context.CancelFunc) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)

	if err := d.docker.Stop(cleanup, r.ID); err != nil {
		log.Warn("container cleanup failed", "error", err)
	}
	if err := d.api.ReportRuntime(cleanup, r.ID, created.ContainerID, "", protocol.RuntimeDestroyed); err != nil {
		log.Warn("runtime report failed", "error", err)
	}
	return cleanup, cancel
}

/*
runControl is what a human has asked of an executing Run.

Written by the lease loop and read by the Run goroutine, so every field is
mutex-guarded. Kept deliberately small: the runner records the request and
acts on it, but never decides whether it was allowed — that judgement already
happened in the control plane.
*/
type runControl struct {
	mu         sync.Mutex
	kind       string
	reason     string
	directives []client.Directive
}

// request records a pause or abort. The first one wins: a graceful pause
// followed by an abort escalates, but an abort is never downgraded.
func (c *runControl) request(kind, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kind == protocol.ControlAbort {
		return
	}
	c.kind = kind
	c.reason = reason
}

func (c *runControl) addDirective(d client.Directive) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.directives = append(c.directives, d)
}

// pending reports what was asked, if anything.
func (c *runControl) pending() (kind, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kind, c.reason
}

// takeDirectives returns undelivered steering text and clears it, so the same
// instruction is not appended to two consecutive turns.
func (c *runControl) takeDirectives() []client.Directive {
	c.mu.Lock()
	defer c.mu.Unlock()
	taken := c.directives
	c.directives = nil
	return taken
}

// stopped reports whether the Run should stop before starting more work.
// Checked at phase boundaries, which is what makes a graceful pause graceful.
func (c *runControl) stopped() bool {
	kind, _ := c.pending()
	return kind != "" && kind != protocol.ControlNone
}

/*
applyControl reports the Run's terminal state after an intervention.

Pause and abort are different terminal shapes: an aborted Run is finished, a
paused one is waiting to be resumed and must not look like a failure. Returns
"" when nothing was asked.
*/
func (d *daemon) applyControl(ctx context.Context, r client.Run, c *runControl, log *slog.Logger) string {
	kind, reason := c.pending()
	switch kind {
	case protocol.ControlAbort:
		log.Info("run aborted by request", "reason", reason)
		// The control plane already marked it aborted; the runner's job was
		// to stop the work, which cancelling runCtx did.
		return protocol.RunAborted

	case protocol.ControlPauseGraceful, protocol.ControlPauseHard:
		log.Info("run paused by request", "mode", kind, "reason", reason)
		if err := d.api.UpdateRun(ctx, r.ID, protocol.RunPaused, reason, ""); err != nil {
			log.Warn("pause status update failed", "error", err)
		}
		return protocol.RunPaused

	default:
		return ""
	}
}
