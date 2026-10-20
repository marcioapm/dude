package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The conductor's verify: a report-only child Run, publishing off, on the
// commit named, whose commands' results wake the conductor.

// headOf is the task's delivery's head in repo.
func (w *world) headOf(task, repo string) string {
	var head string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'heads'->>$2, '') FROM workflow_runs WHERE task_id = $1
		ORDER BY created_at DESC LIMIT 1`, task, repo).Scan(&head)
	return head
}

// verifyResult is what the conductor is told of the verification child,
// once its result is delivered.
func (w *world) verifyResult(child string) string {
	w.t.Helper()
	var text string
	w.until("the verifier's result for the conductor", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT d.text FROM child_result_outbox o JOIN directives d ON d.id = o.directive_id
			WHERE o.child_run_id = $1`, child).Scan(&text)
		return text != ""
	})
	return text
}

// At the decision after implement — no pull request yet — the conductor
// has a commit verified: main's, from before the implementer, not the task's
// head. The child checks that commit out, has no branch to push to and is
// refused a push, runs both commands, and its result reaches the conductor
// as its next input: failed, each command's exit code, and each output
// quoted as external text.
func TestTheConductorHasACommitVerifiedBeforeAnyPullRequest(t *testing.T) {
	w := conducting(t)
	w.onModel("investigator", "fake/scripted")
	task := w.task()
	conductor := w.talk(task)
	w.implemented(task)
	head, main := w.headOf(task, "target"), w.gh.SHA("main")
	if head == "" || head == main || w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1`, task) != 0 {
		t.Fatalf("head %q main %q, or a pull request already", head, main)
	}

	out := w.must(task, "verify", fmt.Sprintf(`{"commit":%q,"commands":["test ! -f FACTORY.md && echo absent","echo boom; exit 3"],"note":"before the change"}`, main))
	child, _ := out["runId"].(string)
	if out["commit"] != main || out["repo"] != "target" {
		t.Fatalf("verify answered %v", out)
	}
	var phase, publish string
	var bases map[string]string
	if err := w.owner.QueryRow(context.Background(), `SELECT phase::text, child_capabilities->>'publishCode', base_refs FROM runs
		WHERE id = $1 AND parent_run_id = $2 AND source_kind = 'task_conductor'`, child, conductor).Scan(&phase, &publish, &bases); err != nil {
		t.Fatal(err)
	}
	if phase != delivery.PhaseInvestigate || publish != "false" || bases["target"] != main {
		t.Fatalf("verifier: phase %s publishCode %s bases %v", phase, publish, bases)
	}

	text := w.verifyResult(child)
	spec := w.specOfRun(child)
	if spec.Git == nil || spec.Git.Push != nil || len(spec.Git.Repositories) != 1 || spec.Git.Repositories[0].Ref != main {
		t.Fatalf("the verifier's git: %+v", spec.Git)
	}
	if err := w.syncer.Lux.Push(context.Background(), w.luxRunOf(child), "try-push", nil); err == nil {
		t.Fatal("lux pushed the verifier's checkout")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND (push_request_id IS NOT NULL OR push_branch IS NOT NULL)`, child); n != 0 {
		t.Fatal("the verifier has a push or a push branch")
	}

	if !strings.HasPrefix(text, "Your verification "+child+" of target at "+main+" failed: 1 of 2 commands exited 0.") ||
		!strings.Contains(text, "- exit 0: test ! -f FACTORY.md && echo absent") || !strings.Contains(text, "- exit 3: echo boom; exit 3") {
		t.Fatalf("the result:\n%s", text)
	}
	var report struct {
		External string
		Status   string
		Results  []struct {
			Command string
			Exit    int
			Tail    string
		}
	}
	if err := json.Unmarshal([]byte(text[strings.Index(text, "{"):]), &report); err != nil {
		t.Fatalf("the result's JSON: %v\n%s", err, text)
	}
	if report.Status != "failed" || len(report.Results) != 2 || report.Results[1].Exit != 3 ||
		!strings.HasPrefix(report.Results[1].Tail, "⟦external ") || !strings.Contains(report.Results[1].Tail, "boom") ||
		!strings.Contains(report.External, "⟦external ") {
		t.Fatalf("the report: %+v", report)
	}
	w.until("the conductor to be given it", func() bool {
		lr := w.luxRunOf(conductor)
		return slices.ContainsFunc(w.lux.Runs(), func(r *fakelux.Run) bool {
			return r.ID == lr && slices.ContainsFunc(r.Inputs, func(in string) bool { return strings.Contains(in, "Your verification "+child) })
		})
	})
	if w.decisionAt(task) != delivery.PointImplemented {
		t.Errorf("the verification moved the delivery: at %q", w.decisionAt(task))
	}
}

