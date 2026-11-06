package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// conducted gives the harness's task a live delivery its conductor decides
// and a live conductor Run.
func conducted(w *harnessWorld) string {
	w.t.Helper()
	mustOwner(w.t, w, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_'||$1, $1, $2, 'task.delivery', 'k_'||$1, 'waiting', 'implement',
		'{"attempt":1,"decider":"conductor","hadConductor":true,"voice":"quiet"}'::jsonb)`, w.run.Org, w.run.TaskID)
	cond := "run_cond_" + w.run.Org
	mustOwner(w.t, w, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, lux_run_id, lux_state)
		VALUES ($1, $2, $3, $4, 1, 'conductor', 'agent', 'running', 'lux_cond', 'running')`, cond, w.run.Org, w.run.ProjectID, w.run.TaskID)
	return cond
}

func (w *harnessWorld) conductorRef(cond string) delivery.RunRef {
	return delivery.RunRef{Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, RunID: cond}
}

func (w *harnessWorld) turn() int64 {
	w.t.Helper()
	var turn int64
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		turn, err = delivery.LatestTurnTx(context.Background(), tx, w.run.ID)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return turn
}

// decide records the conductor's verdict as decide_checks does.
func (w *harnessWorld) decide(cond string, in delivery.DecideChecks) (int64, error) {
	var cursor int64
	err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		cursor, err = delivery.ConductChecksTx(context.Background(), tx, w.conductorRef(cond), in)
		return err
	})
	return cursor, err
}

func (w *harnessWorld) gate() (pass, acted bool) {
	w.t.Helper()
	pass, acted, err := w.s.checksBeforePush(context.Background(), w.run)
	if err != nil {
		w.t.Fatal(err)
	}
	return pass, acted
}

func (w *harnessWorld) judgedCount() int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, w.run.ID, delivery.EvRunChecksJudged)
}

// A conducted delivery's gate never asks the judgement tier: it asks the
// conductor once per turn (a wake keyed on the turn), records no
// judgement, holds the Run unpushed and starts its wait.
func TestChecksGateNeverCallsJudge(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	for range 2 {
		if pass, acted := w.gate(); pass || acted {
			t.Fatalf("pending gate: pass %v acted %v", pass, acted)
		}
	}
	if n := w.judgedCount(); n != 0 {
		t.Fatalf("%d judgements recorded while pending", n)
	}
	key := delivery.ChecksKey(w.run.ID, w.turn())
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'checks' AND key = $2`, w.run.TaskID, key); n != 1 {
		t.Fatalf("%d checks wakes for %s", n, key)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, w.run.ID, delivery.EvRunChecksAsked); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL`, w.run.ID); n != 1 {
		t.Fatal("the held Run does not wait, so it would never be parked")
	}
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE key = $1`, key).Scan(&line); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "decide_checks") || !strings.Contains(line, `"verify" reported pass (evidence `) || !strings.Contains(line, `successful⟦/external`) || !strings.Contains(line, `is the pass backed`) ||
		!strings.Contains(line, "obviously missing") {
		t.Fatalf("wake line %q", line)
	}
}

// The conductor's verdict passes a failure as pre-existing at the task's
// base: the judgement cites the verdict's cursor and the conductor, and
// run.checks.conducted is the conductor's, on the writer's Run.
func TestConductorVerdictPassesPreexistingAndIsAudited(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	h.compares(w, 0)
	cond := conducted(w)
	// Established at the base commit, as the conductor is told to.
	bashCall(w, "base", "git worktree add ../base "+h.Main+" && cd ../base && npx tsc --noEmit", tscFailure, 2)
	bashCall(w, "head", "npx tsc --noEmit", tscFailure, 2)
	checksReply(w, failingTsc+reportedPass)
	endTurn(w)
	if pass, _ := w.gate(); pass {
		t.Fatal("passed before the conductor decided")
	}
	turn := w.turn()
	cursor, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: turn,
		Checks: []delivery.ConductedCheck{
			{Command: "npx tsc --noEmit", Verdict: delivery.VerdictPreexisting, Reason: "verify at the base shows the same 95 errors"},
			{Command: "make lint", Verdict: delivery.VerdictBacked, Reason: "call head"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "lint and types cover it"}})
	if err != nil {
		t.Fatal(err)
	}
	if pass, acted := w.gate(); !pass || !acted {
		t.Fatalf("pass %v acted %v", pass, acted)
	}
	got := recordedJudgement(w)
	if !got.Verified || !got.ConductorPassed || len(got.Verdicts) != 3 {
		t.Fatalf("%+v", got)
	}
	for _, v := range got.Verdicts {
		if v.DecisionCursor != cursor || v.By != "conductor" || v.CallID != "" {
			t.Fatalf("verdict %+v does not cite decision %d by the conductor", v, cursor)
		}
	}
	if got.Verdicts[0].Result != delivery.CheckPreexisting {
		t.Fatalf("%+v", got.Verdicts[0])
	}
	var actorType, actorID, conductorRun string
	var payloadTurn int64
	if err := w.owner.QueryRow(context.Background(), `SELECT actor_type, actor_id, payload->>'conductorRunId', (payload->>'turn')::bigint
		FROM events WHERE run_id = $1 AND event_type = $2 AND cursor = $3`, w.run.ID, delivery.EvRunChecksConducted, cursor).
		Scan(&actorType, &actorID, &conductorRun, &payloadTurn); err != nil {
		t.Fatal(err)
	}
	if actorType != ledger.ActorAgent || actorID != cond || conductorRun != cond || payloadTurn != turn {
		t.Fatalf("audit %s %s %s %d", actorType, actorID, conductorRun, payloadTurn)
	}
	// Decided: a second, otherwise valid, verdict on the turn is refused.
	again := delivery.DecideChecks{RunID: w.run.ID, Turn: turn,
		Checks: []delivery.ConductedCheck{
			{Command: "npx tsc --noEmit", Verdict: delivery.VerdictReject, Reason: "changed my mind"},
			{Command: "make lint", Verdict: delivery.VerdictBacked, Reason: "call head"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}
	if _, err := w.decide(cond, again); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("a second verdict: %v", err)
	}
}

// A verdict on a turn the Run has since moved past is never read: the gate
// asks again about the new turn, and decide_checks refuses the old one.
func TestStaleTurnVerdictIgnored(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	old := w.turn()
	if _, err := w.decide(cond, backedAll(w.run.ID, old)); err != nil {
		t.Fatal(err)
	}
	// A new turn before the gate read the verdict.
	checksReply(w, passingChecks)
	endTurn(w)
	if w.turn() == old {
		t.Fatal("no new turn")
	}
	if pass, acted := w.gate(); pass || acted {
		t.Fatalf("the old turn's verdict passed the new turn: pass %v acted %v", pass, acted)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE key = $1`, delivery.ChecksKey(w.run.ID, w.turn())); n != 1 {
		t.Fatal("the new turn was not asked")
	}
	if _, err := w.decide(cond, backedAll(w.run.ID, old)); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "not the Run's latest turn") {
		t.Fatalf("a verdict on the old turn: %v", err)
	}
}

