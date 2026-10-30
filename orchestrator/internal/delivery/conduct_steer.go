package delivery

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ConductSteer is the steer tool: the task's live conductor steers a live
// phase Run of its own task's current attempt, or a child agent it started
// (start_agent, start_agents, verify), as a person would. Allowed
// whoever decides: steering takes no delivery decision. Returns the
// directive and where the Run's harness last said a steer lands ("" when
// lux has not said yet).
func ConductSteer(ctx context.Context, tx pgx.Tx, ref RunRef, runID, text string, interrupt bool) (Steered, string, error) {
	return ConductSteerFeedback(ctx, tx, ref, runID, text, interrupt, nil)
}

// ConductSteerFeedback is ConductSteer passing people's feedback items by
// id: dude adds each one's words to the steer (FeedbackText), never the
// conductor's summary of them, and records them as given to the Run. Only
// to an implementer or fixer, and only items of the task's current
// attempt still to be handled.
func ConductSteerFeedback(ctx context.Context, tx pgx.Tx, ref RunRef, runID, text string, interrupt bool, feedbackIDs []string) (Steered, string, error) {
	// As parked(): the Chat lock serialises the check with a conductor's
	// end or replacement, so a superseded conductor's late call does nothing.
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return Steered{}, "", err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return Steered{}, "", err
	}
	if !live {
		return Steered{}, "", refuseText(rYouAreNoLongerThisTask2)
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, ref.TaskID).Scan(&status); err != nil {
		return Steered{}, "", err
	}
	if Ended(status) {
		return Steered{}, "", refusef(rThisTaskYouAreReadOnly2, status)
	}
	if strings.TrimSpace(text) == "" && len(feedbackIDs) == 0 {
		return Steered{}, "", refuseText(rSayWhatTellRunTextRequired)
	}
	if len(text) > SteerTextMax {
		return Steered{}, "", refusef(rSteerAtMostKibText, SteerTextMax>>10)
	}
	var taskID, kind, phase, role, rstatus, source, parent string
	var current, ending bool
	// Ending: being aborted, its work being collected (the push asked for),
	// or its container stopped on its own: it would never read the words.
	// A finished turn not yet collected is not: as a person's steer, the
	// words start its next turn.
	err := tx.QueryRow(ctx, `SELECT COALESCE(r.task_id, ''), r.kind, COALESCE(r.phase::text, ''), COALESCE(r.role::text, ''), r.status::text,
			r.attempt = (SELECT max(k.attempt) FROM runs k WHERE k.task_id = r.task_id AND k.phase IS NOT NULL),
			r.control = 'abort' OR r.push_request_id IS NOT NULL
			  OR (COALESCE(r.lux_state, '') IN ('stopped', 'succeeded', 'failed', 'cancelled', 'terminated', 'lost') AND r.lux_stop_reason IS NULL
			      AND r.control = 'none'),
			COALESCE(r.source_kind, ''), COALESCE(r.parent_run_id, '')
		FROM runs r WHERE r.id = $1 FOR UPDATE`, runID).Scan(&taskID, &kind, &phase, &role, &rstatus, &current, &ending, &source, &parent)
	switch {
	case db.IsNotFound(err) || err == nil && taskID != ref.TaskID:
		return Steered{}, "", refusef(rNotRunYourTaskYouSteer, runID)
	case err != nil:
		return Steered{}, "", err
	case runID == ref.RunID || role == RoleConductor:
		return Steered{}, "", refusef(rConductorNotPhaseRunSteerRuns, runID)
	case kind != "agent":
		return Steered{}, "", refusef(rBranchPreviewNotAgentThereNobody, runID)
	case phase == "":
		return Steered{}, "", refusef(rNotPhaseRun, runID)
	case !current:
		return Steered{}, "", refusef(rRunEarlierAttemptSteerOnlyCurrent, runID)
	case source == string(ChildParentTaskConductor) && parent != ref.RunID:
		// A child agent is its own conductor's to steer: one a conductor
		// before this one started is aborted with its parent's end
		// (MarkChildParentEndedTx); this says so wherever it is read.
		return Steered{}, "", refusef(rNotYourChildSteer, runID)
	}
	switch rstatus {
	case "pending", "scheduled", "starting", "running", "paused":
		if ending {
			return Steered{}, "", refusef(rEndingWouldNeverReadSteerWait, runID)
		}
	default:
		return Steered{}, "", refusef(rHasEndedStartAnotherPhaseInstead, runID, rstatus)
	}
	var items []FeedbackItem
	var words string
	if len(feedbackIDs) > 0 {
		if phase != PhaseImplement && phase != PhaseFix {
			return Steered{}, "", refusef(rRunFeedbackGoesImplementerOrFixer, runID, phase)
		}
		open, err := OpenFeedbackTx(ctx, tx, ref.TaskID, 0)
		if err != nil {
			return Steered{}, "", err
		}
		for _, id := range feedbackIDs {
			i := slices.IndexFunc(open, func(it FeedbackItem) bool { return it.ID == id })
			if i < 0 {
				return Steered{}, "", refusef(rNotOpenFeedbackItemThisTask, id)
			}
			items = append(items, open[i])
		}
		words = FeedbackText(items, true)
	}
	out, err := Steer(ctx, tx, ref.Org, SteerInput{RunID: runID, Text: text, Interrupt: interrupt,
		Actor: Writer{ActorType: ledger.ActorAgent, ActorID: ref.RunID}, Conductor: ref.RunID,
		FeedbackIDs: feedbackIDs, FeedbackWords: words})
	if se, ok := err.(SteerError); ok {
		return Steered{}, "", refusef("%s", se.Msg)
	}
	if err != nil {
		return Steered{}, "", err
	}
	if len(items) > 0 {
		if err := recordGivenTx(ctx, tx, runID, out.ID, feedbackIDs); err != nil {
			return Steered{}, "", err
		}
	}
	var lands string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT payload->>'lands' FROM events WHERE run_id = $1
			AND event_type IN ('run.directive.accepted', 'agent.prompt.delivered') AND payload ? 'lands'
			ORDER BY cursor DESC LIMIT 1), '')`, runID).Scan(&lands); err != nil {
		return Steered{}, "", err
	}
	return out, lands, nil
}

// SteerSettledTx wakes the conductor that wrote a directive with what
// became of it, once per instruction and outcome: read by the agent
// (steer_read), or never to be (steer_failed, with why). An instruction is
// the conductor's directive and the same words sent again superseding it
// (Retry, Interrupt now), named by the conductor's own directive. A
// person's directive, or dude's own, wakes nobody.
func SteerSettledTx(ctx context.Context, tx pgx.Tx, org, directiveID string, read bool, why string) error {
	var taskID, runID, phase, root string
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain (id, supersedes, depth) AS (
			SELECT d.id, d.supersedes, 0 FROM directives d WHERE d.id = $1
			UNION ALL
			SELECT s.id, s.supersedes, c.depth + 1 FROM chain c JOIN directives d ON d.id = c.id
			JOIN directives s ON s.id = c.supersedes AND s.run_id = d.run_id AND s.text = d.text
			  AND s.conductor_run_id IS NOT DISTINCT FROM d.conductor_run_id
			WHERE c.depth < 100)
		SELECT d.task_id, d.run_id, COALESCE(r.phase::text, ''), (SELECT id FROM chain ORDER BY depth DESC LIMIT 1)
		FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE d.id = $1 AND d.conductor_run_id IS NOT NULL`, directiveID).Scan(&taskID, &runID, &phase, &root)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	kind, line := "steer_read", steerReadLine(phase, runID, root)
	if !read {
		kind, line = "steer_failed", steerFailedLine(root, phase, runID, why)
	}
	_, err = RecordWakeTx(ctx, tx, org, taskID, kind, kind+":"+root, line)
	return err
}

// steerReadLine tells the conductor its steer root was read.
func steerReadLine(phase, runID, root string) string {
	return render(steerReadTmpl, struct{ Phase, RunID, Root string }{phase, runID, root})
}

// steerFailedLine tells the conductor its steer root was not delivered.
func steerFailedLine(root, phase, runID, why string) string {
	return render(steerFailedTmpl, struct{ Root, Phase, RunID, Why string }{root, phase, runID, why})
}
