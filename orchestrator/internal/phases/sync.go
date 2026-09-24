// Package phases runs phase Runs on lux: submit them, follow their output
// into the ledger, and when the agent's turn is done, collect what it
// produced and tell the workflow.
//
// A phase Run's life, from dude's side:
//
//	pending ─submit─▶ scheduled ─lux running─▶ running ─agent busy→idle─▶ finishing
//	   finishing: push (publishing phases) → fast-forward the work item branch
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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

type Syncer struct {
	DB     *db.DB
	Lux    lux.Client
	Forges delivery.Forges
	Agent  AgentConfig
	Log    *slog.Logger

	// One follower per live lux Run. The follower is the only writer of a
	// Run's cursor, so two must never run for the same Run.
	mu        sync.Mutex
	following map[string]*follower
}

// follower is one goroutine reading a Run's output. A pointer, so a
// finishing follower can tell whether the entry is still its own.
type follower struct{ cancel context.CancelFunc }

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

// phaseRun is a phase Run's row, as the syncer reads it.
type phaseRun struct {
	ID, Org, ProjectID, WorkItemID, Phase, Status, Control string
	RepositoryID, BaseRef, Category                        string
	LuxRunID, LuxState, LuxStopReason                      string
	PushRequestID, BaseSHA                                 string
	TurnDone, HasDirectives                                bool
	PushResult                                             json.RawMessage
	PRFeedback                                             json.RawMessage
	FindingIDs, BlockingSeverities                         []string
	Attempt                                                int
}

const runColumns = `r.id, r.organization_id, r.project_id, r.work_item_id, r.phase::text, r.status::text, r.control::text,
	COALESCE(r.repository_id, ''), COALESCE(r.base_ref, ''), COALESCE(r.category, ''),
	COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''), COALESCE(r.lux_stop_reason, ''),
	COALESCE(r.push_request_id, ''), COALESCE(r.base_sha, ''), r.turn_done_at IS NOT NULL,
	EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL),
	r.push_result, r.pr_feedback, r.finding_ids, r.blocking_severities, r.attempt`

