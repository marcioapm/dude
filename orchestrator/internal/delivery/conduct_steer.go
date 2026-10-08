package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ConductSteer is the steer tool: the task's live conductor steers a live
// phase Run of its own task's current attempt, as a person would. Allowed
// whoever decides: steering takes no delivery decision. Returns the
// directive and where the Run's harness last said a steer lands ("" when
// lux has not said yet).
func ConductSteer(ctx context.Context, tx pgx.Tx, ref RunRef, runID, text string, interrupt bool) (Steered, string, error) {
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
		return Steered{}, "", refusef("you are no longer this task's conductor: another took over from you. Steer nothing")
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, ref.TaskID).Scan(&status); err != nil {
		return Steered{}, "", err
	}
	if Ended(status) {
		return Steered{}, "", refusef("this task is %s: you are read-only now and steer nothing. Offer to record the change as a "+
			"follow-up task (create_task), linked to this one, and create it once the person agrees", status)
	}
	if strings.TrimSpace(text) == "" {
		return Steered{}, "", refusef("say what to tell the Run: text is required")
	}
	if len(text) > SteerTextMax {
		return Steered{}, "", refusef("a steer is at most %d KiB of text", SteerTextMax>>10)
	}
	var taskID, kind, phase, role, rstatus string
	var current, ending bool
	// Ending: being aborted, its work being collected (the push asked for),
	// or its container stopped on its own: it would never read the words.
	// A finished turn not yet collected is not: as a person's steer, the
	// words start its next turn.
	err := tx.QueryRow(ctx, `SELECT COALESCE(r.task_id, ''), r.kind, COALESCE(r.phase::text, ''), COALESCE(r.role::text, ''), r.status::text,
			r.attempt = (SELECT max(k.attempt) FROM runs k WHERE k.task_id = r.task_id AND k.phase IS NOT NULL),
			r.control = 'abort' OR r.push_request_id IS NOT NULL
			  OR (COALESCE(r.lux_state, '') IN ('stopped', 'succeeded', 'failed', 'cancelled', 'terminated', 'lost') AND r.lux_stop_reason IS NULL
			      AND r.control = 'none')
		FROM runs r WHERE r.id = $1 FOR UPDATE`, runID).Scan(&taskID, &kind, &phase, &role, &rstatus, &current, &ending)
	switch {
	case db.IsNotFound(err) || err == nil && taskID != ref.TaskID:
		return Steered{}, "", refusef("%s is not a Run of your task: you steer only the Runs of this task", runID)
	case err != nil:
		return Steered{}, "", err
	case runID == ref.RunID || role == RoleConductor:
		return Steered{}, "", refusef("%s is a conductor, not a phase Run: steer the Runs you started", runID)
	case kind != "agent":
		return Steered{}, "", refusef("%s is a branch preview, not an agent: there is nobody to steer", runID)
	case phase == "":
		return Steered{}, "", refusef("%s is not a phase Run", runID)
	case !current:
		return Steered{}, "", refusef("%s is a Run of an earlier attempt: steer only the current attempt's", runID)
	}
	switch rstatus {
	case "pending", "scheduled", "starting", "running", "paused":
		if ending {
			return Steered{}, "", refusef("%s is ending: it would never read a steer. Wait for it to end, then start another phase", runID)
		}
	default:
		return Steered{}, "", refusef("%s has ended (%s): start another phase instead of steering it", runID, rstatus)
	}
	out, err := Steer(ctx, tx, ref.Org, SteerInput{RunID: runID, Text: text, Interrupt: interrupt,
		Actor: Writer{ActorType: ledger.ActorAgent, ActorID: ref.RunID}, Conductor: ref.RunID})
	if se, ok := err.(SteerError); ok {
		return Steered{}, "", refusef("%s", se.Msg)
	}
	if err != nil {
		return Steered{}, "", err
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
	kind, line := "steer_read", fmt.Sprintf("Your %s Run %s read your steer %s.", phase, runID, root)
	if !read {
		kind, line = "steer_failed", fmt.Sprintf("Your steer %s to your %s Run %s was not delivered: %s", root, phase, runID,
			clip(oneLine(why), 120))
	}
	_, err = RecordWakeTx(ctx, tx, org, taskID, kind, kind+":"+root, line)
	return err
}
