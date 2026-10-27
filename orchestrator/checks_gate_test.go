package orchestrator_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
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
	case strings.Contains(a.Question, "waived a reported failure"):
		// The same failure: both Runs' recorded output names the same
		// failing tests.
		this, waived, _ := strings.Cut(a.Context, "### The waived failure")
		d = judge.No
		if failing(this) != "" && failing(this) == failing(waived) {
			d = judge.Yes
		}
	case strings.HasPrefix(a.Question, "This Run reported a check that did not pass"):
		// Pre-existing: a recorded run at the starting commit fails with
		// the same tests as the Run's own.
		d = judge.No
		if base, head := baseAndHeadFailures(a.Context); base != "" && base == head {
			d = judge.Yes
		}
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

// failing are the distinct "FAIL: <test>" lines of a context, sorted.
func failing(context string) string {
	var out []string
	for _, line := range strings.Split(context, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "FAIL: ") && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// baseAndHeadFailures are failing's of the context's recorded call of
// fakeagent.BaseCommand and of its last other call.
func baseAndHeadFailures(context string) (base, head string) {
	_, calls, _ := strings.Cut(context, "### The tool calls this Run recorded, in order")
	for _, call := range strings.Split(calls, "\n- ")[1:] {
		if strings.Contains(call, fakeagent.BaseCommand) {
			base = failing(call)
		} else {
			head = failing(call)
		}
	}
	return base, head
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
	if !w.checksTested {
		w.deliver(wi)
		return wi
	}
	// With the scripted tester, on a task of one criterion.
	mustExec(w.t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets"]' WHERE id = $1`, wi)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, wi)
	}); err != nil {
		w.t.Fatal(err)
	}
	p := scriptedPolicy()
	p.Test = true
	w.start(wi, p)
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
	backed := "- `missing checks` — **pass** — the recorded call says so (`mcall_test`)"
	if got, want := checksSection(w.gh.Pull(1).Body), "Checks were verified against the Run's recorded commands.\n\n"+
		"- **Implementer** — backed\n"+
		"  - `"+fakeagent.CheckCommand+"` — **pass** — the recorded call says so (`mcall_test`)\n"+
		"  "+backed+"\n"+
		"- **Fixer** — backed\n  "+backed+"\n"+
		"- **Simplifier** — backed\n  "+backed; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
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
	return strings.TrimRight(checks, "\n")
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
	// The judgement names the directive that carried its words, so a reader
	// can tell when the agent received them.
	if got := w.ids(`SELECT d.text FROM events e JOIN directives d ON d.id = e.payload->>'directiveId'
		WHERE e.task_id = $1 AND e.event_type = $2 AND d.delivered_at IS NOT NULL ORDER BY e.cursor`, wi, delivery.EvRunChecksJudged); !slices.Equal(got, back) {
		t.Errorf("the judgements name delivered directives %q, want the send-back %q", got, back)
	}
}

// A person's retry sends the held report back as a directive its decision
// names, with the held words.
func TestRetryingAnUnverifiedReportNamesTheDirectiveItSends(t *testing.T) {
	w := newWorld(t)
	wi := w.unbackedUntilEscalated(0)
	owner, _, _ := w.people(wi)
	held := w.ids(`SELECT payload->>'message' FROM events WHERE task_id = $1 AND event_type = $2 AND payload ? 'escalated'`,
		wi, delivery.EvRunChecksJudged)
	if code, body := w.decide(owner, wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	got := w.ids(`SELECT d.text FROM events e JOIN directives d ON d.id = e.payload->>'directiveId'
		WHERE e.task_id = $1 AND e.event_type = $2 AND e.payload->>'action' = 'retry'`, wi, delivery.EvRunChecksDecided)
	if len(held) != 1 || !slices.Equal(got, held) {
		t.Errorf("the retry names a directive saying %q, want the held %q", got, held)
	}
}

// unbackedUntilEscalated runs a delivery whose implementer never backs its
// report, with the organisation's rounds set (-1: left at the default),
// to the escalation, asserting no push was asked for.
func (w *world) unbackedUntilEscalated(rounds int) string {
	w.t.Helper()
	return w.unbackedUntilEscalatedWith(rounds, &scriptedJudge{})
}

// unbackedUntilEscalatedWith is unbackedUntilEscalated judged by j.
func (w *world) unbackedUntilEscalatedWith(rounds int, j *scriptedJudge) string {
	w.t.Helper()
	if rounds >= 0 {
		mustExec(w.t, w.owner, `UPDATE forge_credentials SET settings = jsonb_build_object('checksFixRounds', $2::int)
			WHERE organization_id = $1`, w.org, rounds)
	}
	wi := w.checksDelivery(fakeagent.ChecksUnbackedModel, j)
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
	backed := "  - `missing checks` — **pass** — the recorded call says so (`mcall_test`)"
	if got, want := checksSection(w.gh.Pull(1).Body), "Checks were waived by a person:\n"+
		"- Implementer: The check needs hardware CI has; it passed there.\n\n"+
		"- **Implementer** — waived\n"+
		"- **Fixer** — backed\n"+backed+"\n"+
		"- **Simplifier** — backed\n"+backed; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
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
	if got, want := checksSection(w.gh.Pull(1).Body), "Checks not model-checked, set a judgement tier in the organisation's settings.\n\n"+
		"- **Implementer** — not model-checked\n"+
		"- **Fixer** — not model-checked\n"+
		"- **Simplifier** — not model-checked"; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
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

// scriptedSimplifier makes the scripted simplifier commit nothing and end
// its turn without a checks report, after writing edits (none for nil)
// into its checkout and leaving them there.
func (w *world) scriptedSimplifier(edits map[string]string) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			b.Commit, b.Checks, b.NoChecksReport, b.Reply, b.Edits = nil, "", true, "Nothing to simplify.", edits
		}
		return b
	}
}

