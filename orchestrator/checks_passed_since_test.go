package orchestrator_test

// At a pull request feedback decision whose failing checks have since
// passed at the same head, nothing is left to fix: what the pull request
// wait allows may start (the review its ready gate names), and next starts
// no empty fix. With a check still failing, the decision is as before.

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// checksFailedThenRerun is a conducted delivery parked at pull request
// feedback for a failing check; when green, the check reran passing at the
// same head, recorded before the conductor acts.
func checksFailedThenRerun(t *testing.T, green bool) (*world, string, string) {
	w, task := conductedDirectWorld(t)
	head := w.gh.SHA(w.gh.Pull(1).Head)
	w.gh.SetChecks("run:failure")
	w.until("the failing check to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	if green {
		w.gh.SetChecks("run:success")
		w.untilChecks(task, "passing")
	}
	if got := w.gh.SHA(w.gh.Pull(1).Head); got != head {
		t.Fatalf("the head moved: %s, was %s", got, head)
	}
	return w, task, head
}

func TestAReviewStartsAtAFeedbackDecisionWhoseFailingCheckPassedSince(t *testing.T) {
	w, task, head := checksFailedThenRerun(t, true)
	fixes := w.phaseRuns(task, "fix")
	out := w.must(task, "start_phase", `{"phase":"review","categories":["performance"]}`)
	if next, _ := out["next"].(string); next == "" {
		t.Fatalf("start_phase review: %v", out)
	}
	w.until("the review", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, task, head) > 0
	})
	if n := w.phaseRuns(task, "fix"); n != fixes {
		t.Errorf("%d fix Runs started for a check that passed since", n-fixes)
	}
}

func TestNextAtAFeedbackDecisionWhoseFailingCheckPassedSinceStartsNoFix(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, true)
	fixes := w.phaseRuns(task, "fix")
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting on the pull request again, or a fix", func() bool {
		return w.waitingOnPR(task) || w.phaseRuns(task, "fix") != fixes
	})
	w.syncRounds(2)
	if n := w.phaseRuns(task, "fix"); n != fixes {
		t.Errorf("%d fix Runs started by next for a check that passed since", n-fixes)
	}
}

func TestAReviewAtAFeedbackDecisionWithACheckStillFailingIsRefused(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, false)
	w.refused(task, "start_phase", `{"phase":"review","categories":["performance"]}`,
		"review cannot start at this decision (pull request feedback); it can start fix")
}

// Each of these leaves something to act on even though the check went
// green: a person's item open in dude, a head other than the one the
// delivery has, a pull request no longer open, an integration under way.
// The decision stays the feedback decision: a review is refused, as with
// the check still failing.
func TestAFeedbackDecisionIsNotSettledWhileSomethingIsLeft(t *testing.T) {
	for _, c := range []struct {
		name string
		left func(w *world, task string)
	}{
		{"a person's item open", func(w *world, task string) {
			ana := w.person("Ana")
			mustExec(w.t, w.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
				SELECT 'fbb-open', $1, $2, task_attempt($2), person_id, 'k' FROM api_keys WHERE id = $3`, w.org, task, ana)
			mustExec(w.t, w.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body,
				anchor_kind, status, submitted_at) SELECT 'fbi-open', $1, $2, task_attempt($2), 'fbb-open', person_id, 'Rename greet.',
				'general', 'open', now() FROM api_keys WHERE id = $3`, w.org, task, ana)
		}},
		{"passing at another head", func(w *world, task string) {
			mustExec(w.t, w.owner, `UPDATE pull_requests SET head_sha = repeat('e', 40) WHERE task_id = $1`, task)
		}},
		{"the pull request closed", func(w *world, task string) {
			mustExec(w.t, w.owner, `UPDATE pull_requests SET state = 'closed' WHERE task_id = $1`, task)
		}},
		{"an integration under way", func(w *world, task string) {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{integration}',
				jsonb_build_object('repo', 'app', 'number', 1, 'base', 'main', 'head', 'x', 'baseHead', 'y'))
				WHERE task_id = $1 AND step = 'conductorDecision' AND status = 'waiting'`, task)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, task, _ := checksFailedThenRerun(t, true)
			c.left(w, task)
			w.refused(task, "start_phase", `{"phase":"review","categories":["performance"]}`,
				"review cannot start at this decision (pull request feedback); it can start fix")
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'prFeedback'
				AND state->'decision'->>'policy' = 'prFix'`, task); n != 1 {
				t.Errorf("the feedback decision was converted with something left")
			}
		})
	}
}

