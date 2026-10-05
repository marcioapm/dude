package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The conductor's edits, as the syncer carries them (delivery/edits.go is
// the delivery's side).
//
// Its checkout is kept current: before a conductor hears anything queued
// for it — a wake note, a person's message — and on every resume, each
// repository it may change is synced to the task branch in lux's
// fast-forward mode, which never loses local work. How that went is told
// with what it hears when the checkout did not simply come current. A lux
// that refuses the mode leaves the conductor read-only, as before.
//
// Its publish: lux pushes its checkout to its own branch; then, as a
// phase's work is published, the task branch is fast-forwarded to it,
// after the checks only the pushed commit can answer — current, small,
// and something to publish.

// checkoutSyncWait bounds how long what is queued for a conductor waits
// for its checkout's sync to be reported: past it, it is heard anyway.
const checkoutSyncWait = 30 * time.Second

// currentRefs are the conductor's repositories to bring to the task
// branch: those it may change and holds, where the task branch exists.
// None once its checkout is read-only, or with no delivery yet.
func (s *Syncer) currentRefs(ctx context.Context, r phaseRun) ([]lux.SyncRef, error) {
	var refs []lux.SyncRef
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var readOnly string
		var held []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(checkout_read_only, ''), lux_repositories FROM runs WHERE id = $1`, r.ID).
			Scan(&readOnly, &held); err != nil {
			return err
		}
		if readOnly != "" {
			return nil
		}
		d, err := delivery.ReadDelivery(ctx, tx, r.TaskID)
		if err != nil || d == nil {
			return err
		}
		branch := d.State.Branch
		if branch == "" {
			branch = delivery.BranchFor(r.TaskID, d.State.Attempt)
		}
		repos, err := delivery.TaskRepositories(ctx, tx, r.TaskID)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if repo.Access == "read" || d.State.Heads[repo.Name] == "" || !slices.Contains(held, repo.Name) {
				continue
			}
			refs = append(refs, lux.SyncRef{Repo: repo.Name, Ref: branch, Mode: lux.SyncFastForward})
		}
		return nil
	})
	return refs, err
}

// resumeSyncID names a resume's sync: lux reports its git.sync events with
// no request id.
func resumeSyncID(before lux.Run) string { return fmt.Sprintf("resume-%d", nextEpoch(before)) }

// syncAsked records the sync lux was asked for: what the conductor's next
// input waits for.
func (s *Syncer) syncAsked(ctx context.Context, r phaseRun, id string, refs []lux.SyncRef) error {
	repos := make([]string, 0, len(refs))
	for _, ref := range refs {
		repos = append(repos, ref.Repo)
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET checkout_sync_id = $2, checkout_sync_repos = $3, checkout_sync_at = now()
			WHERE id = $1`, r.ID, id, repos)
		return err
	})
}

