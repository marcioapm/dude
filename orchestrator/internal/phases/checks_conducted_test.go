package phases

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// conducted gives the harness's task a live delivery its conductor decides
// and a live conductor Run, and turns the judge into one that fails the
// test when asked.
func conducted(w *harnessWorld) string {
	w.t.Helper()
	mustOwner(w.t, w, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_'||$1, $1, $2, 'task.delivery', 'k_'||$1, 'waiting', 'implement',
		'{"attempt":1,"decider":"conductor","hadConductor":true,"voice":"quiet"}'::jsonb)`, w.run.Org, w.run.TaskID)
	cond := "run_cond_" + w.run.Org
	mustOwner(w.t, w, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, lux_run_id, lux_state)
		VALUES ($1, $2, $3, $4, 1, 'conductor', 'agent', 'running', 'lux_cond', 'running')`, cond, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.s.JudgeUnconducted = true
	w.s.Judge = judgeMustNot{w.t}
	return cond
}

// judgeMustNot fails the test if the gate asks it anything.
type judgeMustNot struct{ t *testing.T }

func (j judgeMustNot) Judge(context.Context, string, judge.Ask) (judge.Judgement, error) {
	j.t.Error("the judge was asked")
	return judge.Judgement{}, errors.New("must not be called")
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
	if !strings.Contains(line, "decide_checks") || !strings.Contains(line, `"verify" reported pass: is the pass backed`) ||
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
	// Decided: a second verdict on the turn is refused.
	if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: turn}); !delivery.IsRefusal(err) {
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

// A delivery no conductor decides keeps today's untiered path: the judge
// is not asked, a failure is rejected for a person, passes skip.
func TestPolicyDeliveryChecksAsUntiered(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		pass        bool
	}{{"failure", failingTsc, false}, {"passes", passingChecks, true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			delivering(w, false)
			w.s.JudgeUnconducted = false
			w.s.Judge = judgeMustNot{t}
			checksReply(w, tc.reply)
			endTurn(w)
			if pass, acted := w.gate(); pass != tc.pass || !acted {
				t.Fatalf("pass %v acted %v", pass, acted)
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
	} {
		t.Run(name, func(t *testing.T) {
			in := ok()
			tc.mutate(&in)
			if _, err := w.decide(tc.caller, in); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
	if _, err := w.decide(cond, ok()); err != nil {
		t.Fatal(err)
	}
	if pass, _ := w.gate(); pass {
		t.Fatal("a rejected failure passed")
	}
}
