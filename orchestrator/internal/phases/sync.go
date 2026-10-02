// Package phases runs phase Runs on lux: submit them, follow their output
// into the ledger, and when the agent's turn is done, collect what it
// produced and tell the workflow.
//
// A phase Run's life, from dude's side:
//
//	pending ─submit─▶ scheduled ─lux running─▶ running ─agent busy→idle─▶ finishing
//	   finishing: push (publishing phases) → fast-forward the task branch
//	              → compare for changed paths → findings (review phases)
//	              → stop the lux Run → completed
//
// Pause stops the lux Run (its state is kept); resume resumes it, and the
// agent continues its conversation from the transcript lux kept. Abort
// cancels it.
//
// Every step is idempotent and keyed on durable columns, so the loop can be
// killed at any point and a new orchestrator picks up exactly where the old
// one left off.
package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/images"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

type Syncer struct {
	DB     *db.DB
	Lux    lux.Client
	Forges delivery.Forges
	Agent  AgentConfig
	// The login for the registry agent images come from; nil for none.
	Registry registry.Provider
	Log      *slog.Logger
	// The factory's grace before a Run waiting on a person is parked, and
	// its idle limit, for projects whose policy sets none (DUDE_PARK_AFTER,
	// DUDE_IDLE_AFTER; finer than the policy's minutes, for tests). Zero
	// takes delivery.DefaultPolicy's.
	ParkAfter, IdleAfter time.Duration
	// How long a task's conductor stays running after its turn ends, for
	// projects whose policy sets none (DUDE_CONDUCTOR_WARM; finer than the
	// policy's minutes, for tests). Zero takes delivery.DefaultPolicy's.
	ConductorWarm time.Duration
	// How soon after an edit a Run's live diff is read, how often while its
	// agent works (DUDE_DIFF_EVERY), and how often once several reads found
	// nothing new; zero takes the defaults.
	DiffDelay, DiffEvery, DiffSlow time.Duration
	// The hourly rate of the machine a Run runs on, recorded with each Run
	// when it is submitted (DUDE_MACHINE_USD_PER_HOUR); zero records none.
	MachineUSDPerHour float64
	// For tests: the sweep takes a back-off (next_attempt_at) as due this
	// much earlier, so a retry a minute out is exercised without the wait.
	// Zero outside tests.
	RetryAhead time.Duration
	// For tests: the orchestrator's clock, as its events' occurred_at
	// (ledger.Event.OccurredAt); nil is the ledger's time.Now.
	Now func() time.Time
	// For tests: called as each resume follow-up finishes, its placements
	// recorded and its timing published.
	followedUp func()

	// One follower per live lux Run. The follower is the only writer of a
	// Run's cursor, so two must never run for the same Run.
	mu        sync.Mutex
	following map[string]*follower
	// What each Run's live diff last was (livediff.go).
	diffs map[string]*diffState
}

// follower is one goroutine reading a Run's output. A pointer, so a
// finishing follower can tell whether the entry is still its own.
// poke asks its live diff to be read soon.
type follower struct {
	cancel context.CancelFunc
	poke   chan struct{}
}

// Run statuses (run_status) the syncer decides on.
const (
	statusPending   = "pending"
	statusScheduled = "scheduled"
	statusRunning   = "running"
	statusPaused    = "paused"
	statusAborted   = "aborted"
)

// Why dude stopped a lux Run: "complete", "pause" or "cancel". Recorded so
// a stop dude asked for is not mistaken for the agent dying.
const stopPause = "pause"

// phaseRun is a phase Run's row, as the syncer reads it — or a task's
// conductor's (Phase "", Role conductor), which the syncer drives the same
// way except where conductor() says otherwise.
type phaseRun struct {
	ID, Org, ProjectID, TaskID, Phase, Status, Control string
	Role                                               string
	Category                                           string
	LuxRunID, LuxState, LuxStopReason                  string
	PushRequestID                                      string
	// Where this Run's work is pushed; "" for one that pushes nothing.
	PushBranch string
	// The lux Run holds a repository it may push (runs.lux_pushes).
	HoldsPushable bool
	// Per repository name: the commit this phase was asked to start from,
	// and the one each checkout really started from (lux's git.checkout).
	BaseRefs, BaseSHAs      map[string]string
	TurnDone, HasDirectives bool
	// A directive lux has, sent within unreadGraceSecs, that the agent has
	// not read, or read after its last turn ended (by ledger order: in one
	// transaction the two carry the same now()): a new turn is starting.
	// An interrupt alone starts no turn: the directive carrying its words
	// is the one waited on.
	// Evaluated only where advance reads it (a finished turn of a running
	// Run not yet pushed); false otherwise. Its lookups use the partial
	// indexes of migration 062.
	Unread bool
	// A person approved a repository the lux Run does not have yet.
	RepoApproved bool
	// Why dude paused it itself, and so when it resumes it: "repository",
	// "person" or "idle" (migration 021); "" for a person's own pause.
	DudePause string
	// Something is open for a person: a question, a repository request.
	Waiting bool
	// Its agent has waited on a person past the project's grace period.
	ParkNow bool
	// Paused, and due to be resumed: a person asked, or dude's own reason
	// for pausing it is over (resumable).
	Resumable bool
	// Its agent has been quiet mid-turn for the project's idle limit (since
	// the nudge, if it was nudged).
	Quiet, Nudged bool
	// When its quiet began, as the sweep read it: a pause or nudge acts only
	// if the agent has done nothing since.
	QuietSince                     *time.Time
	PushResult                     json.RawMessage
	PRFeedback                     json.RawMessage
	FindingIDs, BlockingSeverities []string
	Attempt                        int
	// A conductor whose turn ended longer ago than its warm period.
	WarmOver bool
	// The image job a pending Run waits on (runs.image_build_id), "" for none.
	ImageBuildID string
}

// conductor says whether r is its task's conductor: no phase, never
// finished at a turn's end, never published.
func (r phaseRun) conductor() bool { return r.Phase == "" && r.Role == delivery.RoleConductor }

// sweptRuns (SQL, over runs r): the Runs the syncer drives — every phase
// Run, and each task's conductor. Not a branch preview, nor a Run made by
// hand through the API.
const sweptRuns = `(r.phase IS NOT NULL OR (r.role = 'conductor' AND r.kind = 'agent'))`

const runColumns = `r.id, r.organization_id, r.project_id, r.task_id, COALESCE(r.phase::text, ''), r.status::text, r.control::text,
	COALESCE(r.role::text, ''),
	COALESCE(r.category, ''),
	COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''), COALESCE(r.lux_stop_reason, ''),
	COALESCE(r.push_request_id, ''), COALESCE(r.push_branch, ''), cardinality(r.lux_pushes) > 0, r.base_refs, r.base_shas, r.turn_done_at IS NOT NULL,
	EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL),
	CASE WHEN r.turn_done_at IS NOT NULL AND r.status = 'running' AND r.lux_state = 'running' AND r.push_request_id IS NULL
	THEN EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NOT NULL AND d.failed_at IS NULL
	        AND d.interrupt_only IS NOT TRUE
	        AND d.sent_at > now() - make_interval(secs => ` + unreadGraceSecs + `)
	        AND (d.delivered_at IS NULL OR (SELECT max(e.cursor) FROM events e WHERE e.run_id = r.id
	              AND e.event_type = 'run.directive.delivered' AND e.payload->>'directiveId' = d.id)
	            > (SELECT max(e.cursor) FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.session.stopped')))
	ELSE false END,
	EXISTS (SELECT 1 FROM repository_requests q JOIN repositories repo ON repo.id = q.repository_id
	        WHERE q.run_id = r.id AND q.status = 'approved' AND NOT (repo.name = ANY (r.lux_repositories))),
	COALESCE(r.dude_pause, ''), ask.open,
	COALESCE(r.waiting_since < now() - make_interval(secs => lim.park_secs), false) AND ask.open,
	` + resumable + `,
	COALESCE(lim.idle_secs > 0 AND ` + quiet + `, false), r.idle_nudged_at IS NOT NULL, ` + quietSince + `,
	r.push_result, r.pr_feedback, r.finding_ids, r.blocking_severities, r.attempt,
	COALESCE(r.turn_done_at < now() - make_interval(secs => lim.warm_secs), false), COALESCE(r.image_build_id, '')`

// runFrom is what runColumns reads from: the Run, whether it has anything
// open for a person (ask.open, delivery.OpenAsk: asked once per row, read
// in several places), and how long its project
// lets an agent wait on a person (park_secs) or stay quiet (idle_secs, 0 for
// never), and a conductor stay warm after its turn (warm_secs) — the
// project's delivery policy, over its organization's, over the
// factory's defaults.
// Its parameters are Syncer.limits.
const runFrom = `runs r JOIN projects p ON p.id = r.project_id JOIN organizations o ON o.id = r.organization_id
	CROSS JOIN LATERAL (SELECT
		COALESCE((p.delivery_policy->>'parkAfterMinutes')::float8 * 60, (o.delivery_policy->>'parkAfterMinutes')::float8 * 60,
			$1::float8) AS park_secs,
		COALESCE((p.delivery_policy->>'idleNudgeMinutes')::float8 * 60, (o.delivery_policy->>'idleNudgeMinutes')::float8 * 60,
			$2::float8) AS idle_secs,
		COALESCE((p.delivery_policy->>'conductorWarmMinutes')::float8 * 60, (o.delivery_policy->>'conductorWarmMinutes')::float8 * 60,
			$3::float8) AS warm_secs) lim
	CROSS JOIN LATERAL (SELECT ` + delivery.OpenAsk + ` AS open) ask`

