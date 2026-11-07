package delivery

import (
	"strings"
	"testing"
)

// An incomplete verdict set is refused with every asked command, in order,
// and the waiver cursor of each that may carry one: what same_as_waived
// cites, even when the wake that asked was clipped.
func TestTheIncompleteRefusalListsCommandsAndWaiverCursors(t *testing.T) {
	asked := &ChecksAsked{Check: []Check{{Command: "make test", Result: CheckFail}, {Command: "make lint", Result: CheckFail}},
		Waivers: map[int]*CarriedWaiver{1: {Cursor: 4343}}}
	err := checkVerdicts(asked, DecideChecks{Checks: []ConductedCheck{{Command: "make test", Verdict: VerdictReject, Reason: "r"}}})
	if !IsRefusal(err) || !strings.Contains(err.Error(), `"make test", "make lint" (waiverCursor 4343)`) {
		t.Fatalf("%v", err)
	}
}
