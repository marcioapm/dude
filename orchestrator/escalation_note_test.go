package orchestrator_test

// A checks escalation under the conductor: its wake names the checks not
// verified, and the owner's pick of a choice carrying the conductor's
// proposed note decides it with that note as theirs.

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// checksHeld is a conducted delivery whose implementer's reported failure
// the judge did not pass and no send-back is allowed for: escalated
// checks_unverified, the conductor told.
func checksHeld(t *testing.T) (*world, string) {
	w := conducting(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
	w.syncer.Judge = &scriptedJudge{}
	w.onModel("implementer", fakeagent.ChecksPreexistingModel)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the checks escalation", func() bool { return w.escalationReason(task) == delivery.ReasonChecksUnverified })
	return w, task
}

// noted is the conductor's question about a checks escalation offering a
// waiver with the note it proposes.
const (
	waiverNote  = "make test's TestClock failure predates this change"
	notedWaiver = `{"questions":[{"question":"The checks report holds a failure I believe predates the change. Waive it, or send it back?",
		"choices":[{"note":"` + waiverNote + `","recommended":true},{"label":"Send it back"}]}],"actions":["waive","retry"]}`
)

// The owner picking the conductor's noted waiver decides the escalation
// with that note as theirs: recorded on the checks decision and the
// task's decision, attributed to the owner, and the work publishes.
func TestTheOwnersPickOfANotedWaiverDecidesItWithTheNote(t *testing.T) {
	w, task := checksHeld(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", notedWaiver)
	q := w.conductorQuestion(task)
	label := "Waive: " + waiverNote
	// The decision keeps what was asked beside the note the owner picked.
	note := "The conductor proposed: The checks report holds a failure I believe predates the change. Waive it, or send it back?\nThe owner answered: " + label
	if got := w.str(`SELECT items->0->'choices'->0->>'label' FROM questions WHERE id = $1`, q); got != label {
		t.Fatalf("the choice reads %q, want %q", got, label)
	}
	if status, out := w.answerAs(ana, task, label); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	d := w.decided(task)
	if d == nil || d["action"] != delivery.ActionWaive || d["note"] != note || d["questionId"] != q {
		t.Fatalf("decided %v", d)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'action' = 'waive'
		AND payload->>'note' = $3`, task, delivery.EvRunChecksDecided, note); n != 1 {
		t.Errorf("%d checks decisions waiving with the note", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided' AND payload->>'action' = 'waive'
		AND payload->>'questionId' = $2 AND payload->>'answeredBy' = (SELECT person_id FROM api_keys WHERE id = $3)
		AND NOT payload ? 'by'`, task, q, ana); n != 1 {
		t.Errorf("%d task.decided events attributed to the owner's pick", n)
	}
	w.until("the implementer's work published", func() bool { return w.implementerPushes(task) == 1 })
}

// The owner's pick of the conductor's noted retry of a stuck review starts
// the next fix with that note as the person's direction, recorded with what
// the conductor asked.
func TestTheOwnersPickOfANotedStuckRetryDirectsTheNextFix(t *testing.T) {
	w, task := stuck(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	const direction = "change only the greeting's wording, not its structure"
	// A model the scripted agent does not play: its prompt is dude's.
	w.onModel("fixer", "llm-fix")
	fixes := w.phaseRuns(task, "fix")
	w.must(task, "ask_person", `{"questions":[{"question":"Stuck on the finding. Retry with a narrower fix?",
		"choices":[{"note":"`+direction+`"},{"label":"Stop"}]}],"actions":["retry","stop"]}`)
	if status, out := w.answerAs(ana, task, "Retry: "+direction); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	d := w.decided(task)
	if note, _ := d["note"].(string); d == nil || d["action"] != "retry" ||
		!strings.Contains(note, "The conductor proposed: Stuck on the finding. Retry with a narrower fix?") || !strings.Contains(note, direction) {
		t.Fatalf("decided %v", d)
	}
	w.until("the next fix round", func() bool { return w.phaseRuns(task, "fix") == fixes+1 })
	// The newest fixer's prompt, or the round's message when it goes on
	// from the last fixer.
	fixer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at DESC, id DESC LIMIT 1`, task)
	w.until("the fixer submitted", func() bool { return w.specOfRun(fixer).Workload.Prompt != "" })
	told := w.specOfRun(fixer).Workload.Prompt + strings.Join(w.lux.Inputs(w.str(`SELECT COALESCE(lux_run_id, '') FROM runs WHERE id = $1`, fixer)), "\n")
	if !strings.Contains(told, direction) {
		t.Fatalf("the next fixer %s is not told the direction:\n%s", fixer, told)
	}
}

// The owner's pick of the conductor's noted amend on a not-converging
// escalation replaces the named finding's criterion with exactly the note:
// not the question, not the label.
func TestTheOwnersPickOfANotedAmendMakesTheNoteTheCriterion(t *testing.T) {
	w, task := notConvergingConducted(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	_, e := w.escalationOf(task)
	named := e.Detail.(map[string]any)["findingIds"].([]any)[0].(string)
	const criterion = "FACTORY.md names the finding it fixes on its first line."
	w.must(task, "ask_person", `{"questions":[{"question":"The criterion stays unmet after two fixes. Amend it?",
		"choices":[{"note":"`+criterion+`"},{"label":"Stop"}]}],"actions":["amend","stop"]}`)
	if status, out := w.answerAs(ana, task, "Amend: "+criterion); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if d := w.decided(task); d == nil || d["action"] != "amend" || d["note"] != criterion {
		t.Fatalf("decided %v", d)
	}
	w.until("the amend carried out", func() bool {
		return w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND id = $2 AND acceptance <> $3`,
			task, named, fakeagent.FindingAcceptance) == 1
	})
	if got := w.str(`SELECT COALESCE(acceptance, '') FROM review_findings WHERE id = $1`, named); got != criterion {
		t.Fatalf("the amended criterion reads %q, want %q", got, criterion)
	}
}

