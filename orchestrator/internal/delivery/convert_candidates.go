package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// What ConvertCandidateTx did with a delivery.
const (
	// CandidateConverted: it fixes its pull requests directly now; an open
	// candidate's fix is redone from the pull requests' heads.
	CandidateConverted = "converted"
	// CandidateMoving: its candidate is being promoted (promoteCandidate,
	// or the refresh after a loop's): left for the next sweep.
	CandidateMoving = "moving"
	// CandidateLeft: a worker runs its step now: left for the next sweep.
	CandidateLeft = "left"
	// CandidateNone: nothing to convert (another sweep did it).
	CandidateNone = "none"
)

// candidateRowsSQL selects each task's latest delivery, only while it can
// still go on (live, or stopped or halted for a recovery: an ended or
// superseded one never reads its policy or candidate again), that still
// fixes through a candidate or has one's state: its frozen policy not
// direct (keyless included), a candidate, a hand-over, or a step of the
// candidate's promotion.
const candidateRowsSQL = `workflow_type = '` + WorkflowType + `' AND task_id IS NOT NULL
	AND (status IN ('running', 'waiting') OR state ? 'stopped' OR state ? 'halt')
	AND NOT EXISTS (SELECT 1 FROM workflow_runs n WHERE n.task_id = workflow_runs.task_id
	  AND n.workflow_type = workflow_runs.workflow_type AND n.created_at > workflow_runs.created_at)
	AND COALESCE(
	COALESCE(state->'policy'->>'pullRequestFixes', '') <> '` + PullRequestFixesDirect + `'
	OR state ? 'candidate' OR state ? 'handedOver'
	OR state->'promotedRefresh'->>'next' = 'beforePR'
	OR step IN ('promoteCandidate', 'refreshPromoted'), false)`

// convertedNotice is dude's line in Chat for a candidate it dropped.
func convertedNotice(branch string, heads map[string]string) string {
	var at []string
	for _, repo := range slices.Sorted(maps.Keys(heads)) {
		at = append(at, fmt.Sprintf("%s %s", repo, short(heads[repo])))
	}
	where := ""
	if len(at) > 0 {
		where = " (" + strings.Join(at, ", ") + ")"
	}
	return fmt.Sprintf("The fix on %s%s never reached the pull request; dude fixes it again directly. Its commits stay on that branch.",
		branch, where)
}

