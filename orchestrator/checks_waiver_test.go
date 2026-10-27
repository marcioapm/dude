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

// clockNote is the owner's note on a waiver of fakeagent's preexisting failure.
const clockNote = "TestClock needs a hardware clock CI lacks."

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
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->'detail' ? 'failures'`, wi); n != 0 {
		t.Errorf("an escalation with no reported failure names failures")
	}
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
	owner, admin, member := w.people(wi)
	// The escalation names the failures an attempt waiver would cover.
	if got := w.ids(`SELECT payload->'detail'->>'failures' FROM events WHERE task_id = $1 AND event_type = 'question.asked'`, wi); len(got) != 1 ||
		got[0] != `[{"result": "fail", "command": "make test", "evidence": "TestClock needs a hardware clock"}]` {
		t.Errorf("the escalation's failures are %q", got)
	}
	if code, body := w.decideScoped(member, wi, delivery.ActionWaive, "Needs a hardware clock.", delivery.ScopeAttempt); code != 403 {
		t.Fatalf("a member's attempt waiver: %d %v", code, body)
	}
	// The held report has a failure to carry, yet only a waiver takes the
	// attempt's scope.
	for _, action := range []string{"retry", "stop"} {
		code, body := w.decideScoped(owner, wi, action, "", delivery.ScopeAttempt)
		if e, _ := body["error"].(map[string]any); code != 400 || e["code"] != "bad_request" {
			t.Errorf("%s scope attempt: %d %v, want 400 bad_request", action, code, body)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvRunChecksDecided); n != 0 {
		t.Fatalf("%d decisions recorded by refused requests", n)
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
			WHEN e.payload ? 'unchanged' THEN 'unchanged' WHEN e.payload ? 'skipped' THEN 'skipped' WHEN (e.payload->>'verified')::bool THEN 'verified' ELSE 'unverified' END
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

// A writer that shows its failure the same at the target branch's commit
// the task builds on passes without a person: every writer's report is judged pre-existing,
// nothing is sent back or escalated, and the work reaches the pull request.
func TestAFailureShownAtTheTaskBasePassesWithoutAPerson(t *testing.T) {
	w := newWorld(t)
	mustExec(w.t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
	w.onModel("simplifier", fakeagent.ChecksBaseShownModel)
	j := &scriptedJudge{}
	wi := w.checksDelivery(fakeagent.ChecksBaseShownModel, j)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := strings.Join(w.gateJudged(wi), ","); got != "implement:verified,fix:verified,simplify:verified" {
		t.Errorf("judged %s", got)
	}
	if n := w.checksEscalations(wi); n != 0 {
		t.Errorf("%d checks escalations", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->'checks'->0->>'result' = $3
		AND payload->'checks'->0->>'callId' = 'mcall_test' AND (payload->>'judgePassed')::bool`, wi, delivery.EvRunChecksJudged, delivery.CheckPreexisting); n != 3 {
		t.Errorf("%d judgements recording the failure pre-existing, want each writer's", n)
	}
	// The pull request names the judge's pass as its own result, never a
	// plain pass or a person's waiver.
	account := checksSection(w.gh.Pull(1).Body)
	for _, want := range []string{"the judge passed failures that fail the same at the commit of the target branch the task builds on (pre-existing)",
		"- **Implementer** — backed; the rest passed by the judge\n  - `" + fakeagent.FailingCommand + "` — **pre-existing** — the recorded call says so (`mcall_test`)"} {
		if !strings.Contains(account, want) {
			t.Errorf("PR account lacks %q:\n%s", want, account)
		}
	}
	if strings.Contains(account, "waived") || strings.Contains(account, "`"+fakeagent.FailingCommand+"` — **pass**") {
		t.Errorf("PR account reads as a waiver or a pass:\n%s", account)
	}
}

