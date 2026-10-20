package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// scriptedJudge answers the checks gate's questions as a judgement tier
// would: Backed by whether the Run's recorded call of the command it asks
// about failed (the command's own exit, as the context shows it), Missing
// no; or, set, the same answer to every question, or an error.
type scriptedJudge struct {
	mu      sync.Mutex
	asked   []judge.Ask
	answer  string
	missing string
	err     error
}

func (j *scriptedJudge) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.asked = append(j.asked, a)
	if j.err != nil {
		return judge.Judgement{}, j.err
	}
	backedQ := strings.Contains(a.Question, "reported pass")
	d := j.answer
	switch {
	case d != "":
	case backedQ && failed(lastCompletion(a.Context)):
		d = judge.No
	case backedQ:
		d = judge.Yes
	case j.missing != "":
		d = j.missing
	default:
		d = judge.No
	}
	return judge.Judgement{Decision: d, Confidence: 0.9, Reasoning: "the recorded call says so", CallID: "mcall_test"}, nil
}

// lastCompletion is the last recorded completion line of a judge context.
func lastCompletion(context string) string {
	lines := strings.Split(context, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "agent.tool.completed ") || strings.HasPrefix(lines[i], "  status:") {
			return lines[i]
		}
	}
	return ""
}

// failed says a recorded completion is a command that failed: dude records
// a failing call as status error, with its exit code when the harness
// reports one.
func failed(completion string) bool {
	return strings.Contains(completion, `"status": "error"`) || strings.Contains(completion, `"status":"error"`) || strings.Contains(completion, "status: error")
}

func (j *scriptedJudge) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.asked)
}

// checksDelivery starts a delivery whose implementer is model, judged by
// j (nil: the organisation has no judgement tier).
func (w *world) checksDelivery(model string, j *scriptedJudge) string {
	w.t.Helper()
	if j != nil {
		w.syncer.Judge = j
	}
	w.onModel("implementer", model)
	wi := w.task()
	w.deliver(wi)
	return wi
}

// implementerPushes is how many pushes lux was asked for by the task's
// implementer Runs.
func (w *world) implementerPushes(wi string) int {
	n := 0
	for _, r := range w.luxRuns() {
		if l := submitted(w.t, r).Labels; l["dude.task"] == wi && l["dude.phase"] == "implement" {
			n += len(r.PushCredentials)
		}
	}
	return n
}

// judged are the implementer's checks verdicts, oldest first: verified or
// not, with "waived" or "skipped" when it says so.
func (w *world) judged(wi string) []string {
	w.t.Helper()
	return w.ids(`SELECT CASE WHEN payload ? 'waived' THEN 'waived'
			WHEN payload ? 'skipped' THEN 'skipped' WHEN (payload->>'verified')::bool THEN 'verified' ELSE 'unverified' END
		FROM events e JOIN runs r ON r.id = e.run_id AND r.phase = 'implement'
		WHERE r.task_id = $1 AND e.event_type = $2 ORDER BY e.cursor`, wi, delivery.EvRunChecksJudged)
}

// sentBack are the directives the gate queued for the task's implementer.
func (w *world) sentBack(wi string) []string {
	w.t.Helper()
	return w.ids(`SELECT d.text FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE r.task_id = $1 AND r.phase = 'implement' AND d.text LIKE 'Your checks report was not verified%' ORDER BY d.created_at`, wi)
}

// An honest report the judge backs publishes: one judgement, verified, and
// the implementer's work reaches the pull request.
func TestAChecksReportTheJudgeBacksPublishes(t *testing.T) {
	w := newWorld(t)
	j := &scriptedJudge{}
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, j)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.judged(wi); strings.Join(got, ",") != "verified" {
		t.Errorf("judged %v, want one verified", got)
	}
	if n := w.implementerPushes(wi); n != 1 {
		t.Errorf("%d pushes, want 1", n)
	}
	// Backed for its one check and Missing, about the implementer; the
	// fixer and simplifier report none run, and are asked only Missing.
	var implementer string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&implementer)
	asked := 0
	for _, a := range j.asked {
		if a.About == implementer {
			asked++
		}
	}
	if asked != 2 {
		t.Errorf("%d questions about the implementer, want Backed for one check and Missing", asked)
	}
	// The pull request accounts for every gated Run, the implementer's
	// backed command included, not only the last Run judged.
	body := w.gh.Pull(1).Body
	for _, want := range []string{"Checks were verified against the Run's recorded commands.",
		"- **Implementer** — backed\n  - `" + fakeagent.CheckCommand + "` — **pass**",
		"- **Fixer** — backed", "- **Simplifier** — backed"} {
		if !strings.Contains(checksSection(body), want) {
			t.Errorf("the pull request's checks section has no %q:\n%s", want, body)
		}
	}
}