// quiet (SQL, over runFrom): the agent is mid-turn, running no tool, waiting
// on nobody, and has done nothing for the idle limit — counted from the
// nudge once it has had one.
const quiet = `(r.status = 'running' AND r.lux_state = 'running' AND r.agent_busy_at IS NOT NULL
	AND r.turn_done_at IS NULL AND r.waiting_since IS NULL AND cardinality(r.open_tool_calls) = 0
	AND ` + quietSince + ` < now() - make_interval(secs => lim.idle_secs))`

// quietSince (SQL): when the agent last did anything, or was nudged.
const quietSince = `GREATEST(COALESCE(r.agent_active_at, r.agent_busy_at), r.idle_nudged_at)`

// unreadGraceSecs (SQL): how long a sent directive the agent has not read
// holds a finished turn open. lux answers in well under a second; this
// bounds a receipt that never comes, so the Run is still collected.
const unreadGraceSecs = `120`

// resumable (SQL): a paused Run that is due to be resumed. A person asked;
// or dude paused it itself and its reason is over — a repository to bring
// (at once), a person it waited on (once nothing is open for them), a
// conductor parked after its warm period (once someone wrote to it). An idle
// park, like a person's own pause, waits for a person's Resume.
const resumable = `(r.status = 'paused' AND (r.control = 'resume' OR r.dude_pause = 'repository'
	OR (r.dude_pause = 'person' AND NOT ask.open)
	OR (r.dude_pause = 'conductor' AND ` + unsentDirective + `)))`

// unsentDirective (SQL, over runs r): something is queued for the agent
// that lux does not have yet.
const unsentDirective = `EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL)`

// limits are runFrom's parameters: the factory's grace before parking, idle
// limit and conductor's warm period, in seconds, for projects that set none
// — the syncer's own if it has them, else delivery.DefaultPolicy's.
func (s *Syncer) limits() []any {
	d := delivery.DefaultPolicy()
	park, idle, warm := s.ParkAfter, s.IdleAfter, s.ConductorWarm
	if park == 0 {
		park = time.Duration(d.ParkAfterMinutes) * time.Minute
	}
	if idle == 0 {
		idle = time.Duration(d.IdleNudgeMinutes) * time.Minute
	}
	if warm == 0 {
		warm = time.Duration(d.ConductorWarmMinutes) * time.Minute
	}
	return []any{park.Seconds(), idle.Seconds(), warm.Seconds()}
}

func scan(row pgx.Row) (phaseRun, error) {
	var r phaseRun
	err := row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.TaskID, &r.Phase, &r.Status, &r.Control, &r.Role,
		&r.Category, &r.LuxRunID, &r.LuxState, &r.LuxStopReason,
		&r.PushRequestID, &r.PushBranch, &r.HoldsPushable, &r.BaseRefs, &r.BaseSHAs, &r.TurnDone, &r.HasDirectives, &r.Unread, &r.RepoApproved,
		&r.DudePause, &r.Waiting, &r.ParkNow, &r.Resumable, &r.Quiet, &r.Nudged, &r.QuietSince,
		&r.PushResult, &r.PRFeedback, &r.FindingIDs, &r.BlockingSeverities, &r.Attempt, &r.WarmOver, &r.ImageBuildID)
	return r, err
}

// Sweep takes one pass over every phase Run dude still has something to do
// for, advancing each as far as it can. Cross-tenant, because finding the
// Runs that need attention is the job; each is then handled in its own
// organization's scope.
func (s *Syncer) Sweep(ctx context.Context) (int, error) {
	var runs []phaseRun
	err := s.DB.InSystem(ctx, "phase-sync", func(tx pgx.Tx) error {
		// Every live Run, not the oldest N: a Run with nothing to do still
		// needs its stream followed, and a paused or idle Run must not
		// crowd out a newer one that is waiting to be submitted. Runs with
		// something to do sort first.
		rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM `+runFrom+`
			WHERE `+sweptRuns+`
			  AND (r.status IN ('pending', 'scheduled', 'starting', 'running')
			       OR `+resumable+`
			       -- Aborted in dude but not yet cancelled in lux.
			       OR (r.status IN ('aborted', 'failed') AND r.lux_run_id IS NOT NULL AND r.lux_stop_reason IS DISTINCT FROM 'cancel'))
			  -- An abort does not wait out the back-off of the step it ends.
			  AND (r.status = 'aborted' OR r.next_attempt_at IS NULL
			       OR r.next_attempt_at <= now() + make_interval(secs => $4::float8))
			ORDER BY (r.status = 'pending' OR r.control <> 'none' OR r.turn_done_at IS NOT NULL) DESC, r.created_at
			LIMIT 1000`, append(s.limits(), s.RetryAhead.Seconds())...)
		if err != nil {
			return err
		}
		runs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (phaseRun, error) { return scan(row) })
		return err
	})
	if err != nil {
		return 0, err
	}
	// Concurrently, a few at a time: one slow call to lux or GitHub must not
	// hold up submitting, following and steering every other Run.
	var handled atomic.Int64
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, r := range runs {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			acted, err := s.advance(ctx, r)
			if err != nil && !errors.Is(err, errRetry) {
				s.Log.Warn("phase run sync failed", "run", r.ID, "error", err)
				return
			}
			if acted {
				handled.Add(1)
			}
		}()
	}
	wg.Wait()
	return int(handled.Load()), nil
}

// advance moves one Run on by whatever its state calls for. Returns whether
// it did anything, so the sweeper keeps going while there is work.
func (s *Syncer) advance(ctx context.Context, r phaseRun) (bool, error) {
	switch {
	case r.Status == statusAborted || r.Status == "failed":
		return true, s.cancel(ctx, r)
	case r.Status == statusPending && r.LuxRunID == "":
		return true, s.submit(ctx, r)
	case r.Status == statusPaused:
		return s.whilePaused(ctx, r)
	case r.Control == "abort":
		return true, s.cancel(ctx, r)
	case r.Control == "pause_hard" || r.Control == "pause_graceful":
		return true, s.pause(ctx, r)
	}
	// Following comes first: a finishing Run still needs its stream, because
	// that is where the push result arrives — including after a restart —
	// and a resumed one is where lux reports the repositories it added.
	s.follow(r)
	if r.Status == statusRunning && r.LuxState == "running" && r.RepoApproved && !r.TurnDone && r.PushRequestID == "" {
		// A person approved a repository for the agent while it works: pause,
		// so the resume can bring it (lux adds repositories only at a
		// resume). An agent that has finished its turn gets it in the next
		// phase instead — the task names it now.
		return true, s.requestPause(ctx, r, "repository", "a repository was approved")
	}
	if r.Status == statusRunning && r.LuxState == "running" && r.Control == "none" {
		switch {
		case r.ParkNow:
			// Waiting on a person who has not answered in the grace period:
			// stop the container, keeping the agent's conversation, and
			// resume it when they do — the next minute or the next week.
			return true, s.requestPause(ctx, r, "person", "parked while it waits for a person")
		case r.Quiet && r.Nudged:
			// Quiet again as long after its nudge: for a person to look at.
			return true, s.requestPause(ctx, r, "idle", "parked: the agent went quiet and did not answer a nudge")
		case r.Quiet:
			return true, s.nudge(ctx, r)
		}
	}
	if r.TurnDone && r.conductor() {
		return s.betweenTurns(ctx, r)
	}
	if r.TurnDone {
		// A steer that reached dude as the turn ended holds the Run open:
		// sent, the agent takes it as its next turn, whose end is the one
		// collected. Until that turn's "busy" arrives the Run waits (Unread),
		// bounded by unreadGraceSecs. Once the push is asked for, the work
		// is what it is: a steer later still is marked not delivered when
		// the Run completes, not heard after the work was taken.
		if r.PushRequestID == "" && r.Status == statusRunning && r.LuxState == "running" {
			if r.HasDirectives {
				return s.deliverDirectives(ctx, r)
			}
			if r.Unread {
				return false, nil
			}
		}
		return s.finish(ctx, r)
	}
	if !r.HasDirectives {
		return false, nil
	}
	return s.deliverDirectives(ctx, r)
}

// betweenTurns is a conductor whose turn has ended. It is never finished:
// a person's next message is its next turn. It stays running for its warm
// period, then is parked (dude_pause 'conductor') until someone writes.
// One whose container stopped on its own ended there, and a new message
// gets a new conductor.
func (s *Syncer) betweenTurns(ctx context.Context, r phaseRun) (bool, error) {
	if lux.Terminal(r.LuxState) && r.LuxStopReason == "" && r.Control == "none" {
		return true, s.endConductor(ctx, r, "its container stopped")
	}
	if r.Status != statusRunning || r.LuxState != "running" {
		return false, nil
	}
	switch {
	case r.HasDirectives:
		return s.deliverDirectives(ctx, r)
	case r.Unread:
		return false, nil
	case r.WarmOver && r.Control == "none":
		return true, s.requestPause(ctx, r, "conductor", "parked after its warm period")
	}
	return false, nil
}

// endConductor completes a conductor that can no longer be resumed.
func (s *Syncer) endConductor(ctx context.Context, r phaseRun, why string) error {
	s.unfollow(r.ID)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'completed', ended_at = now(), lux_stop_reason = 'complete'
			WHERE id = $1 AND status IN ('scheduled', 'starting', 'running')`, r.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, r, "run.completed", ledger.ActorSystem, map[string]any{"status": "completed", "reason": why})
	})
}

