package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A conductor's publish, carried on by its own worker (SettlePublishes),
// apart from the phase sweep: lux pushes the conductor's checkout to its
// own branch (asked, then pushed); the comparisons with the task branch
// are checked; the publish is reserved (moving, delivery.ReservePublishTx)
// with each repository's intended move; each task branch is then
// fast-forwarded, never forced, its result kept as it happens; and what
// moved is recorded. A retry of a moving publish reads each branch from
// the forge first, so a move already made is recorded, never judged again.

// publishSlots is how many publishes are carried on at once.
const publishSlots = 3

// publishClaim is how long a claimed publish is left to its worker before
// another may take it (a worker that died).
const publishClaim = 2 * time.Minute

// askedGiveUp is how long a push lux accepted may go unreported before the
// publish is refused.
const askedGiveUp = 10 * time.Minute

// askedEvery is how often lux's events are read for a push it accepted and
// the conductor's follower has not reported.
const askedEvery = 3 * time.Second

// Back-off after a transient forge or lux failure: doubling from
// publishBackoffBase, capped at publishBackoffMax, or what the forge asked.
const (
	publishBackoffBase = 2 * time.Second
	publishBackoffMax  = 5 * time.Minute
)

// publishPushed records lux's git.push for a publish of the conductor's:
// due at once.
func publishPushed(ctx context.Context, tx pgx.Tx, requestID string, raw json.RawMessage) error {
	_, err := tx.Exec(ctx, `UPDATE conductor_publishes SET push_result = $2::jsonb, status = 'pushed', next_attempt_at = NULL,
			failures = 0
		WHERE request_id = $1 AND status IN ('requested', 'asked')`, requestID, raw)
	return err
}

// publishRow is a publish to carry on, with its conductor's lux Run.
type publishRow struct {
	ID, Org, ProjectID, TaskID, RunID, RequestID, Status string
	LuxRunID, LuxState, Message                          string
	PushResult                                           json.RawMessage
	WorkflowID                                           string
	Attempt                                              int
	Moves                                                map[string]delivery.Move
	Branch                                               string
	AskedLong                                            bool
	Failures                                             int
	// The database's clock as it was claimed.
	Now time.Time
}

func (p publishRow) ref() delivery.RunRef {
	return delivery.RunRef{Org: p.Org, ProjectID: p.ProjectID, TaskID: p.TaskID, RunID: p.RunID}
}

func (p publishRow) of() delivery.PublishOf {
	return delivery.PublishOf{ID: p.ID, TaskID: p.TaskID, RunID: p.RunID, WorkflowID: p.WorkflowID, Attempt: p.Attempt}
}

// SettlePublishes is one pass of the publish worker: it claims the
// publishes due, oldest due first, and carries each on, a few at once.
// Returns how many it claimed.
func (s *Syncer) SettlePublishes(ctx context.Context) (int, error) {
	var todo []publishRow
	if err := s.DB.InSystem(ctx, "conductor-publishes", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH due AS (
				SELECT id FROM conductor_publishes
				WHERE status IN ('requested', 'asked', 'pushed', 'moving') AND (next_attempt_at IS NULL OR next_attempt_at <= now())
				ORDER BY next_attempt_at NULLS FIRST, created_at LIMIT $1 FOR UPDATE SKIP LOCKED),
			claimed AS (UPDATE conductor_publishes c SET next_attempt_at = now() + $2::interval FROM due WHERE c.id = due.id
				RETURNING c.*)
			SELECT p.id, p.organization_id, r.project_id, p.task_id, p.run_id, p.request_id, p.status,
				COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''), p.message, p.push_result,
				COALESCE(p.workflow_run_id, ''), COALESCE(p.attempt, 0), p.moves, COALESCE(p.branch, ''),
				COALESCE(p.asked_at < now() - $3::interval, false), p.failures, now()
			FROM claimed p JOIN runs r ON r.id = p.run_id ORDER BY p.created_at`,
			publishSlots*4, publishClaim.String(), askedGiveUp.String())
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[publishRow])
		return err
	}); err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, publishSlots)
	for _, p := range todo {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := s.settlePublish(ctx, p); err != nil {
				s.Log.Warn("a conductor's publish failed", "publish", p.ID, "error", err)
				s.publishLater(ctx, p, err)
			}
		}()
	}
	wg.Wait()
	return len(todo), nil
}

// publishLater schedules a publish after a failure that may pass: a
// back-off doubling with each in a row, or what the forge asked for.
func (s *Syncer) publishLater(ctx context.Context, p publishRow, cause error) {
	wait := min(publishBackoffBase<<min(p.Failures, 16), publishBackoffMax)
	wait = max(wait, forge.RetryAfter(cause))
	s.publishAt(ctx, p, wait, true)
}

// publishAt sets when the publish is next carried on; failed counts a
// failure toward its back-off, else the count starts over.
func (s *Syncer) publishAt(ctx context.Context, p publishRow, after time.Duration, failed bool) {
	if err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conductor_publishes SET next_attempt_at = now() + $2::interval,
				failures = CASE WHEN $3 THEN failures + 1 ELSE 0 END
			WHERE id = $1 AND status IN ('requested', 'asked', 'pushed', 'moving')`, p.ID, after.String(), failed)
		return err
	}); err != nil {
		s.Log.Warn("scheduling a conductor's publish failed", "publish", p.ID, "error", err)
	}
}

