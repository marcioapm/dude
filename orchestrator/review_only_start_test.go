package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// started says the start of a review-only task was taken: since the
// verdict step, a review-only task starts as any task does.
func started(t *testing.T, what string, status int, out map[string]any) {
	t.Helper()
	if status >= 300 {
		t.Fatalf("%s of a review-only task: %d %v, want it started", what, status, out)
	}
}

// A review-only task starts from Deliver (PR 2 refused every start until
// the verdict step): one delivery, which reaches its lenses and never a
// writer.
func TestAReviewOnlyTaskStartsAndReachesReview(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	ana := w.person("Ana")
	w.assignOwner(wi, ana)
	status, out := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{})
	started(t, "Deliver", status, out)
	w.agentsDone(wi, "its lenses ran", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d deliveries", n)
	}
	if n := w.writers(wi); n != 0 {
		t.Fatalf("%d writer Runs on a review-only task", n)
	}
}

// A stopped review-only task is picked back up as any task is (PR 2
// refused it until the verdict step): Start over makes a new delivery.
func TestAStoppedReviewOnlyTaskIsPickedBackUp(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	wi, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
	w.stop(ana, wi, map[string]any{})
	status, out := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"})
	started(t, "Start over", status, out)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi); n != 1 {
		t.Fatalf("%d live deliveries after Start over", n)
	}
}

// A person's Update branch on a review-only task's pull request is
// refused: the task changes no code, not even a merge of its base onto
// the author's branch. On a task in a writing mode it goes to GitHub.
func TestUpdateBranchIsRefusedOnAReviewOnlyTasksPullRequest(t *testing.T) {
	for _, mode := range []string{delivery.ModeReviewOnly, delivery.ModeReview} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			ana := w.person("Ana")
			wi, _ := w.holding(n, "feature/greet", mode, ana)
			prID := "pr_" + wi
			mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
				head_branch, base_branch, head_sha, title, state, checks)
				SELECT $1, organization_id, project_id, $2, id, $3::int, url || '/pull/' || $3::text, 'feature/greet', 'main', $4, 'Greet', 'open', 'passing'
				FROM repositories WHERE id = $5`, prID, wi, n, w.gh.SHA("feature/greet"), w.repoID)
			status, out := w.callAs(ana, "/internal/pull-requests/"+prID+"/update-branch", map[string]any{})
			if mode == delivery.ModeReviewOnly {
				e, _ := out["error"].(map[string]any)
				if status != 409 || e["code"] != "review_only" || !strings.Contains(fmt.Sprint(e["message"]), "update-branch is not dude's to take here") {
					t.Fatalf("Update branch on a review-only task: %d %v", status, out)
				}
				if w.updateCalls() != 0 {
					t.Fatal("GitHub was asked to update the author's branch")
				}
				return
			}
			if status != 200 || w.updateCalls() != 1 {
				t.Fatalf("Update branch on a %s task: %d %v, %d GitHub updates", mode, status, out, w.updateCalls())
			}
		})
	}
}

// Picking a stopped review-only task back up beside another live task on
// its pull request: Resume and Try again hold no branch for it, as Start
// over (NewState) holds none, so none is refused superseded_required.
// Start over and Try again start it; Resume answers that nothing was kept
// to resume (its only Run was never on lux), as for any task.
func TestAStoppedReviewOnlyTaskBesideAHolderIsAnsweredAlikeEveryWay(t *testing.T) {
	for _, c := range []struct {
		action string
		status int
		code   string
	}{{"restart", 200, ""}, {"resume", 409, "not_kept"}, {"retry", 200, ""}} {
		t.Run(c.action, func(t *testing.T) {
			w := newWorld(t)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			ana := w.person("Ana")
			wi, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
			w.stop(ana, wi, map[string]any{})
			w.holding(n, "feature/greet", delivery.ModeReview, ana)
			status, out := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": c.action})
			e, _ := out["error"].(map[string]any)
			code, _ := e["code"].(string)
			if status != c.status || code != c.code {
				t.Fatalf("%s beside a holder: %d %v, want %d %q", c.action, status, out, c.status, c.code)
			}
			live := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi)
			if want := map[bool]int{true: 1, false: 0}[c.status == 200]; live != want {
				t.Fatalf("%d live deliveries after %s, want %d", live, c.action, want)
			}
		})
	}
}

// writerOffered is a decision offered the task's conductor that names a
// writer phase: a review-only task's are review and test only.
func (w *world) writerOffered(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->'phases' ?| array['implement', 'fix', 'simplify', 'publish']`, task, delivery.EvDecisionAwaited)
}

