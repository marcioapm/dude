package orchestrator_test

// The conductor's fix-then-review (design step 3, §2): start_phase's then
// queues a review behind a fix or a simplifier, which dude starts as soon
// as it has committed, without a decision between.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
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
			if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $3
				AND strpos(line, $2) > 0`, task, fact, delivery.WakeRound); n != 1 {
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

// roundWakeLine is the task's latest round wake, as recorded.
func (w *world) roundWakeLine(task string) string {
	return w.str(`SELECT COALESCE((SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = $2
		ORDER BY created_at DESC, id DESC LIMIT 1), '')`, task, delivery.WakeRound)
}

// roundWake is a round wake as dude renders it (wakes/round), on one line.
func roundWake(round, open, below int, rng string, lenses []string, shown []map[string]any, fix string, long bool) string {
	return roundWakeMore(round, open, below, rng, lenses, shown, 0, fix, long)
}

// roundWakeMore is roundWake counting more findings at the bar than it shows.
func roundWakeMore(round, open, below int, rng string, lenses []string, shown []map[string]any, more int, fix string, long bool) string {
	line := strings.Join(strings.Fields(delivery.MustText("wakes/round").Render(map[string]any{
		"Round": round, "Open": open, "Below": below, "People": 0, "More": more, "Range": rng, "Lenses": lenses,
		"Shown": shown, "Fix": fix, "Long": long})), " ")
	return delivery.MustText("wakes/decision-waiting").Render(map[string]any{
		"Point": delivery.MustText("wakes/facts/point-" + delivery.PointReviewed).Render(nil), "Line": line})
}

// A conducted review round wakes the conductor with the round wake: its
// number and what it reviewed, each finding at the fix bar with its
// severity, trivial, introduced, unverified and place, the count of those
// below it, and the fix before it — its commits and its writer's outcome.
// From round 8 on it says the loop is long; a fact, no bound.
func TestARoundWakeListsTheFindingsAtTheBarAndTheFix(t *testing.T) {
	w := conducting(t)
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: high\nintroduced: yes", wrongThing) +
					"---\nseverity: medium\ncategory: correctness\nfile: FACTORY.md\nline: 3\ntitle: Unproven\ndescription: D.\n" +
					"---\nseverity: low\ncategory: correctness\nfile: FACTORY.md\nline: 4\ntitle: Below the bar\ndescription: D.\ntrivial: no\n```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix",
				Checks: "```yaml\noutcome: partial\noutcome_note: one left\nchecks: []\n```"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.toRoundOne(task)
	ids := w.ids(`SELECT id FROM review_findings WHERE task_id = $1 ORDER BY line`, task)
	if len(ids) != 3 {
		t.Fatalf("findings %v, want 3", ids)
	}
	shown := []map[string]any{
		{"ID": ids[0], "Severity": "high", "Title": wrongThing, "Where": "FACTORY.md:1", "Introduced": "yes", "Trivial": false, "Unverified": false},
		{"ID": ids[1], "Severity": "medium", "Title": "Unproven", "Where": "FACTORY.md:3", "Introduced": "", "Trivial": false, "Unverified": true},
	}
	whole := delivery.MustText("wakes/facts/round-range").Render(map[string]any{"Of": ""})
	if got, want := w.roundWakeLine(task), roundWake(1, 2, 1, whole, []string{"correctness"}, shown, "", false); got != want {
		t.Errorf("round 1's wake:\n got %q\nwant %q", got, want)
	}
	// Round 8 of the loop: the fix before it, and the long-loop fact.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"round":7}'::jsonb WHERE task_id = $1`, task)
	before := w.str(`SELECT state->'roundHeads'->>'target' FROM workflow_runs WHERE task_id = $1`, task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]},"round":{"scope":"fix"}}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	// The fix reported itself partial: its review waits for the conductor.
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 8", func() bool { return w.reviewedRound(task, 8) || w.escalationReason(task) != "" })
	if r := w.escalationReason(task); r != "" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	fix := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	head := w.str(`SELECT heads->'target'->>'sha' FROM runs WHERE id = $1`, fix)
	fixLine := delivery.MustText("wakes/facts/round-fix").Render(map[string]any{"Phase": "fix", "RunID": fix,
		"Outcome": "partial", "Note": "one left", "Commits": []string{"target@" + head[:7]}})
	narrow := delivery.MustText("wakes/facts/round-range").Render(map[string]any{"Of": delivery.RangeFixRound,
		"Repos": []string{"target@" + before[:7] + ".." + head[:7]}})
	if got, want := w.roundWakeLine(task), roundWake(8, 2, 1, narrow, []string{"tests"}, shown, fixLine, true); got != want {
		t.Errorf("round 8's wake:\n got %q\nwant %q", got, want)
	}
}

// stepFailed is the task's delivery's step failing (retried or dead-lettered),
// its error.
func (w *world) stepFailed(task string) string {
	return w.str(`SELECT COALESCE((SELECT last_error FROM workflow_runs WHERE task_id = $1 AND last_error IS NOT NULL LIMIT 1), '')`, task)
}