// A waiver offered with no note, or a note on an action that needs none,
// is refused, recording no question.
func TestAChoicesNoteIsRequiredWhereNeededAndRefusedElsewhere(t *testing.T) {
	w, task := checksHeld(t)
	w.refused(task, "ask_person", `{"question":"Waive?","choices":["Waive","Send it back"],"actions":["waive","retry"]}`,
		`"waive" is not a choice without a note`)
	w.refused(task, "ask_person", `{"questions":[{"question":"Retry?","choices":[{"note":"one more round"},{"label":"Stop"}]}],"actions":["retry","stop"]}`,
		`"retry" takes no note on checks unverified`)
	w.refused(task, "ask_person", `{"questions":[{"question":"Waive?","choices":[{"note":"`+strings.Repeat("x", 120)+`"},{"label":"Stop"}]}],"actions":["waive","stop"]}`,
		"too long")
	w.refused(task, "ask_person", `{"questions":[{"question":"Waive?","choices":[{"label":"Waive it","note":"it predates the task"},{"label":"Stop"}]}],"actions":["waive","stop"]}`,
		`the "waive" choice has a note and a label`)
	if q := w.conductorQuestion(task); q != "" {
		t.Errorf("question %s recorded with a refused choice", q)
	}
}

// Someone other than the owner picking the noted waiver decides nothing,
// as any of their answers does.
func TestANonOwnersPickOfANotedWaiverDecidesNothing(t *testing.T) {
	w, task := checksHeld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", notedWaiver)
	if status, _ := w.answerAs(bo, task, "Waive: "+waiverNote); status != 403 {
		t.Fatalf("a non-owner's answer: %d", status)
	}
	if d := w.decided(task); d != nil {
		t.Fatalf("decided by a non-owner: %v", d)
	}
}

// On a task nobody owns, a member's pick of the noted waiver decides
// nothing; an admin's does, as on the banner.
func TestOnAnOwnerlessTaskOnlyAnAdminsPickOfANotedWaiverDecides(t *testing.T) {
	w, task := checksHeld(t)
	bo, cy := w.person("Bo"), w.person("Cy")
	mustExec(t, w.owner, `UPDATE people SET role = CASE WHEN name = 'Cy' THEN 'admin' ELSE 'member' END WHERE organization_id = $1`, w.org)
	w.must(task, "ask_person", notedWaiver)
	if status, out := w.answerAs(bo, task, "Waive: "+waiverNote); status != 200 {
		t.Fatalf("the member's answer: %d %v", status, out)
	}
	if d := w.decided(task); d != nil {
		t.Fatalf("a member's pick waived an ownerless task: %v", d)
	}
	w.must(task, "ask_person", notedWaiver)
	if status, out := w.answerAs(cy, task, "Waive: "+waiverNote); status != 200 {
		t.Fatalf("the admin's answer: %d %v", status, out)
	}
	if d := w.decided(task); d == nil || d["action"] != delivery.ActionWaive || !strings.HasSuffix(d["note"].(string), "The owner answered: Waive: "+waiverNote) {
		t.Fatalf("the admin's pick decided %v", d)
	}
}

// The conductor's wake for a checks escalation names each check not
// verified and why, so it never has to guess.
func TestTheChecksEscalationsWakeNamesTheUnverifiedChecks(t *testing.T) {
	w, task := checksHeld(t)
	note := w.wokenWith(task, "Escalated to a person: checks unverified")
	if want := "The checks not verified: " + fakeagent.FailingCommand + ": TestClock needs a hardware clock — not passed by the judge: the recorded call says so."; !strings.Contains(note, want) {
		t.Errorf("the wake lacks %q:\n%s", want, note)
	}
}
