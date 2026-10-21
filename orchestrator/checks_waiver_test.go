package orchestrator_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// decideScoped is decide with a waiver's scope.
func (w *world) decideScoped(as, wi, action, note, scope string) (int, map[string]any) {
	w.t.Helper()
	return w.callAs(as, "/internal/tasks/"+wi+"/decide", map[string]any{"action": action, "note": note, "scope": scope})
}

// checksEscalations counts the checks escalations the task was asked.
func (w *world) checksEscalations(wi string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'kind' = 'escalation' AND payload->>'reason' = $2`, wi, delivery.ReasonChecksUnverified)
}

// failingUntilEscalated runs a delivery whose writers are all on model and
// report a failure, with no send-back allowed, to its first checks
// escalation, judged by j.
func (w *world) failingUntilEscalated(model string, j *scriptedJudge) string {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
	w.onModel("simplifier", model)
	wi := w.checksDelivery(model, j)
	w.until("the checks escalation", func() bool { return w.checksEscalations(wi) == 1 })
	return wi
}

// A scope is a checks waiver's alone, and attempt only for a held report
// with a reported failure to carry: anything else is refused, deciding
// nothing.
func TestAnAttemptWaiverIsRefusedWhereThereIsNothingToCarry(t *testing.T) {
	w := newWorld(t)
	wi := w.unbackedUntilEscalated(0)
	owner, _, _ := w.people(wi)
	for _, tc := range []struct{ action, note, scope string }{
		// The held report's only problem is an unbacked pass.
		{delivery.ActionWaive, "It is fine.", delivery.ScopeAttempt},
		{"retry", "", delivery.ScopeAttempt},
		{"stop", "", delivery.ScopeAttempt},
		{delivery.ActionWaive, "It is fine.", "task"},
	} {
		code, body := w.decideScoped(owner, wi, tc.action, tc.note, tc.scope)
		if e, _ := body["error"].(map[string]any); code != 400 || e["code"] != "bad_request" {
			t.Errorf("%s scope %q: %d %v, want 400 bad_request", tc.action, tc.scope, code, body)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvRunChecksDecided); n != 0 {
		t.Fatalf("%d decisions recorded by refused requests", n)
	}
	// An explicit run scope is the default's waiver.
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, "It is fine.", delivery.ScopeRun); code != 200 {
		t.Fatalf("a run-scoped waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}

// Who may waive for the attempt is who may waive: the owner or an admin,
// never another member.
func TestOnlyTheOwnerOrAnAdminWaivesForTheAttempt(t *testing.T) {
	w := newWorld(t)
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, &scriptedJudge{})
	_, admin, member := w.people(wi)
	if code, body := w.decideScoped(member, wi, delivery.ActionWaive, "Needs a hardware clock.", delivery.ScopeAttempt); code != 403 {
		t.Fatalf("a member's attempt waiver: %d %v", code, body)
	}
	if code, body := w.decideScoped(admin, wi, delivery.ActionWaive, "Needs a hardware clock.", delivery.ScopeAttempt); code != 200 {
		t.Fatalf("an admin's attempt waiver: %d %v", code, body)
	}
	// The decision records who waived what, from which turn, as data.
	got := w.ids(`SELECT concat_ws('|', e.payload->>'scope', e.payload->>'person' = k.person_id, e.payload->>'turn' = j.payload->>'turn',
			e.payload->'covers'->0->>'command', e.payload->'covers'->0->>'result', e.payload->'covers'->0->>'evidence',
			jsonb_array_length(e.payload->'covers'))
		FROM events e JOIN api_keys k ON k.id = $3
		JOIN events j ON j.run_id = e.run_id AND j.event_type = $4 AND j.payload ? 'escalated'
		WHERE e.task_id = $1 AND e.event_type = $2`, wi, delivery.EvRunChecksDecided, admin, delivery.EvRunChecksJudged)
	if want := "attempt|t|t|" + fakeagent.FailingCommand + "|fail|TestClock needs a hardware clock|1"; len(got) != 1 || got[0] != want {
		t.Errorf("the decision recorded %q, want %q", got, want)
	}
}

// gateJudged are the task's checks judgements of every gated Run, oldest
// first, as phase:outcome (waived, carried, skipped, verified, unverified).
func (w *world) gateJudged(wi string) []string {
	w.t.Helper()
	return w.ids(`SELECT r.phase || ':' || CASE WHEN e.payload ? 'waived' THEN 'waived' WHEN e.payload ? 'carried' THEN 'carried'
			WHEN e.payload ? 'skipped' THEN 'skipped' WHEN (e.payload->>'verified')::bool THEN 'verified' ELSE 'unverified' END
		FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND e.event_type = $2 ORDER BY e.cursor`, wi, delivery.EvRunChecksJudged)
}

// waiverQuestions counts the waiver questions j was asked.
func (j *scriptedJudge) waiverQuestions() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, a := range j.asked {
		if strings.Contains(a.Question, "waived a reported failure") {
			n++
		}
	}
	return n
}

// The owner accepts the implementer's failure for the attempt: the fixer
// and simplifier report the same failure, the judge finds it the same, and
// the work reaches the pull request with no further escalation.
func TestAnAttemptWaiverIsCarriedByLaterRunsWithTheSameFailure(t *testing.T) {
	w := newWorld(t)
	j := &scriptedJudge{}
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, j)
	owner, _, _ := w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, "TestClock needs a hardware clock CI lacks.", delivery.ScopeAttempt); code != 200 {
		t.Fatalf("the owner's attempt waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := strings.Join(w.gateJudged(wi), ","); got != "implement:unverified,implement:waived,fix:carried,simplify:carried" {
		t.Errorf("judged %s", got)
	}
	if n := w.checksEscalations(wi); n != 1 {
		t.Errorf("%d checks escalations, want the implementer's alone", n)
	}
	if n := j.waiverQuestions(); n != 2 {
		t.Errorf("%d waiver questions, want one each for the fixer and simplifier", n)
	}
}

// What the owner's waiver does not cover goes back as before: a further
// failure under the same command (the judge says no), another command
// (never asked), a waiver for the Run alone (never asked), or no
// judgement tier, an unsure or failing judge (never a skip).
func TestAFailureTheWaiverDoesNotCoverIsHeld(t *testing.T) {
	for _, tc := range []struct {
		name, fixer, scope string
		judge              *scriptedJudge
		asked              int
	}{
		{"a new failure under the same command", fakeagent.ChecksNewFailureModel, delivery.ScopeAttempt, &scriptedJudge{}, 1},
		{"another command", fakeagent.ChecksOtherFailureModel, delivery.ScopeAttempt, &scriptedJudge{}, 0},
		{"a waiver for the Run alone", fakeagent.ChecksPreexistingModel, "", &scriptedJudge{}, 0},
		{"no judgement tier", fakeagent.ChecksPreexistingModel, delivery.ScopeAttempt, nil, 0},
		{"an unsure judge", fakeagent.ChecksPreexistingModel, delivery.ScopeAttempt, &scriptedJudge{answer: judge.Unsure}, 1},
		{"a failing judge", fakeagent.ChecksPreexistingModel, delivery.ScopeAttempt, &scriptedJudge{err: errors.New("proxy timed out")}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.onModel("fixer", tc.fixer)
			wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, tc.judge)
			owner, _, _ := w.people(wi)
			if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, "TestClock needs a hardware clock CI lacks.", tc.scope); code != 200 {
				t.Fatalf("the owner's waiver: %d %v", code, body)
			}
			w.until("the fixer's checks escalation", func() bool { return w.checksEscalations(wi) == 2 })
			if got := strings.Join(w.gateJudged(wi), ","); got != "implement:unverified,implement:waived,fix:unverified" {
				t.Errorf("judged %s", got)
			}
			if tc.judge != nil {
				if n := tc.judge.waiverQuestions(); n != tc.asked {
					t.Errorf("%d waiver questions, want %d", n, tc.asked)
				}
			}
			if n := len(w.gh.Pulls()); n != 0 {
				t.Errorf("%d pull requests opened past a held fixer", n)
			}
		})
	}
}

// A later checks escalation's retry and stop work as ever, and the earlier
// waiver is not revoked by them; Start over is a new attempt, which
// inherits nothing.
func TestRetryAndStopAfterACarriedWaiverAndStartOverInheritsNothing(t *testing.T) {
	w := newWorld(t)
	w.onModel("fixer", fakeagent.ChecksNewFailureModel)
	j := &scriptedJudge{}
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, j)
	owner, _, _ := w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, "TestClock needs a hardware clock CI lacks.", delivery.ScopeAttempt); code != 200 {
		t.Fatalf("the owner's attempt waiver: %d %v", code, body)
	}
	w.until("the fixer's checks escalation", func() bool { return w.checksEscalations(wi) == 2 })
	if code, body := w.decide(owner, wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	// Sent back, the fixer reports the same new failure, still weighed
	// against the waiver, and is held again.
	w.until("the fixer held again", func() bool { return w.checksEscalations(wi) == 3 })
	if n := j.waiverQuestions(); n != 2 {
		t.Errorf("%d waiver questions, want the fixer's two turns", n)
	}
	if code, body := w.decide(owner, wi, "stop", ""); code != 200 {
		t.Fatalf("stop: %d %v", code, body)
	}
	w.until("stopped", func() bool { return w.taskStatus(wi) == "aborted" })
	if code, body := w.callAs(owner, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"}); code != 200 {
		t.Fatalf("start over: %d %v", code, body)
	}
	w.until("the new attempt's implementer held", func() bool { return w.checksEscalations(wi) == 4 })
	if n := j.waiverQuestions(); n != 2 {
		t.Errorf("%d waiver questions, want none about the new attempt", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND phase = 'implement'`, wi); n != 1 {
		t.Errorf("%d implementers in the new attempt", n)
	}
}
