package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
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

// compares counts the fake GitHub's compare requests from here on, each
// answered status (0: as GitHub would).
func (h *taskHistory) compares(w *harnessWorld, status int) *int {
	w.t.Helper()
	n := new(int)
	h.acc.intercept = func(r *http.Request) int {
		if strings.Contains(r.URL.Path, "/compare/") {
			*n++
			return status
		}
		return 0
	}
	gh := h.acc.github()
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return gh, nil })
	return n
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

// The conductor is asked against the target branch's commit the task
// builds on: never the commit the Run started from, which holds the task's
// earlier work; after a merge of the target branch, the merged commit. The
// send-back of a failure it rejects names that commit.
func TestTheChecksAskNamesTheTaskBase(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	cond := conducted(w)
	checksReply(w, failingTsc)
	endTurn(w)
	w.gate()
	want := "target: main at " + h.Main
	if line := w.checksWake(); !strings.Contains(line, "The task's bases: "+want+".") || strings.Contains(line, h.Earlier) {
		t.Fatalf("the wake lacks %q or names the Run's start %s:\n%s", want, h.Earlier, line)
	}
	h.mainMerged(w)
	checksReply(w, failingTsc)
	endTurn(w)
	w.gate()
	if a := w.asked(); len(a.Bases) != 1 || a.Bases[0].SHA != h.Main {
		t.Fatalf("after the merge asked against %+v, want main at %s", a.Bases, h.Main)
	}
	if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
		Checks:  []delivery.ConductedCheck{{Command: "npx tsc --noEmit", Verdict: delivery.VerdictReject, Reason: "no run at the base"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
		t.Fatal(err)
	}
	if pass, _ := w.gate(); pass {
		t.Fatal("a rejected failure passed")
	}
	if msg := recordedJudgement(w).Message; !strings.Contains(msg, "the commit of the target branch this task builds on (target: main at "+h.Main+")") {
		t.Fatalf("the send-back names no task base:\n%s", msg)
	}
}

// asked is what the gate asked the conductor about the Run's latest turn.
func (w *harnessWorld) asked() *delivery.ChecksAsked {
	w.t.Helper()
	var a *delivery.ChecksAsked
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		a, _, err = delivery.ChecksAskedTx(context.Background(), tx, w.run.ID, w.turn())
		return err
	}); err != nil || a == nil {
		w.t.Fatalf("asked %+v, %v", a, err)
	}
	return a
}

