package phases

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
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
	tscFailure   = "src/legacy/a.ts(3,1): error TS2304\nFound 95 errors."
	failingTsc   = "checks:\n  - command: npx tsc --noEmit\n    result: fail\n    evidence: 95 errors on the parent, 95 with the change, none in changed files\n"
	notRunInUI   = "checks:\n  - command: open the settings page in a browser\n    result: not-run\n    evidence: no browser in this sandbox\n"
	reportedPass = "  - command: make lint\n    result: pass\n    evidence: clean\n"
)

// taskHistory is the harness's task repository "target" on a fake GitHub:
// main at Main, the task's earlier work at Earlier on top of it, and the
// Run started from Earlier, as a fixer after the implementer starts.
type taskHistory struct {
	Main, Earlier string
	acc           *accountingRun
}

// startedFrom gives the harness's Run a task repository whose target
// branch the task builds on is at taskHistory.Main, and starts the Run
// from the task's own earlier commit on top of it.
func startedFrom(w *harnessWorld) *taskHistory {
	w.t.Helper()
	a := newAccountingRun(w.t)
	h := &taskHistory{Main: a.start, acc: a}
	h.Earlier = a.commit(map[string]string{"earlier.txt": "the implementer's work\n"}, "Earlier work")
	git(w.t, a.work, "push", "-q", a.origin, "HEAD:refs/heads/"+delivery.BranchFor(w.run.TaskID, 1))
	gh := a.github()
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return gh, nil })
	w.run.Attempt = 1
	mustOwner(w.t, w, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		SELECT 'repo_' || organization_id, organization_id, project_id, 'target', 'https://github.com/acme/target.git', 'main' FROM runs WHERE id = $1`, w.run.ID)
	mustOwner(w.t, w, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		SELECT organization_id, task_id, 'repo_' || organization_id, 'write' FROM runs WHERE id = $1`, w.run.ID)
	mustOwner(w.t, w, `UPDATE runs SET base_refs = jsonb_build_object('target', $2::text), base_shas = jsonb_build_object('target', $2::text) WHERE id = $1`,
		w.run.ID, h.Earlier)
	return h
}