// checkoutReadOnly records that this lux cannot keep the conductor's
// checkout current: it stays read-only, Chat says so, and so does the
// conductor's next input.
func (s *Syncer) checkoutReadOnly(ctx context.Context, r phaseRun, cause error) error {
	why := "this lux cannot keep your checkout current"
	if le, ok := lux.AsError(cause); ok && le.Message != "" {
		why += " (" + oneLineClip(le.Message, 120) + ")"
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET checkout_read_only = $2, checkout_sync_id = NULL, checkout_sync_repos = '{}'
			WHERE id = $1 AND checkout_read_only IS NULL`, r.ID, why)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvChatNotice, OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
			CorrelationID: r.TaskID, Payload: map[string]any{"about": "checkout_read_only",
				"text": "This lux cannot keep the conductor's checkout current: it reads and does not edit, for this conversation."}}); err != nil {
			return err
		}
		return tellConductor(ctx, tx, r, "checkout:read-only:"+r.ID,
			"Your checkout cannot be kept current by this lux: do not edit; publish is refused. Delegate changes (start_phase).")
	})
}

// keepCurrent runs before what is queued for a running conductor is sent:
// a sync of its checkout when nothing was synced since the input was
// queued. Returns whether the input waits (a sync in flight).
func (s *Syncer) keepCurrent(ctx context.Context, r phaseRun) (bool, error) {
	var pending, due bool
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// In flight, short of its bound; or something queued since the last.
		return tx.QueryRow(ctx, `SELECT r.checkout_sync_id IS NOT NULL AND cardinality(r.checkout_sync_repos) > 0
				AND r.checkout_sync_at > now() - make_interval(secs => $2),
			r.checkout_read_only IS NULL AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id
				AND d.sent_at IS NULL AND d.failed_at IS NULL AND d.created_at > COALESCE(r.checkout_sync_at, '-infinity'))
			FROM runs r WHERE r.id = $1`, r.ID, checkoutSyncWait.Seconds()).Scan(&pending, &due)
	}); err != nil {
		return false, err
	}
	if pending {
		return true, nil
	}
	if !due {
		return false, nil
	}
	refs, err := s.currentRefs(ctx, r)
	if err != nil {
		return false, err
	}
	id := fmt.Sprintf("sync-%s-%d", r.ID, time.Now().UnixNano())
	// Recorded before lux is asked (its git.sync may come first), and
	// cleared when it was not.
	if err := s.syncAsked(ctx, r, id, refs); err != nil || len(refs) == 0 {
		return false, err
	}
	err = s.Lux.SyncRun(ctx, r.LuxRunID, id, refs)
	switch {
	case lux.SyncModesRefused(err):
		return true, s.checkoutReadOnly(ctx, r, err)
	case err != nil:
		// Not running after all, or lux unreachable: the input goes, and
		// the next one tries again.
		s.Log.Debug("syncing a conductor's checkout failed", "run", r.ID, "error", err)
		return false, s.syncAsked(ctx, r, id, nil)
	}
	return true, nil
}

// checkoutSynced records one repository's git.sync of a conductor's
// checkout: one of the sync in flight's, which is then no longer awaited;
// told when it did not simply come current.
func (t *translator) checkoutSynced(ctx context.Context, tx pgx.Tx, s *Syncer, d map[string]any) error {
	str := func(k string) string { v, _ := d[k].(string); return v }
	num := func(k string) int { v, _ := d[k].(float64); return int(v) }
	flag := func(k string) bool { v, _ := d[k].(bool); return v }
	if str("mode") == "" || str("mode") == lux.SyncMove {
		return nil
	}
	var syncID string
	err := tx.QueryRow(ctx, `UPDATE runs SET checkout_sync_repos = array_remove(checkout_sync_repos, $2),
			checkout_synced_at = CASE WHEN checkout_sync_repos = ARRAY[$2]::text[] THEN now() ELSE checkout_synced_at END
		WHERE id = $1 AND checkout_sync_id IS NOT NULL AND $2 = ANY (checkout_sync_repos)
		  AND (NULLIF($3, '') IS NULL OR checkout_sync_id = $3)
		RETURNING checkout_sync_id`, t.run.ID, str("repo"), str("requestId")).Scan(&syncID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	line := delivery.CheckoutLine(str("repo"), str("ref"), str("status"), str("error"), num("ahead"), num("behind"),
		flag("dirty"), flag("diverged"))
	if line == "" {
		return nil
	}
	return tellConductor(ctx, tx, t.run, "checkout:"+syncID+":"+str("repo"), line)
}

// tellConductor gives the conductor a line about its checkout with what it
// hears next: added to a wake note queued for it and not yet sent, else a
// reason that wakes it (recorded as heard when it rode on the note).
func tellConductor(ctx context.Context, tx pgx.Tx, r phaseRun, key, line string) error {
	// The note to ride on, read unlocked: wakes are locked before
	// directives.
	var note string
	err := tx.QueryRow(ctx, `SELECT d.id FROM directives d JOIN conductor_wake_attempts a ON a.directive_id = d.id
		WHERE d.run_id = $1 AND d.sent_at IS NULL AND d.claimed_at IS NULL AND d.failed_at IS NULL
		ORDER BY d.created_at LIMIT 1`, r.ID).Scan(&note)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	var wake string
	err = tx.QueryRow(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, delivered_at, conductor_run_id)
		VALUES ($1, $2, $3, 'checkout', $4, $5, CASE WHEN $6 THEN now() END, CASE WHEN $6 THEN $7 END)
		ON CONFLICT (task_id, key) DO NOTHING RETURNING id`, ids.New("cwk"), r.Org, r.TaskID, key,
		oneLineClip(line, 300), note != "", r.ID).Scan(&wake)
	if db.IsNotFound(err) || note == "" {
		return nil // told already, or a reason that wakes it
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE directives SET text = text || E'\n\n' || $2
		WHERE id = $1 AND sent_at IS NULL AND claimed_at IS NULL AND failed_at IS NULL`, note, line)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	// Claimed meanwhile: the line wakes it on its own.
	_, err = tx.Exec(ctx, `UPDATE conductor_wakes SET delivered_at = NULL, conductor_run_id = NULL WHERE id = $1`, wake)
	return err
}

func oneLineClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- publish -------------------------------------------------------------

// publishPushed records lux's git.push for a publish of the conductor's.
func publishPushed(ctx context.Context, tx pgx.Tx, requestID string, raw json.RawMessage) error {
	_, err := tx.Exec(ctx, `UPDATE conductor_publishes SET push_result = $2::jsonb, status = 'pushed'
		WHERE request_id = $1 AND status IN ('requested', 'asked')`, requestID, raw)
	return err
}

// publishRow is a publish to carry on, with its conductor's lux Run.
type publishRow struct {
	ID, Org, ProjectID, TaskID, RunID, RequestID, Status string
	LuxRunID, LuxState, Message                          string
	PushResult                                           json.RawMessage
	BaseSHAs                                             map[string]string
}

// settlePublishes carries each publish on: asked of lux, then, once
// pushed, published or refused.
func (s *Syncer) settlePublishes(ctx context.Context) error {
	var todo []publishRow
	if err := s.DB.InSystem(ctx, "phase-sync", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.id, p.organization_id, r.project_id, p.task_id, p.run_id, p.request_id, p.status,
				COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''), p.message, p.push_result, r.base_shas
			FROM conductor_publishes p JOIN runs r ON r.id = p.run_id
			WHERE p.status IN ('requested', 'pushed') ORDER BY p.created_at LIMIT 100`)
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[publishRow])
		return err
	}); err != nil {
		return err
	}
	for _, p := range todo {
		var err error
		if p.Status == delivery.PublishRequested {
			err = s.askPush(ctx, p)
		} else {
			err = s.settlePublish(ctx, p)
		}
		if err != nil {
			s.Log.Warn("a conductor's publish failed", "publish", p.ID, "error", err)
		}
	}
	return nil
}

