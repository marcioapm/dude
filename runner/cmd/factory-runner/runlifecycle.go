// The Run lifecycle: taking one leased Run from claim to terminal state.
//
// Separated from daemon plumbing (registration, heartbeats, polling) because
// the two change for different reasons — this file is about what happens to a
// single Run, main.go is about keeping the worker alive.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/marciomartins/dude/runner/internal/client"
	"github.com/marciomartins/dude/runner/internal/harness"
	"github.com/marciomartins/dude/runner/internal/protocol"
	dockerruntime "github.com/marciomartins/dude/runner/internal/runtime"
	"github.com/marciomartins/dude/runner/internal/workspace"
)

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
	wsPath, materialized, err := d.ws.Create(ctx, r.ProjectID, r.ID, repos, r.BaseRef)
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
	agentOutput, agentErr := d.runAgent(runCtx, r, created.ContainerID, materialized, control, log)

	// Capture what changed before the container goes away. The workspace
	// outlives it, but reading the diff here keeps the Run's record complete
	// even if the workspace is later reaped.
	changes, moved := d.collectChanges(ctx, r, created.ContainerID, materialized, log)
	d.sendEvents(ctx, log, changes)

	/*
	 * Publish, when this phase is one that publishes.
	 *
	 * A reviewer gets a full sandbox and may commit locally while it pokes
	 * at things — that is useful. What it must not do is put those commits
	 * on the work item's branch, and the cleanest way to guarantee that is
	 * for the push step not to run at all rather than for a prompt to ask
	 * the agent nicely.
	 *
	 * Before teardown, while the lease is certainly still held: the push
	 * credential is scoped to a leased Run, and asking for one after the
	 * lease lapses is a 404.
	 */
	if r.Publishes {
		d.sendEvents(ctx, log, d.publish(ctx, r, materialized, moved, log))
	} else if len(moved) > 0 {
		log.Info("phase does not publish; leaving commits local",
			"phase", r.Phase, "repos", len(moved))
	}

	// A review or test Run's output is findings, which is what the delivery
	// workflow loops on.
	d.reportFindings(ctx, r, agentOutput, log)

	/*
	 * Tell the control plane whether this workspace could move.
	 *
	 * Only this runner can answer: it is the one that can see the working
	 * tree. Uncommitted work pins the Run here, because a fresh clone on
	 * another node would silently drop it.
	 */
	portable := d.workspacePortable(ctx, created.ContainerID, materialized)
	if err := d.api.ReportWorkspacePortable(ctx, r.ID, portable); err != nil {
		log.Warn("portability report failed", "error", err)
	}

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