func scan(row pgx.Row) (phaseRun, error) {
	var r phaseRun
	err := row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.WorkItemID, &r.Phase, &r.Status, &r.Control,
		&r.RepositoryID, &r.BaseRef, &r.Category, &r.LuxRunID, &r.LuxState, &r.LuxStopReason,
		&r.PushRequestID, &r.BaseSHA, &r.TurnDone, &r.HasDirectives,
		&r.PushResult, &r.PRFeedback, &r.FindingIDs, &r.BlockingSeverities, &r.Attempt)
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
		rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM runs r
			WHERE r.phase IS NOT NULL
			  AND (r.status IN ('pending', 'scheduled', 'starting', 'running')
			       OR (r.status = 'paused' AND r.control = 'resume')
			       -- Aborted in dude but not yet cancelled in lux.
			       OR (r.status = 'aborted' AND r.lux_run_id IS NOT NULL AND r.lux_stop_reason IS DISTINCT FROM 'cancel'))
			  AND (r.next_attempt_at IS NULL OR r.next_attempt_at <= now())
			ORDER BY (r.status = 'pending' OR r.control <> 'none' OR r.turn_done_at IS NOT NULL) DESC, r.created_at
			LIMIT 1000`)
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
	case r.Status == statusAborted:
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
	// that is where the push result arrives — including after a restart.
	s.follow(r)
	if r.TurnDone {
		return s.finish(ctx, r)
	}
	if !r.HasDirectives {
		return false, nil
	}
	return s.deliverDirectives(ctx, r)
}

// submit builds the Run's spec and hands it to lux.
func (s *Syncer) submit(ctx context.Context, r phaseRun) error {
	spec, err := s.spec(ctx, r)
	if err != nil {
		return s.fail(ctx, r, "cannot build the run: "+err.Error())
	}
	// The dude Run id is the idempotency key: a submit that timed out and is
	// retried returns the lux Run the first one created.
	lr, err := s.Lux.Submit(ctx, spec, r.ID)
	if err != nil {
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return s.fail(ctx, r, fmt.Sprintf("lux refused the run: %s", le.Message))
		}
		return s.retryLater(ctx, r, err)
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Guarded on still being pending: an abort that raced the submit wins,
		// and the sweep then cancels the lux Run it made.
		tag, err := tx.Exec(ctx, `UPDATE runs SET lux_run_id = $2, lux_state = $3, next_attempt_at = NULL,
			harness = $4, model = $5,
			status = CASE WHEN status = 'pending' THEN 'scheduled'::run_status ELSE status END
			WHERE id = $1`, r.ID, lr.ID, lr.State, spec.Labels["dude.harness"], spec.Labels["dude.model"])
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, r, "run.lease.acquired", ledger.ActorSystem, map[string]any{"luxRunId": lr.ID})
	})
}

// spec gathers what the Run's lux spec is built from.
func (s *Syncer) spec(ctx context.Context, r phaseRun) (lux.Spec, error) {
	var in specInput
	var title, goal, image string
	var criteria, projectModels, orgModels json.RawMessage
	var findings []delivery.Finding
	var feedback []forge.ActionableFeedback
	_ = json.Unmarshal(r.PRFeedback, &feedback)
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT w.title, w.goal, w.acceptance_criteria, COALESCE(p.runtime_image, ''), p.agent_models,
			       repo.name, repo.url, repo.default_branch
			FROM work_items w JOIN projects p ON p.id = w.project_id
			JOIN repositories repo ON repo.id = $2
			WHERE w.id = $1`, r.WorkItemID, r.RepositoryID).
			Scan(&title, &goal, &criteria, &image, &projectModels, &in.RepoName, &in.RepoURL, &in.Ref); err != nil {
			return fmt.Errorf("load work item and repository: %w", err)
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
		return lux.Spec{}, err
	}
	// Organizations are not tenant rows; read their defaults separately.
	if err := s.DB.Pool.QueryRow(ctx, `SELECT default_agent_models FROM organizations WHERE id = $1`, r.Org).Scan(&orgModels); err != nil {
		return lux.Spec{}, err
	}
	role := delivery.RoleForPhase[r.Phase]
	model, context := resolveModel(role, projectModels, orgModels)
	if model == "" {
		return lux.Spec{}, fmt.Errorf("no model is configured for the %s role", role)
	}

	var ac []string
	_ = json.Unmarshal(criteria, &ac)

	if r.BaseRef != "" {
		in.Ref = r.BaseRef
	}
	in.RunID, in.OrganizationID, in.WorkItemID, in.Phase, in.Role = r.ID, r.Org, r.WorkItemID, r.Phase, role
	in.Model = model
	in.Image = image
	if in.Image == "" {
		in.Image = s.Agent.DefaultImage
	}
	in.Prompt = delivery.Prompt(r.Phase, delivery.PromptInput{
		Title: title, Goal: goal, AcceptanceCriteria: ac, Category: r.Category,
		Findings: findings, PRFeedback: feedback, BlockingSeverities: r.BlockingSeverities, Context: context,
	})
	if delivery.Publishes[r.Phase] {
		in.PushBranch = runBranch(r)
	}
	if gh, err := s.Forges.For(ctx, r.Org); err == nil && gh != nil {
		if in.ForgeToken, err = gh.Token(); err != nil {
			return lux.Spec{}, err
		}
	}
	return buildSpec(s.Agent, in), nil
}

// runBranch is where one phase Run's commits are pushed. Every Run has its
// own, because lux allows a Run's first push only to a branch that does not
// exist yet; dude then fast-forwards the work item's branch to it.
func runBranch(r phaseRun) string {
	return fmt.Sprintf("dude/%s/run-%s", r.WorkItemID, r.ID)
}