func (p publishRow) ref() delivery.RunRef {
	return delivery.RunRef{Org: p.Org, ProjectID: p.ProjectID, TaskID: p.TaskID, RunID: p.RunID}
}

func (s *Syncer) refusePublish(ctx context.Context, p publishRow, why string) error {
	return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error { return delivery.PublishRefusedTx(ctx, tx, p.ref(), p.ID, why) })
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
		return err
	}
	return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'asked' WHERE id = $1 AND status = 'requested'`, p.ID)
		return err
	})
}

// settlePublish takes a pushed publish to the task branch: refused, with
// nothing moved, when its conductor no longer decides, another writer is
// at work, a pushed commit does not descend from the task branch's head,
// nothing was committed, or it is past the project's limit; else each
// repository's task branch is fast-forwarded (never forced), its pull
// request's head updated, and the commit recorded as the conductor's.
func (s *Syncer) settlePublish(ctx context.Context, p publishRow) error {
	var target delivery.PublishTarget
	var repos []delivery.Repository
	if err := s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) (err error) {
		if target, err = delivery.LoadPublishTarget(ctx, tx, p.TaskID, p.RunID); err != nil {
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
		return nil // a step of the delivery is running: its heads may move
	}
	var push struct {
		Results []struct {
			Repo, Branch, Commit, Status, Error string
		} `json:"results"`
	}
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
	type move struct {
		repo     delivery.Repository
		slug     string
		from, to string
		cmp      forge.Comparison
	}
	var moves []move
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
		from := target.Heads[res.Repo]
		if from == "" {
			from = repo.DefaultBranch
		}
		cmp, err := gh.Compare(ctx, slug, from, res.Commit)
		if err != nil {
			if forge.Transient(err) {
				return err
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
		moves = append(moves, move{repo: repo, slug: slug, from: from, to: res.Commit, cmp: cmp})
	}
	if len(moves) == 0 {
		return s.refusePublish(ctx, p, delivery.RefusedNothing)
	}
	if why := target.OverLimit(lines, files); why != "" {
		return s.refusePublish(ctx, p, why)
	}
	heads := map[string]delivery.PublishHead{}
	for _, m := range moves {
		if err := gh.FastForward(ctx, m.slug, target.Branch, m.to); err != nil {
			if forge.Transient(err) {
				return err
			}
			return s.refusePublish(ctx, p, fmt.Sprintf("the task branch in %s moved meanwhile (%v): `git merge lux/%s`, then publish again",
				m.repo.Name, err, target.Branch))
		}
		base := m.from
		if target.Heads[m.repo.Name] == "" {
			base = ""
		}
		heads[m.repo.Name] = delivery.PublishHead{SHA: m.to, Base: base, ChangedPaths: db.NonNil(m.cmp.Paths()), Lines: m.cmp.Lines()}
	}
	r := phaseRun{ID: p.RunID, Org: p.Org, ProjectID: p.ProjectID, TaskID: p.TaskID}
	return s.DB.InOrg(ctx, p.Org, func(tx pgx.Tx) error {
		for name, h := range heads {
			// As a phase's publish: the pull request on the branch has a new
			// head, whose checks are yet to run.
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = $3, head_seen_at = now(),
				checks = CASE WHEN had_ci THEN 'pending' ELSE 'unknown' END::check_state, updated_at = now()
				WHERE task_id = $1 AND repository_id = $2 AND head_branch = $4 AND state IN ('open', 'draft')
				  AND head_sha IS DISTINCT FROM $3`, p.TaskID, byName[name].ID, h.SHA, target.Branch); err != nil {
				return err
			}
			if err := s.event(ctx, tx, r, delivery.EvGitCommitCreated, ledger.ActorAgent, map[string]any{
				"repo": name, "baseSha": h.Base, "headSha": h.SHA, "branch": target.Branch, "changedPaths": h.ChangedPaths,
				"lines": h.Lines, "by": delivery.RoleConductor, "publishId": p.ID, "message": p.Message}); err != nil {
				return err
			}
		}
		return delivery.PublishedTx(ctx, tx, p.ref(), p.ID, target.WorkflowID, heads)
	})
}