// publishNow makes the publish due again at once: it moved on a status.
func (s *Syncer) publishNow(ctx context.Context, p publishRow) {
	s.publishAt(ctx, p, 0, false)
}

func (s *Syncer) refusePublish(ctx context.Context, p publishRow, why string) error {
	return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error { return delivery.PublishRefusedTx(ctx, tx, p.ref(), p.ID, why) })
}

// errPublishTransient wraps a forge or lux failure that may pass.
type errPublishTransient struct{ error }

func (e errPublishTransient) Unwrap() error { return e.error }

func (s *Syncer) settlePublish(ctx context.Context, p publishRow) error {
	switch p.Status {
	case delivery.PublishMoving:
		return s.carryMove(ctx, p)
	case delivery.PublishPushed:
		return s.measurePublish(ctx, p)
	}
	// Not pushed yet: refused once its conductor or task can no longer
	// publish, as nothing moved.
	var why string
	if err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) (err error) {
		why, err = delivery.PublishEligibleTx(ctx, tx, p.of())
		return err
	}); err != nil {
		return err
	}
	if why != "" {
		return s.refusePublish(ctx, p, why)
	}
	if p.Status == delivery.PublishRequested {
		return s.askPush(ctx, p)
	}
	return s.awaitPush(ctx, p)
}