// submit builds the Run's spec and hands it to lux. A Run whose image is
// a library image waits, still pending, until its image has the current
// dude layer, and fails before lux if it cannot have it.
func (s *Syncer) submit(ctx context.Context, r phaseRun) error {
	image, got, err := s.image(ctx, r)
	var waiting images.Waiting
	var refused images.Refused
	switch {
	case errors.As(err, &waiting):
		return s.waitForImage(ctx, r, waiting)
	case errors.As(err, &refused):
		return s.fail(ctx, r, "cannot start: "+refused.Reason)
	case err != nil:
		return s.retryLater(ctx, r, err)
	}
	spec, machine, err := s.spec(ctx, r, nil, image)
	if passing(err) || forge.Transient(err) {
		return s.retryLater(ctx, r, err)
	}
	var noModel errNoModel
	if errors.As(err, &noModel) {
		return s.fail(ctx, r, string(noModel))
	}
	if err != nil {
		return s.fail(ctx, r, "cannot build the run: "+err.Error())
	}
	// The dude Run id is the idempotency key: a submit that timed out and is
	// retried returns the lux Run the first one created.
	NamePool(ctx, s.Lux, machine)
	lr, err := s.Lux.Submit(ctx, spec, r.ID)
	if reason := PoolGone(err, machine); reason != "" {
		return s.fail(ctx, r, reason)
	}
	if err != nil {
		return s.retryOrFail(ctx, r, err, "lux refused the run")
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Guarded on still being pending: an abort that raced the submit wins,
		// and the sweep then cancels the lux Run it made.
		var pushBranch string
		var repos, pushes []string
		if spec.Git != nil {
			if spec.Git.Push != nil {
				pushBranch = spec.Git.Push.Branch
			}
			for _, repo := range spec.Git.Repositories {
				repos = append(repos, repo.Name)
				if repo.Push == nil || *repo.Push {
					pushes = append(pushes, repo.Name)
				}
			}
		}
		// machine, model_tier and image: what it runs on, the tier it
		// requested and what it runs in, recorded with the lux Run it runs
		// as, so a later edit or removal of the size or tier, or a new
		// version of the image, leaves this Run's record — and its resumes —
		// alone.
		tag, err := tx.Exec(ctx, `UPDATE runs SET lux_run_id = $2, lux_state = $3, next_attempt_at = NULL,
			harness = $4, model = $5, push_branch = NULLIF($6, ''), lux_repositories = $7, lux_pushes = $8,
			machine_usd_per_hour = COALESCE(machine_usd_per_hour, NULLIF($9::float8, 0)),
			machine = $10::jsonb, model_tier = NULLIF($11, ''), image = $12::jsonb, image_waiting_since = NULL,
			status = CASE WHEN status = 'pending' THEN 'scheduled'::run_status ELSE status END
			WHERE id = $1`, r.ID, lr.ID, lr.State, spec.Labels["dude.harness"], spec.Labels["dude.model"], pushBranch,
			db.NonNil(repos), db.NonNil(pushes), s.MachineUSDPerHour, machine, spec.Labels["dude.model_tier"], got)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, r, "run.lease.acquired", ledger.ActorSystem, map[string]any{"luxRunId": lr.ID})
	})
}

// image is the image a Run starts in: the first library image its role,
// its project or its organization names, finished with the current dude
// layer (images.Choose: Waiting while it is not, Refused when it cannot
// be), else the project's typed image, else DUDE_AGENT_IMAGE.
func (s *Syncer) image(ctx context.Context, r phaseRun) (string, *images.RunImage, error) {
	var ref string
	var got *images.RunImage
	var outcome error
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var site images.Site
		var projectModels, orgModels json.RawMessage
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(p.runtime_image_id, ''), COALESCE(p.runtime_image, ''), COALESCE(o.default_image_id, ''),
				p.agent_models, o.default_agent_models
			FROM projects p JOIN organizations o ON o.id = p.organization_id WHERE p.id = $1`, r.ProjectID).
			Scan(&site.RuntimeID, &site.RuntimeTyped, &site.DefaultID, &projectModels, &orgModels); err != nil {
			return fmt.Errorf("load the project's image: %w", err)
		}
		known, err := images.Known(ctx, tx)
		if err != nil {
			return err
		}
		site.Role = images.RoleImage(delivery.PromptRoleFor(r.Phase, r.Role), projectModels, orgModels, known)
		site.Fallback = s.Agent.DefaultImage
		ref, got, err = images.Choose(ctx, tx, site, s.Agent.Layer, r.ID, r.ImageBuildID)
		err, outcome = images.Settle(err)
		return err
	})
	if err == nil {
		err = outcome
	}
	return ref, got, err
}

// ImagePoll is how soon a Run (or a preview) waiting for its image looks
// again. The builder takes a Run's finish first; one takes about a minute.
const ImagePoll = 5 * time.Second

// waitForImage leaves a Run pending while the builder prepares its image,
// saying so in its chat the first time.
func (s *Syncer) waitForImage(ctx context.Context, r phaseRun, w images.Waiting) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + make_interval(secs => $2) WHERE id = $1`,
			r.ID, ImagePoll.Seconds()); err != nil {
			return err
		}
		if !w.New {
			return nil
		}
		return s.event(ctx, tx, r, EvImagePreparing, ledger.ActorSystem, map[string]any{"buildId": w.Build})
	})
}

// EvImagePreparing: a Run waits for its image's dude layer (or its first
// version) before it goes to lux. Payload: {buildId}.
const EvImagePreparing = "run.image_preparing"

