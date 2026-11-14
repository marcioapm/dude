package delivery

import (
	"fmt"
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
	asked := ChecksAsked{Turn: 42, Discharge: &ChecksDischarge{Attempt: 1, Heads: map[string]string{"web": "0123456789ab", "api": "fedcba987654"},
		Key: "checks:run_impl:42:d1:0a1b2c3d4e5f"},
		Check: []Check{{Command: "gh workflow run bench.yml", Result: CheckDeferred,
			Evidence: "Criterion 4 needs the bench workflow run on GitHub on the PR branch; the conductor dispatches it (run_workflow) once the branch is published"},
			{Command: "ci workflow on the pull request", Result: CheckDeferred, Evidence: "the PR's CI runs it"}}}
	s := newDeferredState()
	s.conducted = true
	s.checks = []deferredCheck{
		{Command: "gh workflow run bench.yml", Reason: "the conductor dispatches it (run_workflow) once the pull request exists"},
		{Command: "ci workflow on the pull request", Reason: "the PR's CI runs it"},
		{Command: "open the page in a browser", Reason: "the tester runs it"},
		{Command: "make bench", Reason: "the conductor verifies it at the head"},
	}
	s.latest["open the page in a browser"] = dischargeAnswer{Verdict: VerdictReject, Reason: "no tester ran"}
	s.asked["ci workflow on the pull request"] = map[string]int64{"h": 7}
	s.asked["make bench"] = map[string]int64{"h": 8}
	s.asks[7] = &dischargeAsk{Key: "checks:run_impl:42:d1:0a1b2c3d4e5f"}
	s.asks[8] = &dischargeAsk{Overdue: true}
	policy := newDeferredState()
	policy.checks = s.checks[:1]
	policy.asked["gh workflow run bench.yml"] = map[string]int64{"h": 7}
	policy.asks[7] = &dischargeAsk{}
	goldenTexts(t, "deferred-checks", [][2]string{
		{"wake", deferredWakeLine(anchor, asked)},
		{"item-conducted", deferredItem(s, "h").Detail},
		{"item-no-conductor", deferredItem(policy, "h").Detail},
		{"escalation", deferredEscalation(s, "h", true)["message"].(string)},
		{"escalation-no-conductor", deferredEscalation(policy, "h", true)["message"].(string)},
		{"refusal-ask-missing", fmt.Sprintf(rChecksAskMissing, "checks:run_impl:42:d1:0a1b2c3d4e5f")},
	})
}

// A conductor that lost the second ask's id is refused with the open ask
// named: the one at the pull requests' heads.
func TestTheAskMissingRefusalNamesTheOpenAsk(t *testing.T) {
	key := "checks:run_impl:42:d1:0a1b2c3d4e5f"
	err := refusef(rChecksAskMissing, key)
	if !strings.Contains(err.Error(), fmt.Sprintf("here ask %q,", key)) {
		t.Fatalf("the refusal does not name the ask: %v", err)
	}
}

// A gate stored before the deferred item existed (nine items) is served
// with dude's own items read now and nothing missing: never ready:false
// with every item listed holding.
func TestAStoredGateFromBeforeAnItemIsServedWhole(t *testing.T) {
	var old StoredGate
	for _, k := range ReadyKeys[:len(ReadyKeys)-1] {
		old.Items = append(old.Items, GateItem{Key: k, OK: true, Detail: "held"})
	}
	ok := GateItem{OK: true, Detail: "now"}
	p := GateFacts{Findings: ok, Questions: ok, Deferred: GateItem{Key: ReadyDeferred, Detail: "1 deferred check(s) not kept"}}
	p.Findings.Key, p.Questions.Key = ReadyFindings, ReadyQuestions
	got := old.WithOwnItems(p)
	if len(got.Items) != len(ReadyKeys) || got.Ready || got.Items[len(got.Items)-1] != p.Deferred {
		t.Fatalf("served %+v", got)
	}
	// A stored gate missing an item that is not dude's own serves it failing, named.
	old.Items = old.Items[1:]
	p.Deferred.OK = true
	got = old.WithOwnItems(p)
	if got.Ready || got.Items[0] != (GateItem{Key: ReadyOpen, Detail: "not weighed yet at this head"}) {
		t.Fatalf("served %+v", got)
	}
}

