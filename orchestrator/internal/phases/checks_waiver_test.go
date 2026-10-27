package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// waiverFake answers the waiver question with waived (or err), the
// pre-existing question with preexisting ("" is no), Backed with yes and
// Missing with no, counting the questions it was asked.
type waiverFake struct {
	waived     string
	err        error
	unrecorded bool
	// missing answers Missing; "" is no.
	missing     string
	preexisting string
	// offAfterWaiver: every question but the waiver's finds no tier.
	offAfterWaiver bool
	// offPreexisting: the pre-existing question finds no tier;
	// offAfterPreexisting: every question after it does.
	offPreexisting, offAfterPreexisting bool
	asked                               []judge.Ask
}

func (f *waiverFake) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	f.asked = append(f.asked, a)
	id := "call_test"
	if f.unrecorded {
		id = ""
	}
	waiverQ := strings.Contains(a.Question, "waived a reported failure")
	switch {
	case waiverQ && f.err != nil:
		return judge.Judgement{Decision: judge.Yes, CallID: id}, f.err
	case waiverQ:
		return judge.Judgement{Decision: f.waived, CallID: id, Reasoning: "the same TestClock failure"}, nil
	case f.offAfterWaiver:
		return judge.Judgement{}, judge.ErrOff
	case isPreexistingQuestion(a) && f.offPreexisting:
		return judge.Judgement{}, judge.ErrOff
	case isPreexistingQuestion(a):
		return judge.Judgement{Decision: cmp.Or(f.preexisting, judge.No), CallID: id, Reasoning: "no run at the starting commit"}, nil
	case f.offAfterPreexisting:
		return judge.Judgement{}, judge.ErrOff
	case strings.Contains(a.Question, "reported pass"):
		return judge.Judgement{Decision: judge.Yes, CallID: id, Reasoning: "backed"}, nil
	case f.missing != "":
		return judge.Judgement{Decision: f.missing, CallID: id, Reasoning: "a check is missing"}, nil
	}
	return judge.Judgement{Decision: judge.No, CallID: id, Reasoning: "nothing missing"}, nil
}

func (f *waiverFake) waiverQuestions() int {
	n := 0
	for _, a := range f.asked {
		if strings.Contains(a.Question, "waived a reported failure") {
			n++
		}
	}
	return n
}

func isPreexistingQuestion(a judge.Ask) bool {
	return strings.HasPrefix(a.Question, "This Run reported a check that did not pass")
}

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

// A later turn of the waived Run reporting the waived failure again carries
// the waiver once the judge says it is the same failure; anything less from
// the judge, or no tier, leaves it rejected and never skipped.
func TestAWaivedFailurePassesOnlyOnTheJudgesYes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		waived     string
		err        error
		unrecorded bool
		verified   bool
	}{
		{"yes", judge.Yes, nil, false, true},
		{"no", judge.No, nil, false, false},
		{"unsure", judge.Unsure, nil, false, false},
		{"judge error", "", errors.New("proxy timed out"), false, false},
		{"recorded nowhere", judge.Yes, nil, true, false},
		{"no tier", "", judge.ErrOff, false, false},
		{"no model", "", judge.ErrNoModel, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			waive(w, w.run, delivery.ScopeAttempt, heldFailure)
			// The command is compared trimmed.
			checksReply(w, strings.Replace(failingTest, "make test", `"  make test "`, 1))
			endTurn(w)
			fake := &waiverFake{waived: tc.waived, err: tc.err, unrecorded: tc.unrecorded}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified != tc.verified || got.Skipped || fake.waiverQuestions() != 1 {
				t.Fatalf("%+v %v, %d waiver questions", got, err, fake.waiverQuestions())
			}
			if !tc.verified {
				// The agent's reported failure stays as it reported it, so
				// a person may still waive it; the judge's no is beside it.
				v := got.Verdicts
				if got.Carried || len(v) != 1 || v[0].Result != delivery.CheckFail || v[0].Reason != "TestClock needs a hardware clock" || v[0].Waiver != nil {
					t.Fatalf("a failure not carried was recorded %+v", got)
				}
				if asked := tc.err == nil || !(errors.Is(tc.err, judge.ErrOff) || errors.Is(tc.err, judge.ErrNoModel)); asked != (v[0].WaiverReason != "") {
					t.Fatalf("the waiver's reason is %q", v[0].WaiverReason)
				}
				return
			}
			// Carried with the waiver it rests on, and the Missing question
			// still asked over the whole report.
			v := got.Verdicts[0]
			if !got.Carried || len(got.Verdicts) != 2 || v.Result != checksWaived || v.CallID != "call_test" || v.Waiver == nil ||
				v.Waiver.RunID != w.run.ID || v.Waiver.Note != "Needs a hardware clock CI lacks." || v.Waiver.Person != "per_ana" ||
				got.Verdicts[1].Command != "missing checks" || got.Verdicts[1].Result != "pass" {
				t.Fatalf("carried %+v", got)
			}
		})
	}
}

