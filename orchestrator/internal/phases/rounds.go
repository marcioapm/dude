package phases

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/images"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A reviewer's or tester's next round may take over its previous round's
// lux Run (delivery's continueOrCreateTx): the new row is paused, to be
// resumed with each checkout moved to the head the round starts from. One
// that cannot be (lux refuses the resume, the secrets it had are gone, the
// harness came back with a blank session, lux stopped it at its time
// limit: holdTimedOut) is replaced in its slot by a fresh Run given
// its predecessor's conversation (replaceRound).

// roundRefs is a continuation's sync on its resume: each repository the
// lux Run holds whose checkout is not yet at the commit the round starts
// from, moved there (lux's move mode: a reviewer and a tester change
// nothing). Empty for any other Run, and once every checkout is there.
func (s *Syncer) roundRefs(ctx context.Context, r phaseRun) ([]lux.SyncRef, error) {
	if r.talker() || r.Phase == "" {
		return nil, nil
	}
	var refs []lux.SyncRef
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var resumes bool
		var held []string
		if err := tx.QueryRow(ctx, `SELECT continues_how IS NOT DISTINCT FROM 'resume', lux_repositories FROM runs WHERE id = $1`, r.ID).
			Scan(&resumes, &held); err != nil || !resumes {
			return err
		}
		repos, err := delivery.TaskRepositories(ctx, tx, r.TaskID)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			name := lux.SpecName(repo.Name)
			to := r.BaseRefs[repo.Name]
			if to == "" || !slices.Contains(held, name) || lookupBase(r.BaseSHAs, repos, repo.Name) == to {
				continue
			}
			refs = append(refs, lux.SyncRef{Repo: name, Ref: to, Mode: roundSyncMode(r)})
		}
		return nil
	})
	slices.SortFunc(refs, func(a, b lux.SyncRef) int { return strings.Compare(a.Repo, b.Repo) })
	return refs, err
}

// roundSyncMode is how a continuation's checkout moves: a fixer's is at
// its own pushed commit, which the round's head descends from unless
// someone else moved the branch, and must never be reset (fast-forward);
// a reviewer's or tester's changes nothing (move).
func roundSyncMode(r phaseRun) string {
	if delivery.Publishes[r.Phase] {
		return lux.SyncFastForward
	}
	return ""
}

// evRoundUnsynced is a continuing writer's checkout that its resume's sync
// did not bring to the round's head. Payload: {repo, status, error?}.
const evRoundUnsynced = "run.round.unsynced"

// roundSyncLanded is, for a continuing writer not yet taken back up, why
// its checkout is not at the round's head ("" when it is, or it is not
// one), and whether its round message waits for the sync's report. The
// wait is bounded by syncWait from when the stream reported the resumed
// epoch running (run_resumes.running_at: the row is the continuation's
// own, so any is since the handover); before that it holds. A running
// continuation whose resume went untimed (that row is best effort) is
// bounded from its round message instead.
func (s *Syncer) roundSyncLanded(ctx context.Context, r phaseRun) (hold bool, why string, err error) {
	if !delivery.Publishes[r.Phase] || r.talker() {
		return false, "", nil
	}
	var expired *bool
	var unsynced *string
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
				(SELECT e.payload->>'repo' || ' ' || (e.payload->>'status') FROM events e
				 WHERE e.run_id = r.id AND e.event_type = $2 ORDER BY e.cursor LIMIT 1),
				COALESCE((SELECT max(rr.running_at) FROM run_resumes rr WHERE rr.run_id = r.id),
					CASE WHEN r.lux_state = 'running' THEN (SELECT min(d.created_at) FROM directives d
						WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL) END)
					<= now() - make_interval(secs => $3)
			FROM runs r WHERE r.id = $1 AND r.continues_how = 'resume' AND r.finishes = 0`,
			r.ID, evRoundUnsynced, s.syncWait().Seconds()).Scan(&unsynced, &expired)
	})
	if db.IsNotFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if unsynced != nil {
		return false, "its checkout could not be brought to the round's head (" + *unsynced + ")", nil
	}
	refs, err := s.roundRefs(ctx, r)
	if err != nil || len(refs) == 0 {
		return false, "", err
	}
	if expired != nil && *expired {
		return false, "its checkout's sync to the round's head was never reported", nil
	}
	return true, "", nil
}

// roundSecrets refuses a continuation's resume when its phase is now given
// a project secret the lux Run was not: lux keeps the names it was
// submitted with, and the agent would go on without it. Fewer is
// errSecretsGone's, which spec reports.
func (s *Syncer) roundSecrets(ctx context.Context, r phaseRun) error {
	recipient := SecretRecipientFor(r.Phase, r.Role)
	if recipient == "" || r.talker() {
		return nil
	}
	var added []string
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.name FROM runs r, secrets_in_force(r.organization_id, r.project_id) s
			WHERE r.id = $1 AND r.continues_how = 'resume' AND r.finishes = 0 AND $2 = ANY(s.recipients)
			  AND NOT (s.name = ANY(r.project_secret_names))
			ORDER BY s.name`, r.ID, recipient)
		if err != nil {
			return err
		}
		added, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return err
	}
	if len(added) > 0 {
		return errCannotResume{errors.New("its phase is now given " + strings.Join(added, ", "))}
	}
	return nil
}

