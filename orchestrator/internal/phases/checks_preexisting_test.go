package phases

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// bashCall records one finished shell call of the harness's Run.
func bashCall(w *harnessWorld, id, command, output string, exit int) {
	w.t.Helper()
	status := "completed"
	if exit != 0 {
		status = "failed"
	}
	w.feed(map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id,
		"title": "bash", "kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": command}}},
		map[string]any{"type": "acp.tool_call_update", "data": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id,
			"status": status, "rawOutput": map[string]any{"output": output, "metadata": map[string]any{"exit": exit}}}})
}

const (
	baseSHA      = "1111111111111111111111111111111111111111"
	tscFailure   = "src/legacy/a.ts(3,1): error TS2304\nFound 95 errors."
	failingTsc   = "checks:\n  - command: npx tsc --noEmit\n    result: fail\n    evidence: 95 errors on the parent, 95 with the change, none in changed files\n"
	notRunInUI   = "checks:\n  - command: open the settings page in a browser\n    result: not-run\n    evidence: no browser in this sandbox\n"
	reportedPass = "  - command: make lint\n    result: pass\n    evidence: clean\n"
)

// startedFrom records the commit the harness's Run started from.
func startedFrom(w *harnessWorld) {
	w.t.Helper()
	mustOwner(w.t, w, `UPDATE runs SET base_refs = jsonb_build_object('target', $2::text), base_shas = jsonb_build_object('target', $2::text) WHERE id = $1`,
		w.run.ID, baseSHA)
}