// The same writer without a run at the task base is held as before.
func TestAFailureNotShownAtTheTaskBaseIsHeld(t *testing.T) {
	w := newWorld(t)
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, &scriptedJudge{})
	if n := len(w.gh.Pulls()); n != 0 {
		t.Errorf("%d pull requests", n)
	}
	if got := w.ids(`SELECT payload->>'message' FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvRunChecksJudged); len(got) != 1 ||
		!strings.Contains(got[0], "not passed by the judge: the recorded call says so") || !strings.Contains(got[0], "this task builds on (target: main at ") {
		t.Errorf("held %q", got)
	}
}

// An unchanged simplifier after an attempt waiver asks the judge nothing.
func TestAnUnchangedSimplifierAfterAnAttemptWaiverNeedsNoJudge(t *testing.T) {
	w := newWorld(t)
	w.scriptedSimplifier(nil)
	w.checksTested = true
	w.onModel("qa_browser", "claude-test")
	j := &scriptedJudge{}
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, j)
	owner, _, _ := w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, delivery.ScopeAttempt); code != 200 {
		t.Fatalf("the owner's attempt waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := strings.Join(w.gateJudged(wi), ","); got != "implement:unverified,implement:waived,fix:carried,simplify:unchanged" {
		t.Errorf("judged %s", got)
	}
	if n := w.simplifierAsked(wi, j); n != 0 {
		t.Errorf("%d judge calls for the unchanged simplifier", n)
	}
	if n := j.waiverQuestions(); n != 1 {
		t.Errorf("%d waiver questions, want the changed fixer's alone", n)
	}
	account := checksSection(w.gh.Pull(1).Body)
	for _, want := range []string{"Checks were waived by a person:", "- Implementer: " + clockNote,
		"- **Fixer** — waiver carried", "carried from Ana's waiver of the Implementer's report",
		"- **Simplifier** — no changes; checks not needed"} {
		if !strings.Contains(account, want) {
			t.Errorf("PR account lacks %q:\n%s", want, account)
		}
	}
	implementer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi)
	prompt := ""
	for _, r := range w.luxRuns() {
		if spec := submitted(w.t, r); spec.Labels["dude.task"] == wi && spec.Labels["dude.phase"] == "test" {
			prompt = spec.Workload.Prompt
		}
	}
	for _, want := range []string{"waived: " + clockNote, "): waiver carried", "carried from Ana's waiver of the Implementer's report (" + implementer + ", turn ", clockNote,
		"- Simplifier: no changes; checks not needed"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("tester's prompt lacks %q:\n%s", want, prompt)
		}
	}
}

// The owner accepts the implementer's failure for the attempt: the fixer
// and simplifier report the same failure, the judge finds it the same, and
// the work reaches the pull request with no further escalation.
func TestAnAttemptWaiverIsCarriedByLaterRunsWithTheSameFailure(t *testing.T) {
	w := newWorld(t)
	j := &scriptedJudge{}
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, j)
	owner, _, _ := w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, delivery.ScopeAttempt); code != 200 {
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
	// The pull request says the later Runs carried the person's waiver, of
	// whose report, who waived it and why; never that they were backed.
	implementer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi)
	carried := "  - `" + fakeagent.FailingCommand + "` — **waived** — the recorded call says so — carried from Ana's waiver of the Implementer's report (`" +
		implementer + "`) (`mcall_test`)\n" +
		"  - `missing checks` — **pass** — the recorded call says so (`mcall_test`)"
	if got, want := checksSection(w.gh.Pull(1).Body), "Checks were waived by a person:\n"+
		"- Implementer: "+clockNote+"\n"+
		"- Fixer: carried the Implementer's waiver of the same failure: "+clockNote+"\n"+
		"- Simplifier: carried the Implementer's waiver of the same failure: "+clockNote+"\n\n"+
		"- **Implementer** — waived\n"+
		"- **Fixer** — waiver carried\n"+carried+"\n"+
		"- **Simplifier** — waiver carried\n"+carried; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
	}
}