// spec gathers what the Run's lux spec is built from. It is the only
// source of what lux starts a Run with — its submit, and every resume's
// secrets — so each start carries fresh credentials: the forge token,
// the tools token, the registry login.
//
// stored is lux's copy of the Run's spec, for a resume (resumeInput): the
// image and its registry login are the ones it names, whatever the
// project names now. Nil for a submit, which passes the image it chose
// (Syncer.image).
//
// Also returns the size the Run's role resolves to now (nil: the
// organization has none); submit records it, a resume does not.
func (s *Syncer) spec(ctx context.Context, r phaseRun, stored *lux.StoredSpec, image string) (lux.Spec, *delivery.Machine, error) {
	var in specInput
	var title, goal string
	var repos []delivery.Repository
	var decisions []delivery.Decision
	var criteria, projectModels, orgModels json.RawMessage
	var findings []delivery.Finding
	var feedback []forge.ActionableFeedback
	var prompts delivery.Prompts
	var sizes delivery.Sizes
	var briefing string
	var tier delivery.Tier
	var noTier string
	settingsRole := delivery.PromptRoleFor(r.Phase, r.Role)
	promptRole := settingsRole
	var settings delivery.RoleSettings
	_ = json.Unmarshal(r.PRFeedback, &feedback)
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT w.title, w.goal, w.acceptance_criteria, p.agent_models, o.default_agent_models,
				COALESCE((SELECT prompt FROM runs WHERE id = $2), '')
			FROM tasks w JOIN projects p ON p.id = w.project_id JOIN organizations o ON o.id = p.organization_id
			WHERE w.id = $1`, r.TaskID, r.ID).
			Scan(&title, &goal, &criteria, &projectModels, &orgModels, &briefing); err != nil {
			return fmt.Errorf("load task: %w", err)
		}
		var err error
		settings = delivery.ResolveRole(settingsRole, projectModels, orgModels)
		if stored != nil {
			// A resume goes on with what the Run was submitted with, whatever
			// its tier says now: lux keeps the spec's env, model and all.
			if err := tx.QueryRow(ctx, `SELECT COALESCE(model, ''), COALESCE(model_tier, '') FROM runs WHERE id = $1`, r.ID).
				Scan(&tier.Model, &tier.Name); err != nil {
				return fmt.Errorf("load run model: %w", err)
			}
		} else if tier, noTier, err = delivery.TierFor(ctx, tx, settingsRole, settings); err != nil || noTier != "" {
			return err
		}
		if sizes, err = delivery.LoadSizes(ctx, tx); err != nil {
			return err
		}
		if prompts, err = delivery.LoadPrompts(ctx, tx, r.ID, r.ProjectID, promptRole); err != nil {
			return err
		}
		if repos, err = delivery.TaskRepositories(ctx, tx, r.TaskID); err != nil {
			return fmt.Errorf("load repositories: %w", err)
		}
		// What people decided on this task, across its Runs.
		rows, err := tx.Query(ctx, `SELECT prompt, answer FROM questions
			WHERE task_id = $1 AND status = 'answered' AND answer IS NOT NULL ORDER BY answered_at`, r.TaskID)
		if err != nil {
			return err
		}
		if decisions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[delivery.Decision]); err != nil {
			return err
		}
		// The findings the workflow chose for this Run, exactly and in its
		// order: those a fix addresses (a fix for pull request feedback has
		// none, and must not take on findings a person chose to leave), or
		// those a re-review judges — which it answers by position.
		if len(r.FindingIDs) > 0 {
			rows, err := tx.Query(ctx, `SELECT severity::text, category, title, description, suggested_fix,
				COALESCE(repo, ''), COALESCE(file, ''), COALESCE(line, 0)
				FROM review_findings f JOIN unnest($1::text[]) WITH ORDINALITY AS chosen(id, n) ON chosen.id = f.id
				ORDER BY chosen.n`,
				r.FindingIDs)
			if err != nil {
				return err
			}
			findings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[delivery.Finding])
			return err
		}
		return nil
	})
	if err != nil {
		return lux.Spec{}, nil, err
	}
	if noTier != "" {
		return lux.Spec{}, nil, errNoModel(noTier)
	}
	role := delivery.RoleForPhase[r.Phase]
	if r.conductor() {
		role = delivery.RoleConductor
	}

	var ac []string
	_ = json.Unmarshal(criteria, &ac)

	var promptRepos []delivery.PromptRepo
	for _, repo := range repos {
		ref := repo.DefaultBranch
		if base := r.BaseRefs[repo.Name]; base != "" {
			ref = base
		}
		readOnly := repo.Access == "read" || r.conductor()
		in.Repos = append(in.Repos, specRepo{Name: repo.Name, URL: repo.URL, Ref: ref, ReadOnly: readOnly})
		promptRepos = append(promptRepos, delivery.PromptRepo{Name: repo.Name, Path: RepoPath(repo.Name), ReadOnly: readOnly})
	}
	in.RunID, in.OrganizationID, in.TaskID, in.Phase, in.Role = r.ID, r.Org, r.TaskID, r.Phase, role
	in.Model, in.ModelTier, in.Effort, in.TimeLimitMinutes = tier.Model, tier.Name, settings.Effort, settings.TimeLimitMinutes
	if m, ok := sizes.ForRole(settingsRole, projectModels, orgModels); ok {
		in.Machine = &m
	}
	in.Image = image
	if stored != nil {
		in.Image = stored.Image.Ref
	}
	if in.Registry, err = LoginFor(ctx, s.Registry, in.Image, stored); err != nil {
		return lux.Spec{}, nil, err
	}
	promptIn := delivery.PromptInput{
		Title: title, Goal: goal, AcceptanceCriteria: ac, Category: r.Category,
		Findings: findings, PRFeedback: feedback, BlockingSeverities: r.BlockingSeverities, Context: settings.Context,
		Repositories: promptRepos, Decisions: decisions, Tools: s.Agent.ToolsURL != "", CLI: s.Agent.ToolsURL != "" && s.Agent.ToolsService,
		OrgPrompt: prompts.Org, ProjectPrompt: prompts.Project, ProjectPromptMode: prompts.ProjectMode,
		Branch: runBranch(r),
	}
	// What the branch started from: the first repository's, which is where
	// the task starts (a prompt names one base; several repositories each
	// have theirs, in the workspace note).
	if len(in.Repos) > 0 {
		promptIn.BaseRef = in.Repos[0].Ref
	}
	in.Prompt = delivery.Prompt(r.Phase, promptIn)
	if r.conductor() {
		in.Prompt = delivery.ConductorPrompt(briefing, promptIn)
	}
	// Pushed only by a phase that publishes — never a conductor, which has
	// no phase. Even with nowhere to change
	// yet: a repository a person lets it change mid-Run arrives at a
	// resume, and lux pushes only to the branch the spec named at submit.
	if delivery.Publishes[r.Phase] {
		in.PushBranch = runBranch(r)
	}
	// A forge that cannot be read now is asked again, not left out: a spec
	// without its token would clone nothing, and a resume without it is
	// refused by lux for good.
	gh, err := s.Forges.For(ctx, r.Org)
	if err != nil {
		return lux.Spec{}, nil, errForge{err}
	}
	if gh != nil {
		if in.ForgeToken, err = gh.Token(); err != nil {
			return lux.Spec{}, nil, errForge{err}
		}
		if delivery.Publishes[r.Phase] {
			for _, repo := range repos {
				if repo.Access == "read" {
					continue
				}
				if err := gh.CheckPushAccess(ctx, repo.URL); err != nil {
					return lux.Spec{}, nil, fmt.Errorf("push preflight for %s: %w", repo.Name, err)
				}
			}
		}
	}
	if s.Agent.ToolsURL != "" {
		// The token for this start of the Run: the same however often the
		// submit or resume is retried (lux keeps the first attempt's
		// secrets), new for the next start. Storing its hash retires the
		// last start's token.
		var start int
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT tool_starts FROM runs WHERE id = $1`, r.ID).Scan(&start)
		}); err != nil {
			return lux.Spec{}, nil, err
		}
		token, hash := agenttools.RunToken(s.Agent.ToolsKey, r.ID, start)
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET mcp_token_hash = $2 WHERE id = $1`, r.ID, hash)
			return err
		}); err != nil {
			return lux.Spec{}, nil, err
		}
		in.ToolsToken = token
	}
	return buildSpec(s.Agent, in), in.Machine, nil
}

// runBranch is where one phase Run's commits are pushed. Every Run has its
// own, because lux allows a Run's first push only to a branch that does not
// exist yet; dude then fast-forwards the task's branch to it.
func runBranch(r phaseRun) string {
	return fmt.Sprintf("dude/%s/run-%s", r.TaskID, r.ID)
}

// follow starts reading a Run's lux output, if nothing is reading it yet.
func (s *Syncer) follow(r phaseRun) {
	if r.LuxRunID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.following == nil {
		s.following = map[string]*follower{}
	}
	if _, ok := s.following[r.ID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	mine := &follower{cancel: cancel, poke: make(chan struct{}, 1)}
	s.following[r.ID] = mine
	// The live diff is read for as long as the Run's output is followed.
	delay, every, slow := s.diffTimings()
	go watchDiff(ctx, mine.poke, delay, every, slow, func(ctx context.Context, periodic bool) bool {
		changed, err := s.readDiff(ctx, r, periodic)
		if err != nil && ctx.Err() == nil {
			s.Log.Debug("reading the live diff failed", "run", r.ID, "error", err)
		}
		return changed
	})
	go func() {
		defer func() {
			s.mu.Lock()
			// Only its own entry: a newer follower may have taken the slot
			// after this one was told to stop.
			if s.following[r.ID] == mine {
				delete(s.following, r.ID)
				delete(s.diffs, r.ID)
			}
			s.mu.Unlock()
			cancel()
		}()
		err := s.followOutput(ctx, r)
		if err == nil || ctx.Err() != nil {
			return
		}
		// A Run lux lost fails here, or nothing would ever finish it: no
		// output will arrive, and following again would spin forever.
		if lux.IsNotFound(err) {
			if err := s.retryLater(context.Background(), r, err); err != nil {
				s.Log.Warn("failing a Run lux lost", "run", r.ID, "error", err)
			}
			return
		}
		s.Log.Warn("following lux output stopped", "run", r.ID, "error", err)
	}()
}

// Stop ends every follower; used on shutdown.
func (s *Syncer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.following {
		f.cancel()
	}
}

// followOutput reads the lux Run's output from where it was last left and
// records it.
//
// Frames are applied in batches — whatever has arrived, up to a few hundred
// at a time — each batch in one transaction with the cursor that moves past
// it. An agent streams thousands of frames a turn, most of them fragments of
// words, and a transaction per frame would make the database the busiest
// thing in the system. Committing effects and cursor together is what lets a
// restart neither repeat nor skip anything.
func (s *Syncer) followOutput(ctx context.Context, r phaseRun) error {
	var cursor string
	var afterEvent int64
	t := &translator{run: r}
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(lux_cursor, ''), lux_after_event FROM runs WHERE id = $1`, r.ID).
			Scan(&cursor, &afterEvent); err != nil {
			return err
		}
		return t.load(ctx, tx)
	}); err != nil {
		return err
	}
	s.timeResumesLater(r)

	frames := make(chan lux.Frame, 256)
	read := make(chan error, 1)
	go func() {
		read <- s.Lux.Output(ctx, r.LuxRunID, cursor, afterEvent, func(f lux.Frame) error {
			select {
			case frames <- f:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		close(frames)
	}()

	for f := range frames {
		batch := []lux.Frame{f}
	more:
		for len(batch) < cap(frames) {
			select {
			case next, ok := <-frames:
				if !ok {
					break more
				}
				batch = append(batch, next)
			default:
				break more
			}
		}
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			var cursor string
			var afterEvent int64
			for _, f := range batch {
				if err := t.apply(ctx, tx, s, f); err != nil {
					return err
				}
				switch f.Kind {
				case "record":
					cursor = f.Cursor
				case "lux":
					afterEvent = max(afterEvent, f.EventID)
				}
			}
			return t.save(ctx, tx, cursor, afterEvent)
		}); err != nil {
			// What was read but not recorded is read again from the saved
			// cursor by the next follower.
			t.resumes = nil
			return err
		}
		if t.resumes != nil {
			go s.resumeFollowUp(r, t.resumes)
			t.resumes = nil
		}
	}
	return <-read
}