// resolveModel: the project's setting for a role, else the organization's.
func resolveModel(role string, project, org json.RawMessage) (model, context string) {
	for _, layer := range []json.RawMessage{project, org} {
		var m map[string]struct {
			Model   string `json:"model"`
			Context string `json:"context"`
		}
		if json.Unmarshal(layer, &m) == nil && m[role].Model != "" {
			return m[role].Model, m[role].Context
		}
	}
	return "", ""
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
	mine := &follower{cancel}
	s.following[r.ID] = mine
	go func() {
		defer func() {
			s.mu.Lock()
			// Only its own entry: a newer follower may have taken the slot
			// after this one was told to stop.
			if s.following[r.ID] == mine {
				delete(s.following, r.ID)
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
		if le, ok := lux.AsError(err); ok && le.Status == 404 {
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
			if err := t.save(ctx, tx); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE runs SET lux_cursor = COALESCE(NULLIF($2, ''), lux_cursor),
				lux_after_event = GREATEST(lux_after_event, $3) WHERE id = $1`, r.ID, cursor, afterEvent)
			return err
		}); err != nil {
			// What was read but not recorded is read again from the saved
			// cursor by the next follower.
			return err
		}
	}
	return <-read
}

// finish collects what a phase produced once its agent finished its turn.
// Idempotent end to end: each piece is recorded before the next is asked
// for, so a restart resumes the sequence.
func (s *Syncer) finish(ctx context.Context, r phaseRun) (bool, error) {
	if delivery.Publishes[r.Phase] && r.PushResult == nil {
		// Only a state lux has already reported as over rules the push out.
		// Anything else is asked of lux itself: its lifecycle events trail
		// the agent's own records by up to a second, so the state recorded
		// here can still say "scheduled" when the agent has already finished.
		if lux.Terminal(r.LuxState) {
			return true, s.fail(ctx, r, "the agent's container stopped before its work was pushed")
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

	result := publishResult{}
	if delivery.Publishes[r.Phase] {
		var err error
		if result, err = s.publish(ctx, r); err != nil {
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
	return true, s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'completed', ended_at = now(), lux_stop_reason = $2,
			head_sha = COALESCE(NULLIF($3, ''), head_sha), changed_paths = $4, branch = COALESCE(NULLIF($5, ''), branch)
			WHERE id = $1 AND status IN ('scheduled', 'starting', 'running')`,
			r.ID, "complete", result.head, db.NonNil(result.changed), result.branch)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.event(ctx, tx, r, "run.completed", ledger.ActorSystem, map[string]any{"status": "completed"})
	})
}

type publishResult struct {
	head, branch string
	changed      []string
}

// publish moves the work item's branch to what this Run pushed, and works
// out what changed.
//
// A fast-forward, never a force: if someone else moved the branch the update
// is refused and the phase fails loudly rather than overwriting their work.
func (s *Syncer) publish(ctx context.Context, r phaseRun) (publishResult, error) {
	var push struct {
		Results []struct {
			Repo, Branch, Commit, Status, Error string
		} `json:"results"`
	}
	if err := json.Unmarshal(r.PushResult, &push); err != nil || len(push.Results) == 0 {
		return publishResult{}, fmt.Errorf("lux reported no push result")
	}
	res := push.Results[0]
	if res.Status != "pushed" && res.Status != "up-to-date" {
		return publishResult{}, fmt.Errorf("push %s: %s", res.Status, res.Error)
	}

	var repoURL string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT url FROM repositories WHERE id = $1`, r.RepositoryID).Scan(&repoURL)
	}); err != nil {
		return publishResult{}, err
	}
	gh, err := s.Forges.For(ctx, r.Org)
	if err != nil {
		return publishResult{}, err
	}
	slug := forge.SlugFromURL(repoURL)
	if gh == nil || slug == "" {
		return publishResult{}, fmt.Errorf("no forge to publish %s to", repoURL)
	}

	branch := delivery.BranchFor(r.WorkItemID, r.Attempt)
	out := publishResult{head: res.Commit, branch: branch}
	if res.Commit == r.BaseSHA || res.Status == "up-to-date" && res.Commit == "" {
		// Nothing was committed: an empty change, which the workflow reads
		// from the empty path list.
		return out, nil
	}
	if err := gh.FastForward(ctx, slug, branch, res.Commit); err != nil {
		return out, fmt.Errorf("move %s to %s: %w", branch, short(res.Commit), err)
	}
	// Best effort: a leftover per-Run branch is clutter, not a fault.
	_ = gh.DeleteBranch(ctx, slug, res.Branch)

	base := r.BaseSHA
	if base == "" {
		base = r.BaseRef
	}
	if out.changed, err = gh.ChangedFiles(ctx, slug, base, res.Commit); err != nil {
		return out, fmt.Errorf("compare %s...%s: %w", short(base), short(res.Commit), err)
	}
	return out, s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return s.event(ctx, tx, r, delivery.EvGitCommitCreated, ledger.ActorAgent, map[string]any{
			"repo": res.Repo, "baseSha": base, "headSha": res.Commit, "branch": branch, "changedPaths": out.changed})
	})
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

// cancel ends the lux Run of a Run a person aborted.
func (s *Syncer) cancel(ctx context.Context, r phaseRun) error {
	s.unfollow(r.ID)
	if err := s.ask(ctx, r, s.Lux.Cancel); err != nil {
		return err
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = 'cancel', control = 'none' WHERE id = $1`, r.ID)
		return err
	})
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
	if r.Control != "resume" {
		return false, nil
	}
	spec, err := s.spec(ctx, r)
	if err != nil {
		return true, s.fail(ctx, r, "cannot resume: "+err.Error())
	}
	// A resumed agent has its conversation back but waits for input, and a
	// paused one never finished its turn: told nothing, it would sit idle
	// for good. A person's directive, if one is waiting, is that input (sent
	// the usual way once running); otherwise it is told to carry on.
	// An agent waiting on a person's answer is told nothing: the answer,
	// when it comes, is its input.
	var asking bool
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE run_id = $1 AND status = 'open')`, r.ID).Scan(&asking)
	}); err != nil {
		return true, err
	}
	nudge := ""
	if !r.HasDirectives && !asking {
		nudge = resumeNudge
	}
	lr, err := s.Lux.Resume(ctx, r.LuxRunID, spec.Secrets, nudge)
	if err != nil {
		return true, s.retryLater(ctx, r, err)
	}
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// The agent is starting a new turn; the old "done" no longer holds.
		// lux_state is what lux says now ("resuming"), so directives wait for
		// the stream to report it running.
		_, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', lux_state = $2, lux_stop_reason = NULL,
			control = 'none', control_requested_at = NULL, control_reason = NULL,
			turn_done_at = NULL, agent_busy_at = NULL WHERE id = $1 AND status = 'paused'`, r.ID, lr.State)
		return err
	}); err != nil {
		return true, err
	}
	// Directives given while it was paused are sent by the usual path once
	// lux reports the resumed Run running, each on its own so each is
	// acknowledged: lux refuses input to a Run still waiting for a host.
	return true, nil
}

