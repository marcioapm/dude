package phases

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

const failingTest = "checks:\n  - command: make test\n    result: fail\n    evidence: TestClock needs a hardware clock\n"

// waive records a person's waiver on the harness's Run, of scope, covering
// covers, then ends the turn the next report is judged at.
func waive(w *harnessWorld, r phaseRun, scope string, covers ...delivery.Check) {
	w.t.Helper()
	waiveNoted(w, r, "Needs a hardware clock CI lacks.", scope, covers...)
}

func waiveNoted(w *harnessWorld, r phaseRun, note, scope string, covers ...delivery.Check) {
	w.t.Helper()
	payload := map[string]any{"action": delivery.ActionWaive, "note": note}
	if scope != "" {
		payload["scope"], payload["person"], payload["turn"], payload["covers"] = scope, "per_ana", 1, covers
	}
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
		return w.s.event(context.Background(), tx, r, delivery.EvRunChecksDecided, "human", payload)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// endTurn ends the Run's current turn, as lux reports it.
func endTurn(w *harnessWorld) {
	w.t.Helper()
	w.feed(map[string]any{"type": "lux.activity", "data": map[string]any{"activity": "busy"}},
		map[string]any{"type": "lux.activity", "data": map[string]any{"activity": "idle"}})
}

var heldFailure = delivery.Check{Command: "make test", Result: delivery.CheckFail, Evidence: "TestClock needs a hardware clock"}

// Only an attempt waiver of the same failing command, decided before the
// turn ended, in the Run's own task and attempt, is a candidate the
// conductor is asked about; anything else is not, so same_as_waived is
// refused for it.
func TestOnlyAMatchingAttemptWaiverIsAskedAbout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		report  string
		setup   func(w *harnessWorld)
		waivers int
	}{
		{"a run-scoped waiver", failingTest, func(w *harnessWorld) { waive(w, w.run, "", heldFailure) }, 0},
		{"another command", strings.Replace(failingTest, "make test", "make lint", 1),
			func(w *harnessWorld) { waive(w, w.run, delivery.ScopeAttempt, heldFailure) }, 0},
		{"a not-run report of the waived command", strings.Replace(failingTest, "result: fail", "result: not-run", 1),
			func(w *harnessWorld) { waive(w, w.run, delivery.ScopeAttempt, heldFailure) }, 0},
		{"a waived not-run", failingTest, func(w *harnessWorld) {
			waive(w, w.run, delivery.ScopeAttempt, delivery.Check{Command: "make test", Result: delivery.CheckNotRun})
		}, 0},
		// Not carried in v1, whatever a recorded waiver says it covers.
		{"a not-run report of a waived not-run", strings.Replace(failingTest, "result: fail", "result: not-run", 1), func(w *harnessWorld) {
			waive(w, w.run, delivery.ScopeAttempt, delivery.Check{Command: "make test", Result: delivery.CheckNotRun})
		}, 0},
		{"one of two failures waived", failingTest + "  - command: make lint\n    result: fail\n    evidence: one issue\n",
			func(w *harnessWorld) { waive(w, w.run, delivery.ScopeAttempt, heldFailure) }, 1},
		{"another attempt's waiver", failingTest, func(w *harnessWorld) {
			waive(w, w.otherRun(t, "run_old", w.run.TaskID, 0), delivery.ScopeAttempt, heldFailure)
			mustOwner(t, w, `UPDATE runs SET attempt = 2 WHERE id = $1`, w.run.ID)
			mustOwner(t, w, `UPDATE workflow_runs SET state = jsonb_set(state, '{attempt}', '2') WHERE task_id = $1`, w.run.TaskID)
			w.run.Attempt = 2
		}, 0},
		{"another task's waiver", failingTest, func(w *harnessWorld) {
			mustOwner(t, w, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_other_'||$1, $1, $2, 2, 'T', 'G')`,
				w.run.Org, w.run.ProjectID)
			waive(w, w.otherRun(t, "run_other", "wi_other_"+w.run.Org, 1), delivery.ScopeAttempt, heldFailure)
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			startedFrom(w)
			conducted(w)
			tc.setup(w)
			checksReply(w, tc.report)
			endTurn(w)
			if pass, _ := w.gate(); pass {
				t.Fatal("passed before the conductor decided")
			}
			if n := len(w.asked().Waivers); n != tc.waivers {
				t.Fatalf("asked about %d waivers, want %d", n, tc.waivers)
			}
		})
	}
	t.Run("a waiver after the turn ended", func(t *testing.T) {
		w := newHarnessWorld(t)
		conducted(w)
		checksReply(w, failingTest)
		endTurn(w)
		waive(w, w.run, delivery.ScopeAttempt, heldFailure)
		if pass, _ := w.gate(); pass || len(w.asked().Waivers) != 0 {
			t.Fatalf("pass %v, asked about %d waivers", pass, len(w.asked().Waivers))
		}
	})
}