// finish collects what a phase produced once its agent finished its turn.
// Idempotent end to end: each piece is recorded before the next is asked
// for, so a restart resumes the sequence.
func (s *Syncer) finish(ctx context.Context, r phaseRun) (bool, error) {
	// Pushed when it names a branch and holds a repository it may push —
	// as lux was told, at submit or as a resume added one. Work on nothing
	// it may change publishes only what it wrote for people.
	pushes := r.PushBranch != "" && r.HoldsPushable
	if pushes && r.PushResult == nil {
		// Only a state lux has already reported as over rules the push out.
		// Anything else is asked of lux itself: its lifecycle events trail
		// the agent's own records by up to a second, so the state recorded
		// here can still say "scheduled" when the agent has already finished.
		if lux.Terminal(r.LuxState) {
			return true, s.fail(ctx, r, "the agent's container stopped before its work was pushed")
		}
		if r.LuxState == "resuming" {
			// lux is moving it to another host (lux.Recorded): pushed once
			// it runs there, its checkout with it.
			return false, nil
		}
		if r.PushRequestID != "" {
			// Asked; the git.push event will arrive on the stream. Nothing to
			// do meanwhile, so the loop may rest.
			return false, nil
		}
		reqID := "push-" + r.ID
		if err := s.Lux.Push(ctx, r.LuxRunID, reqID); err != nil {
			if le, ok := lux.AsError(err); ok && le.Code == "not_running" {
				return true, s.fail(ctx, r, "the agent's container stopped before its work was pushed: "+le.Message)
			}
			return true, s.retryLater(ctx, r, err)
		}
		return true, s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET push_request_id = $2 WHERE id = $1`, r.ID, reqID)
			return err
		})
	}

	heads := map[string]delivery.RunHead{}
	if pushes {
		var err error
		if heads, err = s.publish(ctx, r); err != nil {
			// A forge that is down or rate-limiting will answer later; a
			// refusal (not a fast-forward, a bad push) will not.
			if forge.Transient(err) {
				return true, s.retryLater(ctx, r, err)
			}
			return true, s.fail(ctx, r, err.Error())
		}
	}
	if r.Phase == delivery.PhaseReview || r.Phase == delivery.PhaseTest {
		if err := s.reportFindings(ctx, r); err != nil {
			return true, err
		}
	}

	// Stopped rather than cancelled: the workspace and the agent's session
	// are kept, so a person can still look at or resume a finished phase.
	if err := s.ask(ctx, r, s.Lux.Stop); err != nil {
		return true, err
	}
	s.unfollow(r.ID)
	raw, _ := json.Marshal(heads)
	branch := ""
	if len(heads) > 0 {
		branch = delivery.BranchFor(r.TaskID, r.Attempt)
	}
	completed := false
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'completed', ended_at = now(), lux_stop_reason = $2,
			heads = $3::jsonb, branch = COALESCE(NULLIF($4, ''), branch)
			WHERE id = $1 AND status IN ('scheduled', 'starting', 'running')`,
			r.ID, "complete", raw, branch)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		completed = true
		// What the agent never read, it never will: said so, not left queued.
		rows, err := tx.Query(ctx, `UPDATE directives SET failed_at = now(), error = $2
			WHERE run_id = $1 AND delivered_at IS NULL AND failed_at IS NULL RETURNING id`,
			r.ID, "the run finished before the agent read it")
		if err != nil {
			return err
		}
		late, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range late {
			if err := s.event(ctx, tx, r, evDirectiveFailed, ledger.ActorSystem,
				map[string]any{"directiveId": id, "error": "the run finished before the agent read it"}); err != nil {
				return err
			}
		}
		return s.event(ctx, tx, r, "run.completed", ledger.ActorSystem, map[string]any{"status": "completed"})
	}); err != nil {
		return true, err
	}
	if completed {
		// Ended: no follower will time a resume whose first output is in.
		s.timeResumesLater(r)
	}
	return true, nil
}

// publish moves the task's branch in each repository this Run changed
// to what it pushed, and works out what changed there. Returns runs.heads:
// per repository changed, its new commit and changed paths.
//
// A fast-forward, never a force: if someone else moved a branch the update
// is refused and the phase fails loudly rather than overwriting their work.
// Repositories that did not move are left alone — no branch, no PR — and a
// read-only one is never pushed at all (lux reports it "skipped").
func (s *Syncer) publish(ctx context.Context, r phaseRun) (map[string]delivery.RunHead, error) {
	if r.conductor() {
		// Read-only: its spec names no push branch, and nothing it holds
		// may move the task's branch.
		return nil, fmt.Errorf("a conductor does not publish")
	}
	var push struct {
		Results []struct {
			Repo, Branch, Commit, Status, Error string
		} `json:"results"`
	}
	if err := json.Unmarshal(r.PushResult, &push); err != nil {
		return nil, fmt.Errorf("lux's push result is unreadable: %w", err)
	}
	var repos []delivery.Repository
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		repos, err = delivery.TaskRepositories(ctx, tx, r.TaskID)
		return err
	}); err != nil {
		return nil, err
	}
	byName := map[string]delivery.Repository{}
	for _, repo := range repos {
		byName[repo.Name] = repo
	}

	heads := map[string]delivery.RunHead{}
	var gh *forge.GitHub
	branch := delivery.BranchFor(r.TaskID, r.Attempt)
	for _, res := range push.Results {
		repo, known := byName[res.Repo]
		switch {
		case res.Status == "skipped" || known && repo.Access == "read":
			continue // cloned for reading; nothing to publish
		case !known:
			return nil, fmt.Errorf("lux pushed %s, which this task does not name", res.Repo)
		case res.Status != "pushed" && res.Status != "up-to-date":
			guidance := ""
			message := strings.ToLower(res.Error)
			if strings.Contains(message, "refusing to allow a personal access token to create or update workflow") && strings.Contains(message, "scope") {
				guidance = "; workflow-file pushes require Workflows: Read and write on a fine-grained PAT, or the workflow scope on a classic PAT; ordinary push preflight does not establish this permission"
			}
			return nil, fmt.Errorf("push %s %s: %s%s", res.Repo, res.Status, res.Error, guidance)
		}
		if res.Commit == "" {
			continue // nothing committed there
		}
		// What the checkout started from, as lux reported it: what the
		// change is measured against.
		base := r.BaseSHAs[res.Repo]
		if base == "" {
			return nil, fmt.Errorf("lux never reported where %s's checkout started", res.Repo)
		}
		if res.Commit == base {
			continue // nothing committed there
		}
		if gh == nil {
			var err error
			if gh, err = s.Forges.For(ctx, r.Org); err != nil {
				return nil, err
			}
			if gh == nil {
				return nil, fmt.Errorf("no forge credential to publish with")
			}
		}
		slug := forge.SlugFromURL(repo.URL)
		if slug == "" {
			return nil, fmt.Errorf("no forge to publish %s to", repo.URL)
		}
		if err := gh.FastForward(ctx, slug, branch, res.Commit); err != nil {
			return nil, fmt.Errorf("move %s in %s to %s: %w", branch, res.Repo, short(res.Commit), err)
		}
		// Best effort: a leftover per-Run branch is clutter, not a fault.
		_ = gh.DeleteBranch(ctx, slug, res.Branch)
		changed, err := gh.ChangedFiles(ctx, slug, base, res.Commit)
		if err != nil {
			return nil, fmt.Errorf("compare %s %s...%s: %w", res.Repo, short(base), short(res.Commit), err)
		}
		heads[res.Repo] = delivery.RunHead{SHA: res.Commit, ChangedPaths: db.NonNil(changed)}
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			// A pull request open on this branch now has this head. What is
			// on record about it — its checks, and when its head was first
			// seen — was about the one before: its CI is yet to run on this.
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = $3, head_seen_at = now(),
				checks = CASE WHEN had_ci THEN 'pending' ELSE 'unknown' END::check_state, updated_at = now()
				WHERE task_id = $1 AND repository_id = $2 AND head_branch = $4 AND state IN ('open', 'draft')
				  AND head_sha IS DISTINCT FROM $3`, r.TaskID, repo.ID, res.Commit, branch); err != nil {
				return err
			}
			return s.event(ctx, tx, r, delivery.EvGitCommitCreated, ledger.ActorAgent, map[string]any{
				"repo": res.Repo, "baseSha": base, "headSha": res.Commit, "branch": branch, "changedPaths": changed})
		}); err != nil {
			return nil, err
		}
	}
	return heads, nil
}

// reportFindings parses what a reviewer said and records it as findings.
// Replaces what an earlier report of the same Run recorded, so a retry does
// not double a review's findings.
func (s *Syncer) reportFindings(ctx context.Context, r phaseRun) error {
	var reply strings.Builder
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload->>'text' FROM events
			WHERE run_id = $1 AND event_type = 'agent.message' ORDER BY cursor`, r.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				return err
			}
			// Messages end where the agent stopped to call a tool; a finding
			// starting the next one must still begin on a line of its own.
			reply.WriteString(text)
			reply.WriteString("\n")
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	return RecordFindings(ctx, s.DB, r.Org, r.ID, delivery.ParseFindings(reply.String()),
		judged(r.FindingIDs, delivery.ParseVerdicts(reply.String())))
}

// judged maps a re-review's verdicts, given by position, to the findings it
// was shown.
func judged(shown []string, verdicts map[int]bool) map[string]bool {
	out := map[string]bool{}
	for i, fixed := range verdicts {
		if i < len(shown) {
			out[shown[i]] = fixed
		}
	}
	return out
}