// ConvertCandidateTx moves one delivery (workflowID, of taskID) off the
// candidate path, now that every fix reaches its pull request directly:
//
//   - its frozen policy says direct;
//   - a hand-over (State.HandedOver) is dropped: the syncer's next reading
//     of a pull request still conflicting escalates it;
//   - an open candidate goes no further: its live Runs are aborted and
//     every kept one let go, what its Runs resolved is reopened for the
//     next fixer (AbandonCandidateTx; a loop's findings with
//     discardLoopCandidateTx), the heads go back to where it started, and
//     the fix is redone directly: a running delivery goes to prFix (a
//     loop's to its writer step) now; one parked on a person or its
//     conductor, or stopped, goes there on its retry. Its commits stay on
//     the candidate branch, which nothing deletes.
//
// A candidate being promoted is left (CandidateMoving), as is a delivery
// whose step a worker runs now (CandidateLeft): the next sweep converts
// them. Locks the task's Chat, then the row, as ConvertToQuietTx does;
// idempotent. The caller commits tx.
func ConvertCandidateTx(ctx context.Context, tx pgx.Tx, org, taskID, workflowID string) (string, error) {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return "", err
	}
	var leased, selected bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(locked_until > now(), false), `+candidateRowsSQL+`
		FROM workflow_runs WHERE id = $1 FOR UPDATE`, workflowID).Scan(&leased, &selected); err != nil {
		if db.IsNotFound(err) {
			return CandidateNone, nil
		}
		return "", err
	}
	if !selected {
		return CandidateNone, nil
	}
	d, err := loadDeliveryByID(ctx, tx, taskID, workflowID)
	if err != nil || d == nil {
		return CandidateNone, err
	}
	st := &d.State
	if d.Step == "promoteCandidate" || d.Step == "refreshPromoted" && st.PromotedRefresh != nil && st.PromotedRefresh.Next == "beforePR" {
		return CandidateMoving, nil
	}
	if leased {
		return CandidateLeft, nil
	}
	live := d.Live()
	// Parked on a person (or a conductor's lost decision): its answer
	// redoes the fix. A conductor's decision parked on is not about the
	// candidate's work: dropped, and the fix redone now (below).
	parked := st.Escalation != nil || d.Step == "decide"
	st.Policy.PullRequestFixes = PullRequestFixesDirect
	st.HandedOver = nil
	next := ""
	if c := st.Candidate; c != nil {
		// What ran on it stops short of the pull request; nothing resumes it.
		if live {
			rows, err := tx.Query(ctx, `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
				control_reason = 'every fix reaches the pull request directly now', ended_at = now(), keep = false
				WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL AND source_kind IS NULL AND `+liveStatus+`
				  AND (branch = $2 OR id = ANY($3)) RETURNING id`, taskID, c.Branch, db.NonNil(st.PendingRunIDs))
			if err != nil {
				return "", err
			}
			aborted, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return "", err
			}
			for _, id := range aborted {
				if err := FailUnreadTx(ctx, tx, RunRef{Org: org, ProjectID: st.ProjectID, TaskID: taskID, RunID: id}, time.Time{}, UnreadRunAborted); err != nil {
					return "", err
				}
			}
		}
		if err := ReleaseKeptTx(ctx, tx, taskID); err != nil {
			return "", err
		}
		if err := AbandonCandidateTx(ctx, tx, org, taskID, st.Attempt, c.Branch); err != nil {
			return "", err
		}
		next = "prFix"
		// Parked on a person's answer about the candidate's work: redone
		// directly, whatever they answer (below). A conductor's lost
		// decision is not about the candidate's work: left as it was.
		redo := st.Escalation != nil && st.Escalation.Reason != ReasonConductorUnavailable
		if c.Loop {
			phase, err := discardLoopCandidateTx(ctx, tx, taskID, workflowID, st.Attempt, c.Round, nil)
			if err != nil {
				return "", err
			}
			next = "fix"
			switch phase {
			case PhaseImplement:
				next = "implementRun"
			case PhaseSimplify:
				next = "simplify"
			}
		} else if st.Integration == nil && (live && !parked || redo) && st.PRIteration > 0 {
			// The fix is redone as its round, not as another: prFix counts
			// it again (a stopped one's retry gives it back as it recovers).
			st.PRIteration--
		}
		if st.PRFixCharged != nil && st.PRFixCharged.Pending {
			// Charged only once a fix lands (chargeLandedPRFix): none did.
			st.PRFixCharged = nil
		}
		if len(c.Accepted) > 0 {
			st.AcceptedHeads = maps.Clone(c.Accepted)
		}
		if c.From != nil {
			st.Heads = maps.Clone(c.From)
		}
		st.Candidate, st.SimplifyBase, st.FixRange, st.PRFixAfter, st.Pipeline = nil, nil, nil, nil, nil
		st.PendingRunIDs, st.ReviewRunIDs = nil, nil
		st.Iteration, st.Round, st.RoundHeads, st.ReviewingHeads = 0, 0, nil, nil
		if err := emitTx(ctx, tx, org, st, EvChatNotice, map[string]any{"about": "candidate_converted",
			"branch": c.Branch, "heads": c.From, "text": convertedNotice(c.Branch, c.From)}); err != nil {
			return "", err
		}
		// However it goes on, the redo's Runs are keyed apart from every
		// earlier one's (Iteration starts again at 0): none from before
		// the sweep is adopted.
		st.Retries++
		if st.Decision != nil && (d.Step == "conductorDecision" || redo) {
			// The conductor's decision was about the dropped candidate: it
			// hears of the redo from the notice. The row leaves
			// conductorDecision, so no quiet deadline fires on it.
			st.Decision = nil
			st.Decisions++
		}
		// Parked or stopped: the person's answer (or the conductor's)
		// fixes again from what the pull requests show, never resuming a
		// Run of the candidate's. A parked one's escalation asked about
		// the candidate's work: whatever resuming answer (retry, accept,
		// amend, waive), the fix is redone directly (Escalation.Redo).
		if e := st.Escalation; e != nil && redo {
			e.Step, e.At, e.AtSignals, e.Redo = next, next, nil, true
		} else if e != nil {
			e.Step, e.At, e.AtSignals = next, "", nil
		}
		if e := st.Stopped; e != nil {
			e.Step, e.At, e.AtSignals = next, "", nil
		}
		if h := st.Halt; h != nil {
			h.At = next
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	if next == "" || !live || parked {
		_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb WHERE id = $1`, workflowID, raw)
		return CandidateConverted, err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = $3, status = 'waiting', wake_at = now(),
		awaiting_signals = '[]'::jsonb, attempt = 0, last_error = NULL WHERE id = $1`, workflowID, raw, next)
	return CandidateConverted, err
}

// CandidatesConverted counts what a sweep of ConvertCandidateTx did.
type CandidatesConverted struct {
	Converted, Moving, Left int
}

// ConvertCandidates is the boot's sweep after ConvertInFlight, and part
// of dude-orchestrator convert-deliveries: every delivery still on the
// candidate path (candidateRowsSQL) is moved off it (ConvertCandidateTx),
// each in its own transaction. Moving and Left are what the next sweep
// converts; run again once a rolling deploy completes.
func ConvertCandidates(ctx context.Context, database *db.DB) (CandidatesConverted, error) {
	var out CandidatesConverted
	type row struct{ Org, TaskID, ID string }
	var rows []row
	if err := database.InSystem(ctx, "convert-candidates", func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT organization_id, task_id, id FROM workflow_runs WHERE `+candidateRowsSQL+`
			ORDER BY created_at`)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, pgx.RowToStructByPos[row])
		return err
	}); err != nil {
		return out, err
	}
	for _, r := range rows {
		var did string
		if err := database.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
			did, err = ConvertCandidateTx(ctx, tx, r.Org, r.TaskID, r.ID)
			return err
		}); err != nil {
			return out, fmt.Errorf("task %s: %w", r.TaskID, err)
		}
		switch did {
		case CandidateConverted:
			out.Converted++
		case CandidateMoving:
			out.Moving++
		case CandidateLeft:
			out.Left++
		}
	}
	return out, nil
}