// checksWake is the conductor's checks wake about the Run's latest turn.
func (w *harnessWorld) checksWake() string {
	w.t.Helper()
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE key = $1`, delivery.ChecksKey(w.run.ID, w.turn())).Scan(&line); err != nil {
		w.t.Fatal(err)
	}
	return line
}

// decideAsTheDefaults decides the latest turn as the defaults-taking
// scripted conductor reads its wake (fakeagent.ChecksDefaults).
func (w *harnessWorld) decideAsTheDefaults(cond string) {
	w.t.Helper()
	calls := fakeagent.ChecksDefaults(w.checksWake())
	var in delivery.DecideChecks
	if len(calls) != 1 || json.Unmarshal([]byte(calls[0][1]), &in) != nil {
		w.t.Fatalf("decided %q", calls)
	}
	if _, err := w.decide(cond, in); err != nil {
		w.t.Fatal(err)
	}
}

// Two writers of one attempt: the implementer's commit brought a failure,
// and the fixer after it shows that failure at the commit it started from.
// That commit is the implementer's, so dude records no call at the task's
// base, the conductor reading the wake rejects it, and it is sent back;
// shown at the target branch's commit the task builds on, where it fails
// the same, it passes as pre-existing.
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
			cond := conducted(w)
			bashCall(w, "base", "git worktree add ../base "+tc.at(h)+" && cd ../base && npx tsc --noEmit", tscFailure, 2)
			bashCall(w, "head", "npx tsc --noEmit", tscFailure, 2)
			checksReply(w, failingTsc)
			endTurn(w)
			w.gate()
			w.decideAsTheDefaults(cond)
			if pass, _ := w.gate(); pass != tc.passes {
				t.Fatalf("pass %v, want %v", pass, tc.passes)
			}
			got := recordedJudgement(w)
			if got.Verified != tc.passes || got.ConductorPassed != tc.passes {
				t.Fatalf("%+v", got)
			}
			if !tc.passes && (got.Verdicts[0].Result != delivery.CheckFail || !strings.Contains(got.Verdicts[0].JudgeReason, "not passed by the conductor")) {
				t.Fatalf("verdicts %+v", got.Verdicts)
			}
		})
	}
}

// GitHub failing to compare leaves the turn unasked, to be asked once it
// answers; GitHub refusing to compare leaves the repository with no task
// base, so the conductor is asked with none and cannot pass the failure
// as pre-existing.
func TestAFailedCompareHoldsTheTurnAndARefusedOneHasNoBase(t *testing.T) {
	t.Run("failing, 503", func(t *testing.T) {
		w := newHarnessWorld(t)
		h := startedFrom(w)
		conducted(w)
		compares := h.compares(w, http.StatusServiceUnavailable)
		checksReply(w, failingTsc)
		endTurn(w)
		pass, _, err := w.s.checksBeforePush(context.Background(), w.run)
		if err == nil || pass || !forge.Transient(err) {
			t.Fatalf("pass %v, %v", pass, err)
		}
		if *compares == 0 || w.countEvents(delivery.EvRunChecksAsked) != 0 || w.countEvents(delivery.EvRunChecksJudged) != 0 {
			t.Fatalf("%d compares, asked %d, %d judgements recorded", *compares, w.countEvents(delivery.EvRunChecksAsked), w.countEvents(delivery.EvRunChecksJudged))
		}
	})
	t.Run("refusing, 404", func(t *testing.T) {
		w := newHarnessWorld(t)
		h := startedFrom(w)
		cond := conducted(w)
		compares := h.compares(w, http.StatusNotFound)
		checksReply(w, failingTsc)
		endTurn(w)
		if pass, _ := w.gate(); pass || *compares != 1 || len(w.asked().Bases) != 0 {
			t.Fatalf("pass %v; %d compares, bases %+v", pass, *compares, w.asked().Bases)
		}
		preexisting := delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
			Checks:  []delivery.ConductedCheck{{Command: "npx tsc --noEmit", Verdict: delivery.VerdictPreexisting, Reason: "fails the same"}},
			Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}
		if _, err := w.decide(cond, preexisting); !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "preexisting needs a recorded commit") {
			t.Fatalf("pre-existing with no base: %v", err)
		}
	})
}

// With no conductor the rejection says only a person can pass the
// failure, and the send-back does not say how one would pass; a failure
// the conductor rejected keeps that paragraph.
func TestWithNoConductorTheSendBackSaysOnlyAPersonCanPassAFailure(t *testing.T) {
	for _, policy := range []bool{true, false} {
		t.Run(map[bool]string{true: "no conductor", false: "the conductor rejected it"}[policy], func(t *testing.T) {
			w := newHarnessWorld(t)
			startedFrom(w)
			var cond string
			if policy {
				delivering(w, false)
			} else {
				cond = conducted(w)
			}
			checksReply(w, failingTsc)
			endTurn(w)
			w.gate()
			if !policy {
				if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
					Checks:  []delivery.ConductedCheck{{Command: "npx tsc --noEmit", Verdict: delivery.VerdictReject, Reason: "no run at the base"}},
					Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
					t.Fatal(err)
				}
				w.gate()
			}
			got := recordedJudgement(w)
			if got.Verified {
				t.Fatalf("%+v", got)
			}
			hint := strings.Contains(got.Message, "passes once this Run's own tool calls show")
			person := strings.Contains(got.Message, "no conductor: only a person can pass a reported failure")
			if hint == policy || person != policy {
				t.Fatalf("hint %v, only a person %v:\n%s", hint, person, got.Message)
			}
		})
	}
}

// A reason that spans lines is one line of the send-back, and the held
// report's escalation keeps each check it did not pass with why.
func TestAMultiLineReasonIsOneCheckOfTheSendBack(t *testing.T) {
	d := checksDecision{Verdicts: []checksVerdict{
		{Command: "npx tsc --noEmit", Result: delivery.CheckFail, Reason: "94 errors",
			JudgeReason: "not passed by the conductor: the calls show\n- base: 95 errors\n- head: 96 errors"},
		{Command: "make lint", Result: delivery.CheckPass},
	}}
	msg := d.sendBack()
	if n := strings.Count(msg, "\n- "); n != 1 || !strings.Contains(msg, "the calls show - base: 95 errors - head: 96 errors") {
		t.Fatalf("%d check lines:\n%s", n, msg)
	}
	if got := d.unverified(); len(got) != 1 || got[0].Command != "npx tsc --noEmit" || !strings.HasPrefix(got[0].Reason, "94 errors — not passed by the conductor") {
		t.Fatalf("unverified %+v", got)
	}
}

// The send-back names every failure the conductor did not pass, and none
// it passed.
func TestTheSendBackNamesEveryFailureNotPassed(t *testing.T) {
	w := newHarnessWorld(t)
	startedFrom(w)
	cond := conducted(w)
	checksReply(w, failingTsc+strings.TrimPrefix(failingTest, "checks:\n")+"  - command: make vet\n    result: fail\n    evidence: one vet issue\n")
	endTurn(w)
	w.gate()
	if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
		Checks: []delivery.ConductedCheck{{Command: "npx tsc --noEmit", Verdict: delivery.VerdictReject, Reason: "new errors"},
			{Command: "make test", Verdict: delivery.VerdictPreexisting, Reason: "fails the same at the base"},
			{Command: "make vet", Verdict: delivery.VerdictReject, Reason: "new issue"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
		t.Fatal(err)
	}
	if pass, _ := w.gate(); pass {
		t.Fatal("passed with two failures rejected")
	}
	msg := recordedJudgement(w).Message
	if !strings.Contains(msg, "- npx tsc --noEmit: ") || !strings.Contains(msg, "- make vet: one vet issue") || strings.Contains(msg, "- make test:") {
		t.Fatalf("the send-back:\n%s", msg)
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

// A task whose delivery targets another branch than the repository's
// default is measured against that branch's commit it builds on.
func TestTheTaskBaseIsOnTheTasksTargetBranch(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	// release forks from main with a commit of its own; the task builds on it.
	git(t, h.acc.work, "checkout", "-q", "-b", "release", h.Main)
	release := h.acc.commit(map[string]string{"release.txt": "release fix\n"}, "Release fix")
	git(t, h.acc.work, "push", "-q", h.acc.origin, "release")
	start := h.acc.commit(map[string]string{"task.txt": "the task's work\n"}, "Task work")
	git(t, h.acc.work, "push", "-q", "-f", h.acc.origin, "HEAD:refs/heads/"+delivery.BranchFor(w.run.TaskID, 1))
	mustOwner(t, w, `UPDATE runs SET base_shas = jsonb_build_object('target', $2::text) WHERE id = $1`, w.run.ID, start)
	state, _ := json.Marshal(map[string]any{"taskId": w.run.TaskID, "attempt": 1, "bases": map[string]string{"target": "release"}})
	mustOwner(t, w, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_'||$1, $1, $2, 'task.delivery', 'k_'||$1, 'waiting', 'implement', $3::jsonb)`, w.run.Org, w.run.TaskID, state)
	got, err := w.s.taskBases(context.Background(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	if want := "target: release at " + release; joinBases(got) != want {
		t.Fatalf("bases %q, want %q (main at %s)", joinBases(got), want, h.Main)
	}
}

// A browser check not run is asked about as deferred to a test phase when
// the attempt's policy runs one after it, and as run by no later phase
// with none: the conductor is told which, and its deferral passes it.
func TestANotRunBrowserCheckIsAskedAboutTheTestPhase(t *testing.T) {
	for _, test := range []bool{true, false} {
		t.Run(map[bool]string{true: "a test phase", false: "no test phase"}[test], func(t *testing.T) {
			w := newHarnessWorld(t)
			cond := conducted(w)
			withTestPhase(w, test)
			checksReply(w, notRunInUI)
			endTurn(w)
			w.gate()
			said := map[bool]string{true: "(this delivery runs a test phase)", false: "(this delivery runs no test phase)"}[test]
			if a := w.asked(); a.TestPhase != test || !strings.Contains(w.checksWake(), said) {
				t.Fatalf("asked %+v, wake %q", a, w.checksWake())
			}
			if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
				Checks:  []delivery.ConductedCheck{{Command: "open the settings page in a browser", Verdict: delivery.VerdictDeferred, Reason: "the tester runs it"}},
				Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
				t.Fatal(err)
			}
			if pass, _ := w.gate(); !pass {
				t.Fatal("a deferred check did not pass")
			}
			if got := recordedJudgement(w); !got.ConductorPassed || got.Verdicts[0].Result != delivery.CheckDeferred || got.Verdicts[0].By != "conductor" {
				t.Fatalf("recorded %+v", got)
			}
		})
	}
}