// A fixer's Run publishing to the pull request's branch directly is a Run
// of the same attempt: it may carry an implementer's attempt waiver.
func TestAPullRequestFixOfTheAttemptCarriesTheWaiver(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	waive(w, w.otherRun(t, "run_impl", w.run.TaskID, 1), delivery.ScopeAttempt, heldFailure)
	mustOwner(t, w, `UPDATE runs SET phase = 'fix', branch = NULL WHERE id = $1`, w.run.ID)
	w.run.Phase = "fix"
	checksReply(w, failingTest)
	endTurn(w)
	w.gate()
	waiver := w.asked().Waivers[0]
	if waiver == nil || waiver.RunID != "run_impl_"+w.run.Org {
		t.Fatalf("asked about waiver %+v", waiver)
	}
	if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
		Checks:  []delivery.ConductedCheck{{Command: "make test", Verdict: delivery.VerdictSameAsWaived, WaiverCursor: waiver.Cursor, Reason: "the same TestClock failure"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
		t.Fatal(err)
	}
	if pass, _ := w.gate(); !pass {
		t.Fatal("the carried waiver did not pass")
	}
	if got := recordedJudgement(w); !got.Verified || !got.Carried || got.Verdicts[0].Waiver.RunID != "run_impl_"+w.run.Org {
		t.Fatalf("%+v", got)
	}
}

// Each reported failure is asked about once, against the newest waiver
// covering its command, whichever waiver that is.
func TestEachFailureIsAskedAboutTheNewestWaiverOfItsCommand(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	lint := delivery.Check{Command: "make lint", Result: delivery.CheckFail, Evidence: "one issue"}
	waiveNoted(w, w.run, "Old clock waiver.", delivery.ScopeAttempt, heldFailure)
	waiveNoted(w, w.run, "Lint waiver.", delivery.ScopeAttempt, lint)
	waiveNoted(w, w.run, "New clock waiver.", delivery.ScopeAttempt, heldFailure)
	checksReply(w, failingTest+"  - command: make lint\n    result: fail\n    evidence: one issue\n")
	endTurn(w)
	w.gate()
	a := w.asked()
	if len(a.Waivers) != 2 || a.Waivers[0] == nil || a.Waivers[1] == nil {
		t.Fatalf("asked about %+v", a.Waivers)
	}
	if x, y := a.Waivers[0].Note, a.Waivers[1].Note; x != "New clock waiver." || y != "Lint waiver." {
		t.Fatalf("asked about %q and %q", x, y)
	}
}

// otherRun adds a running implementer Run of the harness's project, task
// and attempt, named prefix_<org>.
func (w *harnessWorld) otherRun(t *testing.T, prefix, taskID string, attempt int) phaseRun {
	t.Helper()
	id := prefix + "_" + w.run.Org
	if attempt == 0 {
		attempt = 1
	}
	mustOwner(t, w, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status)
		VALUES ($1, $2, $3, $4, $5, 'implement', 'running')`, id, w.run.Org, w.run.ProjectID, taskID, attempt)
	r := w.run
	r.ID, r.TaskID = id, taskID
	return r
}

func mustOwner(t *testing.T, w *harnessWorld, sql string, args ...any) {
	t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Beside a failure carried on a person's waiver, the conductor still
// decides the rest: a reported pass passes only backed, and a missing
// check it names holds the report, carried failure and all.
func TestBesideACarriedFailureThePassesAreBackedAndMissingIsAsked(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pass, missing string
		verified      bool
	}{
		{"backed, nothing missing", delivery.VerdictBacked, delivery.MissingNone, true},
		{"an unbacked pass", delivery.VerdictUnbacked, delivery.MissingNone, false},
		{"a missing check", delivery.VerdictBacked, delivery.MissingFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			cond := conducted(w)
			waive(w, w.run, delivery.ScopeAttempt, heldFailure)
			checksReply(w, failingTest+reportedPass)
			endTurn(w)
			if pass, _ := w.gate(); pass {
				t.Fatal("passed before the conductor decided")
			}
			a := w.asked()
			if a.Waivers[0] == nil || a.Waivers[1] != nil {
				t.Fatalf("asked about %+v, want the failure's waiver alone", a.Waivers)
			}
			if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
				Checks: []delivery.ConductedCheck{
					{Command: "make test", Verdict: delivery.VerdictSameAsWaived, WaiverCursor: a.Waivers[0].Cursor, Reason: "the same TestClock failure"},
					{Command: "make lint", Verdict: tc.pass, Reason: "the recorded call"}},
				Missing: delivery.ConductedMissing{Verdict: tc.missing, Reason: "no test of the clock fallback"}}); err != nil {
				t.Fatal(err)
			}
			if pass, acted := w.gate(); pass != tc.verified || !acted {
				t.Fatalf("pass %v acted %v, want %v", pass, acted, tc.verified)
			}
			got := recordedJudgement(w)
			// The failure reads as waived either way; the decision says it
			// carried a waiver only when it passed.
			if got.Verified != tc.verified || got.Carried != tc.verified || got.Verdicts[0].Result != delivery.CheckWaived {
				t.Fatalf("%+v", got)
			}
			if want := map[bool]string{true: delivery.CheckPass, false: "unverified"}[tc.pass == delivery.VerdictBacked]; got.Verdicts[1].Result != want {
				t.Errorf("the pass recorded %q, want %q", got.Verdicts[1].Result, want)
			}
			if tc.missing == delivery.MissingFound {
				last := got.Verdicts[len(got.Verdicts)-1]
				if last.Command != delivery.MissingChecks || last.Result != "unverified" {
					t.Errorf("the missing check recorded %+v", last)
				}
			}
		})
	}
}