// The conductor's note and its round's bar, each at its own bound, joined
// on a Run fit runs.conductor_note: the step does not fail on it.
func TestANoteAndTheRoundsBarFitTheRunsNote(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", fmt.Sprintf(`{"phase":"fix","note":%q,"then":{"review":["tests"],"wake":"after_fix"},"round":{"bar":%q}}`,
		strings.Repeat("n", 3900), strings.Repeat("b", 900)))
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed || w.stepFailed(task) != "" })
	if e := w.stepFailed(task); e != "" {
		t.Fatalf("the delivery's step failed: %s", e)
	}
	if n := w.count(`SELECT max(length(conductor_note)) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task); n == 0 || n > 4000 {
		t.Errorf("the fixer's note: %d characters", n)
	}
	// What is cut is the note, never the bar: the note ends with it whole.
	bar := strings.TrimSpace(delivery.MustText("contracts/round-bar").Render(strings.Repeat("b", 900)))
	if note := w.str(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'fix'`, task); !strings.HasSuffix(note, "\n\n"+bar) {
		t.Errorf("the fixer's note does not end with the round's whole bar: ...%q", note[max(0, len(note)-120):])
	}
}

// then.note and decide next's note, each at its bound, joined on the
// queued review's Runs fit runs.conductor_note.
func TestAQueuedNoteAndDecideNextsNoteFitTheRunsNote(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", fmt.Sprintf(`{"phase":"fix","then":{"review":["tests"],"wake":"after_fix","note":%q},"round":{"bar":%q}}`,
		strings.Repeat("q", 3000), strings.Repeat("b", 900)))
	w.until("after the fix", func() bool { return w.fixedWith(task, `["tests"]`) })
	w.must(task, "decide", fmt.Sprintf(`{"action":"next","note":%q}`, strings.Repeat("d", 3000)))
	w.until("round 2", func() bool { return w.reviewedRound(task, 2) || w.stepFailed(task) != "" })
	if e := w.stepFailed(task); e != "" {
		t.Fatalf("the delivery's step failed: %s", e)
	}
	if n := w.count(`SELECT max(length(conductor_note)) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2`, task); n == 0 || n > 4000 {
		t.Errorf("the queued review's note: %d characters", n)
	}
	// What is cut is then.note: the conductor's latest words, decide
	// next's note, survive whole, and the round's bar ends it whole.
	bar := strings.TrimSpace(delivery.MustText("contracts/round-bar").Render(strings.Repeat("b", 900)))
	for _, note := range w.ids(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2`, task) {
		if !strings.HasSuffix(note, strings.Repeat("d", 3000)+"\n\n"+bar) {
			t.Errorf("the queued review's note does not end with decide next's note and the round's whole bar: ...%q", note[max(0, len(note)-120):])
		}
	}
}

// A round wake listing a full roundShown of findings with long titles and
// long places fits roundLineChars by listing fewer: its tail — how many
// more, the fix before it, the long loop — is never what is cut.
func TestARoundWakeAtItsBoundKeepsItsTail(t *testing.T) {
	w := conducting(t)
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	dir := strings.Repeat("a-rather-long-directory-name/", 5)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				var b strings.Builder
				for i := range 14 {
					if i > 0 {
						b.WriteString("---\n")
					}
					fmt.Fprintf(&b, "severity: medium\ncategory: correctness\nfile: %sfile%02d.go\nline: %d\ntitle: %s %02d\ndescription: D.\n",
						dir, i, i+1, strings.Repeat("t", 76), i)
				}
				return fakelux.Behaviour{Reply: "```yaml\n" + b.String() + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix",
				Checks: "```yaml\noutcome: partial\noutcome_note: one left\nchecks: []\n```"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.toRoundOne(task)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"round":7}'::jsonb WHERE task_id = $1`, task)
	before := w.str(`SELECT state->'roundHeads'->>'target' FROM workflow_runs WHERE task_id = $1`, task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]},"round":{"scope":"fix"}}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 8", func() bool { return w.reviewedRound(task, 8) || w.escalationReason(task) != "" })
	if r := w.escalationReason(task); r != "" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	line := w.roundWakeLine(task)
	if n := len([]rune(line)); n > 2000 {
		t.Errorf("the wake: %d characters", n)
	}
	// The wake as rendered with the findings it lists, the rest counted
	// (all 14 are at the bar): its tail whole.
	ids := w.ids(`SELECT id FROM review_findings WHERE task_id = $1 ORDER BY line`, task)
	if len(ids) != 14 {
		t.Fatalf("findings %v, want 14", ids)
	}
	var shown []map[string]any
	for i, id := range ids {
		if !strings.Contains(line, " "+id+" [") {
			break
		}
		shown = append(shown, map[string]any{"ID": id, "Severity": "medium", "Title": fmt.Sprintf("%s %02d", strings.Repeat("t", 76), i),
			"Where": fmt.Sprintf("%sfile%02d.go:%d", dir, i, i+1), "Introduced": "", "Trivial": false, "Unverified": true})
	}
	if len(shown) == 0 || len(shown) >= 12 {
		t.Fatalf("the wake lists %d findings, want fewer than the 12 it lists at no bound:\n%s", len(shown), line)
	}
	fix := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	head := w.str(`SELECT heads->'target'->>'sha' FROM runs WHERE id = $1`, fix)
	fixLine := delivery.MustText("wakes/facts/round-fix").Render(map[string]any{"Phase": "fix", "RunID": fix,
		"Outcome": "partial", "Note": "one left", "Commits": []string{"target@" + head[:7]}})
	narrow := delivery.MustText("wakes/facts/round-range").Render(map[string]any{"Of": delivery.RangeFixRound,
		"Repos": []string{"target@" + before[:7] + ".." + head[:7]}})
	if want := roundWakeMore(8, 14, 0, narrow, []string{"tests"}, shown, 14-len(shown), fixLine, true); line != want {
		t.Errorf("round 8's wake:\n got %q\nwant %q", line, want)
	}
}