// A fixer that goes on from the implementer's Run (resumed rounds) is a
// later Run of the attempt too: it carries the waiver.
func TestAResumedFixerCarriesTheImplementersWaiver(t *testing.T) {
	w := resuming(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
	j := &scriptedJudge{}
	w.syncer.Judge = j
	w.onModel("implementer", fakeagent.ChecksPreexistingModel)
	w.onModel("simplifier", fakeagent.ChecksPreexistingModel)
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("the checks escalation", func() bool { return w.checksEscalations(wi) == 1 })
	owner, _, _ := w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, delivery.ScopeAttempt); code != 200 {
		t.Fatalf("the owner's attempt waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if writers := w.writerRows(wi); len(writers) != 2 || writers[1].How != delivery.ContinuesResume || writers[1].Continues != writers[0].ID {
		t.Fatalf("writers %+v, want the fixer resuming the implementer", writers)
	}
	if got := strings.Join(w.gateJudged(wi), ","); got != "implement:unverified,implement:waived,fix:carried,simplify:carried" {
		t.Errorf("judged %s", got)
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
			if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, tc.scope); code != 200 {
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

// A failure the waiver does not cover is still the failure the agent
// reported: the fixer's escalation offers it, and a person may waive the
// fixer's own report for the attempt.
func TestAFailureTheWaiverDoesNotCoverMayBeWaivedForTheAttempt(t *testing.T) {
	w := newWorld(t)
	w.onModel("fixer", fakeagent.ChecksNewFailureModel)
	wi := w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, &scriptedJudge{})
	owner, _, _ := w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, delivery.ScopeAttempt); code != 200 {
		t.Fatalf("the owner's attempt waiver: %d %v", code, body)
	}
	w.until("the fixer's checks escalation", func() bool { return w.checksEscalations(wi) == 2 })
	failures := `[{"result": "fail", "command": "make test", "evidence": "TestClock needs a hardware clock"}]`
	if got := w.ids(`SELECT payload->'detail'->>'failures' FROM events WHERE task_id = $1 AND event_type = 'question.asked' ORDER BY cursor`, wi); len(got) != 2 ||
		got[1] != failures {
		t.Errorf("the escalations' failures are %q", got)
	}
	// The held message keeps the agent's evidence and says why the waiver
	// does not cover it.
	want := "Your checks report was not verified. Nothing from this Run has been pushed.\n\n" +
		"- make test: TestClock needs a hardware clock — the Implementer's waiver does not cover this failure: the recorded call says so (judgement: no)\n\n" +
		"A reported failure that predates this task passes once this Run's own tool calls show the same command failing the same way at the commit of the target branch this task builds on (target: main at "
	if got := w.ids(`SELECT payload->'detail'->>'message' FROM events WHERE task_id = $1 AND event_type = 'question.asked' ORDER BY cursor`, wi); len(got) != 2 || !strings.HasPrefix(got[1], want) {
		t.Errorf("the fixer's held message is %q, want %q", got, want)
	}
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, "TestGreeting is flaky too.", delivery.ScopeAttempt); code != 200 {
		t.Fatalf("the attempt waiver of the fixer's report: %d %v", code, body)
	}
	got := w.ids(`SELECT e.payload->>'covers' FROM events e JOIN runs r ON r.id = e.run_id
		WHERE e.task_id = $1 AND e.event_type = $2 AND r.phase = 'fix'`, wi, delivery.EvRunChecksDecided)
	if len(got) != 1 || got[0] != failures {
		t.Errorf("the fixer's waiver covers %q", got)
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
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, delivery.ScopeAttempt); code != 200 {
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

// stoppedAtTheFixersEscalation is a delivery whose implementer's failure
// the owner waived for the attempt, stopped by the owner's Stop while the
// fixer's further failure waits on a person: j was asked once, about it.
func (w *world) stoppedAtTheFixersEscalation(j *scriptedJudge) (wi, owner string) {
	w.t.Helper()
	w.onModel("fixer", fakeagent.ChecksNewFailureModel)
	wi = w.failingUntilEscalated(fakeagent.ChecksPreexistingModel, j)
	owner, _, _ = w.people(wi)
	if code, body := w.decideScoped(owner, wi, delivery.ActionWaive, clockNote, delivery.ScopeAttempt); code != 200 {
		w.t.Fatalf("the owner's attempt waiver: %d %v", code, body)
	}
	w.until("the fixer's checks escalation", func() bool { return w.checksEscalations(wi) == 2 })
	w.stop(owner, wi, map[string]any{})
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'escalation' IS NULL
		AND state->'stopped'->>'reason' = $2`, wi, delivery.ReasonChecksUnverified); n != 1 {
		w.t.Fatalf("the delivery after the stop: %s", w.str(`SELECT state::text FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, wi))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvRunChecksDecided); n != 1 {
		w.t.Fatalf("%d checks decisions, want the waiver alone: the Stop decides none", n)
	}
	if n := j.waiverQuestions(); n != 1 {
		w.t.Fatalf("%d waiver questions before the pick-up, want the fixer's", n)
	}
	return wi, owner
}

// Stopped at a later checks escalation, Start over is a new attempt: its
// implementer's same failure is held, the earlier attempt's waiver never
// weighed against it.
func TestStartingOverAfterAStopAtTheChecksGateCarriesNoWaiver(t *testing.T) {
	w := newWorld(t)
	j := &scriptedJudge{}
	wi, owner := w.stoppedAtTheFixersEscalation(j)
	w.pickUp(owner, wi, map[string]any{"action": "restart"})
	w.until("the new attempt's implementer held", func() bool { return w.checksEscalations(wi) == 3 })
	if n := j.waiverQuestions(); n != 1 {
		t.Errorf("%d waiver questions, want none about the new attempt", n)
	}
	if got := w.ids(`SELECT r.attempt::text FROM events e JOIN runs r ON r.id = e.run_id WHERE e.task_id = $1
		AND e.event_type = $2 AND r.phase = 'implement' AND NOT e.payload ? 'waived' ORDER BY e.cursor`, wi, delivery.EvRunChecksJudged); strings.Join(got, ",") != "1,2" {
		t.Errorf("the implementers' judged attempts are %v, want each held", got)
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Errorf("%d pull requests opened past the new attempt's held implementer", n)
	}
}

// Resume after the same Stop stays in the attempt: the fixer taken back up
// reports its failure again, and it is weighed against the attempt's
// waiver once more before it is held.
func TestResumingAfterAStopAtTheChecksGateKeepsTheAttemptsWaiver(t *testing.T) {
	w := newWorld(t)
	j := &scriptedJudge{}
	wi, owner := w.stoppedAtTheFixersEscalation(j)
	fixer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi)
	w.until("the fixer kept", func() bool { return w.keptRun(fixer) })
	w.pickUp(owner, wi, map[string]any{"action": "resume"})
	w.until("the fixer held again", func() bool { return w.checksEscalations(wi) == 3 })
	if n := j.waiverQuestions(); n != 2 {
		t.Errorf("%d waiver questions, want the resumed fixer's too", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt <> 1`, wi); n != 0 {
		t.Errorf("%d Runs outside the first attempt after Resume", n)
	}
}
