package delivery

import (
	"cmp"
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// recover_run: the committed work of one of the task's ended Runs, which
// it never published, pushed from lux's checkpoint of its checkout to a
// branch of its own (dude/<task>/recovered-…). The conductor asks
// (ConductRecoverTx); the recovery worker (phases.SettleRecoveries) does
// the work and ends it (RecoveryEndedTx), waking the conductor once. The
// branch moves no task branch and opens no pull request.

// A recovery's statuses: asked of the worker, its publisher Run submitted,
// its push asked of lux; then done, failed or refused.
const (
	RecoveryRequested = "requested"
	RecoverySubmitted = "submitted"
	RecoveryAsked     = "asked"
	RecoveryDone      = "done"
	RecoveryFailed    = "failed"
	RecoveryRefused   = "refused"
)

// RecoverNoteMax bounds the conductor's note, in characters.
const RecoverNoteMax = 500

// RecoveredBranchPrefix is where a task's recovered branches live.
func RecoveredBranchPrefix(taskID string) string { return "dude/" + taskID + "/recovered-" }

// recoveredSuffix is what a named branch may add to the prefix.
var recoveredSuffix = regexp.MustCompile(`^[A-Za-z0-9._-]{1,60}$`)

// RecoverAsk is a recover_run call.
type RecoverAsk struct {
	Run, Branch, Note string
}

// Recovery is what recover_run answers: the recovery, new or the one
// already asked for the same Run and branch.
type Recovery struct {
	ID, Run, Branch, Status string
}

// ConductRecoverTx is recover_run: the task's live conductor asks for the
// committed work of one of the task's ended Runs that publishes to be
// pushed to branch. Asking again for the same Run and branch is the same
// recovery. Locks: Chat; the Run is read unlocked (an ended Run does not
// change), then the recovery inserted.
func ConductRecoverTx(ctx context.Context, tx pgx.Tx, ref RunRef, ask RecoverAsk) (Recovery, error) {
	if err := liveConductorOr(ctx, tx, ref, rYouAreNoLongerThisTaskRecover); err != nil {
		return Recovery{}, err
	}
	var taskStatus string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, ref.TaskID).Scan(&taskStatus); err != nil {
		return Recovery{}, err
	}
	if Ended(taskStatus) {
		return Recovery{}, refusef(rThisTaskYouAreReadOnlyRecover, taskStatus)
	}
	note := strings.TrimSpace(ask.Note)
	if utf8.RuneCountInString(note) > RecoverNoteMax {
		return Recovery{}, refusef(rRecoverNoteAtMostChars, RecoverNoteMax)
	}
	runID := strings.TrimSpace(ask.Run)
	var taskID, kind, source, session, role, phase, status string
	var attempt int
	var publishCode bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(task_id, ''), kind::text, COALESCE(source_kind, ''), COALESCE(session_id, ''),
			COALESCE(role::text, ''), COALESCE(phase::text, ''), status::text, attempt,
			COALESCE((child_capabilities->>'publishCode')::boolean, false)
		FROM runs WHERE id = $1`, runID).Scan(&taskID, &kind, &source, &session, &role, &phase, &status, &attempt, &publishCode)
	switch {
	case db.IsNotFound(err) || err == nil && (taskID != ref.TaskID || kind != "agent" || session != ""):
		return Recovery{}, refusef(rNotRunYourTaskRecover, runID)
	case err != nil:
		return Recovery{}, err
	case runID == ref.RunID:
		return Recovery{}, refusef(rRecoverYourself, runID)
	case runLive(status):
		return Recovery{}, refusef(rRecoverStillLive, runID, status)
	}
	// Only a Run whose work is pushed: a reviewer's or tester's container
	// never is, nor a conductor's (it publishes its own), nor a child not
	// let to publish.
	if role == RoleConductor || !Publishes[phase] || source != "" && !publishCode {
		what := role
		if what == "" {
			what = cmp.Or(phase, "Run")
		}
		return Recovery{}, refusef(rRecoverDoesNotPublish, runID, what)
	}
	var secret bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = 'run.push.refused')`,
		runID).Scan(&secret); err != nil {
		return Recovery{}, err
	}
	if secret {
		return Recovery{}, refusef(rRecoverSecretFound, runID)
	}
	prefix := RecoveredBranchPrefix(ref.TaskID)
	branch := strings.TrimSpace(ask.Branch)
	if branch == "" {
		branch = prefix + runID
	}
	if suffix, ok := strings.CutPrefix(branch, prefix); !ok || !recoveredSuffix.MatchString(suffix) {
		return Recovery{}, refusef(rRecoverBranchOutside, branch, prefix, prefix+runID)
	}
	inUse, err := branchInUseTx(ctx, tx, ref.TaskID, attempt, branch)
	if err != nil {
		return Recovery{}, err
	}
	if inUse {
		return Recovery{}, refusef(rRecoverBranchInUse, branch)
	}
	// One per Run and branch: the same ask is the same recovery. Another
	// branch for a Run already recovered, or being recovered, is refused.
	var other Recovery
	err = tx.QueryRow(ctx, `SELECT id, branch, status FROM run_recoveries
		WHERE run_id = $1 AND branch <> $2 AND status NOT IN ('failed', 'refused') ORDER BY created_at LIMIT 1`, runID, branch).
		Scan(&other.ID, &other.Branch, &other.Status)
	switch {
	case err == nil:
		return Recovery{}, refusef(rRecoverAlreadyRecovered, runID, other.Branch, other.Status)
	case !db.IsNotFound(err):
		return Recovery{}, err
	}
	out := Recovery{ID: ids.New("rcv"), Run: runID, Branch: branch, Status: RecoveryRequested}
	tag, err := tx.Exec(ctx, `INSERT INTO run_recoveries (id, organization_id, task_id, run_id, conductor_run_id, branch, note)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')) ON CONFLICT (run_id, branch) DO NOTHING`,
		out.ID, ref.Org, ref.TaskID, runID, ref.RunID, branch, note)
	if err != nil {
		return Recovery{}, err
	}
	if tag.RowsAffected() == 0 {
		err := tx.QueryRow(ctx, `SELECT id, status FROM run_recoveries WHERE run_id = $1 AND branch = $2`, runID, branch).
			Scan(&out.ID, &out.Status)
		return out, err
	}
	return out, nil
}

// branchInUseTx says branch is the task branch of the attempt, its
// candidate branch, or the head of one of the task's open pull requests.
func branchInUseTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, branch string) (bool, error) {
	tb, err := TaskBranchTx(ctx, tx, taskID, max(attempt, 1))
	if err != nil {
		return false, err
	}
	if branch == tb.Branch || branch == tb.Branch+candidateSuffix || branch == CandidateBranch(taskID, max(attempt, 1)) {
		return true, nil
	}
	var open bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pull_requests WHERE task_id = $1 AND head_branch = $2
		AND state IN ('open', 'draft'))`, taskID, branch).Scan(&open)
	return open, err
}