// Every way a review-only task starts or is picked back up starts it, and
// no writer phase is ever offered its conductor or started: Talk, Chat,
// Feedback, the start when unblocked, Start over, Resume (refused: nothing
// kept) and Try again. Deliver and Stop and start over are above and in
// the verdict's tests.
func TestEveryStartOfAReviewOnlyTaskOffersNoWriter(t *testing.T) {
	for _, c := range []struct {
		name  string
		start func(w *world, wi, ana string) (int, map[string]any)
		// The answer: a status at or under 201, or this refusal.
		refused string
	}{
		{"Talk", func(w *world, wi, ana string) (int, map[string]any) {
			return w.callAs(ana, "/internal/tasks/"+wi+"/talk", map[string]any{})
		}, ""},
		{"Chat", func(w *world, wi, ana string) (int, map[string]any) {
			return w.callAs(ana, "/internal/tasks/"+wi+"/chat", map[string]any{"text": "review it please"})
		}, ""},
		{"Feedback", func(w *world, wi, ana string) (int, map[string]any) {
			return w.feedback(ana, wi, "fbi_ro", "Look at the retry loop.")
		}, ""},
		{"start when unblocked", func(w *world, wi, ana string) (int, map[string]any) {
			w.autoStart(wi, "deliver")
			mustExec(t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, wi)
			if n := w.fireAutoStarts(); n != 1 {
				return 500, map[string]any{"fired": n}
			}
			return 201, nil
		}, ""},
		{"Start over", recoverAs("restart"), ""},
		{"Resume", recoverAs("resume"), "not_kept"},
		{"Try again", recoverAs("retry"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.gh.SetChecks("success")
			w.cleanLenses()
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			ana := w.person("Ana")
			var wi string
			if strings.HasPrefix(c.name, "Start over") || c.name == "Resume" || c.name == "Try again" {
				wi, _ = w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
				w.stop(ana, wi, map[string]any{})
			} else {
				wi = w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
				w.assignOwner(wi, ana)
			}
			status, out := c.start(w, wi, ana)
			if c.refused != "" {
				e, _ := out["error"].(map[string]any)
				if status != 409 || e["code"] != c.refused {
					t.Fatalf("%s: %d %v, want 409 %s", c.name, status, out, c.refused)
				}
				return
			}
			if status > 201 {
				t.Fatalf("%s of a review-only task: %d %v, want it started", c.name, status, out)
			}
			// Started: its lenses run, or its conductor is offered a decision
			// (a chatty one, from Talk, Chat or Feedback, decides itself).
			w.agentsDone(wi, "its lenses or its conductor's decision", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0 ||
					w.events(wi, delivery.EvDecisionAwaited) > 0
			})
			if o := w.writerOffered(wi); o != 0 {
				t.Fatalf("%s: a writer phase was offered the conductor %d time(s)", c.name, o)
			}
			if r := w.writers(wi); r != 0 {
				t.Fatalf("%s: %d writer Runs", c.name, r)
			}
		})
	}
}

func recoverAs(action string) func(w *world, wi, ana string) (int, map[string]any) {
	return func(w *world, wi, ana string) (int, map[string]any) {
		return w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": action})
	}
}
