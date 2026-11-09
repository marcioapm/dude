package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The reason a recovery whose Run's secret copy is gone ends with reaches
// the conductor whole: the recovered wake clips a reason at
// RecoveredWhyChars, and a clipped one would lose what it says to do. The
// wake then warns the person as recover_run's refusal does.
func TestRecoverySecretsExpiredFitsTheWake(t *testing.T) {
	if n := utf8.RuneCountInString(RecoverySecretsExpired); n > RecoveredWhyChars {
		t.Fatalf("RecoverySecretsExpired is %d characters, the recovered wake shows %d", n, RecoveredWhyChars)
	}
	line := recoveredLine(RecoveryEnd{ID: "rcv_1", RunID: "run_1", Status: RecoveryRefused, Error: RecoverySecretsExpired})
	if !strings.Contains(line, RecoverySecretsExpired) {
		t.Fatalf("the wake %q does not hold the reason whole", line)
	}
	if !strings.Contains(line, recoveryExpiredWarning) {
		t.Fatalf("the wake %q does not warn the person", line)
	}
	// As recorded: with the longest ids dude makes, within its bound.
	long := recoveredLine(RecoveryEnd{ID: "rcv_0mw8odw7j3a5d0aada8d9dc30", RunID: "run_0mw8odw0id2e37811145f1a4e",
		Status: RecoveryRefused, Error: RecoverySecretsExpired})
	if n := utf8.RuneCountInString(long); n > recoveredLineChars {
		t.Fatalf("the wake is %d characters, a recovered wake is bounded at %d", n, recoveredLineChars)
	}
	if !strings.Contains(rRecoverSecretsExpired, recoveryExpiredWarning) {
		t.Fatalf("the refusal %q does not give the wake's warning", rRecoverSecretsExpired)
	}
}

// recoveryExpiredWarning is what the person is told, by the recover_run
// refusal and the recovered wake alike, of a Run whose copy is gone.
const recoveryExpiredWarning = "its commits may hold a secret value dude can no longer check for; " +
	"anything taken from lux's checkpoint must be checked for secrets by hand before it is pushed anywhere"

// Only a recovery refused for its copy gone carries the warning: a
// refusal for a secret found, or a failure, does not.
func TestOnlyAnExpiredRecoveryWarnsThePerson(t *testing.T) {
	for _, end := range []RecoveryEnd{
		{ID: "rcv_1", RunID: "run_1", Status: RecoveryRefused, Error: "its work contains the project secret T in a.txt"},
		{ID: "rcv_2", RunID: "run_1", Status: RecoveryFailed, Error: RecoverySecretsExpired},
		{ID: "rcv_3", RunID: "run_1", Status: RecoveryDone, Branch: "b"},
	} {
		if line := recoveredLine(end); strings.Contains(line, recoveryExpiredWarning) {
			t.Errorf("%s (%s) warns: %q", end.ID, end.Status, line)
		}
	}
}
