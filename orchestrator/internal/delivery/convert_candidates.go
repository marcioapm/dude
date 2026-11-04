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

// CandidateRowsSQL selects each task's latest delivery, only while it can
// still go on (live, or stopped or halted for a recovery: an ended or
// superseded one never reads its policy or candidate again, nor does one
// whose task is done or closed, whose State.Stopped Close leaves), that
// still carries a trace of the candidate path: the retired
// pullRequestFixes key in its frozen policy, a candidate, a hand-over, an
// integration's mergeability polls, or a step of the candidate's
// promotion. Whether or not its policy has the key: a delivery frozen
// before the key existed (most of them) fixed through a candidate too.
// ConvertCandidateTx leaves none of these behind, so a second sweep
// selects nothing. A delivery at refreshPromoted after a direct fix
// (no next) is not one: that step is live.
const CandidateRowsSQL = `workflow_type = '` + WorkflowType + `' AND task_id IS NOT NULL
	AND (status IN ('running', 'waiting') OR state ? 'stopped' OR state ? 'halt')
	AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = workflow_runs.task_id
	  AND (t.status = 'done' OR t.resolution IS NOT NULL))
	AND NOT EXISTS (SELECT 1 FROM workflow_runs n WHERE n.task_id = workflow_runs.task_id
	  AND n.workflow_type = workflow_runs.workflow_type AND n.created_at > workflow_runs.created_at)
	AND ` + CandidateTraceSQL

// CandidateTraceSQL (over workflow_runs) holds while a delivery's row
// carries a trace of the candidate path (CandidateRowsSQL); legacyTrace
// is its reading of a decoded State.
const CandidateTraceSQL = `COALESCE(
	state->'policy' ? 'pullRequestFixes'
	OR state ? 'candidate' OR state ? 'handedOver'
	OR state->'integration' ? 'polls'
	OR state->'promotedRefresh'->>'next' = 'beforePR'
	OR step = 'promoteCandidate', false)`

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
// conversion only: no live path opens a candidate.
//
//   - the retired pullRequestFixes key leaves its frozen policy;
//   - a hand-over (handedOver, not a State field) is dropped: the
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
// left (CandidateLeft): the next sweep, or its step's entry, converts
// it. Locks the task's Chat, then the row, as ConvertToQuietTx does;
// idempotent. The caller commits tx.
func ConvertCandidateTx(ctx context.Context, tx pgx.Tx, org, taskID, workflowID string) (string, error) {
	return convertCandidateTx(ctx, tx, org, taskID, workflowID, false)
}