// cancel ends the lux Run of a failed or aborted Run, including resumable stopped and lost Runs.
func (s *Syncer) cancel(ctx context.Context, r phaseRun) error {
	s.unfollow(r.ID)
	if r.LuxRunID != "" && r.LuxState != "cancelled" && r.LuxState != "succeeded" && r.LuxState != "failed" {
		if err := s.askControl(ctx, r, s.Lux.Cancel); err != nil {
			return err
		}
	}
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = 'cancel', control = 'none' WHERE id = $1`, r.ID)
		return err
	}); err != nil {
		return err
	}
	// Ended: no follower will time a resume whose first output is in.
	s.timeResumesLater(r)
	return nil
}

// pause stops the lux Run, keeping its state. Graceful and hard are the same
// here: lux asks the agent to end its turn cleanly before the container stops.
func (s *Syncer) pause(ctx context.Context, r phaseRun) error {
	// The reason is recorded before lux is asked, so the "stopped" it reports
	// is known to be dude's doing and not read as the agent dying.
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = $2 WHERE id = $1`, r.ID, stopPause)
		return err
	}); err != nil {
		return err
	}
	if err := s.ask(ctx, r, s.Lux.Stop); err != nil {
		return err
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'paused', control = 'none', control_requested_at = NULL
			WHERE id = $1 AND status IN ('scheduled', 'starting', 'running')`, r.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, r, "run.paused", ledger.ActorSystem, map[string]any{"confirmed": true})
	})
}

// whilePaused resumes the lux Run once a person asks. The agent continues
// its conversation from the transcript lux kept; directives given while it
// was paused are sent once it is running, each acknowledged on its own.
func (s *Syncer) whilePaused(ctx context.Context, r phaseRun) (bool, error) {
	// Paused by dude to bring an approved repository: resume, with it, as
	// soon as lux has stopped the Run — resuming earlier would clear the
	// stop reason before lux's "stopped" arrives, and that would read as the
	// agent dying. A person's pause is theirs to end.
	if !r.Resumable {
		return false, nil
	}
	if !lux.Terminal(r.LuxState) {
		// Still stopping: follow the stream, where lux will say so.
		s.follow(r)
		return false, nil
	}
	// A resumed agent has its conversation back but waits for input, and a
	// paused one never finished its turn: told nothing, it would sit idle
	// for good. A person's directive, if one is waiting, is that input (sent
	// the usual way once running); otherwise it is told to carry on.
	// An agent waiting on a person's answer is told nothing: the answer,
	// when it comes, is its input.
	nudge := ""
	if !r.HasDirectives && !r.Waiting {
		nudge = resumeNudge
	}
	lr, foreseen, err := s.resume(ctx, r, nudge)
	var cannot errCannotResume
	var noLogin errLoginUnavailable
	if errors.As(err, &noLogin) {
		return true, s.waitForLogin(ctx, r, noLogin)
	}
	if passing(err) {
		return true, s.retryLater(ctx, r, err)
	}
	if errors.As(err, &cannot) {
		return true, s.fail(ctx, r, "cannot resume: "+cannot.Error())
	}
	if le, ok := lux.AsError(err); ok && le.Status == http.StatusConflict {
		// Already resuming: an earlier attempt got through and its answer
		// was lost. lux's stream reports how it went.
		err = nil
		lr.State = "resuming"
	}
	if err != nil {
		if le, ok := lux.AsError(err); ok && !le.Retryable() && foreseen != 0 {
			// Refused for good: there is no such resume to time.
			s.resumeRefused(ctx, r, foreseen)
		}
		return true, s.retryOrFail(ctx, r, err, "lux refused to resume the run")
	}
	missed := false
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// The Run's row first, held against the stream's batches until this
		// commits: the resume's timing reads what they committed of it.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, r.ID); err != nil {
			return err
		}
		missed = s.resumeAccepted(ctx, tx, r, foreseen, lr)
		// The agent is starting a new turn; the old "done" no longer holds.
		// lux_state is what lux says now ("resuming"), so directives wait for
		// the stream to report it running. A resumed lux Run's cost is no
		// longer final, so it goes back on the cost work list; the stored
		// amounts stand until the next read replaces them.
		_, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', lux_state = $2, lux_stop_reason = NULL,
			lux_cost_next_at = now(),
			control = 'none', control_requested_at = NULL, control_reason = NULL, dude_pause = NULL,
			tool_starts = tool_starts + 1, idle_nudged_at = NULL, agent_active_at = NULL,
			-- Still waiting on a person (a person resumed it anyway): the
			-- grace period starts again.
			waiting_since = CASE WHEN $3 THEN now() END,
			turn_done_at = NULL, agent_busy_at = NULL WHERE id = $1 AND status = 'paused'`, r.ID, lr.State, r.Waiting)
		if err != nil {
			return err
		}
		// Taken back up from a park — whoever resumes it: a person may have
		// made dude's park their own pause meanwhile.
		var last, reason, taskStatus string
		if err := tx.QueryRow(ctx, `SELECT event_type, COALESCE(payload->>'reason', ''), COALESCE(payload->>'taskStatus', '')
			FROM events WHERE run_id = $1 AND event_type IN ($2, $3) ORDER BY cursor DESC LIMIT 1`,
			r.ID, evParked, evUnparked).Scan(&last, &reason, &taskStatus); err != nil || last != evParked {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		if taskStatus != "" {
			// The flag its idle park raised, lowered.
			if _, err := delivery.SetTaskStatusTx(ctx, tx, r.Org, r.ProjectID, r.TaskID, "awaiting_input", taskStatus,
				"a person resumed the agent"); err != nil {
				return err
			}
		}
		// The epoch it resumed into, its timing's (resumeAccepted).
		return s.event(ctx, tx, r, evUnparked, ledger.ActorSystem,
			map[string]any{"reason": reason, "epoch": resumedEpoch(foreseen, lr)})
	}); err != nil {
		return true, err
	}
	if missed {
		// No frame of its epoch is left to stamp it: timed as it stands.
		go s.resumeFollowUp(r, map[int]bool{resumedEpoch(foreseen, lr): true})
	}
	// Directives given while it was paused are sent by the usual path once
	// lux reports the resumed Run running, each on its own so each is
	// acknowledged: lux refuses input to a Run still waiting for a host.
	return true, nil
}

// resume is the one call to lux's resume. lux keeps no secret (every resume
// supplies all of them again, lux internal/server/api.go resumeRun), so
// the secrets come from s.spec, fresh: a Run parked for a day needs a new
// registry login and forge token, not the ones it started with. Its
// timing's row is in before lux is asked (resumeAsked); foreseen is the
// epoch it is under, 0 when lux was not asked.
func (s *Syncer) resume(ctx context.Context, r phaseRun, input string) (resumed lux.Run, foreseen int, err error) {
	// lux's copy says whether the Run was started with a login, whatever
	// this orchestrator is configured with now (the runs row does not
	// record it), and the image lux stored at submit is the one it pulls.
	lr, err := s.Lux.Get(ctx, r.LuxRunID)
	if err != nil {
		return lux.Run{}, 0, err
	}
	RecordMemoryLimit(ctx, s.DB, s.Log, r.Org, r.ID, lr)
	spec, _, err := s.spec(ctx, r, &lr.Spec, "")
	if passing(err) || forge.Transient(err) || errors.As(err, new(errLoginUnavailable)) {
		return lux.Run{}, 0, err
	}
	if err != nil {
		return lux.Run{}, 0, errCannotResume{err}
	}
	in := lux.ResumeInput{Secrets: spec.Secrets, Input: input}
	if err := s.addedRepositories(ctx, r, spec, &in); err != nil {
		return lux.Run{}, 0, err
	}
	foreseen = s.resumeAsked(ctx, r, lr)
	resumed, err = s.Lux.Resume(ctx, r.LuxRunID, in)
	return resumed, foreseen, err
}

// requestPause asks for a graceful pause, saying why, marked as dude's own
// (kind is a dude_pause) so the syncer knows when to resume it; a person's
// later pause or resume wins. A pause other than for a repository is a
// park, and says so in the Run's chat; an idle one also raises the work
// item for a person to look at (lowered when they resume it).
//
// The sweep decided from what it read; the pause checks it still holds — a
// Run parked for a person still waits on one, an idle one has done nothing
// since — so an answer or a word from the agent in between wins.
func (s *Syncer) requestPause(ctx context.Context, r phaseRun, kind, why string) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// parkedAt: when the park began, on the database's clock, as the
		// answers and decisions that end it are stamped.
		var parkedAt time.Time
		err := tx.QueryRow(ctx, `UPDATE runs r SET control = 'pause_graceful', control_requested_at = now(), control_reason = $2,
			dude_pause = $3
			WHERE r.id = $1 AND r.control = 'none' AND r.status = 'running'
			  AND ($3 <> 'person' OR (r.waiting_since IS NOT NULL AND `+delivery.OpenAsk+`))
			  AND ($3 <> 'idle' OR (r.turn_done_at IS NULL AND r.waiting_since IS NULL
			       AND cardinality(r.open_tool_calls) = 0 AND `+quietSince+` = $4))
			  AND ($3 <> 'conductor' OR (r.turn_done_at IS NOT NULL AND NOT `+unsentDirective+`))
			RETURNING r.control_requested_at`, r.ID, why, kind, r.QuietSince).Scan(&parkedAt)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil || kind == "repository" {
			return err
		}
		var taskStatus string
		if kind == "idle" {
			// Raised for a person to look at, from whatever it was (running,
			// in review); put back when the Run is taken up again.
			if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, r.TaskID).Scan(&taskStatus); err != nil {
				return err
			}
			if _, err := delivery.SetTaskStatusTx(ctx, tx, r.Org, r.ProjectID, r.TaskID, "", "awaiting_input",
				"the agent went quiet"); err != nil {
				return err
			}
		}
		return s.event(ctx, tx, r, evParked, ledger.ActorSystem,
			map[string]any{"reason": kind, "message": why, "taskStatus": db.Nullable(taskStatus), "parkedAt": parkedAt})
	})
}

