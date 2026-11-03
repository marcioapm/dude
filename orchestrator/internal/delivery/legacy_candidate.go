// Legacy, conversion only. The candidate path (an open pull request's fix
// checked on a "-candidate" branch, then promoted onto the pull request) is
// deleted: every fix reaches its pull request directly. What is left here
// is what ConvertCandidates needs to move a delivery stored before that off
// it (the Candidate it reads from old state, and the helpers that reopen
// what a dropped candidate's Runs resolved), and the filter that keeps old
// candidate Runs' branches out of previews and briefings. No live path
// creates, reviews, tests, promotes or hands over a candidate.

package delivery

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// CandidateBranch is where a fix for an open pull request's feedback, and
// every fix of it, is taken while it is reviewed and tested: the task
// branch (the pull request's head) is fast-forwarded to it only once the
// gate passes (promoteCandidate).
func CandidateBranch(taskID string, attempt int) string {
	return BranchFor(taskID, attempt) + candidateSuffix
}

const candidateSuffix = "-candidate"

// NotGeneratedCandidateSQL (SQL, over runs aliased run) holds when the
// Run's branch is not its own CandidateBranch: a fix not yet checked.
func NotGeneratedCandidateSQL(run string) string {
	return fmt.Sprintf(`%[1]s.branch <> ('dude/' || %[1]s.task_id || '/attempt-' || %[1]s.attempt || '%[2]s')`, run, candidateSuffix)
}

// Candidate is one round of pull request feedback being fixed on the
// candidate branch.
type Candidate struct {
	// The round's PRFixKey: what keeps its Runs apart from another round's.
	Round  int    `json:"round"`
	Branch string `json:"branch"`
	// The heads the fix started from: each pull request's head as GitHub
	// had it, the rest where the delivery had them. The pull request
	// branch is moved only from these.
	From map[string]string `json:"from"`
	// Where its review range starts per repository the fix changed: From,
	// or where the fix's checkout started in a repository From lacks.
	Base map[string]string `json:"base,omitempty"`
	// The heads at which a person accepted a review round whose reviewers
	// failed: promotion takes them as reviewed (unreviewedHeads).
	Accepted map[string]string `json:"accepted,omitempty"`
	// Opened by a writer of the review loop while a pull request was open
	// (writerCandidate), not by a pull request's fix: once promoted, the
	// delivery goes on as that loop would have (beforePR).
	Loop bool `json:"loop,omitempty"`
	// Set by writerCandidate in the step that opens it, until phase has
	// read the pull requests' heads (or a replay's Run's). Never persisted.
	opening bool
}

// discardLoopCandidateTx reopens the findings a refused loop candidate's Runs
// resolved outside the moved repositories and returns the round's first
// writer phase. A round's Runs are those whose creation key carries
// ":candidate:<round>" (key), optionally followed by its retry suffix, and
// the Runs that replaced them (restart_run, replaceRound), which have no key.
func discardLoopCandidateTx(ctx context.Context, tx pgx.Tx, taskID, workflowID string, attempt, round int, moved []string) (string, error) {
	inRound := `r.task_id = $1 AND r.attempt = ` + attemptOr + ` AND starts_with(r.creation_key, $3)
		AND (r.creation_key LIKE '%:candidate:' || $4::int OR r.creation_key LIKE '%:candidate:' || $4::int || ':retry:%')`
	// A finding naming no repository may concern any of them: reopened.
	if _, err := tx.Exec(ctx, `WITH RECURSIVE round AS (
			SELECT r.id, r.replaced_by FROM runs r WHERE `+inRound+`
			UNION SELECT r.id, r.replaced_by FROM runs r JOIN round o ON r.id = o.replaced_by)
		UPDATE review_findings f SET status = 'open', resolved_by_run_id = NULL,
			resolution_note = 'its fix never reached the pull request', updated_at = now()
		FROM round r WHERE r.id = f.resolved_by_run_id AND f.task_id = $1 AND f.status = 'resolved'
		  AND (f.repo IS NULL OR NOT f.repo = ANY($5))`, taskID, attempt, workflowID, round, db.NonNil(moved)); err != nil {
		return "", err
	}
	var phase string
	err := tx.QueryRow(ctx, `SELECT r.phase::text FROM runs r WHERE `+inRound+`
		AND r.phase IN ('implement', 'fix', 'simplify') ORDER BY r.created_at, r.id LIMIT 1`,
		taskID, attempt, workflowID, round).Scan(&phase)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return phase, err
}

// AbandonCandidateTx reopens the pending resolutions of the candidate
// branch's Runs (task, attempt; 0 for the task's current) when its round
// ends without reaching the pull request: the next fixer is given them.
func AbandonCandidateTx(ctx context.Context, tx pgx.Tx, org, taskID string, attempt int, branch string) error {
	return candidateResolutionsTx(ctx, tx, org, taskID, attempt, branch, nil, nil)
}

// candidateResolutionsTx settles the pending resolutions made by the Runs
// that published to branch: verified in a repository of verify, reopened
// in any other. One in a repository of ended (its pull request merged or
// closed during the check) is reopened as such, not as a commit that never
// reached a pull request.
func candidateResolutionsTx(ctx context.Context, tx pgx.Tx, org, taskID string, attempt int, branch string, verify, ended []string) error {
	rows, err := tx.Query(ctx, `SELECT f.id, f.resolved_by_run_id, COALESCE(f.resolved_commit, ''), COALESCE(f.resolved_repo, '')
		FROM feedback_items f JOIN runs r ON r.id = f.resolved_by_run_id
		WHERE r.task_id = $1 AND r.attempt = `+attemptOr+` AND r.branch = $3
		  AND f.status = 'resolved' AND f.resolved_verified_at IS NULL
		ORDER BY f.id FOR UPDATE OF f`, taskID, attempt, branch)
	if err != nil {
		return err
	}
	type pending struct{ ID, RunID, Commit, Repo string }
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pending])
	if err != nil {
		return err
	}
	for _, p := range got {
		ref := RunRef{Org: org, TaskID: taskID, RunID: p.RunID}
		var err error
		switch {
		case slices.Contains(verify, p.Repo):
			err = verifyResolutionTx(ctx, tx, ref, p.ID, p.Commit, p.Repo)
		case slices.Contains(ended, p.Repo):
			err = reopenResolutionTx(ctx, tx, ref, p.ID, ReopenPullRequestEnded,
				"its pull request ended while the fix was checked; a fixer checks it again on the follow-up pull request")
		default:
			err = reopenResolutionTx(ctx, tx, ref, p.ID, ReopenNotPublished, "its commit never reached the pull request")
		}
		if err != nil {
			return err
		}
	}
	return nil
}