func backedAll(runID string, turn int64) delivery.DecideChecks {
	return delivery.DecideChecks{RunID: runID, Turn: turn,
		Checks:  []delivery.ConductedCheck{{Command: "verify", Verdict: delivery.VerdictBacked, Reason: "the call shows it"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "nothing else changed"}}
}

// A delivery no conductor decides keeps the untiered verdict: nothing is
// asked, a failure is rejected for a person, passes skip.
func TestPolicyDeliveryChecksAsUntiered(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		pass        bool
	}{{"failure", failingTsc, false}, {"passes", passingChecks, true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			delivering(w, false)
			checksReply(w, tc.reply)
			endTurn(w)
			if pass, acted := w.gate(); pass != tc.pass || !acted {
				t.Fatalf("pass %v acted %v", pass, acted)
			}
			if n := w.countEvents(delivery.EvRunChecksAsked); n != 0 {
				t.Fatalf("asked %d times with no conductor", n)
			}
			got := recordedJudgement(w)
			if tc.pass {
				if !got.Skipped || !strings.Contains(got.Message, "no conductor") {
					t.Fatalf("%+v", got)
				}
				return
			}
			if got.Verified || len(got.Verdicts) != 1 || got.Verdicts[0].JudgeReason != "no conductor: only a person can pass a reported failure" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// overdue moves the gate's ask past its wait.
func (w *harnessWorld) overdue() {
	mustOwner(w.t, w, `UPDATE events SET payload = jsonb_set(payload, '{deadline}', to_jsonb(now() - interval '1 second'))
		WHERE run_id = $1 AND event_type = $2`, w.run.ID, delivery.EvRunChecksAsked)
}

// Unanswered past the wait, a report of passes passes unverified.
func TestUnansweredAllPassSkips(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	w.overdue()
	if pass, acted := w.gate(); !pass || !acted {
		t.Fatalf("pass %v acted %v", pass, acted)
	}
	if got := recordedJudgement(w); !got.Skipped || !strings.Contains(got.Message, "not verified by the conductor") {
		t.Fatalf("%+v", got)
	}
}

// Unanswered past the wait, a failure is rejected and sent back: it counts
// a round and never passes without a decision.
func TestUnansweredNonPassNeverPasses(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	checksReply(w, failingTsc+reportedPass)
	endTurn(w)
	w.gate()
	w.overdue()
	if pass, acted := w.gate(); pass || !acted {
		t.Fatalf("pass %v acted %v", pass, acted)
	}
	got := recordedJudgement(w)
	if got.Verified || got.Skipped || !strings.Contains(got.Message, "not verified by the conductor") {
		t.Fatalf("%+v", got)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE 'Your checks report was not verified%'`, w.run.ID); n != 1 {
		t.Fatalf("%d send-backs", n)
	}
}

// With the conductor's Run failed, or no model to start one, the gate does
// not wait: passes skip, a failure is rejected.
func TestChecksWithTheConductorUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		pass        bool
	}{{"failure", failingTsc, false}, {"passes", passingChecks, true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			cond := conducted(w)
			mustOwner(t, w, `UPDATE runs SET status = 'failed', ended_at = now() WHERE id = $1`, cond)
			checksReply(w, tc.reply)
			endTurn(w)
			if pass, acted := w.gate(); pass != tc.pass || !acted {
				t.Fatalf("pass %v acted %v", pass, acted)
			}
			if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE kind = 'checks'`); n != 0 {
				t.Fatal("asked a failed conductor")
			}
		})
	}
}