// withTestPhase sets whether the harness's conducted delivery runs a test
// phase.
func withTestPhase(w *harnessWorld, test bool) {
	w.t.Helper()
	p := delivery.DefaultPolicy()
	p.Test = test
	raw, _ := json.Marshal(p)
	mustOwner(w.t, w, `UPDATE workflow_runs SET state = jsonb_set(state, '{policy}', $2::jsonb) WHERE task_id = $1`, w.run.TaskID, raw)
}

// A report whose only non-pass is a check not run asks GitHub for no task
// base: nothing is measured against one.
func TestANotRunOnlyReportComparesNothing(t *testing.T) {
	w := newHarnessWorld(t)
	h := startedFrom(w)
	compares := h.compares(w, 0)
	conducted(w)
	withTestPhase(w, true)
	checksReply(w, notRunInUI)
	endTurn(w)
	w.gate()
	if a := w.asked(); len(a.Bases) != 0 || *compares != 0 {
		t.Fatalf("asked %+v; %d compares", a, *compares)
	}
	// A failure beside it is measured, with one compare.
	checksReply(w, failingTsc+strings.TrimPrefix(notRunInUI, "checks:\n"))
	endTurn(w)
	w.gate()
	if a := w.asked(); len(a.Bases) != 1 || *compares != 1 {
		t.Fatalf("asked %+v; %d compares", a, *compares)
	}
}