// mainMerged is a merge of the target branch, moved on by another task's
// work, into the task's branch: the Run then starts from that merge.
func (h *taskHistory) mainMerged(w *harnessWorld) {
	w.t.Helper()
	h.Main = h.acc.advanceBase(map[string]string{"other.txt": "another task's work\n"})
	h.acc.syncBase()
	git(w.t, h.acc.work, "merge", "-q", "--no-edit", "refs/remotes/lux/main")
	merged := h.acc.head()
	git(w.t, h.acc.work, "push", "-q", h.acc.origin, "HEAD:refs/heads/"+delivery.BranchFor(w.run.TaskID, 1))
	mustOwner(w.t, w, `UPDATE runs SET base_refs = jsonb_build_object('target', $2::text), base_shas = jsonb_build_object('target', $2::text) WHERE id = $1`,
		w.run.ID, merged)
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

// A reported failure the judge finds failing the same way at the target
// branch's commit the task builds on passes as pre-existing, its judgement
// named; the rest of the report is judged as ever.
func TestAPreexistingFailurePassesOnTheJudgesYes(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	bashCall(w, "base", "git worktree add ../base "+h.Main+" && cd ../base && npx tsc --noEmit", tscFailure, 2)
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

// The judge reads the target branch's commit the task builds on: never
// the commit the Run started from, which holds the task's earlier work;
// after a merge of the target branch, the merged commit.
func TestTheChecksContextNamesTheTaskBase(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	checksReply(w, failingTsc)
	endTurn(w)
	fake := &waiverFake{}
	w.s.Judge = fake
	if _, err := w.s.judgeChecks(context.Background(), w.run); err != nil {
		t.Fatal(err)
	}
	want := "### The commit of each target branch this task builds on\n- target: main at " + h.Main + "\n\n### The project's notes"
	if len(fake.asked) != 1 || !strings.Contains(fake.asked[0].Context, want) || strings.Contains(fake.asked[0].Context, h.Earlier) {
		t.Fatalf("the context lacks %q or names the Run's start %s:\n%s", want, h.Earlier, clip(fake.asked[0].Context))
	}
	h.mainMerged(w)
	fake.asked = nil
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	if want := "- target: main at " + h.Main + "\n"; len(fake.asked) != 1 || !strings.Contains(fake.asked[0].Context, want) {
		t.Fatalf("after the merge the context lacks %q:\n%s", want, clip(fake.asked[0].Context))
	}
	if msg := got.sendBack(); !strings.Contains(msg, "the commit of the target branch this task builds on (target: main at "+h.Main+")") {
		t.Fatalf("the send-back names no task base:\n%s", msg)
	}
}

// taskBaseJudge answers the pre-existing question as a judge reading the
// context would: yes only when a recorded call runs the command at the
// commit the context names as the task's base, failing the same way.
type taskBaseJudge struct{ asked int }

func (j *taskBaseJudge) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	j.asked++
	if !isPreexistingQuestion(a) {
		return judge.Judgement{Decision: map[bool]string{true: judge.Yes, false: judge.No}[strings.Contains(a.Question, "reported pass")], CallID: "call_test"}, nil
	}
	_, listed, _ := strings.Cut(a.Context, "### The commit of each target branch this task builds on\n- target: main at ")
	base, _, _ := strings.Cut(listed, "\n")
	_, calls, _ := strings.Cut(a.Context, "### The tool calls this Run recorded, in order")
	if base != "" && strings.Contains(calls, "worktree add ../base "+base) {
		return judge.Judgement{Decision: judge.Yes, CallID: "call_test", Reasoning: "fails the same at the task's base"}, nil
	}
	return judge.Judgement{Decision: judge.No, CallID: "call_test", Reasoning: "no run at the task's base"}, nil
}

// Two writers of one attempt: the implementer's commit brought a failure,
// and the fixer after it shows that failure at the commit it started from.
// That commit is the implementer's, so the failure is the task's own and is
// sent back; shown at the target branch's commit the task builds on, where
// it fails the same, it passes.
func TestAFailureShownOnlyAtTheRunsStartIsTheTasksOwn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     func(*taskHistory) string
		passes bool
	}{
		{"at the fixer's own start", func(h *taskHistory) string { return h.Earlier }, false},
		{"at the task's base", func(h *taskHistory) string { return h.Main }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			w.run.Phase = "fix"
			h := startedFrom(w)
			bashCall(w, "base", "git worktree add ../base "+tc.at(h)+" && cd ../base && npx tsc --noEmit", tscFailure, 2)
			bashCall(w, "head", "npx tsc --noEmit", tscFailure, 2)
			checksReply(w, failingTsc)
			endTurn(w)
			w.s.Judge = &taskBaseJudge{}
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified != tc.passes || got.JudgePassed != tc.passes {
				t.Fatalf("%+v %v", got, err)
			}
			if !tc.passes && (got.Verdicts[0].Result != delivery.CheckFail || !strings.Contains(got.Verdicts[0].JudgeReason, "no run at the task's base")) {
				t.Fatalf("verdicts %+v", got.Verdicts)
			}
		})
	}
}