// The verdict resumes a Run parked on it, as an answer does (woken).
func TestAConductedVerdictWakesAParkedRun(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	wokenAt := func() *time.Time {
		var at *time.Time
		if err := w.owner.QueryRow(context.Background(), `SELECT `+woken+` FROM runs r WHERE r.id = $1 AND $2::int IS NOT NULL`,
			w.run.ID, 0, causeAnswer).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	mustOwner(t, w, `INSERT INTO events (id, organization_id, event_type, occurred_at, run_id, task_id, actor_type, actor_id, source, payload)
		VALUES ('evt_park', $1, 'run.parked', now() - interval '1 minute', $2, $3, 'system', 'dude', 'orchestrator', jsonb_build_object('parkedAt', now() - interval '1 minute'))`,
		w.run.Org, w.run.ID, w.run.TaskID)
	if at := wokenAt(); at != nil {
		t.Fatalf("woken at %v before the verdict", at)
	}
	if _, err := w.decide(cond, backedAll(w.run.ID, w.turn())); err != nil {
		t.Fatal(err)
	}
	if wokenAt() == nil {
		t.Fatal("the verdict does not wake the parked Run")
	}
}

// decide_checks refuses what the gate did not ask or cannot read.
func TestDecideChecksRefusals(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, "checks:\n  - command: make test\n    result: fail\n    evidence: one\n  - command: make e2e\n    result: not-run\n    evidence: needs a browser\n")
	endTurn(w)
	// Not asked yet: not waiting.
	turn := w.turn()
	ok := func() delivery.DecideChecks {
		return delivery.DecideChecks{RunID: w.run.ID, Turn: turn, Checks: []delivery.ConductedCheck{
			{Command: "make test", Verdict: delivery.VerdictReject, Reason: "new"},
			{Command: "make e2e", Verdict: delivery.VerdictDeferred, Reason: "the tester runs it"}},
			Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}
	}
	if _, err := w.decide(cond, ok()); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("not asked: %v", err)
	}
	w.gate()
	// A writer Run of the task's previous attempt, asked about and not
	// settled: of the task, but not of its current attempt (2).
	mustOwner(t, w, `UPDATE runs SET attempt = 2 WHERE task_id = $1`, w.run.TaskID)
	mustOwner(t, w, `UPDATE workflow_runs SET state = jsonb_set(state, '{attempt}', '2') WHERE task_id = $1`, w.run.TaskID)
	mustOwner(t, w, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status)
		SELECT 'run_old_'||organization_id, organization_id, project_id, task_id, 1, 'implement', 'completed' FROM runs WHERE id = $1`, w.run.ID)
	mustOwner(t, w, `INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_old_stop', organization_id, 'run_old_'||organization_id, 'agent.session.stopped', 'agent', 'x', 'orchestrator', '{}'
		FROM runs WHERE id = $1`, w.run.ID)
	oldTurn := int64(w.count(`SELECT cursor FROM events WHERE id = 'evt_old_stop'`))
	mustOwner(t, w, `INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_old_ask', organization_id, 'run_old_'||organization_id, event_type, actor_type, actor_id, source,
			jsonb_set(payload, '{turn}', to_jsonb($3::bigint))
		FROM events WHERE run_id = $1 AND event_type = $2`, w.run.ID, delivery.EvRunChecksAsked, oldTurn)
	for name, tc := range map[string]struct {
		mutate func(*delivery.DecideChecks)
		caller string
		want   string
	}{
		"wrong turn":                      {func(d *delivery.DecideChecks) { d.Turn-- }, cond, "not the Run's latest turn"},
		"other task":                      {func(d *delivery.DecideChecks) { d.RunID = "run_elsewhere" }, cond, "not a writer Run of this task"},
		"incomplete":                      {func(d *delivery.DecideChecks) { d.Checks = d.Checks[:1] }, cond, "decide every reported check"},
		"preexisting no base":             {func(d *delivery.DecideChecks) { d.Checks[0].Verdict = delivery.VerdictPreexisting }, cond, "preexisting needs a recorded commit"},
		"deferred on a fail":              {func(d *delivery.DecideChecks) { d.Checks[0].Verdict = delivery.VerdictDeferred }, cond, "is not a verdict for"},
		"same_as_waived without a waiver": {func(d *delivery.DecideChecks) { d.Checks[0].Verdict = delivery.VerdictSameAsWaived }, cond, "is not a verdict for"},
		"no reason":                       {func(d *delivery.DecideChecks) { d.Checks[1].Reason = " " }, cond, "needs its reason"},
		"no missing":                      {func(d *delivery.DecideChecks) { d.Missing = delivery.ConductedMissing{} }, cond, "missing needs"},
		"non-conductor caller":            {func(*delivery.DecideChecks) {}, w.run.ID, "no longer"},
		"a reason too long":               {func(d *delivery.DecideChecks) { d.Checks[0].Reason = strings.Repeat("é", 2001) }, cond, "a reason is at most 2000 characters"},
		"a missing reason too long":       {func(d *delivery.DecideChecks) { d.Missing.Reason = strings.Repeat("x", 2001) }, cond, "and missing's is 2001"},
		"extra":                           {func(d *delivery.DecideChecks) { d.Checks = append(d.Checks, d.Checks[0]) }, cond, "decide every reported check"},
		"a previous attempt's writer":     {func(d *delivery.DecideChecks) { d.RunID, d.Turn = "run_old_"+w.run.Org, oldTurn }, cond, "not a writer Run of this task"},
	} {
		t.Run(name, func(t *testing.T) {
			in := ok()
			tc.mutate(&in)
			if _, err := w.decide(tc.caller, in); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
	// At the bound, accepted.
	bounded := ok()
	bounded.Checks[0].Reason, bounded.Missing.Reason = strings.Repeat("é", 2000), strings.Repeat("x", 2000)
	if _, err := w.decide(cond, bounded); err != nil {
		t.Fatal(err)
	}
	if pass, _ := w.gate(); pass {
		t.Fatal("a rejected failure passed")
	}
}

// The scripted DefaultsModel conductor reads the real checks wake: its
// decide_checks backs passes and rejects every failure or not-run check,
// one verdict per reported check, in order — never passing a failure.
func TestTheDefaultsConductorReadsTheChecksWake(t *testing.T) {
	a := delivery.ChecksAsked{Turn: 42, Check: []delivery.Check{
		{Command: `make "it"`, Result: delivery.CheckPass}, {Command: "make test", Result: delivery.CheckFail},
		{Command: "e2e", Result: delivery.CheckNotRun}, {Command: "make lint", Result: delivery.CheckPass},
		{Command: "make vet", Result: delivery.CheckPass}}}
	// dude recorded make "it" exiting 0, make lint exiting 1, and no call
	// of make vet: only the first pass is backed.
	line := checksWakeLine(phaseRun{ID: "run_x", Phase: "implement"}, a, nil, []string{"exit 0", "", "", "exit 1", "none recorded"})
	calls := fakeagent.ChecksDefaults("Since you last heard: " + line)
	if len(calls) != 1 || calls[0][0] != "decide_checks" {
		t.Fatalf("%q from %q", calls, line)
	}
	var in delivery.DecideChecks
	if err := json.Unmarshal([]byte(calls[0][1]), &in); err != nil {
		t.Fatal(err)
	}
	if in.RunID != "run_x" || in.Turn != 42 || len(in.Checks) != 5 || in.Checks[0].Command != `make "it"` ||
		in.Checks[0].Verdict != delivery.VerdictBacked || in.Checks[1].Verdict != delivery.VerdictReject ||
		in.Checks[2].Verdict != delivery.VerdictReject || in.Checks[3].Verdict != delivery.VerdictUnbacked ||
		in.Checks[4].Verdict != delivery.VerdictUnbacked || in.Missing.Verdict != delivery.MissingNone {
		t.Fatalf("%+v from %q", in, line)
	}
}

// The gate's own reading of a verdict passes nothing the ask did not
// allow, whatever was recorded: a pre-existing verdict with no base, or a
// same_as_waived citing another waiver, rejects the failure.
func TestAVerdictPassesOnlyWhatTheAskAllowed(t *testing.T) {
	checks := []delivery.Check{{Command: "make test", Result: delivery.CheckFail, Evidence: "x"}}
	for name, tc := range map[string]struct {
		asked   *delivery.ChecksAsked
		verdict delivery.ConductedCheck
		want    bool
	}{
		"preexisting with a base":    {&delivery.ChecksAsked{Bases: []delivery.ChecksBase{{Repo: "r", Branch: "main", SHA: "abc"}}}, delivery.ConductedCheck{Command: "make test", Verdict: delivery.VerdictPreexisting, Reason: "r"}, true},
		"preexisting with no base":   {&delivery.ChecksAsked{}, delivery.ConductedCheck{Command: "make test", Verdict: delivery.VerdictPreexisting, Reason: "r"}, false},
		"preexisting with no ask":    {nil, delivery.ConductedCheck{Command: "make test", Verdict: delivery.VerdictPreexisting, Reason: "r"}, false},
		"the waiver it cites":        {&delivery.ChecksAsked{Waivers: map[int]*delivery.CarriedWaiver{0: {Cursor: 7}}}, delivery.ConductedCheck{Command: "make test", Verdict: delivery.VerdictSameAsWaived, WaiverCursor: 7, Reason: "r"}, true},
		"another waiver":             {&delivery.ChecksAsked{Waivers: map[int]*delivery.CarriedWaiver{0: {Cursor: 7}}}, delivery.ConductedCheck{Command: "make test", Verdict: delivery.VerdictSameAsWaived, WaiverCursor: 8, Reason: "r"}, false},
		"a deferred failure":         {&delivery.ChecksAsked{}, delivery.ConductedCheck{Command: "make test", Verdict: delivery.VerdictDeferred, Reason: "r"}, false},
		"a verdict on another check": {&delivery.ChecksAsked{Bases: []delivery.ChecksBase{{SHA: "abc"}}}, delivery.ConductedCheck{Command: "make lint", Verdict: delivery.VerdictPreexisting, Reason: "r"}, false},
		"an unbacked pass":           {&delivery.ChecksAsked{}, delivery.ConductedCheck{Command: "make lint", Verdict: delivery.VerdictUnbacked, Reason: "r"}, false},
		"a backed pass":              {&delivery.ChecksAsked{}, delivery.ConductedCheck{Command: "make lint", Verdict: delivery.VerdictBacked, Reason: "r"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			v := &delivery.ConductedChecks{Checks: []delivery.ConductedCheck{tc.verdict}, Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone}, Cursor: 3}
			checks := checks
			if tc.verdict.Command == "make lint" && tc.verdict.Verdict != delivery.VerdictPreexisting {
				checks = []delivery.Check{{Command: "make lint", Result: delivery.CheckPass, Evidence: "clean"}}
			}
			if got := verdictDecision(checksDecision{}, checks, tc.asked, v); got.Verified != tc.want {
				t.Fatalf("verified %v, want %v: %+v", got.Verified, tc.want, got)
			}
		})
	}
}

// A pending conducted gate does not inspect the Run's checkouts again on
// every pass: the ask was made only once the turn was found changed, and
// until the verdict, the deadline or the conductor's loss nothing about the
// turn it asked about can change. One lux exec for the turn, however many
// sweeps pass while the conductor decides.
func TestAPendingChecksAskIsNotInspectedEveryPass(t *testing.T) {
	w := newUnchangedWorld(t, "api")
	cond := conducted(w.harnessWorld)
	w.write("api", "new.txt", "work\n")
	checksReply(w.harnessWorld, passingChecks)
	w.endTurn()
	for range 6 {
		if pass, acted := w.harnessWorld.gate(); pass || acted {
			t.Fatalf("pending gate: pass %v acted %v", pass, acted)
		}
	}
	if n := w.lux.execCount(); n != 1 {
		t.Fatalf("%d lux execs over 6 pending passes, want 1", n)
	}
	if _, err := w.decide(cond, backedAll(w.run.ID, w.turn())); err != nil {
		t.Fatal(err)
	}
	if pass, acted := w.harnessWorld.gate(); !pass || !acted {
		t.Fatalf("after the verdict: pass %v acted %v", pass, acted)
	}
}

// The same holds the other way: with the ask past its deadline, or the
// conductor lost, the pending shortcut never stands in for the fallback.
// A delivery that leaves its conductor while an ask is open (a person
// accepts Deliver's defaults: decider policy) falls back on the gate's
// next pass, not at the ask's deadline.
func TestAnOpenAskYieldsWhenTheDeliveryLeavesItsConductor(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	if pass, acted := w.gate(); pass || acted {
		t.Fatalf("asked: pass %v acted %v, want pending", pass, acted)
	}
	mustOwner(t, w, `UPDATE workflow_runs SET state = jsonb_set(state, '{decider}', '"policy"') WHERE task_id = $1`, w.run.TaskID)
	if pass, acted := w.gate(); !pass || !acted {
		t.Fatalf("pass %v acted %v, want the fallback's skip at once", pass, acted)
	}
	if got := recordedJudgement(w); !got.Skipped {
		t.Fatalf("%+v", got)
	}
}

func TestAPendingAskShortcutYieldsToTheFallback(t *testing.T) {
	for _, c := range []string{"overdue", "unavailable"} {
		t.Run(c, func(t *testing.T) {
			w := newUnchangedWorld(t, "api")
			cond := conducted(w.harnessWorld)
			w.write("api", "new.txt", "work\n")
			checksReply(w.harnessWorld, passingChecks)
			w.endTurn()
			w.harnessWorld.gate()
			if c == "overdue" {
				w.overdue()
			} else {
				mustOwner(t, w.harnessWorld, `UPDATE runs SET status = 'failed', ended_at = now() WHERE id = $1`, cond)
			}
			if pass, acted := w.harnessWorld.gate(); !pass || !acted {
				t.Fatalf("pass %v acted %v, want the fallback's skip", pass, acted)
			}
			if got := recordedJudgement(w.harnessWorld); !got.Skipped {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// dude's reading of a reported pass's latest call is of that command, not
// of any later call that merely contains it: a failed `verify` followed by
// `echo verify` exiting 0 reads "exit 1". A call that only contains the
// command (piped, ORed, grepped, a longer command) is not one of it; a
// `cd X && ` before it is.
func TestRecordedCallsReadTheReportedCommandNotOneContainingIt(t *testing.T) {
	for _, c := range []struct {
		name, reported string
		calls          [][]any // command, exit
		want           string
	}{
		{"a later echo of it", "verify", [][]any{{"verify", 1}, {"echo verify", 0}}, "exit 1"},
		{"ORed with true", "make test", [][]any{{"make test || true", 0}}, "none recorded"},
		{"grepped for", "make test", [][]any{{"make test", 2}, {`grep "make test" Makefile`, 0}}, "exit 2"},
		{"a longer command", "go test ./...", [][]any{{"go test ./... -run X", 0}}, "none recorded"},
		{"the command itself, last", "go test ./...", [][]any{{"go test ./... -run X", 1}, {"go  test\n./...", 0}}, "exit 0"},
		{"in its directory", "make test", [][]any{{"cd api && make test", 0}}, "exit 0"},
		{"in a directory, then more", "make test", [][]any{{"cd api && make test && echo ok", 0}}, "none recorded"},
		{"reported with its cd", "cd api && make test", [][]any{{"cd api && make test", 0}}, "exit 0"},
		{"reported with its cd, run in it", "cd api && make test", [][]any{{"cd  api &&\nmake test", 0}}, "exit 0"},
		{"reported in another directory", "cd api && make test", [][]any{{"cd web && make test", 0}}, "none recorded"},
		{"reported with its cd, run without", "cd api && make test", [][]any{{"make test", 0}}, "none recorded"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			got := w.recordedAfter(c.calls, c.reported, delivery.CheckPass, nil)
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("read %q, want %q", got, c.want)
			}
		})
	}
}

// The conductor saying a check is obviously missing holds the report as an
// unbacked pass does: not passed, sent back naming what is missing, the
// recorded missing-checks verdict unverified.
func TestAMissingCheckVerdictHoldsTheReport(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	in := backedAll(w.run.ID, w.turn())
	in.Missing = delivery.ConductedMissing{Verdict: delivery.MissingFound, Reason: "no test of the new parser"}
	if _, err := w.decide(cond, in); err != nil {
		t.Fatal(err)
	}
	if pass, acted := w.gate(); pass || !acted {
		t.Fatalf("pass %v acted %v", pass, acted)
	}
	got := recordedJudgement(w)
	last := got.Verdicts[len(got.Verdicts)-1]
	if got.Verified || last.Command != delivery.MissingChecks || last.Result != "unverified" || last.Reason != "no test of the new parser" {
		t.Fatalf("%+v", got)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE 'Your checks report was not verified%'
		AND text LIKE '%no test of the new parser%'`, w.run.ID); n != 1 {
		t.Fatalf("%d send-backs naming the missing check", n)
	}
}