// convertCandidateTx is ConvertCandidateTx: the one conversion, run by
// the boot's sweep and by a delivery's step entry (convertLegacy). byStep
// says the caller is the step the workflow claimed for this row: it holds
// the row's lease, so the row is converted rather than left, and so is
// any trace the step would otherwise run on, whatever the sweep selects.
func convertCandidateTx(ctx context.Context, tx pgx.Tx, org, taskID, workflowID string, byStep bool) (string, error) {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return "", err
	}
	var leased, selected bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(locked_until > now(), false), `+CandidateRowsSQL+`
		FROM workflow_runs WHERE id = $1 FOR UPDATE`, workflowID).Scan(&leased, &selected); err != nil {
		if db.IsNotFound(err) {
			return CandidateNone, nil
		}
		return "", err
	}
	d, err := loadDeliveryByID(ctx, tx, taskID, workflowID)
	if err != nil || d == nil {
		return CandidateNone, err
	}
	st := &d.State
	if byStep {
		if !legacyTrace(d.Step, st) {
			return CandidateNone, nil
		}
	} else if !selected {
		return CandidateNone, nil
	} else if leased {
		return CandidateLeft, nil
	}
	if midPromotion(d.Step, st) {
		return CandidateStoppedMidPromotion, stopMidPromotionTx(ctx, tx, org, workflowID, d)
	}
	if loopMoveRefresh(st) {
		// Not midPromotion: its candidate is gone.
		return CandidateConverted, finishLoopMoveTx(ctx, tx, workflowID, d)
	}
	live := d.Live()
	// Parked on a person (or a conductor's lost decision): its answer
	// redoes the fix. A conductor's decision waited on is not parked: it
	// was about the candidate's work, dropped below with the fix redone.
	// A recovery about to run (its step's entry converting it) is too:
	// the recovery's retry goes to the redo (Stopped, Halt).
	parked := st.Escalation != nil || d.Step == "decide" || d.Step == "recover"
	// The retired key and the hand-over go as the state is written back:
	// neither is a field of State.
	next := ""
	if c := st.Candidate; c != nil {
		// What ran on it stops short of the pull request; nothing resumes it.
		if live {
			if err := abortCandidateRunsTx(ctx, tx, org, taskID, st); err != nil {
				return "", err
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
// candidate path (CandidateRowsSQL) is moved off it (ConvertCandidateTx),
// each in its own transaction. Left is what the next sweep converts; run
// again once a rolling deploy completes.
func ConvertCandidates(ctx context.Context, database *db.DB) (CandidatesConverted, error) {
	var out CandidatesConverted
	type row struct{ Org, TaskID, ID string }
	var rows []row
	if err := database.InSystem(ctx, "convert-candidates", func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT organization_id, task_id, id FROM workflow_runs WHERE `+CandidateRowsSQL+`
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

// abortCandidateRunsTx aborts the live agent Runs of st's candidate (on its
// branch, or waited on by the step): nothing resumes them.
func abortCandidateRunsTx(ctx context.Context, tx pgx.Tx, org, taskID string, st *State) error {
	rows, err := tx.Query(ctx, `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
		control_reason = 'every fix reaches the pull request directly now', ended_at = now(), keep = false
		WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL AND source_kind IS NULL AND `+liveStatus+`
		  AND (branch = $2 OR id = ANY($3)) RETURNING id`, taskID, st.Candidate.Branch, db.NonNil(st.PendingRunIDs))
	if err != nil {
		return err
	}
	aborted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range aborted {
		if err := FailUnreadTx(ctx, tx, RunRef{Org: org, ProjectID: st.ProjectID, TaskID: taskID, RunID: id}, time.Time{}, UnreadRunAborted); err != nil {
			return err
		}
	}
	return nil
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
// loop candidate's) when dude was upgraded. Nothing finishes that
// move, and a branch may have moved in one repository and not another: a
// person redoes the fix (retry, from what the pull requests hold) or
// checks the pull request (wait).
const ReasonStoppedMidPromotion = "stopped_mid_promotion"

// stoppedMidPromotionNotice is dude's line in Chat for such a delivery.
const stoppedMidPromotionNotice = "This delivery was moving its fix onto the pull request when dude was upgraded; redo the fix (Try again) or check the pull request."

// midPromotion says a delivery at step stands where only the deleted
// promotion could go on from: at its step, or at the refresh after a
// loop candidate's promotion with the candidate still held (no old dude
// wrote that, which cleared it first: loopMoveRefresh).
func midPromotion(step string, st *State) bool {
	return step == "promoteCandidate" || st.Candidate != nil && loopMoveRefresh(st)
}

// loopMoveRefresh says st holds the refresh after a loop candidate's
// promotion, bound for the step that opens a pull request for a
// repository newly changed (beforePR). Without the candidate (as an old
// dude left it mid-refresh, its commits on the task branch) the move is
// complete: nothing is redone (finishLoopMoveTx).
func loopMoveRefresh(st *State) bool {
	return st.PromotedRefresh != nil && st.PromotedRefresh.Next == "beforePR"
}

// legacyTrace says a delivery at step, as decoded, still holds something
// of the candidate path a step would run on: a candidate, the deleted
// promotion's step, or a loop move's refresh. The Go side of
// CandidateRowsSQL's trace: the retired key, a hand-over and an
// integration's polls are not fields of State, dropped by any write.
func legacyTrace(step string, st *State) bool {
	return step == "promoteCandidate" || st.Candidate != nil || loopMoveRefresh(st)
}

// finishLoopMoveTx goes on from a loop candidate's completed move
// (loopMoveRefresh, no candidate) as a direct fix that landed does: at prFixDone,
// with the refresh's repositories as the fix's range, so that movedOnPR
// asks reviewers again, opens a pull request for a repository newly
// changed and refreshes every open pull request's body (refreshPromoted,
// retried), then weighs what happened meanwhile (prFixLanded). Nothing is
// fixed again and no person is asked. A stopped, halted or parked one
// goes there on its retry. The caller holds the row.
func finishLoopMoveTx(ctx context.Context, tx pgx.Tx, workflowID string, d *Delivery) error {
	st := &d.State
	var fixed map[string]string
	for _, repo := range st.PromotedRefresh.Repos {
		if h := st.Heads[repo]; h != "" {
			if fixed == nil {
				fixed = map[string]string{}
			}
			fixed[repo] = h
		}
	}
	const next = "prFixDone"
	st.PromotedRefresh, st.FixRange, st.QuestionsHeld = nil, fixed, false
	if e := st.Escalation; e != nil {
		e.Step, e.At, e.AtSignals = next, "", nil
	}
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
	if !d.Live() || st.Escalation != nil || d.Step == "decide" || d.Step == "recover" {
		_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = CASE WHEN step = 'refreshPromoted' THEN $3 ELSE step END
			WHERE id = $1`, workflowID, raw, next)
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = $3, status = 'waiting', wake_at = now(),
		awaiting_signals = '[]'::jsonb, attempt = 0, last_error = NULL WHERE id = $1`, workflowID, raw, next)
	return err
}

// convertLegacyTx is a delivery's step entry (steps.legacy) on a row
// still holding a trace of the candidate path (legacyTrace), one the
// boot's sweep left (CandidateLeft: leased by the pod before) or never
// reached: the same conversion the sweep runs (convertCandidateTx), on
// the row the step claimed. Returns where the row stands now: its step,
// state, the signals it awaits and the time it wakes at (nil: due).
func convertLegacyTx(ctx context.Context, tx pgx.Tx, org, taskID, workflowID string) (step string, st *State, awaiting []string, wake *time.Time, err error) {
	if _, err = convertCandidateTx(ctx, tx, org, taskID, workflowID, true); err != nil {
		return "", nil, nil, nil, err
	}
	var raw, signals []byte
	if err = tx.QueryRow(ctx, `SELECT step, state, awaiting_signals, wake_at FROM workflow_runs WHERE id = $1`,
		workflowID).Scan(&step, &raw, &signals, &wake); err != nil {
		return "", nil, nil, nil, err
	}
	st = &State{}
	if err = json.Unmarshal(raw, st); err != nil {
		return "", nil, nil, nil, err
	}
	if err = json.Unmarshal(signals, &awaiting); err != nil {
		return "", nil, nil, nil, err
	}
	if wake != nil && !wake.After(time.Now()) {
		wake = nil
	}
	return step, st, awaiting, wake, nil
}

// stopMidPromotionTx stops, for a person, a delivery found mid-promotion
// (midPromotion). What it held is let go (its candidate, the refresh, any
// Runs or queued work), with its heads where the candidate started, and
// Chat is told. Its retry fixes again: at the candidate's step, or, with
// none (its fix gone or moved already), at the pull request step, which
// opens one for a repository newly changed and waits on them: no fix
// with nothing to fix. A live one waits at decide on a
// stopped_mid_promotion escalation; a stopped or halted one's retry goes
// there too. The caller holds the row.
func stopMidPromotionTx(ctx context.Context, tx pgx.Tx, org, workflowID string, d *Delivery) error {
	st := &d.State
	next, branch := "openPullRequest", ""
	if c := st.Candidate; c != nil {
		if err := abortCandidateRunsTx(ctx, tx, org, st.TaskID, st); err != nil {
			return err
		}
		var err error
		if next, err = dropCandidateTx(ctx, tx, org, st.TaskID, workflowID, st); err != nil {
			return err
		}
		if c.From != nil {
			st.Heads = maps.Clone(c.From)
		}
		branch = c.Branch
	}
	st.Candidate, st.PromotedRefresh, st.SimplifyBase, st.FixRange, st.PRFixAfter, st.Pipeline = nil, nil, nil, nil, nil, nil
	st.PendingRunIDs, st.ReviewRunIDs = nil, nil
	// A promotion held for a reviewer's question (holdForQuestions) holds
	// nothing: left set, it would keep the task's waits blocked
	// (waitBlocked) for the rest of the delivery.
	st.QuestionsHeld = false
	if st.PRFixCharged != nil && st.PRFixCharged.Pending {
		st.PRFixCharged = nil
	}
	if err := emitTx(ctx, tx, org, st, EvChatNotice, map[string]any{"about": ReasonStoppedMidPromotion,
		"branch": branch, "text": stoppedMidPromotionNotice}); err != nil {
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
		// Off the deleted step too, or the next sweep would select it again;
		// what runs on a recovery is the retry's (Stopped, Halt), not this.
		_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = $3 WHERE id = $1`, workflowID, raw, next)
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