// deliverDirectives sends a person's steering to the running agent.
func (s *Syncer) deliverDirectives(ctx context.Context, r phaseRun) (bool, error) {
	if r.Status != statusRunning || r.LuxState != "running" {
		return false, nil
	}
	type directive struct {
		ID, Text  string
		Interrupt bool
	}
	var pending []directive
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, text, interrupt FROM directives WHERE run_id = $1 AND sent_at IS NULL ORDER BY created_at`, r.ID)
		if err != nil {
			return err
		}
		pending, err = pgx.CollectRows(rows, pgx.RowToStructByPos[directive])
		return err
	}); err != nil || len(pending) == 0 {
		return false, err
	}
	for _, d := range pending {
		// The directive id is the request id, so a retried send is delivered
		// once.
		if err := s.Lux.Input(ctx, r.LuxRunID, d.Text, d.ID, d.Interrupt); err != nil {
			return true, s.retryLater(ctx, r, err)
		}
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE directives SET sent_at = now() WHERE id = $1`, d.ID)
			return err
		}); err != nil {
			return true, err
		}
	}
	return true, nil
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

// ask stops or cancels the Run's lux Run, if it is still going. An answer
// that may change later backs the Run off and returns errRetry; lux refusing
// outright (the Run is already over) counts as done.
func (s *Syncer) ask(ctx context.Context, r phaseRun, call func(context.Context, string) error) error {
	if r.LuxRunID == "" || lux.Terminal(r.LuxState) {
		return nil
	}
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

// errRetry ends a step that will be tried again after a back-off.
var errRetry = errors.New("retrying later")

// retryLater backs a Run off after a failure that may pass. A lux Run lux
// no longer has will not come back, whichever call found out: that fails
// the Run instead of retrying it forever.
func (s *Syncer) retryLater(ctx context.Context, r phaseRun, cause error) error {
	if le, ok := lux.AsError(cause); ok && le.Status == 404 {
		return s.fail(ctx, r, "lux no longer has this Run")
	}
	s.Log.Info("lux call failed; retrying later", "run", r.ID, "error", cause)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + interval '5 seconds' WHERE id = $1`, r.ID)
		return err
	})
}

func (s *Syncer) event(ctx context.Context, tx pgx.Tx, r phaseRun, typ, actor string, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: r.Org, ProjectID: r.ProjectID, WorkItemID: r.WorkItemID, RunID: r.ID,
		ActorType: actor, ActorID: r.ID, Source: ledger.SourceRunner, CorrelationID: r.WorkItemID, Payload: payload,
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
		var projectID, workItemID, phase string
		if err := tx.QueryRow(ctx, `SELECT project_id, work_item_id, phase::text FROM runs WHERE id = $1`, runID).
			Scan(&projectID, &workItemID, &phase); err != nil {
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
			if _, err := tx.Exec(ctx, `INSERT INTO review_findings (id, organization_id, work_item_id, run_id, category,
				severity, repo, file, line, title, description, suggested_fix)
				VALUES ($1, $2, $3, $4, $5, $6::finding_severity, $7, $8, $9, $10, $11, $12)`,
				ids.New(ids.Finding), org, workItemID, runID, f.Category, f.Severity,
				db.Nullable(f.Repo), db.Nullable(f.File), nullableInt(f.Line), f.Title, f.Description, f.SuggestedFix); err != nil {
				return err
			}
		}
		_, err := ledger.Append(ctx, tx, ledger.Event{
			Type: delivery.EvReviewCompleted, OrganizationID: org, ProjectID: projectID, WorkItemID: workItemID, RunID: runID,
			ActorType: ledger.ActorAgent, ActorID: runID, Source: ledger.SourceRunner, CorrelationID: workItemID,
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
			JOIN workflow_runs w ON w.work_item_id = r.work_item_id AND w.organization_id = r.organization_id
			WHERE r.phase IS NOT NULL AND r.status IN ('completed', 'failed', 'aborted')
			  AND r.phase_notified_at IS NULL AND w.status = 'waiting'
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
