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

// The conductor's wake for a checks escalation names each check not
// verified and why, so it never has to guess.
func TestTheChecksEscalationsWakeNamesTheUnverifiedChecks(t *testing.T) {
	w, task := checksHeld(t)
	note := w.wokenWith(task, "Escalated to a person: checks unverified")
	if want := "The checks not verified: " + fakeagent.FailingCommand + ": TestClock needs a hardware clock — not passed by the judge: the recorded call says so (judgement: no)."; !strings.Contains(note, want) {
		t.Errorf("the wake lacks %q:\n%s", want, note)
	}
}
