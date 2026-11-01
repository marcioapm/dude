package orchestrator_test

// The conductor's fix-then-review (design step 3, §2): start_phase's then
// queues a review behind a fix or a simplifier, which dude starts as soon
// as it has committed, without a decision between.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// untaken says the task's delivery is parked on a decision not yet taken.
func (w *world) untaken(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'conductorDecision'
		AND state->'decision' ? 'point' AND NOT state->'decision' ? 'taken'`, task) == 1
}

// start_phase's then is for a fix or a simplifier, naming known reviewers;
// round is for a review, a fix or a simplifier. Each refused call takes no
// decision; the decision is still the conductor's after it, and a valid
// then is taken, its then and round on conductor.decided.
func TestThenIsOnlyForFixOrSimplify(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.talk(task)
	refused := func(args string) {
		t.Helper()
		if status, out := w.as(task, "start_phase", args); status != 422 {
			t.Errorf("start_phase %s: %d %v, want refused", args, status, out)
		}
		if !w.untaken(task) {
			t.Fatalf("start_phase %s took the decision", args)
		}
	}
	refused(`{"phase":"implement","then":{"review":["tests"]}}`)
	refused(`{"phase":"implement","round":{"scope":"whole"}}`)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	refused(`{"phase":"review","then":{"review":["tests"]}}`)
	refused(`{"phase":"test","then":{"review":["tests"]}}`)
	refused(`{"phase":"fix","then":{"review":["tests","spelling"]}}`)
	refused(`{"phase":"fix","then":{"review":[]}}`)
	refused(`{"phase":"fix","then":{"review":["tests"],"wake":"later"}}`)
	refused(`{"phase":"fix","round":{"scope":"part"}}`)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"],"wake":"after_fix","note":"N"},"round":{"bar":"B"}}`)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'phase' = 'fix'
		AND payload->'then' = '{"review":["tests"],"wake":"after_fix","note":"N"}'::jsonb AND payload->'round' = '{"bar":"B"}'::jsonb`,
		task, delivery.EvDecisionTaken); n != 1 {
		t.Errorf("conductor.decided with then and round: %d, want 1", n)
	}
}

// then.wake is after_review when not said.
func TestThenWakesAfterTheReviewByDefault(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->'then'->>'wake' = $3 AND NOT payload ? 'round'`, task, delivery.EvDecisionTaken, delivery.WakeAfterReview); n != 1 {
		t.Errorf("conductor.decided with then.wake after_review: %d, want 1", n)
	}
}

// fixPlays has w's fixer (and simplifier) play b instead of committing;
// nil plays it as scripted.
func (w *world) fixPlays(play func(spec map[string]any) *fakelux.Behaviour) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if p := labels["dude.phase"]; p == "fix" || p == "simplify" {
			if b := play(spec); b != nil {
				return *b
			}
		}
		return scripted(spec)
	}
}

// cursor is the task's latest event's cursor.
func (w *world) cursor(task string) int64 {
	var c int64
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(max(cursor), 0) FROM events WHERE task_id = $1`, task).Scan(&c)
	return c
}

// awaited is the decision points the task's delivery parked on for its
// conductor since cursor from, in order.
func (w *world) awaited(task string, from int64) []string {
	return w.ids(`SELECT payload->>'point' FROM events WHERE task_id = $1 AND cursor > $2 AND event_type = $3 ORDER BY cursor`,
		task, from, delivery.EvDecisionAwaited)
}

// phasesSince is the phases of the task's Runs created since cursor from,
// in order.
func (w *world) phasesSince(task string, from int64) []string {
	return w.ids(`SELECT payload->>'phase' FROM events WHERE task_id = $1 AND cursor > $2 AND event_type = 'run.created'
		AND payload ? 'reviewers' IS FALSE AND run_id IS NOT NULL ORDER BY cursor`, task, from)
}

// reviewersSince is each review round's reviewers since cursor from, in order.
func (w *world) reviewersSince(task string, from int64) []string {
	return w.ids(`SELECT payload->>'reviewers' FROM events WHERE task_id = $1 AND cursor > $2 AND event_type = 'run.created'
		AND payload->>'phase' = 'review' AND payload ? 'reviewers' ORDER BY cursor`, task, from)
}

// fixedWith says the task's delivery is parked after a fix, the review it
// queued offered with the reviewers want.
func (w *world) fixedWith(task, want string) bool {
	return w.decisionAt(task) == delivery.PointFixed && w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
		AND state->'decision'->'queued'->'categories' = $2::jsonb`, task, want) == 1
}