// Taken as the pull request wait's, the decision's stored state keeps no
// feedback: the failing check it was for is gone with it, in the row the
// workflow's next step reads.
func TestASettledFeedbackDecisionDropsTheFeedbackFromItsRow(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, true)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'prFeedback'`, task); n != 1 {
		t.Fatalf("no feedback stored before the conversion")
	}
	w.must(task, "start_phase", `{"phase":"review","categories":["performance"]}`)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'conductorDecision'
		AND state->'decision'->>'policy' = 'awaitPullRequest' AND state->'decision' ? 'taken' AND NOT state ? 'prFeedback'`, task); n != 1 {
		t.Errorf("the settled decision's row still has prFeedback (or was not converted)")
	}
}

// start_phase fix at a settled decision is a fix of what note says, as at
// the wait: refused without a note, refused naming findings, and given a
// note, the fixer is not handed the check that passed since.
func TestAFixAtASettledFeedbackDecisionIsAFixOfTheNote(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, true)
	w.refused(task, "start_phase", `{"phase":"fix"}`, "say what the fix is to do (note)")
	w.refused(task, "start_phase", `{"phase":"fix","note":"Rename it.","findings":["f-1"]}`,
		"a fix while the pull request waits addresses what you ask in note, not findings")
	fixes := w.phaseRuns(task, "fix")
	out := w.must(task, "start_phase", `{"phase":"fix","note":"Rename greet to welcome."}`)
	if next, _ := out["next"].(string); next == "" {
		t.Fatalf("start_phase fix: %v", out)
	}
	w.until("the fix", func() bool { return w.phaseRuns(task, "fix") > fixes })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback::text LIKE '%"checks"%'`,
		task); n != 0 {
		t.Errorf("the fixer was handed the check that passed since (%d fix Runs with checks feedback)", n)
	}
	fix := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at DESC LIMIT 1`, task)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND (pr_feedback IS NULL OR pr_feedback = '[]'::jsonb)
		AND (check_logs IS NULL OR check_logs = '[]'::jsonb OR check_logs = '{}'::jsonb)`, fix); n != 1 {
		t.Errorf("the fixer was handed the check that passed since: pr_feedback %s",
			w.str(`SELECT COALESCE(pr_feedback::text, '') FROM runs WHERE id = $1`, fix))
	}
	if note := w.str(`SELECT COALESCE(conductor_note, '') FROM runs WHERE id = $1`, fix); !strings.Contains(note, "Rename greet to welcome.") {
		t.Errorf("the fixer's note is %q, want the conductor's", note)
	}
}

// next at a settled decision waits on the pull requests again and weighs
// their gate at once: the gate is stored anew with no sync and no timer.
// The signals the rerun's sync queued are spent first, so that only next
// can be what weighs it.
func TestNextAtASettledFeedbackDecisionWeighsTheGateAtOnce(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, true)
	mustExec(t, w.owner, `UPDATE workflow_signals SET consumed_at = now() WHERE consumed_at IS NULL
		AND workflow_run_id IN (SELECT id FROM workflow_runs WHERE task_id = $1)`, task)
	before := w.str(`SELECT now()::text`)
	w.must(task, "decide", `{"action":"next"}`)
	for range 5 {
		w.pump()
	}
	if !w.waitingOnPR(task) {
		t.Fatalf("not waiting on the pull request after next\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND (ready_gate->>'at')::timestamptz > $2::timestamptz`,
		task, before); n != 1 {
		t.Errorf("the ready gate was not weighed after next (task %s)", w.taskStatus(task))
	}
}
