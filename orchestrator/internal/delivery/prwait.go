package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// A conducted delivery waiting on its pull requests has no decision open,
// yet its conductor may want a phase run now — a review of what a person
// pushed, a fix of something said in Chat, a test, the simplifier after
// the last change (what the ready gate asks for). start_phase fix, review,
// simplify or test is taken then as a decision at pull request feedback,
// under the same bounds; refused while anything that writes the task
// branch is at work (one writer at a time).

// prWaitPhases are what may start while the delivery waits on its pull requests.
var prWaitPhases = []string{PhaseFix, PhaseReview, PhaseSimplify, PhaseTest}

// waitingOnPR says the delivery is parked on its pull requests, no phase
// running.
func (d *Delivery) waitingOnPR() bool {
	// "running" too: a signal (a person's push, readiness) woke the step and
	// the poller has not run it yet. A step being run holds its lease, which
	// startAtPRWait refuses as "in a moment" (stepHolds).
	return (d.Status == "waiting" || d.Status == "running") && d.Step == "awaitPullRequest" && len(d.State.PullRequestIDs) > 0
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
		return "", refuseText(rDeliveryMovedOnAsYouAsked)
	}
	if !slices.Contains(prWaitPhases, in.Phase) {
		return "", refusef(rCannotStartWhileDeliveryWaitsOn, in.Phase, listOr(prWaitPhases))
	}
	if held, err := stepHolds(ctx, tx, d.WorkflowID, ""); err != nil {
		return "", err
	} else if held {
		return "", refusef(rDeliveryReadingWhatChangedOnIts, in.Phase)
	}
	var writer, publishing bool
	if err := tx.QueryRow(ctx, `SELECT `+liveWriter+`, EXISTS (SELECT 1 FROM conductor_publishes WHERE task_id = $1 AND status IN `+publishLive+`)`,
		ref.TaskID).Scan(&writer, &publishing); err != nil {
		return "", err
	}
	if writer || publishing {
		return "", refusef(rRunOrPublishChangingTaskBranch, in.Phase)
	}
	if len(in.Categories) > 0 && in.Phase != PhaseReview || len(in.FindingIDs) > 0 && in.Phase != PhaseFix {
		return "", refuseText(rCategoriesAreForReviewFindingsFor)
	}
	then, round, err := pipelineOf(in)
	if err != nil {
		return "", err
	}
	st := &d.State
	switch in.Phase {
	case PhaseSimplify:
		// Not past a push of dude's no review covered: the simplifier's
		// own commits would be reviewed with it, unasked.
		if err := refusePullRequestOpenTx(ctx, tx, ref.TaskID, d); err != nil {
			return "", err
		}
	case PhaseReview:
		for _, c := range in.Categories {
			if !slices.Contains(reviewCategories(), c) {
				return "", refusef(rNoReviewerCategoriesAre, c, listOr(reviewCategories()))
			}
		}
	case PhaseFix:
		if err := refuseFixAtPRWait(ctx, tx, st, in); err != nil {
			return "", err
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
		Note: clip(strings.TrimSpace(in.Note), noteChars), Then: then, Round: round}); err != nil {
		return "", err
	}
	return prPhaseStarted(in.Phase), nil
}

// refuseFixAtPRWait bounds a fix started with no pull request feedback
// waiting for it: a fix of what note says, not of findings, within the
// fix bound of the pull requests still open.
func refuseFixAtPRWait(ctx context.Context, tx pgx.Tx, st *State, in StartPhase) error {
	if len(in.FindingIDs) > 0 {
		return refuseText(rFixWhilePullRequestWaitsAddresses)
	}
	repos, err := openPRRepos(ctx, tx, st.PullRequestIDs)
	if err != nil {
		return err
	}
	if err := prFixBoundFor(ctx, tx, st, repos); err != nil {
		return err
	}
	if strings.TrimSpace(in.Note) == "" {
		return refuseText(rSayWhatFixDoNoteNo)
	}
	return nil
}

// prPhaseStarted is what start_phase answers for a phase started while the
// delivery waits on its pull requests.
func prPhaseStarted(phase string) string { return render(prPhaseStartedTmpl, phase) }

// openPRRepos names the repositories of the pull requests still open.
func openPRRepos(ctx context.Context, tx pgx.Tx, ids []string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT r.name FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.id = ANY($1) AND pr.state IN ('open', 'draft') ORDER BY r.name`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// checksPassedSinceTx says the pull request feedback decision d waits on
// has nothing left to act on: its feedback was failing checks only, no
// integration and no person's item in dude is open, and each pull request
// those checks failed on is open with its checks recorded passing at the
// head the delivery has for it (a rerun went green; a head someone moved
// is left as it is). Such a decision is taken as one at the pull request
// wait (Policy awaitPullRequest, the feedback dropped, written to the
// row): what may start there may start, and next waits on the pull
// requests again instead of starting a fix with nothing to do.
func checksPassedSinceTx(ctx context.Context, tx pgx.Tx, d *Delivery) (bool, error) {
	p, st := d.State.Decision, &d.State
	if p == nil || p.Point != PointPRFeedback || p.Policy != "prFix" || st.Integration != nil || len(st.PRFeedback) == 0 {
		return false, nil
	}
	for _, f := range st.PRFeedback {
		if !isChecks(f) {
			return false, nil
		}
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM feedback_items f WHERE `+openItems, st.TaskID, st.Attempt).Scan(&open); err != nil {
		return false, err
	}
	if open > 0 {
		return false, nil
	}
	repos := feedbackRepos(st.PRFeedback)
	heads := make([]string, len(repos))
	for i, repo := range repos {
		if heads[i] = st.Heads[repo]; heads[i] == "" {
			return false, nil
		}
	}
	var passed bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM unnest($2::text[], $3::text[]) h(repo, head) WHERE NOT EXISTS (
			SELECT 1 FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = ANY($1) AND r.name = h.repo AND pr.state IN ('open', 'draft') AND pr.head_sha = h.head
			AND pr.checks::text = 'passing'))`, st.PullRequestIDs, repos, heads).Scan(&passed); err != nil || !passed {
		return false, err
	}
	p.Policy, st.PRFeedback = "awaitPullRequest", nil
	raw, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{decision}', $2::jsonb) - 'prFeedback' WHERE id = $1`,
		d.WorkflowID, raw)
	return err == nil, err
}