// A discharge escalation whose checks all wait on an open second ask past
// its deadline names that ask, and its wake says a backed answer to it
// settles the escalation; one with a check rejected (or no conductor to
// answer) names none, as only a person settles it. A held report's
// checks_unverified escalation reads no ask, whatever its detail holds.
func TestADischargeEscalationNamesTheOpenAskThatSettlesIt(t *testing.T) {
	const key = "checks:run_impl:42:d1:0a1b2c3d4e5f"
	overdue := func() *deferredState {
		s := newDeferredState()
		s.conducted = true
		s.checks = []deferredCheck{{RunID: "run_impl", Turn: 42, Command: "gh workflow run bench.yml", Reason: "the conductor dispatches it"},
			{RunID: "run_impl", Turn: 42, Command: "make bench", Reason: "the conductor verifies it"}}
		s.asked["gh workflow run bench.yml"] = map[string]int64{"h": 7}
		s.asked["make bench"] = map[string]int64{"h": 7}
		s.asks[7] = &dischargeAsk{Key: key, RunID: "run_impl", Turn: 42, Overdue: true}
		return s
	}
	line := func(detail map[string]any) string {
		return escalatedLine(&Escalation{Reason: ReasonChecksUnverified, Detail: detail})
	}
	const settles = "A backed decide_checks answer to the open second checks ask (runId run_impl, turn 42, ask " + key +
		") at the current heads settles this escalation by itself, with no person's waiver: keep the checks, then answer it. " +
		"Waive is for a check that was not kept; a reject still goes to the person."
	d := deferredEscalation(overdue(), "h", true)
	if got := fmt.Sprint(d["asks"]); got != fmt.Sprint([]DischargeOpenAsk{{"run_impl", 42, key}}) {
		t.Errorf("the open asks: %s", got)
	}
	if l := line(d); !strings.Contains(l, settles) {
		t.Errorf("the discharge wake lacks the settling line:\n%s", l)
	}
	rejected := overdue()
	rejected.latest["make bench"] = dischargeAnswer{Verdict: VerdictReject, Reason: "not run"}
	d = deferredEscalation(rejected, "h", true)
	if _, ok := d["asks"]; ok {
		t.Errorf("an escalation with a check rejected names an ask: %v", d["asks"])
	}
	if l := line(d); strings.Contains(l, "settles this escalation") {
		t.Errorf("a rejected check's wake says an answer settles it:\n%s", l)
	}
	held := map[string]any{"runId": "run_impl", "message": "held", "asks": []DischargeOpenAsk{{"run_impl", 42, key}}}
	without := map[string]any{"runId": "run_impl", "message": "held"}
	if line(held) != line(without) {
		t.Errorf("a held report's wake reads an ask:\n%s", line(held))
	}
}

// Of a check's asks, only the one at the current heads is named: not an
// older or newer one at other heads. And none is named while another
// deferred check at these heads waits on an ask not yet overdue: a backed
// answer to the overdue one would not settle the escalation, as that check
// is outstanding still.
func TestADischargeEscalationNamesOnlyItsOwnCurrentAsk(t *testing.T) {
	s := newDeferredState()
	s.conducted = true
	s.checks = []deferredCheck{{RunID: "run_impl", Turn: 42, Command: "make bench", Reason: "the conductor verifies it"}}
	s.asked["make bench"] = map[string]int64{"old": 3, "h": 7, "new": 9}
	s.asks[3] = &dischargeAsk{Key: "k3", RunID: "run_impl", Turn: 42, Overdue: true}
	s.asks[7] = &dischargeAsk{Key: "k7", RunID: "run_impl", Turn: 42, Overdue: true}
	s.asks[9] = &dischargeAsk{Key: "k9", RunID: "run_impl", Turn: 42, Overdue: true}
	if got := fmt.Sprint(deferredEscalation(s, "h", true)["asks"]); got != fmt.Sprint([]DischargeOpenAsk{{"run_impl", 42, "k7"}}) {
		t.Errorf("the open asks at heads h: %s", got)
	}
	s.checks = append(s.checks, deferredCheck{RunID: "run_impl", Turn: 43, Command: "make e2e", Reason: "the conductor runs it"})
	s.asked["make e2e"] = map[string]int64{"h": 11}
	s.asks[11] = &dischargeAsk{Key: "k11", RunID: "run_impl", Turn: 43}
	d := deferredEscalation(s, "h", true)
	if _, ok := d["asks"]; ok {
		t.Errorf("an ask is named while another check's ask is not yet overdue: %v", d["asks"])
	}
	if l := escalatedLine(&Escalation{Reason: ReasonChecksUnverified, Detail: d}); strings.Contains(l, "settles this escalation") {
		t.Errorf("the wake says an answer settles it while another check is outstanding:\n%s", l)
	}
	// Once that ask is overdue too, both are named, and the wake asks for
	// an answer to each.
	s.asks[11].Overdue = true
	l := escalatedLine(&Escalation{Reason: ReasonChecksUnverified, Detail: deferredEscalation(s, "h", true)})
	if !strings.Contains(l, "A backed decide_checks answer to each open second checks ask (runId run_impl, turn 42, ask k7; runId run_impl, turn 43, ask k11)") ||
		!strings.Contains(l, "keep the checks, then answer each.") {
		t.Errorf("the wake for two asks:\n%s", l)
	}
}