// delivering gives the harness's task a delivery whose attempt's policy
// runs a test phase, or none.
func delivering(w *harnessWorld, test bool) {
	w.t.Helper()
	p := delivery.DefaultPolicy()
	p.Test = test
	state, _ := json.Marshal(map[string]any{"taskId": w.run.TaskID, "attempt": 1, "policy": p})
	mustOwner(w.t, w, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_'||$1, $1, $2, 'task.delivery', 'k_'||$1, 'waiting', 'implement', $3::jsonb)`, w.run.Org, w.run.TaskID, state)
}

func (f *waiverFake) preexistingQuestions() int {
	n := 0
	for _, a := range f.asked {
		if isPreexistingQuestion(a) {
			n++
		}
	}
	return n
}

// A reported failure the judge finds failing the same way at the Run's
// starting commit passes as pre-existing, its judgement named; the rest of
// the report is judged as ever.
func TestAPreexistingFailurePassesOnTheJudgesYes(t *testing.T) {
	w := newHarnessWorld(t)
	startedFrom(w)
	bashCall(w, "base", "git worktree add ../parent "+baseSHA+" && cd ../parent && npx tsc --noEmit", tscFailure, 2)
	bashCall(w, "head", "npx tsc --noEmit", tscFailure, 2)
	checksReply(w, failingTsc+reportedPass)
	endTurn(w)
	fake := &waiverFake{preexisting: judge.Yes}
	w.s.Judge = fake
	pass, _, err := w.s.checksBeforePush(context.Background(), w.run)
	if err != nil || !pass {
		t.Fatalf("pass %v, %v", pass, err)
	}
	got := recordedJudgement(w)
	v := got.Verdicts
	if !got.Verified || !got.JudgePassed || got.Carried || got.Skipped || len(v) != 3 ||
		v[0].Command != "npx tsc --noEmit" || v[0].Result != delivery.CheckPreexisting || v[0].CallID != "call_test" ||
		v[0].Reason != "no run at the starting commit" || v[0].Waiver != nil ||
		v[1].Command != "make lint" || v[1].Result != delivery.CheckPass || v[2].Command != "missing checks" {
		t.Fatalf("recorded %+v", got)
	}
	if fake.preexistingQuestions() != 1 || len(fake.asked) != 3 {
		t.Fatalf("asked %d, %d of them pre-existing", len(fake.asked), fake.preexistingQuestions())
	}
	if n := w.countEvents(delivery.EvRunChecksJudged); n != 1 {
		t.Fatalf("%d judgements", n)
	}
	if n := countOwner(w, `SELECT count(*) FROM directives WHERE run_id = $1`, w.run.ID); n != 0 {
		t.Fatalf("%d directives sent for a passed report", n)
	}
}

// The judge reads where each repository started, beside the Run's calls.
func TestTheChecksContextNamesTheStartingCommits(t *testing.T) {
	w := newHarnessWorld(t)
	startedFrom(w)
	text, err := w.s.checksContext(context.Background(), w.run, []delivery.Check{{Command: "make check", Result: "fail"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "### The commit each repository this Run started from\n- target@" + baseSHA + "\n\n### The project's notes"; !strings.Contains(text, want) {
		t.Fatalf("the context lacks %q:\n%s", want, clip(text))
	}
}

// Anything but the judge's recorded yes keeps the failure rejected, with
// its reason in the send-back and how such a failure passes.
func TestAFailureTheJudgeDoesNotFindPreexistingIsSentBack(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		err          error
		unrecorded   bool
		reason       string
	}{
		{"no", judge.No, nil, false, "no run at the starting commit"},
		{"unsure", judge.Unsure, nil, false, "no run at the starting commit"},
		{"judge error", judge.Yes, errors.New("proxy timed out"), false, "proxy timed out"},
		{"recorded nowhere", judge.Yes, nil, true, "no run at the starting commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			startedFrom(w)
			checksReply(w, failingTsc+reportedPass)
			endTurn(w)
			fake := &preexistingFake{answer: tc.answer, err: tc.err, unrecorded: tc.unrecorded}
			w.s.Judge = fake
			pass, _, err := w.s.checksBeforePush(context.Background(), w.run)
			if err != nil || pass {
				t.Fatalf("pass %v, %v", pass, err)
			}
			got := recordedJudgement(w)
			// Rejected at the failure: nothing more is asked.
			if got.Verified || got.JudgePassed || fake.calls != 1 || len(got.Verdicts) != 1 ||
				got.Verdicts[0].Result != delivery.CheckFail || got.Verdicts[0].JudgeReason == "" {
				t.Fatalf("recorded %+v, asked %d", got, fake.calls)
			}
			for _, want := range []string{"- npx tsc --noEmit: 95 errors on the parent, 95 with the change, none in changed files — not passed by the judge: " + tc.reason,
				"at the commit you started from (target@" + baseSHA + ")", "A failure in code you changed"} {
				if !strings.Contains(got.Message, want) {
					t.Errorf("the send-back lacks %q:\n%s", want, got.Message)
				}
			}
		})
	}
}

// preexistingFake answers every question answer (or err).
type preexistingFake struct {
	answer     string
	err        error
	unrecorded bool
	calls      int
}

func (f *preexistingFake) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	f.calls++
	id := "call_test"
	if f.unrecorded {
		id = ""
	}
	if f.err != nil {
		return judge.Judgement{Decision: judge.Yes, CallID: id}, f.err
	}
	return judge.Judgement{Decision: f.answer, CallID: id, Reasoning: "no run at the starting commit"}, nil
}

// With no judgement tier a reported failure stays rejected: never the
// skip a tier's absence gives a report of passes.
func TestWithNoTierAReportedFailureIsRejectedNotSkipped(t *testing.T) {
	for _, off := range []error{judge.ErrOff, judge.ErrNoModel} {
		t.Run(off.Error(), func(t *testing.T) {
			w := newHarnessWorld(t)
			checksReply(w, failingTsc)
			endTurn(w)
			w.s.Judge = &preexistingFake{err: off}
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified || got.Skipped || got.JudgePassed || len(got.Verdicts) != 1 || got.Verdicts[0].Result != delivery.CheckFail {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	t.Run("through the judge service", func(t *testing.T) {
		w := newHarnessWorld(t)
		checksReply(w, failingTsc)
		endTurn(w)
		w.s.Judge = &judge.Service{DB: w.s.DB}
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || got.Verified || got.Skipped {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// A browser check not run passes as deferred when the attempt's policy
// runs a test phase after it; with none, it is rejected.
func TestANotRunBrowserCheckIsDeferredOnlyToATestPhase(t *testing.T) {
	for _, test := range []bool{true, false} {
		t.Run(map[bool]string{true: "a test phase", false: "no test phase"}[test], func(t *testing.T) {
			w := newHarnessWorld(t)
			delivering(w, test)
			checksReply(w, notRunInUI)
			endTurn(w)
			fake := &deferringFake{}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified != test || got.JudgePassed != test {
				t.Fatalf("%+v %v", got, err)
			}
			if test && (got.Verdicts[0].Result != delivery.CheckDeferred || got.Verdicts[0].CallID != "call_test") {
				t.Fatalf("recorded %+v", got.Verdicts)
			}
			if !test && (got.Verdicts[0].Result != delivery.CheckNotRun || !strings.Contains(got.Verdicts[0].JudgeReason, "no test phase")) {
				t.Fatalf("recorded %+v", got.Verdicts)
			}
		})
	}
}

// deferringFake answers the not-run question by what it says of the
// delivery's test phase, as a judge reading it would; Missing no.
type deferringFake struct{}

func (deferringFake) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	if !isPreexistingQuestion(a) {
		return judge.Judgement{Decision: judge.No, CallID: "call_test", Reasoning: "nothing missing"}, nil
	}
	if strings.Contains(a.Question, "This delivery runs a test phase after this one") {
		return judge.Judgement{Decision: judge.Yes, CallID: "call_test", Reasoning: "the tester runs it"}, nil
	}
	return judge.Judgement{Decision: judge.No, CallID: "call_test", Reasoning: "no test phase runs it"}, nil
}

// A carried waiver is weighed first, and its failure is never asked the
// pre-existing question; another failure is.
func TestACarriedWaiverIsAskedFirstAndAloneForItsFailure(t *testing.T) {
	w := newHarnessWorld(t)
	waive(w, w.run, delivery.ScopeAttempt, heldFailure)
	// The unwaived failure is listed first; the waiver is still asked first.
	checksReply(w, failingTsc+strings.TrimPrefix(failingTest, "checks:\n"))
	endTurn(w)
	fake := &waiverFake{waived: judge.Yes, preexisting: judge.Yes}
	w.s.Judge = fake
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil || !got.Verified || !got.Carried || !got.JudgePassed {
		t.Fatalf("%+v %v", got, err)
	}
	if len(fake.asked) != 3 || fake.waiverQuestions() != 1 || !strings.Contains(fake.asked[0].Question, "waived a reported failure") ||
		!isPreexistingQuestion(fake.asked[1]) || !strings.Contains(fake.asked[1].Question, "npx tsc --noEmit") {
		t.Fatalf("asked %d", len(fake.asked))
	}
	if v := got.Verdicts; v[0].Result != checksWaived || v[0].Command != "make test" || v[1].Result != delivery.CheckPreexisting {
		t.Fatalf("verdicts %+v", v)
	}
}

func recordedJudgement(w *harnessWorld) checksDecision {
	w.t.Helper()
	var got checksDecision
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor DESC LIMIT 1`,
			w.run.ID, delivery.EvRunChecksJudged).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &got)
	}); err != nil {
		w.t.Fatal(err)
	}
	return got
}

func countOwner(w *harnessWorld, sql string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}