// A verification of the task's head that passes says so; the commit is
// the task branch's head when none is named.
func TestAVerificationOfTheHeadPasses(t *testing.T) {
	w := conducting(t)
	w.onModel("investigator", "fake/scripted")
	task := w.task()
	w.talk(task)
	w.implemented(task)
	out := w.must(task, "verify", `{"commands":["test -f FACTORY.md"]}`)
	if out["commit"] != w.headOf(task, "target") {
		t.Fatalf("verified %v, want the head %s", out["commit"], w.headOf(task, "target"))
	}
	if text := w.verifyResult(out["runId"].(string)); !strings.Contains(text, " passed: 1 of 1 commands exited 0.") {
		t.Fatalf("the result:\n%s", text)
	}
}

// A verifier of a repository whose name lux rewrites is told the checkout
// path its spec gives that repository, not one built from the name.
func TestAVerifierIsToldItsCheckoutPathForARepositoryLuxRenames(t *testing.T) {
	w := conducting(t)
	w.onModel("investigator", "fake/scripted")
	const name = "My.Repo"
	mustExec(t, w.owner, `UPDATE repositories SET name = $2 WHERE id = $1`, w.repoID, name)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	child, _ := w.must(task, "verify", `{"commands":["true"]}`)["runId"].(string)
	w.verifyResult(child)
	spec := w.specOfRun(child)
	want := "/workspace/repos/" + lux.SpecName(name)
	if spec.Git == nil || len(spec.Git.Repositories) != 1 || spec.Git.Repositories[0].Path != want {
		t.Fatalf("the verifier's git: %+v", spec.Git)
	}
	var brief string
	if err := w.owner.QueryRow(context.Background(), `SELECT child_prompt FROM runs WHERE id = $1`, child).Scan(&brief); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "checked out at `"+want+"`") || !strings.Contains(brief, "each from `"+want+"`") {
		t.Fatalf("the brief does not name %s:\n%s", want, brief)
	}
}

// What verify refuses before any Run starts: a commit not named by its
// SHA, a repository not the task's, too many commands; and its Runs are
// bounded per conductor Run, as limits shows.
func TestVerifyIsBounded(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.refused(task, "verify", `{"commit":"HEAD"}`, "is not a commit")
	w.refused(task, "verify", `{"repo":"web"}`, "is not one of this task's repositories")
	w.refused(task, "verify", `{"commands":["a","b","c","d","e","f","g","h","i","j","k"]}`, "at most 10 commands")
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND source_kind IS NOT NULL`, task); n != 0 {
		t.Fatalf("%d verifiers started by refused calls", n)
	}
	for i := range 10 {
		w.must(task, "verify", fmt.Sprintf(`{"commands":["true"],"key":"v%d"}`, i))
	}
	w.refused(task, "verify", `{"commands":["true"],"key":"v10"}`, "verify")
	if v := obj(field(w.limitsOf(task), "verifiesThisRun")); num(v["limit"]) != 10 || num(v["used"]) != 10 || num(v["remaining"]) != 0 {
		t.Fatalf("limits' verifications: %v", v)
	}
}