// With no target-branch commit known for the task, a reported failure is
// sent back without asking the judge.
func TestAFailureWithNoTaskBaseIsRejectedUnasked(t *testing.T) {
	w := newHarnessWorld(t)
	checksReply(w, failingTsc)
	endTurn(w)
	fake := &waiverFake{preexisting: judge.Yes}
	w.s.Judge = fake
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil || got.Verified || got.JudgePassed || len(fake.asked) != 0 {
		t.Fatalf("%+v %v, asked %d", got, err, len(fake.asked))
	}
	if v := got.Verdicts; len(v) != 1 || v[0].Result != delivery.CheckFail || !strings.Contains(v[0].JudgeReason, "no recorded commit of the target branch") {
		t.Fatalf("verdicts %+v", v)
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
			h := startedFrom(w)
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
				"this task builds on (target: main at " + h.Main + ")", "in a separate checkout of that commit"} {
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
			startedFrom(w)
			checksReply(w, failingTsc)
			endTurn(w)
			fake := &preexistingFake{err: off}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || fake.calls != 1 || got.Verified || got.Skipped || got.JudgePassed || len(got.Verdicts) != 1 || got.Verdicts[0].Result != delivery.CheckFail {
				t.Fatalf("%+v %v, asked %d", got, err, fake.calls)
			}
		})
	}
	t.Run("through the judge service", func(t *testing.T) {
		w := newHarnessWorld(t)
		startedFrom(w)
		checksReply(w, failingTsc)
		endTurn(w)
		w.s.Judge = &judge.Service{DB: w.s.DB}
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || got.Verified || got.Skipped {
			t.Fatalf("%+v %v", got, err)
		}
	})
	// The tier is gone for the pre-existing question alone: Backed and
	// Missing would pass the rest of the report.
	t.Run("on the pre-existing question alone", func(t *testing.T) {
		w := newHarnessWorld(t)
		startedFrom(w)
		checksReply(w, failingTsc+reportedPass)
		endTurn(w)
		fake := &waiverFake{offPreexisting: true}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || got.Verified || got.Skipped || got.JudgePassed || len(got.Verdicts) == 0 || got.Verdicts[0].Result != delivery.CheckFail {
			t.Fatalf("%+v %v", got, err)
		}
	})
	// The judge passed the failure, then the tier is gone for Backed and
	// Missing: the judge-passed failure is not let through by the skip.
	t.Run("after the judge passed the failure", func(t *testing.T) {
		w := newHarnessWorld(t)
		startedFrom(w)
		checksReply(w, failingTsc+reportedPass)
		endTurn(w)
		fake := &waiverFake{preexisting: judge.Yes, offAfterPreexisting: true}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || got.Verified || got.Skipped || got.JudgePassed || fake.preexistingQuestions() != 1 || len(fake.asked) != 2 {
			t.Fatalf("%+v %v, asked %d", got, err, len(fake.asked))
		}
	})
	t.Run("a not-run check", func(t *testing.T) {
		w := newHarnessWorld(t)
		delivering(w, true)
		checksReply(w, notRunInUI)
		endTurn(w)
		fake := &preexistingFake{err: judge.ErrOff}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || fake.calls != 1 || got.Verified || got.Skipped || got.JudgePassed || len(got.Verdicts) != 1 || got.Verdicts[0].Result != delivery.CheckNotRun {
			t.Fatalf("%+v %v, asked %d", got, err, fake.calls)
		}
	})
}

// A failure of a waived command that the judge finds is not the waived
// failure is still asked whether it predates the task: shown at the task's
// base it passes as pre-existing, otherwise it is rejected with both reasons.
func TestAFailureTheWaiverDoesNotCoverMayStillBePreexisting(t *testing.T) {
	for _, shown := range []bool{true, false} {
		t.Run(map[bool]string{true: "shown at the task base", false: "not shown"}[shown], func(t *testing.T) {
			w := newHarnessWorld(t)
			startedFrom(w)
			waive(w, w.run, delivery.ScopeAttempt, heldFailure)
			checksReply(w, failingTest)
			endTurn(w)
			fake := &waiverFake{waived: judge.No, preexisting: map[bool]string{true: judge.Yes, false: judge.No}[shown]}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified != shown || got.JudgePassed != shown || got.Carried {
				t.Fatalf("%+v %v", got, err)
			}
			if fake.waiverQuestions() != 1 || fake.preexistingQuestions() != 1 || !isPreexistingQuestion(fake.asked[1]) {
				t.Fatalf("asked %d, %d about the waiver", len(fake.asked), fake.waiverQuestions())
			}
			v := got.Verdicts[0]
			if shown && (v.Result != delivery.CheckPreexisting || v.Waiver != nil) {
				t.Fatalf("verdict %+v", v)
			}
			if !shown && (v.Result != delivery.CheckFail || !strings.Contains(v.WaiverReason, "does not cover") || !strings.Contains(v.JudgeReason, "not passed by the judge")) {
				t.Fatalf("verdict %+v", v)
			}
		})
	}
}