// A conducted start_phase fix with then.review runs the fix, then the
// review with exactly those reviewers, and parks once, after the review:
// no decision after the fix between them.
func TestAFixThenReviewRunsTheReviewWithoutWakingTheConductor(t *testing.T) {
	for _, phase := range []string{"fix", "simplify"} {
		t.Run(phase, func(t *testing.T) {
			w := roundsWorld(t)
			task := w.task()
			w.toRoundOne(task)
			from := w.cursor(task)
			w.must(task, "start_phase", `{"phase":"`+phase+`","then":{"review":["tests"]}}`)
			w.until("the queued review's round", func() bool {
				return w.decisionAt(task) == delivery.PointReviewed && len(w.awaited(task, from)) > 0
			})
			if got := w.awaited(task, from); fmt.Sprint(got) != "["+delivery.PointReviewed+"]" {
				t.Errorf("decisions after the start: %v, want [%s]", got, delivery.PointReviewed)
			}
			if got := w.reviewersSince(task, from); fmt.Sprint(got) != `[["tests"]]` {
				t.Errorf("review rounds' reviewers after the start: %v, want [[\"tests\"]]\n%s", got, w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'pipeline'`, task); n != 0 {
				t.Errorf("the pipeline is still in State after its review")
			}
		})
	}
}

// then.wake after_fix parks after the fix with the queued review offered:
// decide next runs it with its reviewers; start_phase review with others
// replaces it.
func TestAFixThenReviewWakesAfterTheFixWhenAskedTo(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests","quality"],"wake":"after_fix"}}`)
	w.until("after the fix, the review queued", func() bool { return w.fixedWith(task, `["tests","quality"]`) })
	if n := w.phaseRuns(task, "review"); n != w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 1`, task) {
		t.Fatalf("a review started after the fix before the conductor decided\n%s", w.describeRuns())
	}
	from := w.cursor(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
	if got := w.reviewersSince(task, from); fmt.Sprint(got) != `[["tests", "quality"]]` {
		t.Errorf("decide next's review: %v, want the queued [tests quality]", got)
	}

	t.Run("start_phase review replaces it", func(t *testing.T) {
		w := roundsWorld(t)
		task := w.task()
		w.toRoundOne(task)
		w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"],"wake":"after_fix"}}`)
		w.until("after the fix, the review queued", func() bool { return w.fixedWith(task, `["tests"]`) })
		from := w.cursor(task)
		w.must(task, "start_phase", `{"phase":"review","categories":["performance"]}`)
		w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
		if got := w.reviewersSince(task, from); fmt.Sprint(got) != `[["performance"]]` {
			t.Errorf("the conductor's own review: %v, want [performance]", got)
		}
	})
}

// stoppedFact is the fact a pipeline stopped at its fix says, as dude
// renders it (wakes/facts/pipeline-stopped).
func stoppedFact(c, outcome, note string, review ...string) string {
	return delivery.MustText("wakes/facts/pipeline-stopped").Render(struct {
		Case, Error, Outcome, Note string
		Review                     []string
		Queued                     bool
	}{c, "", outcome, note, review, true})
}

// A pipeline's fix that commits nothing, or whose writer reports itself
// partial or blocked, wakes the conductor at the fix with that fact; the
// review it queued does not start, and is offered as decide next.
func TestAPipelinesFixThatFallsShortWakesTheConductor(t *testing.T) {
	for _, c := range []struct {
		name, kase, outcome, note string
		play                      fakelux.Behaviour
	}{
		{"commits nothing", "nothing", "done", "", fakelux.Behaviour{Reply: "Nothing to change."}},
		{"partial", "outcome", "partial", "the second greeting is left", fakelux.Behaviour{
			Commit: map[string]string{"FACTORY.md": "half\n"}, Message: "fix",
			Checks: "```yaml\noutcome: partial\noutcome_note: the second greeting is left\nchecks: []\n```"}},
		{"blocked", "outcome", "blocked", "no access", fakelux.Behaviour{
			Commit: map[string]string{"FACTORY.md": "half\n"}, Message: "fix",
			Checks: "```yaml\noutcome: blocked\noutcome_note: no access\nchecks: []\n```"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := roundsWorld(t)
			w.fixPlays(func(map[string]any) *fakelux.Behaviour { b := c.play; return &b })
			task := w.task()
			w.toRoundOne(task)
			reviews := w.phaseRuns(task, "review")
			w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
			w.until("after the fix", func() bool { return w.fixedWith(task, `["tests"]`) })
			if n := w.phaseRuns(task, "review"); n != reviews {
				t.Errorf("%d reviews started after the fix", n-reviews)
			}
			fact := stoppedFact(c.kase, c.outcome, c.note, "tests")
			if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'decision'
				AND strpos(line, $2) > 0`, task, fact); n != 1 {
				t.Errorf("wakes saying %q: %d, want 1", fact, n)
			}
		})
	}
}

// A pipeline's fix that fails escalates as any fix does: its queued
// review goes with it.
func TestAFailedFixInAPipelineEscalatesAndDropsTheQueuedReview(t *testing.T) {
	w := roundsWorld(t)
	w.fixPlays(func(map[string]any) *fakelux.Behaviour { return &fakelux.Behaviour{FailToStart: true} })
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
	w.until("the fix's escalation", func() bool { return w.escalationReason(task) == "fix_failed" })
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state ? 'pipeline' OR state->'decision' ? 'queued')`, task); n != 0 {
		t.Errorf("the queued review outlived the failed fix")
	}
}

// hangingFix has w's first fixer hang, committing once its turn ends
// (after a resume); every later one commits at once.
func (w *world) hangingFix() {
	var n atomic.Int32
	w.fixPlays(func(spec map[string]any) *fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] != "fix" || n.Add(1) > 1 {
			return nil
		}
		return &fakelux.Behaviour{Hang: true, Commit: map[string]string{"FACTORY.md": "fixed after a while\n"}, Message: "fix"}
	})
}

// fixAtWork is the pipeline's fix, running.
func (w *world) fixAtWork(task string) string {
	var fix string
	w.until("the fix at work", func() bool {
		fix = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'
			AND lux_state = 'running'`, task)
		return fix != ""
	})
	return fix
}

// A pipeline's fix restarted is the fix its review is queued behind: the
// replacement's end starts the review with no decision between.
func TestRestartingAPipelinesFixKeepsItsReview(t *testing.T) {
	w := roundsWorld(t)
	w.hangingFix()
	task := w.task()
	w.toRoundOne(task)
	from := w.cursor(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
	fix := w.fixAtWork(task)
	out := w.must(task, "restart_run", `{"run":"`+fix+`","note":"again"}`)
	fresh, _ := out["run"].(string)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'pipeline'->>'fix' = $2`, task, fresh); n != 1 {
		t.Errorf("the pipeline's fix is not the replacement %s", fresh)
	}
	w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
	if got := w.awaited(task, from); fmt.Sprint(got) != "["+delivery.PointReviewed+"]" {
		t.Errorf("decisions after the start: %v, want [%s]", got, delivery.PointReviewed)
	}
	if got := w.reviewersSince(task, from); fmt.Sprint(got) != `[["tests"]]` {
		t.Errorf("review rounds' reviewers: %v, want [[\"tests\"]]", got)
	}
}

// A person's Stop while a pipeline's fix works keeps the pipeline: a
// resume takes the same fix up again, and its review follows with no
// decision. A Try again runs the fix afresh, no longer the conductor's
// queued unit: it parks after the fix, nothing queued.
func TestAStoppedPipelineResumesWithItsReview(t *testing.T) {
	for _, action := range []string{"resume", "retry"} {
		t.Run(action, func(t *testing.T) {
			w := roundsWorld(t)
			w.hangingFix()
			task := w.task()
			ana := w.ownedBy(task, "Ana")
			w.toRoundOne(task)
			from := w.cursor(task)
			w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
			fix := w.fixAtWork(task)
			w.stop(ana, task, map[string]any{"note": "hold on"})
			w.until("the fixer kept", func() bool { return w.keptRun(fix) })
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'pipeline'->>'fix' = $2`, task, fix); n != 1 {
				t.Fatalf("the stop dropped the pipeline")
			}
			if status, body := w.callAs(ana, "/internal/tasks/"+task+"/recover", map[string]any{"action": action}); status != 200 {
				t.Fatalf("%s: %d %v", action, status, body)
			}
			if action == "resume" {
				w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
				if got := w.awaited(task, from); fmt.Sprint(got) != "["+delivery.PointReviewed+"]" {
					t.Errorf("decisions after the start: %v, want [%s]", got, delivery.PointReviewed)
				}
				if got := w.reviewersSince(task, from); fmt.Sprint(got) != `[["tests"]]` {
					t.Errorf("review rounds' reviewers: %v, want [[\"tests\"]]", got)
				}
				return
			}
			w.until("after the retried fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state ? 'pipeline' OR state->'decision' ? 'queued')`, task); n != 0 {
				t.Errorf("the retried fix kept the conductor's queued review")
			}
		})
	}
}
