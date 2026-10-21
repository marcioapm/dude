package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
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
