package orchestrator_test

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// checksConductor is the scripted conductor the checks gate's tests decide
// reports with, as the task's conductor, through the real decide_checks:
// each pass is backed only on dude's recording of its call exiting 0
// (fakeagent.ChecksDefaults reads the wake), a failure is the one a
// person waived when the waiver's Run's recorded output fails the same
// tests (same_as_waived), else pre-existing only on dude's recording of it
// failing at the task's base, a check not run deferred only when a later
// phase runs it, and nothing is missing. Set reject to reject every
// failure, or silent to decide nothing (the gate's deadline settles it).
// A checks escalation it holds goes to a person at once: its tests are of
// the person's waiver, which only a person gives.
type checksConductor struct {
	mu      sync.Mutex
	decided []delivery.DecideChecks
	reject  bool
	silent  bool
	busy    bool
}

// checksReason is what the scripted conductor says of every verdict.
const checksReason = "the recorded call says so"

// decide is the scripted conductor's turn, once per pump: every checks
// wake not yet decided, then any checks escalation it holds.
func (c *checksConductor) decide(w *world) {
	w.t.Helper()
	if c.busy {
		return
	}
	c.busy = true
	defer func() { c.busy = false }()
	for _, row := range w.rows(`SELECT k.task_id, k.line FROM conductor_wakes k WHERE k.organization_id = $1 AND k.kind = $2
		AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = split_part(k.key, ':', 2) AND v.event_type IN ($3, $4)
			AND v.payload->>'turn' = split_part(k.key, ':', 3))
		AND EXISTS (SELECT 1 FROM runs r WHERE r.task_id = k.task_id AND r.role = 'conductor' AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused'))
		ORDER BY k.created_at`, w.org, delivery.WakeChecks, delivery.EvRunChecksConducted, delivery.EvRunChecksJudged) {
		task, line := row[0], row[1]
		if c.silent {
			continue
		}
		for _, call := range fakeagent.ChecksDefaults(line) {
			var in delivery.DecideChecks
			if err := json.Unmarshal([]byte(call[1]), &in); err != nil {
				w.t.Fatal(err)
			}
			asked := w.checksAsked(in.RunID, in.Turn)
			if asked == nil {
				continue
			}
			for i := range in.Checks {
				v := &in.Checks[i]
				v.Reason = checksReason
				if wv := asked.Waivers[i]; wv != nil && !c.reject && w.sameFailure(in.RunID, wv, v.Command) {
					v.Verdict, v.WaiverCursor = delivery.VerdictSameAsWaived, wv.Cursor
				}
				if c.reject && asked.Check[i].Result != delivery.CheckPass {
					v.Verdict = delivery.VerdictReject
				}
			}
			in.Missing.Reason = checksReason
			args, _ := json.Marshal(in)
			spec := w.conductorSpecNow(task)
			if spec == "" {
				continue
			}
			status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "decide_checks", string(args))
			if status != 200 {
				out := body
				// A turn moved on, or settled at its deadline: asked again.
				w.t.Logf("decide_checks %s: %d %v", args, status, out)
				continue
			}
			c.mu.Lock()
			c.decided = append(c.decided, in)
			c.mu.Unlock()
		}
	}
	// The escalation it holds is the person's to waive: handed over now,
	// as its deadline would.
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET wake_at = now(), state = jsonb_set(state, '{escalation,raisedAt}', to_jsonb(now() - interval '1 hour'))
		WHERE organization_id = $1 AND workflow_type = 'task.delivery' AND status = 'waiting' AND state->'escalation'->>'holder' = 'conductor'
		AND state->'escalation'->>'reason' = $2 AND NOT (state->'escalation' ? 'decided')`, w.org, delivery.ReasonChecksUnverified)
}

// waived counts the verdicts that carried a waiver, about runs (all for
// none).
func (c *checksConductor) waived(runs ...string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, d := range c.decided {
		if len(runs) > 0 && !slices.Contains(runs, d.RunID) {
			continue
		}
		for _, v := range d.Checks {
			if v.Verdict == delivery.VerdictSameAsWaived {
				n++
			}
		}
	}
	return n
}

// about counts the verdicts given about runs.
func (c *checksConductor) about(runs ...string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, d := range c.decided {
		if slices.Contains(runs, d.RunID) {
			n++
		}
	}
	return n
}

// conductorSpecNow is the lux spec of the task's latest conductor, ""
// while lux has none yet.
func (w *world) conductorSpecNow(task string) string {
	id, _, _ := w.conductor(task)
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.run"] == id {
			return string(r.Spec)
		}
	}
	return ""
}

// checksAsked is what the gate asked about runID's turn.
func (w *world) checksAsked(runID string, turn int64) *delivery.ChecksAsked {
	w.t.Helper()
	var a *delivery.ChecksAsked
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		a, _, err = delivery.ChecksAskedTx(context.Background(), tx, runID, turn)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return a
}

// sameFailure says runID's latest recorded call of command fails the same
// tests ("FAIL: " lines) as the waiver's Run's last call of it before the
// waiver.
func (w *world) sameFailure(runID string, wv *delivery.CarriedWaiver, command string) bool {
	w.t.Helper()
	output := func(run string, before int64) string {
		return w.str(`SELECT COALESCE((SELECT d.payload::text FROM events c JOIN events d ON d.run_id = c.run_id AND d.event_type = 'agent.tool.completed'
			AND d.payload->>'callId' = c.payload->>'callId'
			WHERE c.run_id = $1 AND c.event_type = 'agent.tool.called' AND c.payload->'input'->>'command' = $2 AND ($3::bigint = 0 OR d.cursor < $3)
			ORDER BY d.cursor DESC LIMIT 1), '')`, run, command, before)
	}
	this, waived := failing(output(runID, 0)), failing(output(wv.RunID, wv.Cursor))
	return this != "" && this == waived
}

// failing are the distinct "FAIL: <test>" names of a recorded output, sorted.
func failing(output string) string {
	var out []string
	for _, m := range regexp.MustCompile(`FAIL: (\w+)`).FindAllStringSubmatch(output, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// rows are the string columns of sql's rows.
func (w *world) rows(sql string, args ...any) [][]string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), sql, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, []string{a, b})
	}
	return out
}

// checksDelivery starts a delivery its conductor decides (as Deliver
// does), whose implementer is model; c, the scripted conductor, decides
// its checks (nil: a fresh one).
func (w *world) checksDelivery(model string, c *checksConductor) string {
	w.t.Helper()
	if c == nil {
		c = &checksConductor{}
	}
	w.checks = c
	w.lux.ManualChecks = true
	w.syncer.ConductorWarm = time.Hour
	w.onModel("implementer", model)
	wi := w.task()
	if !w.checksTested {
		w.deliverByAPI(wi)
		return wi
	}
	// With the scripted tester, on a task of one criterion.
	mustExec(w.t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets"]' WHERE id = $1`, wi)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, wi)
	}); err != nil {
		w.t.Fatal(err)
	}
	w.withTools()
	p := scriptedPolicy()
	p.Test = true
	w.startConducted(wi, p)
	return wi
}

