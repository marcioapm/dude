package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The reason a recovery whose Run's secret copy is gone ends with reaches
// the conductor whole: the recovered wake clips a reason at
// RecoveredWhyChars, and a clipped one would lose what it says to do.
func TestRecoverySecretsExpiredFitsTheWake(t *testing.T) {
	if n := utf8.RuneCountInString(RecoverySecretsExpired); n > RecoveredWhyChars {
		t.Fatalf("RecoverySecretsExpired is %d characters, the recovered wake shows %d", n, RecoveredWhyChars)
	}
	line := recoveredLine(RecoveryEnd{ID: "rcv_1", RunID: "run_1", Status: RecoveryRefused, Error: RecoverySecretsExpired})
	if !strings.Contains(line, RecoverySecretsExpired) {
		t.Fatalf("the wake %q does not hold the reason whole", line)
	}
}
