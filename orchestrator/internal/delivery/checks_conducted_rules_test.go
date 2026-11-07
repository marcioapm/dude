package delivery

import (
	"strings"
	"testing"
)

// decide_checks' rules on one verdict set, without a database: a
// same_as_waived must cite the waiver's cursor and is allowed only where a
// waiver was a candidate.
func TestCheckVerdictsWaiverCursor(t *testing.T) {
	asked := &ChecksAsked{Check: []Check{{Command: "make test", Result: CheckFail}}, Waivers: map[int]*CarriedWaiver{0: {Cursor: 9}}}
	in := func(cursor int64) DecideChecks {
		return DecideChecks{Checks: []ConductedCheck{{Command: "make test", Verdict: VerdictSameAsWaived, WaiverCursor: cursor, Reason: "same"}},
			Missing: ConductedMissing{Verdict: MissingNone, Reason: "none"}}
	}
	if err := checkVerdicts(asked, in(9)); err != nil {
		t.Fatalf("the cited waiver: %v", err)
	}
	for _, cursor := range []int64{0, 8} {
		if err := checkVerdicts(asked, in(cursor)); err == nil || !strings.Contains(err.Error(), "must cite the waiver") {
			t.Fatalf("cursor %d: %v", cursor, err)
		}
	}
	asked.Waivers = nil
	if err := checkVerdicts(asked, in(9)); err == nil || !strings.Contains(err.Error(), "is not a verdict for") {
		t.Fatalf("no candidate: %v", err)
	}
}
