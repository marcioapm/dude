package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// A conducted delivery waiting on its pull requests has no decision open,
// yet its conductor may want a phase run now — a review of what a person
// pushed, a fix of something said in Chat, a test. start_phase fix, review
// or test is taken then as a decision at pull request feedback, under the
// same bounds; refused while anything that writes the task branch is at
// work (one writer at a time).

// prWaitPhases are what may start while the delivery waits on its pull requests.
var prWaitPhases = []string{PhaseFix, PhaseReview, PhaseTest}

// waitingOnPR says the delivery is parked on its pull requests, nothing running.
func (d *Delivery) waitingOnPR() bool {
	return d.Status == "waiting" && d.Step == "awaitPullRequest" && len(d.State.PullRequestIDs) > 0
}

// startAtPRWait takes start_phase on a delivery waiting on its pull
// requests: the workflow moves to the decision, taken, and carries it out
// as it would at pull request feedback. The caller holds the task's Chat
// lock (parked); this locks the delivery's row.
func startAtPRWait(ctx context.Context, tx pgx.Tx, ref RunRef, w *Delivery, in StartPhase) (string, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return "", err
	}
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if d == nil || d.WorkflowID != w.WorkflowID || !d.waitingOnPR() {
		return "", refusef("the delivery moved on as you asked; you are woken at its next decision")
	}
	if !slices.Contains(prWaitPhases, in.Phase) {
		return "", refusef("%s cannot start while the delivery waits on its pull requests; it can start %s", in.Phase, listOr(prWaitPhases))
	}
	if held, err := stepHolds(ctx, tx, d.WorkflowID, ""); err != nil {
		return "", err
	} else if held {
		return "", refusef("the delivery is reading what changed on its pull requests now; start %s again in a moment", in.Phase)
	}
	var writer, publishing bool
	if err := tx.QueryRow(ctx, `SELECT `+liveWriter+`, EXISTS (SELECT 1 FROM conductor_publishes WHERE task_id = $1 AND status IN `+publishLive+`)`,
		ref.TaskID).Scan(&writer, &publishing); err != nil {
		return "", err
	}
	if writer || publishing {
		return "", refusef("a Run or a publish is changing the task branch now: one writer at a time; start %s once it is done", in.Phase)
	}
	if len(in.Categories) > 0 && in.Phase != PhaseReview || len(in.FindingIDs) > 0 && in.Phase != PhaseFix {
		return "", refusef("categories are for review, findings for fix")
	}
	st := &d.State
	switch in.Phase {
	case PhaseReview:
		for _, c := range in.Categories {
			if !slices.Contains(reviewCategories(), c) {
				return "", refusef("no reviewer %q: the categories are %s", c, listOr(reviewCategories()))
			}
		}
		if err := reviewBound(st); err != nil {
			return "", err
		}
	case PhaseFix:
		if len(in.FindingIDs) > 0 {
			return "", refusef("a fix while the pull request waits addresses what you ask in note, not findings: review first")
		}
		repos, err := openPRRepos(ctx, tx, st.PullRequestIDs)
		if err != nil {
			return "", err
		}
		if err := prFixBoundFor(ctx, tx, st, repos); err != nil {
			return "", err
		}
		if strings.TrimSpace(in.Note) == "" {
			return "", refusef("say what the fix is to do (note): no pull request feedback is waiting for it")
		}
	}
	st.Decision = &Pending{Point: PointPRFeedback, Policy: "awaitPullRequest"}
	st.PRFeedback = nil
	raw, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	// The row's state as the decision found it, at the decision step; the
	// pull request signals waiting stay unconsumed for when it waits again.
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = 'conductorDecision',
			awaiting_signals = '["`+SignalConductorDecision+`"]'::jsonb, wake_at = NULL
		WHERE id = $1`, d.WorkflowID, raw); err != nil {
		return "", err
	}
	if err := take(ctx, tx, ref, d, Taken{Action: "start_phase", Phase: in.Phase, Categories: in.Categories,
		Note: clip(strings.TrimSpace(in.Note), noteChars)}); err != nil {
		return "", err
	}
	return fmt.Sprintf("The delivery starts %s now, from the pull request's head, while it waits on it. You are woken when it is done.", in.Phase), nil
}

// openPRRepos names the repositories of the pull requests still open.
func openPRRepos(ctx context.Context, tx pgx.Tx, ids []string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT r.name FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.id = ANY($1) AND pr.state IN ('open', 'draft') ORDER BY r.name`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