// A carried waiver is a candidate for its own failure alone, whatever the
// report's order; another failure beside it may still pass as
// pre-existing, and the waived one as the same failure.
func TestACarriedWaiverIsAskedAboutAloneForItsFailure(t *testing.T) {
	w := newHarnessWorld(t)
	startedFrom(w)
	cond := conducted(w)
	waive(w, w.run, delivery.ScopeAttempt, heldFailure)
	// The unwaived failure is listed first.
	checksReply(w, failingTsc+strings.TrimPrefix(failingTest, "checks:\n"))
	endTurn(w)
	w.gate()
	a := w.asked()
	if a.Waivers[0] != nil || a.Waivers[1] == nil || len(a.Bases) != 1 {
		t.Fatalf("asked %+v", a)
	}
	if !strings.Contains(w.checksWake(), fmt.Sprintf(`Check 2, "make test", may be the failure the Implementer's waiver accepted: same_as_waived cites waiverCursor %d.`, a.Waivers[1].Cursor)) {
		t.Fatalf("wake %q", w.checksWake())
	}
	if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
		Checks: []delivery.ConductedCheck{{Command: "npx tsc --noEmit", Verdict: delivery.VerdictPreexisting, Reason: "fails the same at the base"},
			{Command: "make test", Verdict: delivery.VerdictSameAsWaived, WaiverCursor: a.Waivers[1].Cursor, Reason: "the same TestClock failure"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
		t.Fatal(err)
	}
	if pass, _ := w.gate(); !pass {
		t.Fatal("did not pass")
	}
	got := recordedJudgement(w)
	if !got.Verified || !got.Carried || !got.ConductorPassed {
		t.Fatalf("%+v", got)
	}
	if v := got.Verdicts; v[0].Result != delivery.CheckPreexisting || v[1].Result != delivery.CheckWaived || v[1].Command != "make test" {
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