// A carried failure changes nothing for the rest of the report: each pass
// is still asked Backed, and a missing check still rejects the turn.
func TestBesideACarriedFailureThePassesAreBackedAndMissingIsAsked(t *testing.T) {
	report := failingTest + "  - command: make lint\n    result: pass\n    evidence: clean\n"
	for _, tc := range []struct {
		missing  string
		verified bool
	}{{"", true}, {judge.Yes, false}} {
		t.Run("missing "+tc.missing, func(t *testing.T) {
			w := newHarnessWorld(t)
			waive(w, w.run, delivery.ScopeAttempt, heldFailure)
			checksReply(w, report)
			endTurn(w)
			fake := &waiverFake{waived: judge.Yes, missing: tc.missing}
			w.s.Judge = fake
			pass, _, err := w.s.checksBeforePush(context.Background(), w.run)
			var got checksDecision
			if err == nil {
				err = w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
					var raw []byte
					if err := tx.QueryRow(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2`,
						w.run.ID, delivery.EvRunChecksJudged).Scan(&raw); err != nil {
						return err
					}
					return json.Unmarshal(raw, &got)
				})
			}
			if err != nil || pass != tc.verified || got.Verified != tc.verified || len(fake.asked) != 3 || len(got.Verdicts) != 3 ||
				got.Verdicts[0].Result != checksWaived || got.Verdicts[1].Command != "make lint" || got.Verdicts[1].Result != "pass" {
				t.Fatalf("%+v %v, asked %d", got, err, len(fake.asked))
			}
			// The send-back names the missing check alone, never the
			// carried failure or the backed pass.
			if !tc.verified {
				if want := (checksDecision{Verdicts: got.Verdicts[2:]}).sendBack(); got.Verdicts[2].Command != "missing checks" || got.Message != want {
					t.Fatalf("the send-back is\n%s\nwant\n%s", got.Message, want)
				}
			}
		})
	}
}

// A tier gone between the waiver's question and the rest of the report's
// leaves the carried turn rejected, never skipped.
func TestNoTierAfterACarryIsARejectionNotASkip(t *testing.T) {
	w := newHarnessWorld(t)
	waive(w, w.run, delivery.ScopeAttempt, heldFailure)
	checksReply(w, failingTest)
	endTurn(w)
	fake := &waiverFake{waived: judge.Yes, offAfterWaiver: true}
	w.s.Judge = fake
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil || got.Verified || got.Skipped || got.Carried || fake.waiverQuestions() != 1 || len(fake.asked) != 2 {
		t.Fatalf("%+v %v, asked %d", got, err, len(fake.asked))
	}
}

// The judge reads both Runs' evidence: the waived report's failure and the
// person's note beside this Run's own calls.
func TestTheWaiverQuestionShowsTheWaivedFailure(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1",
		"title": "bash", "kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "make test"}}},
		map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1",
			"status": "failed", "rawOutput": map[string]any{"output": "FAIL: TestClock", "metadata": map[string]any{"exit": 1}}}})
	waive(w, w.run, delivery.ScopeAttempt, heldFailure)
	// A call after the waiver is this Run's, never the waived failure's.
	w.feed(map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2",
		"title": "bash", "kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "make test -run TestGreeting"}}},
		map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2",
			"status": "completed", "rawOutput": map[string]any{"output": "ok", "metadata": map[string]any{"exit": 0}}}})
	checksReply(w, failingTest)
	endTurn(w)
	fake := &waiverFake{waived: judge.Yes}
	w.s.Judge = fake
	if _, err := w.s.judgeChecks(context.Background(), w.run); err != nil {
		t.Fatal(err)
	}
	_, waived, ok := strings.Cut(fake.asked[0].Context, "### The waived failure\n")
	if !ok {
		t.Fatalf("no waived failure in the context:\n%s", clip(fake.asked[0].Context))
	}
	want := "A person waived the Implementer's report at its Run " + w.run.ID + ", saying:\n" +
		"    Needs a hardware clock CI lacks.\n" +
		"That report said \"make test\" failed:\n" +
		"    TestClock needs a hardware clock\n\n" +
		"### The tool calls the Implementer's Run recorded up to the waiver, in order\n\n" +
		"- bash {\"command\":\"make test\"}\n  status: error, exit code 1\n  output:\n    FAIL: TestClock"
	if waived != want {
		t.Fatalf("the waived failure reads\n%s\nwant\n%s", waived, want)
	}
}

// Only an attempt waiver of the same failing command, decided before the
// turn ended, in the Run's own task and attempt, is a candidate; anything
// else is asked only the pre-existing question, whose no rejects it.
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
			tc.setup(w)
			checksReply(w, tc.report)
			endTurn(w)
			fake := &waiverFake{waived: judge.Yes}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified || got.Carried || fake.waiverQuestions() != tc.waivers || len(fake.asked) != tc.waivers+1 ||
				!isPreexistingQuestion(fake.asked[len(fake.asked)-1]) {
				t.Fatalf("%+v %v, asked %d", got, err, len(fake.asked))
			}
		})
	}
	t.Run("a waiver after the turn ended", func(t *testing.T) {
		w := newHarnessWorld(t)
		checksReply(w, failingTest)
		endTurn(w)
		waive(w, w.run, delivery.ScopeAttempt, heldFailure)
		fake := &waiverFake{waived: judge.Yes}
		w.s.Judge = fake
		if got, err := w.s.judgeChecks(context.Background(), w.run); err != nil || got.Verified || fake.waiverQuestions() != 0 {
			t.Fatalf("%+v %v, asked %d", got, err, len(fake.asked))
		}
	})
}

// A fixer's Run on the pull request's candidate branch is a Run of the same
// attempt: it may carry an implementer's attempt waiver.
func TestACandidateFixOfTheAttemptCarriesTheWaiver(t *testing.T) {
	w := newHarnessWorld(t)
	waive(w, w.otherRun(t, "run_impl", w.run.TaskID, 1), delivery.ScopeAttempt, heldFailure)
	mustOwner(t, w, `UPDATE runs SET phase = 'fix', branch = $2 WHERE id = $1`, w.run.ID, delivery.CandidateBranch(w.run.TaskID, 1))
	w.run.Phase = "fix"
	checksReply(w, failingTest)
	endTurn(w)
	fake := &waiverFake{waived: judge.Yes}
	w.s.Judge = fake
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil || !got.Verified || !got.Carried || got.Verdicts[0].Waiver.RunID != "run_impl_"+w.run.Org {
		t.Fatalf("%+v %v", got, err)
	}
}

// Each reported failure is asked about once, against the newest waiver
// covering its command, whichever waiver that is.
func TestEachFailureIsAskedAboutTheNewestWaiverOfItsCommand(t *testing.T) {
	w := newHarnessWorld(t)
	lint := delivery.Check{Command: "make lint", Result: delivery.CheckFail, Evidence: "one issue"}
	waiveNoted(w, w.run, "Old clock waiver.", delivery.ScopeAttempt, heldFailure)
	waiveNoted(w, w.run, "Lint waiver.", delivery.ScopeAttempt, lint)
	waiveNoted(w, w.run, "New clock waiver.", delivery.ScopeAttempt, heldFailure)
	checksReply(w, failingTest+"  - command: make lint\n    result: fail\n    evidence: one issue\n")
	endTurn(w)
	fake := &waiverFake{waived: judge.Yes}
	w.s.Judge = fake
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil || !got.Verified || !got.Carried || fake.waiverQuestions() != 2 || len(got.Verdicts) != 3 {
		t.Fatalf("%+v %v, %d waiver questions", got, err, fake.waiverQuestions())
	}
	if a, b := got.Verdicts[0].Waiver.Note, got.Verdicts[1].Waiver.Note; a != "New clock waiver." || b != "Lint waiver." {
		t.Fatalf("carried %q and %q", a, b)
	}
}

// A restart while the judge weighs the waiver records nothing; the next
// process asks again, and one after that reads the carried verdict.
func TestARestartJudgesAWaivedFailureAgain(t *testing.T) {
	w := newHarnessWorld(t)
	waive(w, w.run, delivery.ScopeAttempt, heldFailure)
	checksReply(w, failingTest)
	endTurn(w)
	ctx, stop := context.WithCancel(context.Background())
	cut := &interruptingJudge{stop: stop}
	w.s.Judge = cut
	if ok, _, err := w.s.checksBeforePush(ctx, w.run); ok || err == nil || cut.calls != 1 {
		t.Fatalf("an interrupted judgement: pass %v, %v", ok, err)
	}
	if n := w.countEvents(delivery.EvRunChecksJudged); n != 0 {
		t.Fatalf("%d verdicts recorded for an interrupted judgement", n)
	}
	again := &Syncer{DB: w.s.DB, Judge: &waiverFake{waived: judge.Yes}}
	if ok, _, err := again.checksBeforePush(context.Background(), w.run); !ok || err != nil {
		t.Fatalf("judged again: pass %v, %v", ok, err)
	}
	after := &Syncer{DB: w.s.DB, Judge: &checksFake{err: errors.New("must not be called")}}
	if ok, _, err := after.checksBeforePush(context.Background(), w.run); !ok || err != nil {
		t.Fatalf("after the carried verdict: pass %v, %v", ok, err)
	}
	var carried bool
	if err := w.owner.QueryRow(context.Background(), `SELECT (payload->>'carried')::bool AND payload->'checks'->0->'waiver'->>'runId' = $1
		AND NOT payload ? 'waived' FROM events WHERE run_id = $1 AND event_type = $2`, w.run.ID, delivery.EvRunChecksJudged).Scan(&carried); err != nil || !carried {
		t.Fatalf("the recorded judgement is not carried: %v", err)
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
