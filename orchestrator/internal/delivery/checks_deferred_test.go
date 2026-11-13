package delivery

import (
	"strings"
	"testing"
)

// At the second ask about a deferred check, once the pull request is open,
// deferred is no verdict: the later phase has come. Backed or reject is,
// each with a reason; the missing question is not asked again.
func TestCheckVerdictsRefusesDeferredAtTheSecondAsk(t *testing.T) {
	asked := &ChecksAsked{Check: []Check{{Command: "bench", Result: CheckDeferred, Evidence: "the conductor dispatches it"}},
		Discharge: &ChecksDischarge{Attempt: 1, Heads: map[string]string{"target": "abc"}}}
	in := func(verdict string) DecideChecks {
		return DecideChecks{Checks: []ConductedCheck{{Command: "bench", Verdict: verdict, Reason: "dispatch wd_1 completed: success"}}}
	}
	for _, v := range []string{VerdictDeferred, VerdictUnbacked, VerdictPreexisting} {
		if err := checkVerdicts(asked, in(v)); err == nil || !strings.Contains(err.Error(), "is not a verdict for") {
			t.Errorf("%s at the second ask: %v", v, err)
		}
	}
	for _, v := range []string{VerdictBacked, VerdictReject} {
		if err := checkVerdicts(asked, in(v)); err != nil {
			t.Errorf("%s at the second ask: %v", v, err)
		}
	}
	// The first ask about a check not run still allows deferred.
	first := &ChecksAsked{Check: []Check{{Command: "bench", Result: CheckNotRun}}}
	d := in(VerdictDeferred)
	d.Missing = ConductedMissing{Verdict: MissingNone, Reason: "none"}
	if err := checkVerdicts(first, d); err != nil {
		t.Errorf("deferred at the first ask: %v", err)
	}
}

// The second ask's wake and the gate's deferred item, word for word.
func TestGoldenDeferredChecks(t *testing.T) {
	anchor := deferredCheck{RunID: "run_impl", Turn: 42, Command: "gh workflow run bench.yml"}
	asked := ChecksAsked{Turn: 42, Discharge: &ChecksDischarge{Attempt: 1, Heads: map[string]string{"web": "0123456789ab", "api": "fedcba987654"}},
		Check: []Check{{Command: "gh workflow run bench.yml", Result: CheckDeferred,
			Evidence: "Criterion 4 needs the bench workflow run on GitHub on the PR branch; the conductor dispatches it (run_workflow) once the branch is published"},
			{Command: "ci workflow on the pull request", Result: CheckDeferred, Evidence: "the PR's CI runs it"}}}
	s := &deferredState{
		checks: []deferredCheck{
			{Command: "gh workflow run bench.yml", Reason: "the conductor dispatches it (run_workflow) once the pull request exists"},
			{Command: "ci workflow on the pull request", Reason: "the PR's CI runs it"},
			{Command: "open the page in a browser", Reason: "the tester runs it"},
		},
		latest: map[string]dischargeAnswer{"open the page in a browser": {Verdict: VerdictReject, Reason: "no tester ran"}},
		backed: map[string]map[string]bool{}, asked: map[string]map[string]bool{"ci workflow on the pull request": {"h": true}},
		waived: map[string]int64{},
	}
	goldenTexts(t, "deferred-checks", [][2]string{
		{"wake", deferredWakeLine(anchor, asked)},
		{"item-conducted", deferredItem(s, "h", true).Detail},
		{"item-no-conductor", deferredItem(&deferredState{checks: s.checks[:1], latest: map[string]dischargeAnswer{},
			backed: map[string]map[string]bool{}, asked: map[string]map[string]bool{}, waived: map[string]int64{}}, "h", false).Detail},
	})
}