// startConducted starts the delivery workflow as Deliver's API does: its
// conductor decides it, quietly.
func (w *world) startConducted(taskID string, policy delivery.Policy) string {
	id, _, err := w.runtime.Start(context.Background(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + taskID, TaskID: taskID,
		Input: delivery.State{TaskID: taskID, ProjectID: w.project, Policy: policy, Branch: delivery.BranchFor(taskID, 1),
			HadConductor: true, Decider: delivery.DeciderConductor, Voice: delivery.VoiceQuiet},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
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

// An honest report the conductor backs publishes: one judgement, verified,
// and the implementer's work reaches the pull request.
func TestAChecksReportTheConductorBacksPublishes(t *testing.T) {
	w := newWorld(t)
	c := &checksConductor{}
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, c)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.judged(wi); strings.Join(got, ",") != "verified" {
		t.Errorf("judged %v, want one verified", got)
	}
	if n := w.implementerPushes(wi); n != 1 {
		t.Errorf("%d pushes, want 1", n)
	}
	// One verdict about the implementer: its one check backed, and Missing.
	implementer := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi)
	if n := c.about(implementer); n != 1 {
		t.Errorf("%d verdicts about the implementer, want one", n)
	}
	// The pull request accounts for every gated Run, the implementer's
	// backed command included, not only the last Run judged.
	backed := "- `missing checks` — **pass** — " + checksReason + " (decision)"
	if got, want := decisionsNamed(checksSection(w.gh.Pull(1).Body)), "Checks were verified against the Run's recorded commands.\n\n"+
		"- **Implementer** — backed\n"+
		"  - `"+fakeagent.CheckCommand+"` — **pass** — "+checksReason+" (decision)\n"+
		"  "+backed+"\n"+
		"- **Fixer** — backed\n  "+backed+"\n"+
		"- **Simplifier** — backed\n  "+backed; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
	}
}