// A Run parked on its checks ask is not resumed while the ask is open, and
// is once its deadline passes, so the gate's fallback runs.
func TestAParkedChecksWaitResumesAtItsDeadline(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	mustOwner(t, w, `UPDATE runs SET status = 'paused', dude_pause = 'person' WHERE id = $1`, w.run.ID)
	q := `SELECT ` + resumable + ` FROM ` + runFrom + ` WHERE r.id = $4`
	ok := func() (b bool) {
		t.Helper()
		if err := w.owner.QueryRow(context.Background(), q, 600.0, 0.0, 600.0, w.run.ID).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if ok() {
		t.Fatal("resumable while the ask is open")
	}
	w.overdue()
	if !ok() {
		t.Fatal("not resumed at the deadline")
	}
}

// A verdict that arrives after the deadline's fallback judged the turn is
// refused: the turn is settled, and nothing contradicts its judgement.
func TestALateVerdictAfterTheFallbackIsRefused(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	w.overdue()
	if pass, acted := w.gate(); !pass || !acted {
		t.Fatalf("fallback: pass %v acted %v", pass, acted)
	}
	if _, err := w.decide(cond, backedAll(w.run.ID, w.turn())); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("a late verdict: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, w.run.ID, delivery.EvRunChecksConducted); n != 0 {
		t.Fatalf("%d verdicts recorded after the fallback", n)
	}
}

// The wake puts the task's bases and each waiver's cursor before the
// per-check questions: with eight long failing checks, the line clipped,
// every cursor and base is still there for same_as_waived and preexisting.
// The incomplete refusal lists the commands and the cursors again.
func TestTheChecksWakeLeadsWithBasesAndWaiverCursors(t *testing.T) {
	a := delivery.ChecksAsked{Turn: 9, Waivers: map[int]*delivery.CarriedWaiver{}}
	for i := range 8 {
		a.Check = append(a.Check, delivery.Check{Command: fmt.Sprintf("go test ./pkg%d/... -run TestSomethingRatherLong", i), Result: delivery.CheckFail,
			Evidence: strings.Repeat("FAIL: TestSomethingRatherLong (0.00s) expected the greeting ", 3)})
	}
	a.Waivers[5] = &delivery.CarriedWaiver{Role: "implementer", Cursor: 4242}
	a.Waivers[7] = &delivery.CarriedWaiver{Role: "fixer", Cursor: 4343}
	bases := []taskBase{{Repo: "api", Branch: "main", SHA: "0123456789abcdef0123456789abcdef01234567"}}
	line := []rune(checksWakeLine(phaseRun{ID: "run_x", Phase: "fix"}, a, bases, nil))
	if len(line) <= 2000 {
		t.Fatalf("the wake is %d characters: not long enough to be clipped", len(line))
	}
	head := string(line[:2000])
	for _, want := range []string{"waiverCursor 4242", "waiverCursor 4343", "api: main at 0123456789abcdef0123456789abcdef01234567"} {
		if !strings.Contains(head, want) {
			t.Errorf("the clipped wake lacks %q: %s", want, head)
		}
	}
	if strings.Index(head, "waiverCursor 4343") > strings.Index(head, "reported fail") {
		t.Errorf("a waiver's cursor comes after the questions: %s", head)
	}
}

// A waived check's long command is clipped in its waiver sentence, so
// every waiver's cursor still leads the clipped wake.
func TestTheChecksWakeClipsWaivedCommands(t *testing.T) {
	a := delivery.ChecksAsked{Turn: 9, Waivers: map[int]*delivery.CarriedWaiver{}}
	for i := range 8 {
		a.Check = append(a.Check, delivery.Check{Command: fmt.Sprintf("go test ./pkg%d/... -run '%s'", i, strings.Repeat("TestSomethingRatherLong|", 20)),
			Result: delivery.CheckFail, Evidence: "FAIL"})
		a.Waivers[i] = &delivery.CarriedWaiver{Role: "implementer", Cursor: int64(7000 + i)}
	}
	line := []rune(checksWakeLine(phaseRun{ID: "run_x", Phase: "fix"}, a, nil, nil))
	head := string(line[:min(len(line), 2000)])
	for i := range 8 {
		if want := fmt.Sprintf("waiverCursor %d", 7000+i); !strings.Contains(head, want) {
			t.Errorf("the clipped wake lacks %q: %s", want, head)
		}
	}
}

// The scripted conductor takes a failure as pre-existing only on dude's
// recording of it failing at the base, never on the writer's evidence
// saying so.
func TestTheDefaultsConductorNeedsARecordedBaseCall(t *testing.T) {
	a := delivery.ChecksAsked{Turn: 3, Check: []delivery.Check{{Command: "make test", Result: delivery.CheckFail,
		Evidence: "TestClock " + fakeagent.ShownAtBase + " the starting commit"}}}
	bases := []taskBase{{Repo: "target", Branch: "main", SHA: "0123456789abcdef0123456789abcdef01234567"}}
	for _, c := range []struct {
		name, call, want string
	}{
		{"no call at the base", "none recorded", delivery.VerdictReject},
		{"passing at the base", "exit 0", delivery.VerdictReject},
		{"failing at the base", "exit 2", delivery.VerdictPreexisting},
		{"not run at the base", "exit 128", delivery.VerdictReject},
		{"not found at the base", "exit 127", delivery.VerdictReject},
		// dude's readings of a call naming the base whose failure is not
		// the command's (recordedCalls): never pre-existing.
		{"the worktree add failed", "did not run or was stopped: exit 128", delivery.VerdictReject},
		{"an exit a step before it may have returned", "unclear: exit 1, which a step before it may have returned", delivery.VerdictReject},
		{"a status, no exit", "failed", delivery.VerdictReject},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := checksWakeLine(phaseRun{ID: "run_x", Phase: "implement"}, a, bases, []string{c.call})
			calls := fakeagent.ChecksDefaults(line)
			var in delivery.DecideChecks
			if len(calls) != 1 || json.Unmarshal([]byte(calls[0][1]), &in) != nil || len(in.Checks) != 1 {
				t.Fatalf("%q from %q", calls, line)
			}
			if in.Checks[0].Verdict != c.want {
				t.Fatalf("decided %s, want %s, from %q", in.Checks[0].Verdict, c.want, line)
			}
		})
	}
}