// runAgent drives the harness for one Run, returning what the agent wrote.
//
// The output matters beyond logging: a review Run's findings are in it, and
// parsing them here is what closes the delivery workflow's loop.
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
) (string, error) {
	if len(repos) == 0 {
		return "", fmt.Errorf("no repository materialized for run %s", r.ID)
	}

	// Work in the first repository. Multi-repo Runs (plan §14) will need the
	// orchestrator to choose, but the workspace root is never a git repo.
	workDir := repoDir(repos[0].Name)

	prompt := r.Prompt
	if prompt == "" {
		return "", fmt.Errorf("run %s has no prompt", r.ID)
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
		return "", fmt.Errorf("run %s has no model configured", r.ID)
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
		return result.Output, err
	}
	if result.ExitCode != 0 {
		// The harness output is the only explanation of why; keep the tail.
		return result.Output, fmt.Errorf("harness exited %d: %s",
			result.ExitCode, tail(result.Output, 2000))
	}

	log.Info("agent finished", "events", len(result.Events))
	return result.Output, nil
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
) ([]client.Event, map[string]string) {
	var events []client.Event
	// Repository name -> new HEAD, for the repositories worth pushing.
	moved := map[string]string{}

	for _, repo := range repos {
		changes := d.inspectRepo(ctx, containerID, repo)
		if changes == nil {
			continue
		}
		if len(changes.commits) > 0 {
			moved[repo.Name] = changes.head
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
	return events, moved
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
		/*
		 * Against the ref this Run started from, not against HEAD.
		 *
		 * `git diff --stat HEAD` shows only what is uncommitted, which is
		 * empty for an agent that committed its work — the normal case. The
		 * result was a diffstat of nothing for every successful Run, and a
		 * delivery workflow that read that as "the implementer changed
		 * nothing" and escalated.
		 *
		 * The `|| git diff --stat HEAD` fallback covers a Run whose base ref
		 * is not in this clone, and an agent that left work uncommitted.
		 */
		fmt.Sprintf("git diff --stat %s..HEAD 2>/dev/null || git diff --stat HEAD 2>/dev/null || true", repo.HeadSHA),
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

// FakeModelPrefix marks a Run that should execute without a model.
//
// Platform tests assert on lease handling, event ordering, container
// lifecycle and workspace materialization — none of which depend on a real
// model. Running one would make the suite slow, non-deterministic and
// expensive for no added coverage.
const FakeModelPrefix = "fake/"

/*
runFakeAgent plays a phase without calling a model.

Returns what the agent "wrote", because a fake reviewer's findings travel the
same path a real one's do — parsed out of its output by the caller. Without
that the review → fix loop could only ever be tested by mocking the thing
under test.

Each phase behaves the way its real counterpart would, in the smallest way
that exercises the downstream path: the ones that publish make a commit, and
the ones that report findings write them to stdout in the format the prompt
asks a model for.
*/
func (d *daemon) runFakeAgent(
	ctx context.Context,
	r client.Run,
	containerID string,
	repo workspace.MaterializedRepo,
	log *slog.Logger,
) (string, error) {
	log.Info("fake agent starting", "repo", repo.Name, "phase", r.Phase)

	script, message := fakeScript(r)

	var output strings.Builder
	if script != "" {
		exitCode, err := d.docker.ExecStream(ctx, containerID,
			[]string{"sh", "-c", fmt.Sprintf("cd %q && %s", repoDir(repo.Name), script)}, nil,
			func(line string) { output.WriteString(line + "\n") })
		if err != nil {
			return output.String(), fmt.Errorf("fake agent: %w", err)
		}
		if exitCode != 0 {
			return output.String(), fmt.Errorf("fake agent exited %d: %s",
				exitCode, tail(output.String(), 500))
		}
	}

	d.sendEvents(ctx, log, []client.Event{{
		EventType:  protocol.EventAgentMessage,
		RunID:      r.ID,
		ProjectID:  r.ProjectID,
		WorkItemID: r.WorkItemID,
		ActorType:  "agent",
		ActorID:    r.ID,
		Payload:    map[string]any{"text": message, "fake": true},
	}})

	/*
	 * The script's output, not a separate value: a fake reviewer's findings
	 * travel the same path a real one's do — written to stdout and parsed by
	 * the caller. Returning them any other way would test a path production
	 * never takes.
	 */
	log.Info("fake agent finished", "phase", r.Phase, "output", len(output.String()))
	return output.String(), nil
}

/*
fakeScript is what a fake agent does for one phase.

Returns the shell to run in the container, the message it reports, and the
text the caller parses findings out of.

The reviewer's behaviour is the interesting one: it reports a blocking
finding on its first pass and nothing afterwards, which is exactly the shape
the review → fix loop needs to be exercised — one cycle, then convergence.
Keyed on whether the fix Run's file exists rather than on an attempt counter,
so it stays deterministic across a retried or replayed Run.
*/
func fakeScript(r client.Run) (script, message string) {
	switch r.Phase {
	case "review":
		/*
		 * Report a problem, unless a fixer has already been here.
		 *
		 * `test -f FIXED.md` is the check, evaluated in the container: a
		 * review of a tree that already contains the fixer's file is
		 * reviewing a fix, and reporting the same finding again would loop
		 * until the policy bound stopped it — proving the bound works, not
		 * that the loop converges. Keyed on the workspace rather than an
		 * attempt counter so it stays deterministic across a replayed Run.
		 */
		return "test -f FIXED.md && echo " + fakeReviewClean + " || cat <<'FINDING'\n" +
				fakeBlockingFinding + "\nFINDING",
			"fake reviewer inspected the change"

	case "fix":
		// Content names the Run, so a second fix of the same tree is still a
		// change — a fixer sent back must produce a commit, not fail on an
		// empty one.
		return strings.Join([]string{
			"set -e",
			fmt.Sprintf("printf '%%s\\n' 'addressed by %s' >> FIXED.md", r.ID),
			"git add FIXED.md",
			fmt.Sprintf("git commit -q -m 'Address review findings for %s'", r.ID),
		}, "\n"), "fake fixer addressed the findings"

	case "simplify":
		return strings.Join([]string{
			"set -e",
			"printf '%s\\n' 'simplified' >> FACTORY.md",
			"git add FACTORY.md",
			fmt.Sprintf("git commit -q -m 'Simplify %s'", r.ID),
		}, "\n"), "fake simplifier tidied the change"

	case "test":
		return "", "fake tester exercised the feature"

	default:
		// implement, and any Run with no phase at all.
		return strings.Join([]string{
			"set -e",
			// Content includes the Run id so concurrent Runs produce distinct
			// commits and cannot be confused for one another.
			fmt.Sprintf("printf '%%s\\n' 'Written by run %s' > FACTORY.md", r.ID),
			"git add FACTORY.md",
			fmt.Sprintf("git commit -q -m 'Add FACTORY.md for %s'", r.ID),
		}, "\n"), "fake agent wrote FACTORY.md"
	}
}

// What a fake reviewer says when it has nothing to report. Prose rather than
// an empty string, because a real reviewer that finds nothing says so.
const fakeReviewClean = "'reviewed the fix; no further problems'"

// One blocking finding, in the format the review prompt asks a model for.
const fakeBlockingFinding = `severity: blocking
category: correctness
file: FACTORY.md
line: 1
title: FACTORY.md does not record the fix
description: The change is missing a record that the review was addressed.
suggested_fix: Add a file naming what was fixed.`

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
workspacePortable reports whether every repository in the workspace could be
rebuilt elsewhere from the mirror.

Committed work is reproducible; uncommitted work is not. A Run with a dirty
tree must stay on this node until that changes, or resuming it somewhere else
would quietly discard the agent's work.

Errs on the side of caution: if the state cannot be read, the workspace is
treated as non-portable rather than risking the loss.
*/
func (d *daemon) workspacePortable(
	ctx context.Context,
	containerID string,
	repos []workspace.MaterializedRepo,
) bool {
	for _, repo := range repos {
		changes := d.inspectRepo(ctx, containerID, repo)
		if changes == nil {
			continue // unchanged: nothing to lose
		}
		if changes.uncommitted != "" {
			return false
		}
	}
	return true
}