// checksSection is the checks account inside the body's marked
// verification section, "" when there is none.
func checksSection(body string) string {
	_, after, ok := strings.Cut(body, "<!-- dude:verification -->")
	if !ok {
		return ""
	}
	section, _, _ := strings.Cut(after, "<!-- /dude:verification -->")
	_, checks, _ := strings.Cut(section, "### Checks\n")
	return checks
}

// A pass over a failing command is sent back in the same session, naming
// the check, and nothing is pushed until the next turn's report is backed.
func TestAnUnbackedReportIsSentBackAndPublishesOnceFixed(t *testing.T) {
	w := newWorld(t)
	wi := w.checksDelivery(fakeagent.ChecksFixedModel, &scriptedJudge{})
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.judged(wi); strings.Join(got, ",") != "unverified,verified" {
		t.Errorf("judged %v, want sent back once then verified", got)
	}
	back := w.sentBack(wi)
	if len(back) != 1 || !strings.Contains(back[0], fakeagent.CheckCommand) {
		t.Errorf("sent back %q, want one naming %q", back, fakeagent.CheckCommand)
	}
	if n := w.phaseRuns(wi, "implement"); n != 1 {
		t.Errorf("%d implementer Runs, want the one, in its session", n)
	}
	if n := w.implementerPushes(wi); n != 1 {
		t.Errorf("%d pushes, want 1, after the backed turn", n)
	}
}

// unbackedUntilEscalated runs a delivery whose implementer never backs its
// report, with the organisation's rounds set (-1: left at the default),
// to the escalation, asserting no push was asked for.
func (w *world) unbackedUntilEscalated(rounds int) string {
	w.t.Helper()
	if rounds >= 0 {
		mustExec(w.t, w.owner, `UPDATE forge_credentials SET settings = jsonb_build_object('checksFixRounds', $2::int)
			WHERE organization_id = $1`, w.org, rounds)
	}
	wi := w.checksDelivery(fakeagent.ChecksUnbackedModel, &scriptedJudge{})
	w.until("the checks escalation", func() bool { return w.escalationReason(wi) == delivery.ReasonChecksUnverified })
	if n := w.implementerPushes(wi); n != 0 {
		w.t.Fatalf("%d pushes of an unbacked report", n)
	}
	if s := w.taskStatus(wi); s != "awaiting_input" {
		w.t.Fatalf("task %s, want awaiting_input", s)
	}
	return wi
}

// Sent back as many times as the organisation says, the default 2, then a
// person decides; the Run stays live with its work unpushed.
func TestAnUnbackedReportIsSentBackTwiceByDefaultThenEscalates(t *testing.T) {
	w := newWorld(t)
	wi := w.unbackedUntilEscalated(-1)
	if n := len(w.sentBack(wi)); n != 2 {
		t.Errorf("sent back %d times, want the default 2", n)
	}
	if got := w.judged(wi); strings.Join(got, ",") != "unverified,unverified,unverified" {
		t.Errorf("judged %v", got)
	}
}

func TestTheChecksRoundsAreTheOrganisationsSetting(t *testing.T) {
	w := newWorld(t)
	wi := w.unbackedUntilEscalated(1)
	if n := len(w.sentBack(wi)); n != 1 {
		t.Errorf("sent back %d times, want the organisation's 1", n)
	}
}