// nudge tells an agent that has been quiet mid-turn for the idle limit to
// carry on or ask for what it needs — interrupting the turn, which may be
// stuck. Once: if it stays quiet as long again, it is parked for a person.
func (s *Syncer) nudge(ctx context.Context, r phaseRun) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Only if it has done nothing since the sweep saw it quiet.
		tag, err := tx.Exec(ctx, `UPDATE runs r SET idle_nudged_at = now() WHERE r.id = $1 AND r.idle_nudged_at IS NULL
			AND r.waiting_since IS NULL AND cardinality(r.open_tool_calls) = 0 AND `+quietSince+` = $2`, r.ID, r.QuietSince)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		id, _, err := delivery.QueueDirective(ctx, tx, delivery.RunRef{Org: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID},
			delivery.Directive{Text: idleNudge, Scope: "turn", Interrupt: true})
		if err != nil {
			return err
		}
		return s.event(ctx, tx, r, evIdleNudged, ledger.ActorSystem, map[string]any{"directiveId": id})
	})
}

// addedRepositories puts on the resume the approved repositories the lux
// Run does not have yet: each as the spec has it (the task names it
// now). The resume's request id is the first request's; lux's git.clone for
// each carries it, and settles the requests (translate.go). An approval the
// spec cannot carry — the task no longer names it, or names two by that
// name — fails, rather than pausing the Run again and again.
func (s *Syncer) addedRepositories(ctx context.Context, r phaseRun, spec lux.Spec, in *lux.ResumeInput) error {
	if !r.RepoApproved {
		return nil
	}
	type approved struct{ ID, Name, Access string }
	var reqs []approved
	var have []string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT lux_repositories FROM runs WHERE id = $1`, r.ID).Scan(&have); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT q.id, repo.name, q.access::text FROM repository_requests q
			JOIN repositories repo ON repo.id = q.repository_id WHERE q.run_id = $1 AND q.status = 'approved' ORDER BY q.created_at`, r.ID)
		if err != nil {
			return err
		}
		reqs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[approved])
		return err
	}); err != nil {
		return err
	}
	byName := map[string][]lux.Repository{}
	if spec.Git != nil {
		for _, repo := range spec.Git.Repositories {
			byName[repo.Name] = append(byName[repo.Name], repo)
		}
	}
	var told []string
	var carried, failed []string
	for _, q := range reqs {
		if slices.Contains(have, q.Name) {
			continue
		}
		if len(byName[q.Name]) != 1 {
			failed = append(failed, q.ID)
			continue
		}
		repo := byName[q.Name][0]
		in.AddRepositories = append(in.AddRepositories, repo)
		carried = append(carried, q.ID)
		note := "read only"
		if q.Access == "write" {
			note = "you may change it; commit there and it gets its own pull request"
		}
		told = append(told, fmt.Sprintf("%s is now checked out at %s (%s).", q.Name, repo.Path, note))
	}
	if len(failed) > 0 {
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE repository_requests SET status = 'failed',
				error = 'the task no longer names it, or names another repository by that name'
				WHERE id = ANY($1) AND status = 'approved'`, failed)
			return err
		}); err != nil {
			return err
		}
	}
	if len(carried) > 0 {
		in.RequestID = carried[0]
		in.Input = strings.TrimSpace("A person approved your request. " + strings.Join(told, " ") + " Carry on.\n\n" + in.Input)
	}
	return nil
}

// deliverDirectives sends a person's steering to the running agent.
func (s *Syncer) deliverDirectives(ctx context.Context, r phaseRun) (bool, error) {
	if r.Status != statusRunning || r.LuxState != "running" {
		return false, nil
	}
	type directive struct {
		ID, Text  string
		Interrupt bool
		// Resends: the root of an "Interrupt now" (QueueDirective), "" for
		// any other directive.
		Resends string
	}
	var pending []directive
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, text, interrupt, COALESCE(resends, '')
			FROM directives WHERE run_id = $1 AND sent_at IS NULL AND failed_at IS NULL ORDER BY created_at`, r.ID)
		if err != nil {
			return err
		}
		pending, err = pgx.CollectRows(rows, pgx.RowToStructByPos[directive])
		return err
	}); err != nil || len(pending) == 0 {
		return false, err
	}
	for _, d := range pending {
		text := d.Text
		if d.Resends != "" {
			only, err := s.interruptOnly(ctx, r, d.ID)
			if err != nil {
				return true, err
			}
			if only {
				text = ""
			}
		}
		// The directive id is the request id, so a retried send is delivered
		// once.
		if err := s.Lux.Input(ctx, r.LuxRunID, text, d.ID, d.Interrupt); err != nil {
			return true, s.retryLater(ctx, r, err)
		}
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE directives SET sent_at = now() WHERE id = $1`, d.ID); err != nil {
				return err
			}
			// lux sends no receipt for an interrupt alone: one whose words
			// the agent already has is delivered now; otherwise with the
			// directive carrying them (directiveReceipt).
			return settleInterrupts(ctx, tx, s, r, nil,
				`UPDATE directives d SET delivered_at = now(), accepted_at = COALESCE(d.accepted_at, now())
				WHERE d.id = $1 AND d.interrupt_only AND d.delivered_at IS NULL AND d.failed_at IS NULL
				  AND EXISTS (SELECT 1 FROM directives c WHERE c.run_id = d.run_id AND `+carrierOf+`
				    AND c.delivered_at IS NOT NULL)
				RETURNING d.id`, d.ID)
		}); err != nil {
			return true, err
		}
	}
	return true, nil
}

// carrierOf (SQL): c is a directive carrying the words of d's instruction,
// its root or a resend sent with them (d an "Interrupt now", resends set).
const carrierOf = `(c.id = d.resends OR c.resends = d.resends) AND c.id <> d.id AND c.interrupt_only IS FALSE`

// interruptOnly decides, at an "Interrupt now"'s first send attempt,
// whether it goes as the interrupt alone: when a directive carrying its
// words is with lux and has not failed. Otherwise (the root failed, or
// never went) it carries them. Stored then, so every retry of the request
// is the same.
func (s *Syncer) interruptOnly(ctx context.Context, r phaseRun, id string) (only bool, err error) {
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Locked, so a failure of the words committing meanwhile either is
		// seen here or sees this decision and fails the interrupt with it.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM directives d JOIN directives c ON c.run_id = d.run_id AND `+carrierOf+`
			WHERE d.id = $1 FOR UPDATE OF c`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE directives d SET interrupt_only = EXISTS (SELECT 1 FROM directives c
				WHERE c.run_id = d.run_id AND `+carrierOf+` AND c.sent_at IS NOT NULL AND c.failed_at IS NULL)
			WHERE d.id = $1 AND d.interrupt_only IS NULL`, id); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT interrupt_only FROM directives WHERE id = $1`, id).Scan(&only)
	})
	return only, err
}