// A conducted pipeline's note, then.note and round: the fixer is told the
// note and the bar, the queued reviewers then.note and the bar, and
// round.scope whole reviews the whole branch in a later round.
func TestAPipelinesNotesAndRoundReachItsRuns(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", `{"phase":"fix","note":"N1","then":{"review":["tests"],"note":"N2"},"round":{"scope":"whole","bar":"BAR"}}`)
	w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
	fix := w.str(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	if !strings.Contains(fix, "N1") || !strings.Contains(fix, "BAR") {
		t.Errorf("the fixer's note %q, want N1 and BAR", fix)
	}
	review := w.str(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2 LIMIT 1`, task)
	if !strings.Contains(review, "N2") || !strings.Contains(review, "BAR") {
		t.Errorf("the queued reviewer's note %q, want N2 and BAR", review)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2
		AND COALESCE(review_range->>'of', '') <> ''`, task); n != 0 {
		t.Errorf("%d of round 2's reviews are not of the whole branch\n%s", n, w.describeRuns())
	}
}

// A pipeline's fixer that asks a person is no exception (design §9.1 #5):
// once answered, the fix commits and its queued review starts with no
// conductor decision between.
func TestAPipelinesFixerThatAsksAPersonGoesOnToItsReview(t *testing.T) {
	w := roundsWorld(t)
	var asked atomic.Bool
	w.fixPlays(func(spec map[string]any) *fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] != "fix" || !asked.CompareAndSwap(false, true) {
			return nil
		}
		return &fakelux.Behaviour{Ask: fakeagent.Question, Reply: "Fixed it, as asked.",
			Commit: map[string]string{"FACTORY.md": "fixed as asked\n"}, Message: "fix"}
	})
	task := w.task()
	w.toRoundOne(task)
	from := w.cursor(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
	var question string
	w.until("the fixer's question", func() bool {
		question = w.str(`SELECT COALESCE((SELECT q.id FROM questions q JOIN runs r ON r.id = q.run_id
			WHERE q.task_id = $1 AND r.phase = 'fix' AND q.status = 'open' LIMIT 1), '')`, task)
		return question != ""
	})
	if status, body := w.call("/internal/questions/"+question+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the queued review's round", func() bool {
		return w.decisionAt(task) == delivery.PointReviewed && len(w.awaited(task, from)) > 0 || w.escalationReason(task) != ""
	})
	if got := w.awaited(task, from); fmt.Sprint(got) != "["+delivery.PointReviewed+"]" {
		t.Errorf("decisions after the start: %v, want [%s]\n%s", got, delivery.PointReviewed, w.describeRuns())
	}
	if got := w.reviewersSince(task, from); fmt.Sprint(got) != `[["tests"]]` {
		t.Errorf("review rounds' reviewers after the start: %v, want [[\"tests\"]]", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'`, task); n != 1 {
		t.Errorf("%d fixes completed, want 1", n)
	}
}

// A simplifier with then that commits nothing stops its pipeline: the
// delivery parks after it with that fact and the review offered, rather
// than going on to the test.
func TestAPipelinesSimplifierThatCommitsNothingWakesTheConductor(t *testing.T) {
	w := roundsWorld(t)
	w.fixPlays(func(map[string]any) *fakelux.Behaviour { return &fakelux.Behaviour{Reply: "Simple enough."} })
	task := w.task()
	w.toRoundOne(task)
	tests := w.phaseRuns(task, "test")
	w.must(task, "start_phase", `{"phase":"simplify","then":{"review":["tests"]}}`)
	w.until("after the simplifier", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify' AND status = 'completed'`, task) == 1 &&
			(w.untaken(task) || w.phaseRuns(task, "test") > tests)
	})
	if !w.fixedWith(task, `["tests"]`) {
		t.Errorf("parked at %q, want after the fix with the review offered\n%s", w.decisionAt(task), w.describeRuns())
	}
	if n := w.phaseRuns(task, "test"); n != tests {
		t.Errorf("%d tests started after the simplifier", n-tests)
	}
	fact := stoppedFact("nothing", "done", "", "tests")
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $3
		AND strpos(line, $2) > 0`, task, fact, delivery.WakeRound); n != 1 {
		t.Errorf("wakes saying %q: %d, want 1", fact, n)
	}
}