// askPush asks lux to push the conductor's checkouts to its branch.
func (s *Syncer) askPush(ctx context.Context, p publishRow) error {
	if p.LuxRunID == "" || p.LuxState != "running" {
		return s.refusePublish(ctx, p, "your container is not running; publish again once you are")
	}
	if err := s.Lux.Push(ctx, p.LuxRunID, p.RequestID); err != nil {
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return s.refusePublish(ctx, p, "lux would not push: "+le.Message)
		}
		return errPublishTransient{err}
	}
	return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'asked', asked_at = now(), failures = 0,
				next_attempt_at = now() + $2::interval
			WHERE id = $1 AND status = 'requested'`, p.ID, askedEvery.String())
		return err
	})
}

// awaitPush looks for lux's report of a push it accepted that the
// conductor's follower has not recorded (it ended, or the orchestrator
// restarted): its git.push among the lux Run's events, by request id.
// Given up on, refused, once askedGiveUp has passed.
func (s *Syncer) awaitPush(ctx context.Context, p publishRow) error {
	if p.LuxRunID != "" {
		var after int64
		for range 20 {
			frames, err := s.Lux.Events(ctx, p.LuxRunID, after)
			if err != nil {
				if le, ok := lux.AsError(err); ok && !le.Retryable() {
					break
				}
				return errPublishTransient{err}
			}
			for _, f := range frames {
				after = max(after, f.EventID)
				if f.EventType != "git.push" {
					continue
				}
				var d struct {
					RequestID string `json:"requestId"`
				}
				if json.Unmarshal(f.EventData, &d) == nil && d.RequestID == p.RequestID {
					return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error { return publishPushed(ctx, tx, p.RequestID, f.EventData) })
				}
			}
			if len(frames) == 0 {
				break
			}
		}
	}
	if p.AskedLong {
		return s.refusePublish(ctx, p, "lux never reported the push")
	}
	s.publishAt(ctx, p, askedEvery, false)
	return nil
}

// pushResult is lux's git.push.
type pushResult struct {
	Results []struct {
		Repo, Branch, Commit, Status, Error string
	} `json:"results"`
}

// measurePublish checks a pushed publish against the task branch — each
// pushed commit descends from its head, something was committed, and it
// is within the project's limit — then reserves it with its moves, and
// moves. Refused, with nothing moved, when any check fails or its
// conductor or task can no longer publish.
func (s *Syncer) measurePublish(ctx context.Context, p publishRow) error {
	var target delivery.PublishTarget
	var repos []delivery.Repository
	if err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) (err error) {
		if target, err = delivery.LoadPublishTarget(ctx, tx, p.of()); err != nil {
			return err
		}
		repos, err = delivery.TaskRepositories(ctx, tx, p.TaskID)
		return err
	}); err != nil {
		return err
	}
	if target.Refused != "" {
		return s.refusePublish(ctx, p, target.Refused)
	}
	if !target.Settled {
		// A step of the delivery is running: its heads may move.
		s.publishAt(ctx, p, time.Second, false)
		return nil
	}
	var push pushResult
	if err := json.Unmarshal(p.PushResult, &push); err != nil {
		return s.refusePublish(ctx, p, "lux's push result is unreadable")
	}
	byName := map[string]delivery.Repository{}
	for _, repo := range repos {
		byName[repo.Name] = repo
	}
	gh, err := s.Forges.For(ctx, p.Org)
	if err != nil {
		return err
	}
	if gh == nil {
		return s.refusePublish(ctx, p, "no forge credential to publish with")
	}
	moves := map[string]delivery.Move{}
	lines, files := 0, 0
	for _, res := range push.Results {
		repo, known := byName[res.Repo]
		switch {
		case res.Status == "skipped" || known && repo.Access == "read":
			continue
		case !known:
			return s.refusePublish(ctx, p, fmt.Sprintf("lux pushed %s, which this task does not name", res.Repo))
		case res.Status != "pushed" && res.Status != "up-to-date":
			return s.refusePublish(ctx, p, fmt.Sprintf("the push of %s failed: %s", res.Repo, res.Error))
		case res.Commit == "":
			continue
		}
		slug := forge.SlugFromURL(repo.URL)
		if slug == "" {
			return s.refusePublish(ctx, p, "no forge to publish "+repo.URL+" to")
		}
		// Measured against the task branch's head, or the default branch
		// where the task has none yet.
		from, base := target.Heads[res.Repo], target.Heads[res.Repo]
		if from == "" {
			from = repo.DefaultBranch
		}
		cmp, err := gh.Compare(ctx, slug, from, res.Commit)
		if err != nil {
			if !forgeRefused(err) {
				return errPublishTransient{err}
			}
			return s.refusePublish(ctx, p, fmt.Sprintf("comparing %s with the task branch failed: %v", res.Repo, err))
		}
		if cmp.BehindBy > 0 {
			return s.refusePublish(ctx, p, fmt.Sprintf(delivery.RefusedBehind, target.Branch))
		}
		if cmp.AheadBy == 0 {
			continue
		}
		lines += cmp.Lines()
		files += len(cmp.Files)
		moves[res.Repo] = delivery.Move{RepoID: repo.ID, Slug: slug, From: from, Base: base, Head: res.Commit,
			ChangedPaths: db.NonNil(cmp.Paths()), Lines: cmp.Lines(), Status: delivery.MovePending}
	}
	if len(moves) == 0 {
		return s.refusePublish(ctx, p, delivery.RefusedNothing)
	}
	if why := target.OverLimit(lines, files); why != "" {
		return s.refusePublish(ctx, p, why)
	}
	var why string
	err = s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) (err error) {
		why, err = delivery.ReservePublishTx(ctx, tx, p.of(), target.Heads, moves)
		return err
	})
	switch {
	case errors.Is(err, delivery.ErrNotReserved):
		// Measured against what is no longer the task's: again.
		s.publishAt(ctx, p, time.Second, false)
		return nil
	case err != nil:
		return err
	case why != "":
		return s.refusePublish(ctx, p, why)
	}
	p.Status, p.Moves, p.Branch, p.WorkflowID = delivery.PublishMoving, moves, target.Branch, target.WorkflowID
	return s.carryMove(ctx, p)
}

// carryMove takes a moving publish to its end. Each repository not known
// to have moved is first read from the forge: at its intended head, it
// moved (an earlier attempt's request landed). Those still to move are
// judged again under the publish's locks (delivery.RecheckMovingTx) —
// never those that moved — then fast-forwarded one by one, each result
// kept as it happens. Then what moved is recorded (recordPublish), or,
// with nothing moved, the publish is refused.
//
// A forge refusing for good (forgeRefused) refuses a repository whose
// move was never sent. One whose move was sent and whose outcome the
// forge will not confirm is asked again with back-off until
// delivery.MoveUncertainFor has passed since it was first sent, then
// settled as stalled: never recorded as moved, never as not moved.
func (s *Syncer) carryMove(ctx context.Context, p publishRow) error {
	gh, err := s.Forges.For(ctx, p.Org)
	if err != nil {
		return err
	}
	keep := func(repo string, m delivery.Move) error {
		p.Moves[repo] = m
		return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error { return delivery.MovedTx(ctx, tx, p.ID, repo, m) })
	}
	// unconfirmed settles a sent move as stalled once its time is up, or
	// asks again later.
	unconfirmed := func(repo string, m delivery.Move, cause error) error {
		if p.Now.Sub(*m.AttemptedAt) < delivery.MoveUncertainFor {
			return errPublishTransient{cause}
		}
		m.Status, m.Error = delivery.MoveStalled, fmt.Sprintf("could not confirm it moved to %s: %v", m.Head, cause)
		return keep(repo, m)
	}
	names := slices.Sorted(maps.Keys(p.Moves))
	var pending []string
	var transient error
	for _, name := range names {
		m := p.Moves[name]
		if m.Status != delivery.MovePending {
			continue
		}
		at, err := branchSHA(ctx, gh, m.Slug, p.Branch)
		if err != nil {
			switch {
			case m.AttemptedAt != nil:
				err = unconfirmed(name, m, err)
			case forgeRefused(err):
				m.Status, m.Error = delivery.MoveRefused, fmt.Sprintf("reading %s's task branch failed: %v", name, err)
				err = keep(name, m)
			default:
				err = errPublishTransient{err}
			}
			var te errPublishTransient
			if errors.As(err, &te) {
				transient = err
				continue
			}
			if err != nil {
				return err
			}
			continue
		}
		if at == m.Head {
			m.Status = delivery.MoveMoved
			if err := keep(name, m); err != nil {
				return err
			}
			continue
		}
		pending = append(pending, name)
	}
	if transient != nil {
		// Nothing more is sent while a repository's state is unknown.
		return transient
	}
	if len(pending) > 0 {
		var why string
		if err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) (err error) {
			why, err = delivery.RecheckMovingTx(ctx, tx, p.of())
			return err
		}); err != nil {
			return err
		}
		for _, name := range pending {
			m := p.Moves[name]
			if why != "" {
				m.Status, m.Error = delivery.MoveRefused, why
				if err := keep(name, m); err != nil {
					return err
				}
				continue
			}
			if m.AttemptedAt == nil {
				var at time.Time
				if err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) (err error) {
					at, err = delivery.AttemptMoveTx(ctx, tx, p.ID, name)
					return err
				}); err != nil {
					return err
				}
				m.AttemptedAt = &at
				p.Moves[name] = m
			}
			err := gh.FastForward(ctx, m.Slug, p.Branch, m.Head)
			switch {
			case err == nil:
				m.Status = delivery.MoveMoved
			case !forgeRefused(err):
				// Sent, and no answer to trust: it may have landed.
				if err := unconfirmed(name, m, err); err != nil {
					return err
				}
				continue
			default:
				why := fmt.Sprintf("the forge refused the move: %v", err)
				if e := (*forge.Error)(nil); errors.As(err, &e) && e.Status == 422 {
					why = fmt.Sprintf("the task branch moved meanwhile (%v): `git merge lux/%s`, then publish again", err, p.Branch)
				}
				m.Status, m.Error = delivery.MoveRefused, why
			}
			if err := keep(name, m); err != nil {
				return err
			}
		}
	}
	return s.recordPublish(ctx, p)
}

// errNoForge: the organization has no forge credential to move with.
var errNoForge = errors.New("no forge credential to read the task branch with")

// branchSHA is forge.GitHub.BranchSHA, errNoForge without a forge.
func branchSHA(ctx context.Context, gh *forge.GitHub, slug, branch string) (string, error) {
	if gh == nil {
		return "", errNoForge
	}
	return gh.BranchSHA(ctx, slug, branch)
}

// forgeRefused says the forge refused for good: 401 or 403 that is not a
// rate limit, 404 or 422, or no forge credential. Rate limits, 5xx, no
// answer and anything else may pass.
func forgeRefused(err error) bool {
	if errors.Is(err, errNoForge) {
		return true
	}
	var e *forge.Error
	if !errors.As(err, &e) || forge.Transient(err) {
		return false
	}
	switch e.Status {
	case 401, 403, 404, 422:
		return true
	}
	return false
}

// recordPublish records a publish whose moves are all settled: the pull
// request heads, git.commit_created and the delivery's heads from what
// moved, and the outcome wake; refused when nothing moved. A step holding
// the delivery puts the recording off, never the moves.
func (s *Syncer) recordPublish(ctx context.Context, p publishRow) error {
	heads := map[string]delivery.PublishHead{}
	refused := map[string]string{}
	stalled := map[string]string{}
	for name, m := range p.Moves {
		switch m.Status {
		case delivery.MoveMoved:
			heads[name] = delivery.PublishHead{SHA: m.Head, Base: m.Base, ChangedPaths: db.NonNil(m.ChangedPaths), Lines: m.Lines}
		case delivery.MoveRefused:
			refused[name] = m.Error
		case delivery.MoveStalled:
			stalled[name] = m.Head
		}
	}
	if len(heads) == 0 && len(stalled) > 0 {
		return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error { return delivery.PublishStalledTx(ctx, tx, p.ref(), p.ID, stalled) })
	}
	if len(heads) == 0 {
		var why []string
		for _, name := range slices.Sorted(maps.Keys(refused)) {
			why = append(why, refused[name])
		}
		return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
			return delivery.PublishRefusedTx(ctx, tx, p.ref(), p.ID, strings.Join(why, "; "))
		})
	}
	r := phaseRun{ID: p.RunID, Org: p.Org, ProjectID: p.ProjectID, TaskID: p.TaskID}
	err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
		for _, name := range slices.Sorted(maps.Keys(heads)) {
			h, m := heads[name], p.Moves[name]
			// As a phase's publish: the pull request on the branch has a new
			// head, whose checks are yet to run.
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = $3, head_seen_at = now(),
				checks = CASE WHEN had_ci THEN 'pending' ELSE 'unknown' END::check_state, updated_at = now()
				WHERE task_id = $1 AND repository_id = $2 AND head_branch = $4 AND state IN ('open', 'draft')
				  AND head_sha IS DISTINCT FROM $3`, p.TaskID, m.RepoID, h.SHA, p.Branch); err != nil {
				return err
			}
			if err := s.event(ctx, tx, r, delivery.EvGitCommitCreated, ledger.ActorAgent, map[string]any{
				"repo": name, "baseSha": h.Base, "headSha": h.SHA, "branch": p.Branch, "changedPaths": h.ChangedPaths,
				"lines": h.Lines, "by": delivery.RoleConductor, "publishId": p.ID, "message": p.Message}); err != nil {
				return err
			}
		}
		return delivery.PublishedTx(ctx, tx, p.ref(), p.ID, p.WorkflowID, heads, refused, stalled)
	})
	if errors.Is(err, delivery.ErrStepRunning) {
		// Sooner than a waiting step's retry (delivery.ErrPublishMoving),
		// so the two do not keep meeting.
		s.publishAt(ctx, p, 200*time.Millisecond, false)
		return nil
	}
	return err
}
