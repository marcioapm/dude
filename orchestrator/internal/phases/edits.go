package phases

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
// and something to publish (publish.go).

// checkoutSyncWait bounds how long what is queued for a conductor waits
// for its checkout's sync to be reported: past it, it is heard anyway.
// Syncer.CheckoutSyncWait overrides it for tests.
const checkoutSyncWait = 30 * time.Second

func (s *Syncer) syncWait() time.Duration {
	if s.CheckoutSyncWait > 0 {
		return s.CheckoutSyncWait
	}
	return checkoutSyncWait
}

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
// queued. hold: the input waits for a sync in flight. issued: a sync was
// asked for now — progress for the sweep; waiting on one already asked
// is not. upTo, when set, bounds what may be sent now by when it was
// queued: a sync in flight past its bound releases only the batch that
// waited on it, without its outcome line, before newer input asks for a
// sync of its own.
//
// The bound counts from the oldest input waiting, never from the latest
// sync: input that keeps arriving cannot hold the first back.
func (s *Syncer) keepCurrent(ctx context.Context, r phaseRun) (hold, issued bool, upTo *time.Time, err error) {
	var inFlight, expired, due bool
	var askedAt *time.Time
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.checkout_sync_id IS NOT NULL AND cardinality(r.checkout_sync_repos) > 0,
			COALESCE((SELECT min(d.created_at) FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL)
				<= now() - make_interval(secs => $2), false),
			r.checkout_read_only IS NULL AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id
				AND d.sent_at IS NULL AND d.failed_at IS NULL AND d.created_at > COALESCE(r.checkout_sync_at, '-infinity')),
			r.checkout_sync_at
			FROM runs r WHERE r.id = $1`, r.ID, s.syncWait().Seconds()).Scan(&inFlight, &expired, &due, &askedAt)
	}); err != nil {
		return false, false, nil, err
	}
	if inFlight {
		if !expired {
			return true, false, nil, nil
		}
		// Past its bound: no longer waited on; what it covered goes now.
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET checkout_sync_id = NULL, checkout_sync_repos = '{}' WHERE id = $1`, r.ID)
			return err
		}); err != nil {
			return false, false, nil, err
		}
		return false, false, askedAt, nil
	}
	if !due {
		return false, false, nil, nil
	}
	refs, err := s.currentRefs(ctx, r)
	if err != nil {
		return false, false, nil, err
	}
	id := fmt.Sprintf("sync-%s-%d", r.ID, time.Now().UnixNano())
	// Recorded before lux is asked (its git.sync may come first), and
	// cleared when it was not.
	if err := s.syncAsked(ctx, r, id, refs); err != nil || len(refs) == 0 {
		return false, false, nil, err
	}
	err = s.Lux.SyncRun(ctx, r.LuxRunID, id, refs)
	switch {
	case lux.SyncModesRefused(err):
		return true, true, nil, s.checkoutReadOnly(ctx, r, err)
	case err != nil:
		// Not running after all, or lux unreachable: the input goes, and
		// the next one tries again.
		s.Log.Debug("syncing a conductor's checkout failed", "run", r.ID, "error", err)
		return false, false, nil, s.syncAsked(ctx, r, id, nil)
	}
	return true, true, nil, nil
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