// decisionsNamed is a checks account with each conductor's decision named
// "(decision)", whatever its event's cursor.
func decisionsNamed(account string) string {
	return regexp.MustCompile(` \(the conductor's decision, event \d+\)`).ReplaceAllString(account, " (decision)")
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
	wi := w.checksDelivery(fakeagent.ChecksFixedModel, nil)
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
// to the escalation with a person, asserting no push was asked for.
func (w *world) unbackedUntilEscalated(rounds int) string {
	w.t.Helper()
	return w.unbackedUntilEscalatedWith(rounds, nil)
}

// unbackedUntilEscalatedWith is unbackedUntilEscalated decided by c.
func (w *world) unbackedUntilEscalatedWith(rounds int, c *checksConductor) string {
	w.t.Helper()
	if rounds >= 0 {
		mustExec(w.t, w.owner, `UPDATE forge_credentials SET settings = jsonb_build_object('checksFixRounds', $2::int)
			WHERE organization_id = $1`, w.org, rounds)
	}
	wi := w.checksDelivery(fakeagent.ChecksUnbackedModel, c)
	w.until("the checks escalation", func() bool { return w.escalationReason(wi) == delivery.ReasonChecksUnverified })
	// The conductor holds it, then hands it to a person (waive is a
	// person's).
	w.until("the escalation with a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if n := w.implementerPushes(wi); n != 0 {
		w.t.Fatalf("%d pushes of an unbacked report", n)
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
	backed := "  - `missing checks` — **pass** — " + checksReason + " (decision)"
	if got, want := decisionsNamed(checksSection(w.gh.Pull(1).Body)), "Checks were waived by a person:\n"+
		"- Implementer: The check needs hardware CI has; it passed there.\n\n"+
		"- **Implementer** — waived\n"+
		"- **Fixer** — backed\n"+backed+"\n"+
		"- **Simplifier** — backed\n"+backed; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
	}
}

// With no conductor deciding the delivery (Deliver's policy), a report of
// passes publishes unverified, and the skip is recorded.
func TestWithNoConductorTheChecksAreSkippedAndPublished(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", fakeagent.ChecksUnbackedModel)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.judged(wi); strings.Join(got, ",") != "skipped" {
		t.Errorf("judged %v, want the skip recorded", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvRunChecksAsked); n != 0 {
		t.Errorf("asked %d times with no conductor", n)
	}
	if got, want := checksSection(w.gh.Pull(1).Body), "Checks not verified: no conductor decided them.\n\n"+
		"- **Implementer** — not verified\n"+
		"- **Fixer** — not verified\n"+
		"- **Simplifier** — not verified"; got != want {
		t.Errorf("the pull request's checks section is\n%s\nwant\n%s", got, want)
	}
}

// Only the declared phases are judged: the reviewer, which publishes
// nothing, is never asked about.
func TestAReviewerIsNeverJudged(t *testing.T) {
	w := newWorld(t)
	c := &checksConductor{}
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, c)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND r.phase NOT IN ('implement', 'fix', 'simplify') AND e.event_type IN ($2, $3)`, wi, delivery.EvRunChecksJudged, delivery.EvRunChecksAsked); n != 0 {
		t.Errorf("%d judgements or asks of a phase that is not gated", n)
	}
	gated := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix', 'simplify')`, wi)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range c.decided {
		if !slices.Contains(gated, d.RunID) {
			t.Errorf("decided about %q", d.RunID)
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

// simplifierAsked is how many verdicts c gave about the task's simplifier
// Runs.
func (w *world) simplifierAsked(wi string, c *checksConductor) int {
	w.t.Helper()
	return c.about(w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi)...)
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

// A simplifier that changes nothing and reports no checks goes on with
// nothing asked and nothing sent back, recorded as needing no checks; the pull
// request still says the implementer's checks were waived, and why, and
// names the simplifier's outcome as its own, not as backed.
func TestAnUnchangedSimplifierNeedsNoChecksAndKeepsTheImplementersWaiver(t *testing.T) {
	w := newWorld(t)
	w.scriptedSimplifier(nil)
	// A real model's tester, so its spec carries the prompt a real one gets;
	// the fake lux still plays it as the scripted tester.
	w.checksTested = true
	w.onModel("qa_browser", "claude-test")
	c := &checksConductor{}
	wi := w.unbackedUntilEscalatedWith(0, c)
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
	if n := w.simplifierAsked(wi, c); n != 0 {
		t.Errorf("%d verdicts about the unchanged simplifier", n)
	}
	if n := w.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE r.task_id = $1 AND r.phase = 'simplify'`, wi); n != 0 {
		t.Errorf("%d directives sent to the unchanged simplifier", n)
	}
	backed := "  - `missing checks` — **pass** — " + checksReason + " (decision)"
	if got, want := decisionsNamed(checksSection(w.gh.Pull(1).Body)), "Checks were waived by a person:\n"+
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
	wi := w.checksDelivery(fakeagent.ChecksHonestModel, nil)
	w.until("the checks escalation", func() bool { return w.escalationReason(wi) == delivery.ReasonChecksUnverified })
	if got := w.simplifierJudged(wi); strings.Join(got, ",") != "unverified" {
		t.Errorf("the simplifier was judged %v, want its missing report rejected", got)
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Errorf("%d pull requests opened over a held simplifier", n)
	}
}