// dude reads a failure's call at the base: a call naming a base's commit
// whose last step is the command.
func TestRecordedCallsReadAFailureAtTheBase(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	bases := []taskBase{{Repo: "target", Branch: "main", SHA: sha}}
	for _, c := range []struct {
		name     string
		reported string // "" is "make test"
		calls    [][]any
		want     string
	}{
		{"at the base", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && make test", 2}, {"make test", 2}}, "exit 2"},
		{"at the base by its short SHA", "", [][]any{{"git worktree add ../task-base " + sha[:12] + " && cd ../task-base && make test", 2}}, "exit 2"},
		{"checked out in place", "", [][]any{{"git checkout " + sha + " && make test", 2}}, "exit 2"},
		{"switched to, detached", "", [][]any{{"git switch --detach " + sha + " && make test", 2}}, "exit 2"},
		{"passing at the base", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && make test", 0}}, "exit 0"},
		{"at another commit", "", [][]any{{"git worktree add ../b fedcba9876543210 && cd ../b && make test", 2}}, "none recorded"},
		{"only at the head", "", [][]any{{"make test", 2}}, "none recorded"},
		{"another command at the base", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && make lint", 2}}, "none recorded"},
		// The worktree add failed: the command never ran.
		{"the worktree add failed", "", [][]any{{"git worktree add ../task-base " + sha + " && cd ../task-base && make test", 128}}, "did not run or was stopped: exit 128"},
		// The SHA only named, the command run at the head.
		{"a diff naming the base, run at the head", "", [][]any{{"git diff " + sha[:12] + " --stat && make test", 2}}, "none recorded"},
		{"the worktree added, the command run outside it", "", [][]any{{"git worktree add ../b " + sha + " && make test", 2}}, "none recorded"},
		{"exit 1 a cd returned", "", [][]any{{"git worktree add ../b " + sha + " && cd ../c && make test", 1, "Preparing worktree (detached HEAD 0123456)\nbash: line 1: cd: ../c: No such file or directory"}}, "none recorded"},
		{"exit 1 a cd may have returned", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && make test", 1, "bash: line 1: cd: ../b: No such file or directory"}}, "unclear: exit 1, which a step before it may have returned"},
		{"exit 1 with no output", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && make test", 1, ""}}, "unclear: exit 1, which a step before it may have returned"},
		{"exit 1 of the command", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && make test", 1, "FAIL: TestClock (0.00s)\nexit status 1"}}, "exit 1"},
		{"another step before it", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && npm ci && make test", 2}}, "unclear: exit 2, which a step before it may have returned"},
		// A reported directory is the one under the base's worktree.
		{"reported in its directory", "cd api && make test", [][]any{{"git worktree add ../task-base " + sha + " && cd ../task-base/api && make test", 2}}, "exit 2"},
		{"reported in its directory, entered in two steps", "cd api && make test", [][]any{{"git worktree add ../task-base " + sha + " && cd ../task-base && cd api && make test", 2}}, "exit 2"},
		{"reported in its directory, run in another", "cd api && make test", [][]any{{"git worktree add ../task-base " + sha + " && cd ../task-base/web && make test", 2}}, "none recorded"},
		{"reported in its directory, run at the root", "cd api && make test", [][]any{{"git worktree add ../task-base " + sha + " && cd ../task-base && make test", 2}}, "none recorded"},
		{"reported with no directory, run in one", "", [][]any{{"git worktree add ../task-base " + sha + " && cd ../task-base/api && make test", 2}}, "exit 2"},
		// dash (/bin/sh on the runtime image) exits 2 on a failed cd; bash 1.
		{"exit 2 dash's cd returned", "cd api && make test", [][]any{{"git checkout " + sha + " && cd api && make test", 2, "sh: 1: cd: can't cd to api"}}, "unclear: exit 2, which a step before it may have returned"},
		{"exit 1 bash's cd returned", "cd api && make test", [][]any{{"git checkout " + sha + " && cd api && make test", 1, "bash: line 1: cd: api: No such file or directory"}}, "unclear: exit 1, which a step before it may have returned"},
		{"exit 2 with no output", "", [][]any{{"git checkout " + sha + " && make test", 2, ""}}, "unclear: exit 2, which a step before it may have returned"},
		{"exit 2 of make", "cd api && make test", [][]any{{"git checkout " + sha + " && cd api && make test", 2, "--- FAIL: TestClock (0.00s)\n    clock_test.go:12: got 3, want 2\nFAIL\nexit status 1\nmake: *** [Makefile:4: test] Error 1"}}, "exit 2"},
		// cd - goes back where the shell was, not down.
		{"cd - after entering the worktree", "", [][]any{{"git worktree add ../b " + sha + " && cd ../b && cd - && make test", 2}}, "none recorded"},
		{"cd -x after a checkout in place", "", [][]any{{"git checkout " + sha + " && cd -P && make test", 2}}, "none recorded"},
		// error: is a step's failure as git's shapes or as the first line;
		// a test framework's later in the output is the command's.
		{"a test's error: line", "", [][]any{{"git checkout " + sha + " && make test", 1, "running 1 test\ntest clock ... FAILED\nerror: expected 2, got 3\nFAIL"}}, "exit 1"},
		{"git's pathspec error", "", [][]any{{"git checkout " + sha + " && make test", 1, "Note: switching to '0123456'.\nerror: pathspec 'x' did not match any file(s) known to git"}}, "unclear: exit 1, which a step before it may have returned"},
		{"git's local changes error", "", [][]any{{"git checkout " + sha + " && make test", 1, "error: Your local changes to the following files would be overwritten by checkout:\n\tMakefile"}}, "unclear: exit 1, which a step before it may have returned"},
		{"an error: line first", "", [][]any{{"git checkout " + sha + " && make test", 1, "\nerror: could not detach HEAD\n"}}, "unclear: exit 1, which a step before it may have returned"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			got := w.recordedAfter(c.calls, cmp.Or(c.reported, "make test"), delivery.CheckFail, bases)
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("read %q, want %q", got, c.want)
			}
			// The scripted conductor, woken with that reading, takes the
			// failure as pre-existing only on a failing exit of the command.
			a := delivery.ChecksAsked{Turn: 3, Check: []delivery.Check{{Command: cmp.Or(c.reported, "make test"), Result: delivery.CheckFail, Evidence: "fails the same at the base"}}}
			var in delivery.DecideChecks
			calls := fakeagent.ChecksDefaults(checksWakeLine(phaseRun{ID: "run_x", Phase: "implement"}, a, bases, got))
			if len(calls) != 1 || json.Unmarshal([]byte(calls[0][1]), &in) != nil || len(in.Checks) != 1 {
				t.Fatalf("%q", calls)
			}
			want := delivery.VerdictReject
			if strings.HasPrefix(c.want, "exit ") && c.want != "exit 0" {
				want = delivery.VerdictPreexisting
			}
			if in.Checks[0].Verdict != want {
				t.Fatalf("the scripted conductor decided %s on %q, want %s", in.Checks[0].Verdict, got, want)
			}
		})
	}
}

// recordedAfter records calls (command, exit[, output]; output "out" when
// omitted) as the Run's shell calls and is recordedCalls of one reported
// check, command with result.
func (w *harnessWorld) recordedAfter(calls [][]any, command, result string, bases []taskBase) []string {
	w.t.Helper()
	for i, call := range calls {
		output := "out"
		if len(call) > 2 {
			output = call[2].(string)
		}
		bashCall(w, fmt.Sprintf("call_%d", i), call[0].(string), output, call[1].(int))
	}
	var got []string
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		got, err = recordedCalls(context.Background(), tx, w.run.ID, []delivery.Check{{Command: command, Result: result}}, bases)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return got
}