// A person may ship the held work past the gate only with a reason; the
// waiver is recorded and the work published without another agent turn.
func TestWaivingAnUnverifiedReportNeedsAReasonAndPublishes(t *testing.T) {
	w := newWorld(t)
	wi := w.unbackedUntilEscalated(0)
	owner, _, _ := w.people(wi)
	if code, body := w.decide(owner, wi, delivery.ActionWaive, " "); code != 400 || !strings.Contains(errMessage(body), "needs a note") {
		t.Fatalf("a waiver without a reason: %d %v", code, body)
	}
	if n := w.implementerPushes(wi); n != 0 {
		t.Fatalf("%d pushes after a refused waiver", n)
	}
	if code, body := w.decide(owner, wi, delivery.ActionWaive, "The check needs hardware CI has; it passed there."); code != 200 {
		t.Fatalf("the owner's waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.judged(wi); strings.Join(got, ",") != "unverified,waived" {
		t.Errorf("judged %v, want the waiver recorded", got)
	}
	if n := w.implementerPushes(wi); n != 1 {
		t.Errorf("%d pushes, want 1", n)
	}
	// The fixer's and simplifier's later reports are backed, yet the pull
	// request says the implementer's checks were waived, and why.
	checks := checksSection(w.gh.Pull(1).Body)
	for _, want := range []string{"Checks were waived by a person:\n- Implementer: The check needs hardware CI has; it passed there.",
		"- **Implementer** — waived", "- **Simplifier** — backed"} {
		if !strings.Contains(checks, want) {
			t.Errorf("the pull request's checks section has no %q:\n%s", want, checks)
		}
	}
	if strings.Contains(checks, "verified") {
		t.Errorf("the pull request calls waived checks verified:\n%s", checks)
	}
}

// With no judgement tier the work publishes, and the skip is recorded.
func TestWithNoJudgementTierTheChecksAreSkippedAndPublished(t *testing.T) {
	w := newWorld(t)
	wi := w.checksDelivery(fakeagent.ChecksUnbackedModel, nil)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.judged(wi); strings.Join(got, ",") != "skipped" {
		t.Errorf("judged %v, want the skip recorded", got)
	}
	checks := checksSection(w.gh.Pull(1).Body)
	for _, want := range []string{"Checks not model-checked, set a judgement tier in the organisation's settings.",
		"- **Implementer** — not model-checked"} {
		if !strings.Contains(checks, want) {
			t.Errorf("the pull request's checks section has no %q:\n%s", want, checks)
		}
	}
	if strings.Contains(checks, "verified") {
		t.Errorf("the pull request calls unjudged checks verified:\n%s", checks)
	}
}

// A judge that fails, with a tier set, is not verified: never a pass. The
// report goes back like an unbacked one and, the rounds spent, to a person.
func TestAJudgeErrorWithATierNeverPublishes(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":1}' WHERE organization_id = $1`, w.org)
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, &scriptedJudge{err: errors.New("proxy timed out")})
	w.until("the checks escalation", func() bool { return w.escalationReason(wi) == delivery.ReasonChecksUnverified })
	if n := w.implementerPushes(wi); n != 0 {
		t.Fatalf("%d pushes after a judge error", n)
	}
	if got := w.judged(wi); strings.Join(got, ",") != "unverified,unverified" {
		t.Errorf("judged %v, want each turn unverified", got)
	}
	if back := w.sentBack(wi); len(back) != 1 || !strings.Contains(back[0], "proxy timed out") {
		t.Errorf("sent back %q, want the judgement's failure named", back)
	}
}

// Only the declared phases are judged: the reviewer, which publishes
// nothing, is never asked about.
func TestAReviewerIsNeverJudged(t *testing.T) {
	w := newWorld(t)
	j := &scriptedJudge{}
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, j)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND r.phase NOT IN ('implement', 'fix', 'simplify') AND e.event_type = $2`, wi, delivery.EvRunChecksJudged); n != 0 {
		t.Errorf("%d judgements of a phase that is not gated", n)
	}
	for _, a := range j.asked {
		if !strings.Contains(a.About, "run_") {
			t.Errorf("asked about %q", a.About)
		}
	}
}