// settleInterrupts runs update, which moves interrupt-only directives with
// the directive carrying their words and returns their ids, and writes an
// event for each: run.directive.delivered (flagged interruptOnly, never
// read), or with fail, run.directive.failed with fail as its error.
func settleInterrupts(ctx context.Context, tx pgx.Tx, s *Syncer, r phaseRun, fail *string, update string, args ...any) error {
	rows, err := tx.Query(ctx, update, args...)
	if err != nil {
		return err
	}
	moved, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range moved {
		typ, payload := evDirectiveDelivered, map[string]any{"directiveId": id, "interruptOnly": true}
		if fail != nil {
			typ, payload = evDirectiveFailed, map[string]any{"directiveId": id, "error": *fail}
		}
		if err := s.event(ctx, tx, r, typ, ledger.ActorSystem, payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) unfollow(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.following[runID]; ok {
		f.cancel()
		delete(s.following, runID)
	}
}

// fail ends a Run as failed, and its lux Run with it.
func (s *Syncer) fail(ctx context.Context, r phaseRun, reason string) error {
	s.unfollow(r.ID)
	if r.LuxRunID != "" && !lux.Terminal(r.LuxState) {
		_ = s.Lux.Cancel(ctx, r.LuxRunID)
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now()
			WHERE id = $1 AND status NOT IN ('completed', 'failed', 'aborted')`, r.ID, reason)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, r, "run.failed", ledger.ActorSystem, map[string]any{"status": "failed", "error": reason})
	})
}

// ask stops the Run's lux Run if it is still going.
func (s *Syncer) ask(ctx context.Context, r phaseRun, call func(context.Context, string) error) error {
	if r.LuxRunID == "" || lux.Terminal(r.LuxState) {
		return nil
	}
	return s.askControl(ctx, r, call)
}

// askControl retries transient failures; a definitive refusal counts as done.
func (s *Syncer) askControl(ctx context.Context, r phaseRun, call func(context.Context, string) error) error {
	err := call(ctx, r.LuxRunID)
	if le, ok := lux.AsError(err); err == nil || ok && !le.Retryable() {
		return nil
	}
	if rerr := s.retryLater(ctx, r, err); rerr != nil {
		return rerr
	}
	return errRetry
}

// resumeNudge is what a resumed agent is told when nobody said anything
// while it was paused.
const resumeNudge = "You were paused and have been resumed. Continue the task where you left off."

// idleNudge is what an agent quiet mid-turn for the idle limit is told.
const idleNudge = "You have not done anything for a while. Carry on with your task — or, if you need " +
	"something only a person can give, ask for it with ask_person and end your turn."

// Events of dude parking a Run and taking it back up.
const (
	evParked     = "run.parked"
	evUnparked   = "run.unparked"
	evIdleNudged = "run.idle_nudged"
)

// errForge: the forge's credentials could not be read. Passing, and asked
// again, rather than the Run built without them.
type errForge struct{ err error }

func (e errForge) Error() string { return "reading the forge's credentials: " + e.err.Error() }
func (e errForge) Unwrap() error { return e.err }

// errRegistry: no registry login could be had now (ECR unreachable, the
// role's credentials expiring). Passing, like errForge.
type errRegistry struct{ err error }

func (e errRegistry) Error() string {
	return "logging in to the agent image's registry: " + e.err.Error()
}
func (e errRegistry) Unwrap() error { return e.err }

// errNoModel: the Run's role names no tier, or one that names no model; it
// is the reason the Run fails, in words, as it is.
type errNoModel string

func (e errNoModel) Error() string { return string(e) }

// passing: err is a credential that could not be had now, and may be later.
func passing(err error) bool {
	return errors.As(err, new(errForge)) || errors.As(err, new(errRegistry))
}

// errLoginUnavailable: lux's stored spec names a registry login this
// orchestrator cannot supply — logins are off (Configured is ""), or it
// logs in to another registry. Lasts until the configuration is restored.
type errLoginUnavailable struct{ Registry, Configured string }

func (e errLoginUnavailable) Error() string {
	now := "DUDE_REGISTRY_AUTH is none"
	if e.Configured != "" {
		now = "this orchestrator logs in to " + e.Configured
	}
	return fmt.Sprintf("the Run was started with a login for %s and %s: restore DUDE_REGISTRY_AUTH (and DUDE_REGISTRY or DUDE_AGENT_IMAGE) for %s to resume it",
		e.Registry, now, e.Registry)
}

// LoginRetry is how long a Run or preview waiting for its registry login is
// left before the next check: a configuration change needs a restart anyway.
const LoginRetry = time.Minute

// IsLoginUnavailable says whether err, from LoginFor, is a resume's login
// this orchestrator is not configured to supply.
func IsLoginUnavailable(err error) bool { return errors.As(err, new(errLoginUnavailable)) }

// waitForLogin leaves a Run paused, still due to resume, until its login
// is configured again.
func (s *Syncer) waitForLogin(ctx context.Context, r phaseRun, cause errLoginUnavailable) error {
	s.Log.Warn("paused Run not resumed: "+cause.Error(), "run", r.ID)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + make_interval(secs => $2) WHERE id = $1`,
			r.ID, LoginRetry.Seconds())
		return err
	})
}

// errCannotResume: the resume's spec cannot be built, and will not be.
type errCannotResume struct{ err error }

func (e errCannotResume) Error() string { return e.err.Error() }
func (e errCannotResume) Unwrap() error { return e.err }

// errRetry ends a step that will be tried again after a back-off.
var errRetry = errors.New("retrying later")

// retryLater backs a Run off after a failure that may pass. A lux Run lux
// no longer has will not come back, whichever call found out: that fails
// the Run instead of retrying it forever.
// retryOrFail: a request lux refused, and would refuse again, fails the Run
// with lux's reason; anything else is tried again later.
func (s *Syncer) retryOrFail(ctx context.Context, r phaseRun, cause error, refused string) error {
	if le, ok := lux.AsError(cause); ok && !le.Retryable() {
		return s.fail(ctx, r, fmt.Sprintf("%s: %s", refused, le.Message))
	}
	return s.retryLater(ctx, r, cause)
}

func (s *Syncer) retryLater(ctx context.Context, r phaseRun, cause error) error {
	if lux.IsNotFound(cause) {
		return s.fail(ctx, r, "lux no longer has this Run")
	}
	s.Log.Info("lux call failed; retrying later", "run", r.ID, "error", cause)
	// GitHub penalises requests made during a limit it asked to be waited out.
	delay := 5 * time.Second
	if wait := forge.RetryAfter(cause); wait > delay {
		delay = wait
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + make_interval(secs => $2) WHERE id = $1`,
			r.ID, delay.Seconds())
		return err
	})
}

func (s *Syncer) event(ctx context.Context, tx pgx.Tx, r phaseRun, typ, actor string, payload map[string]any) error {
	var at time.Time
	if s.Now != nil {
		at = s.Now()
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID, OccurredAt: at,
		ActorType: actor, ActorID: r.ID, Source: ledger.SourceRunner, CorrelationID: r.TaskID, Payload: payload,
	})
	return err
}

// RecordFindings stores a review's findings, and resolves the earlier ones
// the re-review judged fixed.
//
// Resolution is what lets the loop converge: a finding stays open until
// something closes it. What closes it is the reviewer of its category, shown
// the finding after a fix and asked, reading the code — not the absence of
// the finding from a new review, nor a fix having touched its file, both of
// which close findings nobody checked.
func RecordFindings(ctx context.Context, database *db.DB, org, runID string, findings []delivery.Finding,
	verdicts map[string]bool) error {
	return database.InOrg(ctx, org, func(tx pgx.Tx) error {
		var projectID, taskID, phase string
		if err := tx.QueryRow(ctx, `SELECT project_id, task_id, phase::text FROM runs WHERE id = $1`, runID).
			Scan(&projectID, &taskID, &phase); err != nil {
			return err
		}
		// Only a review or test Run may report: a fixer reporting findings
		// could manufacture the evidence that its own work is finished.
		if phase != delivery.PhaseReview && phase != delivery.PhaseTest {
			return fmt.Errorf("a %s run may not report findings", phase)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM review_findings WHERE run_id = $1`, runID); err != nil {
			return err
		}
		// The reviewer's judgement of what it was shown: fixed is resolved;
		// still stays open, for the next fix. One it said nothing about stays
		// as it was.
		for id, fixed := range verdicts {
			if !fixed {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE review_findings SET status = 'resolved', resolved_by_run_id = $2,
				resolution_note = 'judged fixed by the re-review', updated_at = now()
				WHERE id = $1 AND status = 'open'`, id, runID); err != nil {
				return err
			}
		}
		counts := map[string]int{}
		for _, f := range findings {
			counts[f.Severity]++
			if _, err := tx.Exec(ctx, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category,
				severity, repo, file, line, title, description, suggested_fix)
				VALUES ($1, $2, $3, $4, $5, $6::finding_severity, $7, $8, $9, $10, $11, $12)`,
				ids.New(ids.Finding), org, taskID, runID, f.Category, f.Severity,
				db.Nullable(f.Repo), db.Nullable(f.File), nullableInt(f.Line), f.Title, f.Description, f.SuggestedFix); err != nil {
				return err
			}
		}
		_, err := ledger.Append(ctx, tx, ledger.Event{
			Type: delivery.EvReviewCompleted, OrganizationID: org, ProjectID: projectID, TaskID: taskID, RunID: runID,
			ActorType: ledger.ActorAgent, ActorID: runID, Source: ledger.SourceRunner, CorrelationID: taskID,
			Payload: map[string]any{"phase": phase, "count": len(findings), "bySeverity": counts},
		})
		return err
	})
}

// NotifyFinished signals each workflow whose phase Run has finished but has
// not yet been told. A sweep rather than a hook on the status change, so a
// Run ended by any path — including one this process never saw — still
// wakes its workflow. The signal's key makes a repeat harmless.
func NotifyFinished(ctx context.Context, database *db.DB, signal func(ctx context.Context, org, workflowRunID, runID, status string) error) (int, error) {
	type finished struct{ ID, Org, Status, WorkflowRunID string }
	var runs []finished
	if err := database.InSystem(ctx, "phase-notifier", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.status::text, w.id FROM runs r
			JOIN workflow_runs w ON w.task_id = r.task_id AND w.organization_id = r.organization_id
			WHERE r.phase IS NOT NULL AND r.status IN ('completed', 'failed', 'aborted')
			  AND r.phase_notified_at IS NULL AND w.status = 'waiting'
			  -- Work that changes no code is judged by what it published, so
			  -- that waits for lux to report it. Work on code is judged by its
			  -- commits, and does not wait on lux's snapshot upload.
			  AND (r.artifacts_due_at IS NULL OR EXISTS (SELECT 1 FROM task_repositories wr
			       WHERE wr.task_id = r.task_id AND wr.access = 'write'))
			ORDER BY r.ended_at LIMIT 50`)
		if err != nil {
			return err
		}
		runs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[finished])
		return err
	}); err != nil {
		return 0, err
	}
	handled := 0
	for _, r := range runs {
		if err := signal(ctx, r.Org, r.WorkflowRunID, r.ID, r.Status); err != nil {
			// One workflow that cannot be signalled must not stall the rest.
			continue
		}
		if err := database.InSystem(ctx, "phase-notifier", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET phase_notified_at = now() WHERE id = $1`, r.ID)
			return err
		}); err != nil {
			return handled, err
		}
		handled++
	}
	return handled, nil
}

func nullableInt(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