// roundImage refuses a continuation's first resume when a fresh Run of its
// role would now start on another image than lux stored for the lux Run.
// A later resume (finishes > 0) is a person's pause/resume and is not
// checked. An image still being prepared or refused also goes fresh: the
// replacement's submit waits for it or reports it as any submit does. Any
// other failure to resolve it is returned, for the resume to be retried.
func (s *Syncer) roundImage(ctx context.Context, r phaseRun, stored lux.StoredSpec) error {
	var continued bool
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT continues_how IS NOT DISTINCT FROM 'resume' AND finishes = 0 FROM runs WHERE id = $1`, r.ID).Scan(&continued)
	}); err != nil || !continued {
		return err
	}
	ref, _, _, err := s.image(ctx, r)
	if errors.As(err, new(images.Waiting)) || errors.As(err, new(images.Refused)) {
		return errCannotResume{err}
	}
	if err != nil {
		return err
	}
	if ref != stored.Image.Ref {
		return errCannotResume{errors.New("its runtime image changed")}
	}
	return nil
}

// syncedBase records a continuation's checkout moved by its resume: the
// commit it now starts from is its base (what the round reviews from, and
// what roundRefs compares with), as git.checkout records a submit's.
func syncedBase(ctx context.Context, tx pgx.Tx, r phaseRun, data map[string]any) error {
	if r.talker() || r.Phase == "" {
		return nil
	}
	repo, _ := data["repo"].(string)
	to, _ := data["to"].(string)
	status, _ := data["status"].(string)
	if mode, _ := data["mode"].(string); mode == lux.SyncFastForward && repo != "" && status != "fast-forward" && status != "up-to-date" {
		// Kept, ahead, diverged or failed: someone else moved the branch
		// (a person's push, the base merged in). The checkout is not the
		// round's, and the continuation is replaced (roundSyncLanded).
		var continued bool
		if err := tx.QueryRow(ctx, `SELECT continues_how IS NOT DISTINCT FROM 'resume' FROM runs WHERE id = $1`, r.ID).Scan(&continued); err != nil || !continued {
			return err
		}
		payload := map[string]any{"repo": repo, "status": status}
		if e, _ := data["error"].(string); e != "" {
			payload["error"] = e
		}
		_, err := ledger.Append(ctx, tx, r.ref().Event(evRoundUnsynced, ledger.ActorSystem, payload))
		return err
	}
	if repo == "" || !isSHA(to) || status == "failed" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE runs SET base_shas = base_shas || jsonb_build_object($2::text, $3::text)
		WHERE id = $1 AND continues_how = 'resume'`, r.ID, repo, to)
	return err
}

// roundLost is why a continuation's agent lost its earlier rounds — the
// harness could not reload its session on the resume (sessionKept
// recorded agent.session.replaced) — "" when it did not.
func (s *Syncer) roundLost(ctx context.Context, r phaseRun) (string, error) {
	var why *string
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(e.payload->>'reason', ''), 'the harness started a new session')
			FROM runs r JOIN events e ON e.run_id = r.id AND e.event_type = $2
			WHERE r.id = $1 AND r.continues_how = 'resume' ORDER BY e.cursor LIMIT 1`, r.ID, evSessionReplaced).Scan(&why)
	})
	if err != nil && !db.IsNotFound(err) {
		return "", err
	}
	if why == nil {
		return "", nil
	}
	return *why, nil
}

// replaceRound replaces a continuation that cannot go on in its slot (as
// RestartRunTx, by dude) with a fresh Run given its predecessor's
// conversation. false: the continuation is not one, or the delivery does
// not wait on it, and the caller handles it as any Run. A delivery taking
// a step now is waited for (retryLater).
func (s *Syncer) replaceRound(ctx context.Context, r phaseRun, why string) (bool, error) {
	var predecessor string
	var refused, busy bool
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(continues_run_id, '') FROM runs WHERE id = $1 AND continues_how = 'resume'`, r.ID).
			Scan(&predecessor); err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		_, err := delivery.RestartRunTx(ctx, tx, r.Org, r.TaskID, delivery.Restart{RunID: r.ID, ByDude: true, Replays: predecessor,
			Note:  "The previous round's agent could not be taken back up (" + why + "); you are given its conversation instead.",
			Actor: delivery.Writer{ActorType: ledger.ActorSystem, ActorID: "dude"}})
		switch {
		case errors.Is(err, delivery.ErrStepRunning):
			busy = true
			return nil
		case delivery.IsRefusal(err):
			refused = true
			return nil
		}
		return err
	})
	switch {
	case err != nil:
		return true, err
	case predecessor == "":
		return false, nil
	case busy:
		return true, s.retryLater(ctx, r, errRetry)
	case refused:
		return false, nil
	}
	s.Log.Info("a next round's agent could not be taken back up; started a fresh one with its conversation", "run", r.ID, "why", why)
	return true, nil
}