// simplifierAsked is how many judge questions were about the task's
// simplifier Runs.
func (w *world) simplifierAsked(wi string, j *scriptedJudge) int {
	w.t.Helper()
	ids := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi)
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, a := range j.asked {
		if slices.Contains(ids, a.About) {
			n++
		}
	}
	return n
}

// simplifierJudged is judged's for the simplifier, "unchanged" for a turn
// recorded as changing nothing.
func (w *world) simplifierJudged(wi string) []string {
	w.t.Helper()
	return w.ids(`SELECT CASE WHEN payload ? 'unchanged' THEN 'unchanged' WHEN payload ? 'waived' THEN 'waived'
			WHEN (payload->>'verified')::bool THEN 'verified' ELSE 'unverified' END
		FROM events e JOIN runs r ON r.id = e.run_id AND r.phase = 'simplify'
		WHERE r.task_id = $1 AND e.event_type = $2 ORDER BY e.cursor`, wi, delivery.EvRunChecksJudged)
}

// A simplifier that changes nothing and reports no checks goes on with no
// judge call and nothing sent back, recorded as needing no checks; the pull
// request still says the implementer's checks were waived, and why, and
// names the simplifier's outcome as its own, not as backed.
func TestAnUnchangedSimplifierNeedsNoChecksAndKeepsTheImplementersWaiver(t *testing.T) {
	w := newWorld(t)
	w.scriptedSimplifier(nil)
	// A real model's tester, so its spec carries the prompt a real one gets;
	// the fake lux still plays it as the scripted tester.
	w.checksTested = true
	w.onModel("qa_browser", "claude-test")
	j := &scriptedJudge{}
	wi := w.unbackedUntilEscalatedWith(0, j)
	owner, _, _ := w.people(wi)
	if code, body := w.decide(owner, wi, delivery.ActionWaive, "Pre-existing failures on main."); code != 200 {
		t.Fatalf("the owner's waiver: %d %v", code, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.phaseRuns(wi, "test"); n != 1 {
		t.Fatalf("%d tester Runs, want 1", n)
	}
	if got := w.simplifierJudged(wi); strings.Join(got, ",") != "unchanged" {
		t.Errorf("the simplifier was judged %v, want one unchanged turn", got)
	}
	if n := w.simplifierAsked(wi, j); n != 0 {
		t.Errorf("%d judge questions about the unchanged simplifier", n)
	}
	if n := w.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE r.task_id = $1 AND r.phase = 'simplify'`, wi); n != 0 {
		t.Errorf("%d directives sent to the unchanged simplifier", n)
	}
	backed := "  - `missing checks` — **pass** — the recorded call says so (`mcall_test`)"
	if got, want := checksSection(w.gh.Pull(1).Body), "Checks were waived by a person:\n"+
		"- Implementer: Pre-existing failures on main.\n\n"+
		"- **Implementer** — waived\n"+
		"- **Fixer** — backed\n"+backed+"\n"+
		"- **Simplifier** — no changes; checks not needed"; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
	}
	// The tester is given the writers' outcomes: the implementer's waiver
	// and its reason, with the commit it published, so it does not rerun
	// the failing checks to find them again.
	var implementerHead string
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->'target'->>'sha' FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).
		Scan(&implementerHead)
	prompt := ""
	for _, r := range w.luxRuns() {
		if spec := submitted(w.t, r); spec.Labels["dude.task"] == wi && spec.Labels["dude.phase"] == "test" {
			prompt = spec.Workload.Prompt
		}
	}
	if line := "- Implementer (target@" + implementerHead[:7] + "): waived: Pre-existing failures on main."; !strings.Contains(prompt, line) {
		t.Errorf("the tester's prompt does not carry %q:\n%s", line, prompt)
	}
}

// A simplifier that leaves an edit in its checkout, uncommitted and with no
// report, is gated as before: sent back, then held for a person, its work
// unpublished.
func TestASimplifierWithAnUncommittedEditIsStillGated(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"checksFixRounds":0}' WHERE organization_id = $1`, w.org)
	w.scriptedSimplifier(map[string]string{"SCRATCH.md": "notes\n"})
	j := &scriptedJudge{}
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, j)
	w.until("the checks escalation", func() bool { return w.escalationReason(wi) == delivery.ReasonChecksUnverified })
	if got := w.simplifierJudged(wi); strings.Join(got, ",") != "unverified" {
		t.Errorf("the simplifier was judged %v, want its missing report rejected", got)
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Errorf("%d pull requests opened over a held simplifier", n)
	}
}