// With no judgement tier the rejection says only a person can pass the
// failure, and the send-back does not say how the judge would pass it;
// a tier that answered no keeps that paragraph.
func TestWithNoTierTheSendBackSaysOnlyAPersonCanPassAFailure(t *testing.T) {
	for _, off := range []bool{true, false} {
		t.Run(map[bool]string{true: "no tier", false: "the judge said no"}[off], func(t *testing.T) {
			w := newHarnessWorld(t)
			startedFrom(w)
			checksReply(w, failingTsc)
			endTurn(w)
			fake := &preexistingFake{answer: judge.No}
			if off {
				fake.err = judge.ErrOff
			}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified {
				t.Fatalf("%+v %v", got, err)
			}
			msg := got.sendBack()
			hint := strings.Contains(msg, "passes once this Run's own tool calls show")
			person := strings.Contains(msg, "no judgement tier: only a person can pass a reported failure")
			if hint == off || person != off {
				t.Fatalf("hint %v, only a person %v:\n%s", hint, person, msg)
			}
		})
	}
}

// The first failure the judge does not pass rejects the report there, and
// the send-back names it and every failure not yet asked about.
func TestTheSendBackNamesTheFailuresAfterTheFirstRejected(t *testing.T) {
	w := newHarnessWorld(t)
	startedFrom(w)
	checksReply(w, failingTsc+strings.TrimPrefix(failingTest, "checks:\n"))
	endTurn(w)
	fake := &preexistingFake{answer: judge.No}
	w.s.Judge = fake
	got, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil || got.Verified || fake.calls != 1 || len(got.Verdicts) != 2 || got.Verdicts[1].Command != "make test" {
		t.Fatalf("%+v %v, asked %d", got, err, fake.calls)
	}
	if msg := got.sendBack(); !strings.Contains(msg, "- make test: TestClock needs a hardware clock\n") {
		t.Fatalf("the send-back lacks the second failure:\n%s", msg)
	}
}

// The task base comes from the commit the checkout really started at
// (runs.base_shas), never the one it was asked to start from; a repository
// with no recorded start, or one the task only reads, has none; several
// are listed by name.
func TestTheTaskBaseIsMeasuredFromTheRecordedStart(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	// web: another repository row over the same fake GitHub repository,
	// started from the task's earlier work; docs: asked for, never started;
	// spec: read only.
	for _, repo := range []struct{ name, access string }{{"web", "write"}, {"docs", "write"}, {"spec", "read"}} {
		mustOwner(t, w, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			SELECT 'repo_'||$2||'_'||organization_id, organization_id, project_id, $2, 'https://github.com/acme/target.git', 'main' FROM runs WHERE id = $1`, w.run.ID, repo.name)
		mustOwner(t, w, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
			SELECT organization_id, task_id, 'repo_'||$2||'_'||organization_id, $3::text::repository_access FROM runs WHERE id = $1`, w.run.ID, repo.name, repo.access)
	}
	mustOwner(t, w, `UPDATE runs SET base_refs = jsonb_build_object('target', 'main', 'web', $2::text, 'docs', $2::text),
		base_shas = jsonb_build_object('web', $2::text, 'target', $2::text, 'spec', $2::text) WHERE id = $1`, w.run.ID, h.Earlier)
	got, err := w.s.taskBases(context.Background(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	if want := "target: main at " + h.Main + ", web: main at " + h.Main; joinBases(got) != want {
		t.Fatalf("bases %q, want %q", joinBases(got), want)
	}
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
	startedFrom(w)
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
