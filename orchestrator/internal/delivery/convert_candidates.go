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
	// CandidateConverted: it fixes its pull requests directly; an open
	// candidate's fix is redone from the pull requests' heads.
	CandidateConverted = "converted"
	// CandidateStoppedMidPromotion: its candidate was being promoted
	// (promoteCandidate, or the refresh after a loop's) by a dude from
	// before the candidate path was deleted. Nothing can finish that now:
	// the delivery is stopped for a person (stopMidPromotionTx).
	CandidateStoppedMidPromotion = "stopped_mid_promotion"
	// CandidateLeft: a worker is running its step: left for the next sweep.
	CandidateLeft = "left"
	// CandidateNone: nothing to convert (another sweep did it).
	CandidateNone = "none"
)

// candidateRowsSQL selects each task's latest delivery, only while it can
// still go on (live, or stopped or halted for a recovery: an ended or
// superseded one never reads its policy or candidate again, nor does one
// whose task is done or closed, whose State.Stopped Close leaves), that still
// has a candidate's state: the retired pullRequestFixes key in its frozen
// policy, a candidate, a hand-over, or a step of the candidate's
// promotion. A delivery without the key fixes directly as it is.
const candidateRowsSQL = `workflow_type = '` + WorkflowType + `' AND task_id IS NOT NULL
	AND (status IN ('running', 'waiting') OR state ? 'stopped' OR state ? 'halt')
	AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = workflow_runs.task_id
	  AND (t.status = 'done' OR t.resolution IS NOT NULL))
	AND NOT EXISTS (SELECT 1 FROM workflow_runs n WHERE n.task_id = workflow_runs.task_id
	  AND n.workflow_type = workflow_runs.workflow_type AND n.created_at > workflow_runs.created_at)
	AND COALESCE(
	state->'policy' ? 'pullRequestFixes'
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
// candidate path, as every fix reaches its pull request directly. Legacy,
// conversion only: no live path opens a candidate any more.
//
//   - the retired pullRequestFixes key leaves its frozen policy;
//   - a hand-over (handedOver, no longer a State field) is dropped: the
//     syncer's next reading of a pull request still conflicting
//     escalates it;
//   - an open candidate goes no further: its live Runs are aborted and
//     every kept one let go, what its Runs resolved is reopened for the
//     next fixer (AbandonCandidateTx; a loop's findings with
//     discardLoopCandidateTx), the heads go back to where it started, and
//     the fix is redone directly at prFix (a loop's at its writer step),
//     its Runs keyed apart (Retries): a running delivery, or one waiting
//     on its conductor's decision, goes there at once; one parked on a
//     person goes there on whatever resuming answer (Escalation.Redo); a
//     stopped one on its retry. Its commits stay on the candidate branch,
//     which nothing deletes.
//
// A candidate being promoted is stopped for a person
// (CandidateStoppedMidPromotion, stopMidPromotionTx): the code that would
// finish the move is gone. A delivery whose step a worker is running is
// left (CandidateLeft): the next sweep converts it. Locks the task's Chat, then the row, as ConvertToQuietTx does;
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
	if leased {
		return CandidateLeft, nil
	}
	if midPromotion(d.Step, st) {
		return CandidateStoppedMidPromotion, stopMidPromotionTx(ctx, tx, org, workflowID, d)
	}
	live := d.Live()
	// Parked on a person (or a conductor's lost decision): its answer
	// redoes the fix. A conductor's decision waited on is not parked: it
	// was about the candidate's work, dropped below with the fix redone.
	parked := st.Escalation != nil || d.Step == "decide"
	// The retired key and the hand-over go as the state is written back:
	// neither is a field of State now.
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
		if next, err = dropCandidateTx(ctx, tx, org, taskID, workflowID, st); err != nil {
			return "", err
		}
		// The escalation parked on asked about the candidate's work: its
		// answer redoes the fix. A conductor's lost decision did not.
		redo := st.Escalation != nil && st.Escalation.Reason != ReasonConductorUnavailable
		if !c.Loop && st.Integration == nil && (live && !parked || redo) && st.PRIteration > 0 {
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
			// conductorDecision, so no quiet deadline fires on it; the
			// decision's wake (ConvertToQuietTx's, at boot) is settled
			// with it, so no turn is asked for a decision nobody waits on.
			if _, err := tx.Exec(ctx, `UPDATE conductor_wakes SET delivered_at = now()
				WHERE task_id = $1 AND key = $2 AND delivered_at IS NULL`,
				taskID, decisionWakeKey(workflowID, st.Decisions)); err != nil {
				return "", err
			}
			st.Decision = nil
			st.Decisions++
		}
		// Parked or stopped: the person's answer (or the conductor's)
		// fixes again from what the pull requests show, never resuming a
		// Run of the candidate's; any resuming answer to a redo (retry,
		// accept, amend, waive) goes straight to next (Escalation.Redo).
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
	Converted, StoppedMidPromotion, Left int
}

// ConvertCandidates is the boot's sweep after ConvertInFlight, and part
// of dude-orchestrator convert-deliveries: every delivery still on the
// candidate path (candidateRowsSQL) is moved off it (ConvertCandidateTx),
// each in its own transaction. Left is what the next sweep converts; run
// again once a rolling deploy completes.
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
		case CandidateStoppedMidPromotion:
			out.StoppedMidPromotion++
		case CandidateLeft:
			out.Left++
		}
	}
	return out, nil
}

// dropCandidateTx lets go of st's candidate in tx: its kept Runs released,
// what its Runs resolved reopened for the next fixer (a loop's findings
// too). Returns the step that fixes again: prFix, or a loop's writer.
func dropCandidateTx(ctx context.Context, tx pgx.Tx, org, taskID, workflowID string, st *State) (string, error) {
	c := st.Candidate
	if err := ReleaseKeptTx(ctx, tx, taskID); err != nil {
		return "", err
	}
	if err := AbandonCandidateTx(ctx, tx, org, taskID, st.Attempt, c.Branch); err != nil {
		return "", err
	}
	if !c.Loop {
		return "prFix", nil
	}
	phase, err := discardLoopCandidateTx(ctx, tx, taskID, workflowID, st.Attempt, c.Round, nil)
	if err != nil {
		return "", err
	}
	switch phase {
	case PhaseImplement:
		return "implementRun", nil
	case PhaseSimplify:
		return "simplify", nil
	}
	return "fix", nil
}

// ReasonStoppedMidPromotion: the delivery was moving a checked fix onto
// its pull request (the deleted promoteCandidate, or the refresh after a
// loop candidate's) when dude was upgraded. Nothing finishes that move
// now, and a branch may have moved in one repository and not another: a
// person redoes the fix (retry, from what the pull requests hold) or
// checks the pull request (wait).
const ReasonStoppedMidPromotion = "stopped_mid_promotion"

// stoppedMidPromotionNotice is dude's line in Chat for such a delivery.
const stoppedMidPromotionNotice = "This delivery was moving its fix onto the pull request when dude was upgraded; redo the fix (Try again) or check the pull request."

// midPromotion says a delivery at step stands where only the deleted
// promotion could go on from.
func midPromotion(step string, st *State) bool {
	return step == "promoteCandidate" ||
		step == "refreshPromoted" && st.PromotedRefresh != nil && st.PromotedRefresh.Next == "beforePR"
}

// leaveMidPromotionTx lets go of what a delivery stopped mid-promotion
// held (its candidate, the refresh, any Runs or queued work), with its
// heads where the candidate started, and tells Chat. Returns the step
// that fixes again.
func leaveMidPromotionTx(ctx context.Context, tx pgx.Tx, org, workflowID string, st *State) (string, error) {
	next := "prFix"
	branch := ""
	if c := st.Candidate; c != nil {
		var err error
		if next, err = dropCandidateTx(ctx, tx, org, st.TaskID, workflowID, st); err != nil {
			return "", err
		}
		if c.From != nil {
			st.Heads = maps.Clone(c.From)
		}
		branch = c.Branch
	}
	st.Candidate, st.PromotedRefresh, st.SimplifyBase, st.FixRange, st.PRFixAfter, st.Pipeline = nil, nil, nil, nil, nil, nil
	st.PendingRunIDs, st.ReviewRunIDs = nil, nil
	if st.PRFixCharged != nil && st.PRFixCharged.Pending {
		st.PRFixCharged = nil
	}
	return next, emitTx(ctx, tx, org, st, EvChatNotice, map[string]any{"about": ReasonStoppedMidPromotion,
		"branch": branch, "text": stoppedMidPromotionNotice})
}

// stopMidPromotionTx stops, for a person, a delivery the sweep found
// mid-promotion (midPromotion): what it held is let go
// (leaveMidPromotionTx), and a live one waits at decide on a
// stopped_mid_promotion escalation whose retry fixes again; a stopped or
// halted one's retry fixes again too. The caller holds the row.
func stopMidPromotionTx(ctx context.Context, tx pgx.Tx, org, workflowID string, d *Delivery) error {
	st := &d.State
	next, err := leaveMidPromotionTx(ctx, tx, org, workflowID, st)
	if err != nil {
		return err
	}
	st.Retries++
	if !d.Live() {
		if e := st.Stopped; e != nil {
			e.Step, e.At, e.AtSignals = next, "", nil
		}
		if h := st.Halt; h != nil {
			h.At = next
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb WHERE id = $1`, workflowID, raw)
		return err
	}
	detail := map[string]any{"step": d.Step}
	st.Escalation = &Escalation{Reason: ReasonStoppedMidPromotion, Detail: detail, Step: next}
	st.Decision, st.Directed = nil, nil
	if _, err := SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "", "awaiting_input", ReasonStoppedMidPromotion); err != nil {
		return err
	}
	if err := emitTx(ctx, tx, org, st, EvQuestionAsked, map[string]any{"kind": "escalation", "reason": ReasonStoppedMidPromotion,
		"detail": detail, "actions": st.Escalation.Actions()}); err != nil {
		return err
	}
	if st.conducted() {
		if _, err := tellEscalationTx(ctx, tx, org, workflowID, st); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = 'decide', status = 'waiting', wake_at = NULL,
		awaiting_signals = $3::jsonb, attempt = 0, last_error = NULL WHERE id = $1`, workflowID, raw, `["`+SignalHumanDecision+`"]`)
	return err
}
