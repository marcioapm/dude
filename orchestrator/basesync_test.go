package orchestrator_test

// Keeping a task current with its base: sync_base, the integration Run a
// conflict starts, the merge before a pull request opens, and what a merge
// of the base counts as.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// toolsWithSync serves dude's tools with sync_base wired to the world's
// syncer and lux, as the orchestrator's main does.
func (w *world) toolsWithSync() string {
	w.t.Helper()
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux, SyncBase: w.syncer.SyncBase}).Handler())
	w.t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	w.syncer.Agent.ToolsService = true
	return tools.URL
}

func TestBeforePRIntegrationFailureCanWaitForAPerson(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.task()
	w.deliver(wi)
	w.until("implemented", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='implement' AND status='completed'`, wi) == 1
	})
	w.gh.AdvanceBase("main", "Base moved")
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/merges") {
				return 409
			}
			return 0
		}
	})
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.integration"] == "true" {
			return fakelux.Behaviour{Crash: true}
		}
		return fakelux.Behaviour{Reply: "No findings."}
	}
	w.until("failed integration", func() bool { return w.escalationReason(wi) == "pull_request_conflict" })
	w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
	w.lux.Decide = nil
	branch := delivery.BranchFor(wi, 1)
	// A person brings the base in before choosing wait.
	gitOut(t, w.gh.Repo, "update-ref", "refs/heads/"+branch, w.gh.SHA("main"))
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "wait"}); code != 200 {
		t.Fatal(code, body)
	}
	w.until("PR after human wait", func() bool { return len(w.gh.Pulls()) == 1 })
}

func TestBeforePRTellDoesNotMergeBase(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings='{"whenBehind":"tell"}' WHERE organization_id=$1`, w.org)
	wi := w.task()
	w.deliver(wi)
	w.until("implemented", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='implement' AND status='completed'`, wi) == 1
	})
	base := w.gh.AdvanceBase("main", "Other work")
	w.until("PR", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := gitOut(t, w.gh.Repo, "rev-list", "--count", w.gh.Pull(1).Head+".."+base); got != "1" {
		t.Fatalf("tell merged automatically: %s", got)
	}
}

func TestBeforePRConflictingBaseIsResolvedReviewedAndTested(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	seed := t.TempDir()
	gitOut(t, seed, "clone", "-q", w.gh.Repo, "work")
	work := filepath.Join(seed, "work")
	if err := os.WriteFile(filepath.Join(work, "FACTORY.md"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, work, "add", ".")
	gitOut(t, work, "-c", "user.name=Acme", "-c", "user.email=a@example.com", "commit", "-qm", "Seed")
	gitOut(t, work, "push", "-q", "origin", "main")
	wi := w.task()
	p := scriptedPolicy()
	p.Test = true
	w.names(wi, w.repoID)
	w.start(wi, p)
	w.until("implementer finished", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='implement' AND status='completed'`, wi) == 1
	})
	if err := os.WriteFile(filepath.Join(work, "FACTORY.md"), []byte("base intent\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, work, "add", ".")
	gitOut(t, work, "-c", "user.name=Acme", "-c", "user.email=a@example.com", "commit", "-qm", "Base change")
	gitOut(t, work, "push", "-q", "origin", "main")
	w.until("integrated PR", func() bool {
		if r := w.escalationReason(wi); r != "" {
			var s string
			_ = w.owner.QueryRow(context.Background(), `SELECT state::text FROM workflow_runs WHERE task_id=$1`, wi).Scan(&s)
			t.Fatal(r, s)
		}
		return len(w.gh.Pulls()) == 1
	})
	branch := w.gh.Pull(1).Head
	content := gitOut(t, w.gh.Repo, "show", branch+":FACTORY.md")
	if !strings.Contains(content, "base intent") || !strings.Contains(content, "Written by run") {
		t.Fatalf("lost intent %s", content)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND pr_feedback::text LIKE '%remerge-diff%'`, wi); n < 1 {
		t.Fatalf("integration reviews %d", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='test' AND status='completed'`, wi); n < 2 {
		t.Fatalf("verification was not repeated: %d", n)
	}
}

func TestBeforePRMergesMovedBaseAndAdoptsHead(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.task()
	w.names(wi, w.repoID)
	policy := scriptedPolicy()
	policy.Test = true
	w.start(wi, policy)
	w.until("implementer finished", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='implement' AND status='completed'`, wi) == 1
	})
	base := w.gh.AdvanceBase("main", "Other work")
	w.until("pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	branch := w.gh.Pull(1).Head
	if got := gitOut(t, w.gh.Repo, "rev-list", "--count", branch+".."+base); got != "0" {
		t.Fatalf("PR omitted base: %s", got)
	}
	var head string
	if err := w.owner.QueryRow(context.Background(), `SELECT state->'heads'->>'target' FROM workflow_runs WHERE task_id=$1`, wi).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != w.gh.SHA(branch) {
		t.Fatalf("state %s forge %s", head, w.gh.SHA(branch))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='test' AND status='completed'`, wi); n != 2 {
		t.Fatalf("server merge verification runs %d", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='pull_request.action' AND payload->>'action'='merge-base'`, wi); n != 1 {
		t.Fatalf("merge events %d", n)
	}
}

func TestIntegrationWaitsForAnExistingWriter(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	mustExec(t, w.owner, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,phase,role) VALUES('run_existing_writer',$1,$2,$3,1,'paused','fix','implementer')`, w.org, w.project, wi)
	w.gh.AdvanceBase("main", "Moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	for range 5 {
		w.sync()
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`, wi); n != 0 {
		t.Fatalf("integration raced writer: %d", n)
	}
	mustExec(t, w.owner, `UPDATE runs SET status='aborted' WHERE id='run_existing_writer'`)
	w.until("integration after writer", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`, wi) == 1
	})
}

func TestIntegrationRejectsAStaleConflictAndKeepsFeedback(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.sync()
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.until("stale signal consumed", func() bool {
		return w.count(`SELECT count(*) FROM workflow_signals WHERE workflow_run_id=(SELECT id FROM workflow_runs WHERE task_id=$1) AND consumed_at IS NULL`, wi) == 0
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`, wi); n != 0 {
		t.Fatalf("stale conflict ran %d integrations", n)
	}
	if w.escalationReason(wi) != "" {
		t.Fatal("stale conflict escalated")
	}
}

func TestIntegrationStillConflictingEscalatesWithoutLooping(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.gh.AdvanceBase("main", "Moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("integration exhausted", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	for range 5 {
		w.sync()
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`, wi); n != 1 {
		t.Fatalf("integration runs %d", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND pr_feedback::text LIKE '%remerge-diff%'`, wi); n != 1 {
		t.Fatalf("integration reviews %d", n)
	}
}

func TestBaseMovedNoticeReachesOtherConductorsOnce(t *testing.T) {
	w := newWorld(t)
	merging := w.reviewing()
	other, readOnly, done := w.task(), w.task(), w.task()
	for _, id := range []string{other, readOnly, done} {
		access := "write"
		if id == readOnly {
			access = "read"
		}
		mustExec(t, w.owner, `INSERT INTO task_repositories(task_id, repository_id, access, organization_id) VALUES($1,$2,$3::repository_access,$4)`, id, w.repoID, access, w.org)
		mustExec(t, w.owner, `INSERT INTO workflow_runs(id, organization_id, workflow_type, idempotency_key, task_id, status, step, state) VALUES($1,$2,$3,$1,$4,'waiting','awaitPullRequest','{}')`, "wf_"+id, w.org, delivery.WorkflowType, id)
		mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind) VALUES($1,$2,$3,$4,1,'paused','conductor','agent')`, "run_c_"+id, w.org, w.project, id)
	}
	mustExec(t, w.owner, `UPDATE tasks SET status='done' WHERE id=$1`, done)
	w.gh.Merge(1)
	w.until("merge recorded", func() bool {
		var id string
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id=$1`, merging).Scan(&id)
		if err := w.prs.Sync(context.Background(), w.org, id); err != nil {
			t.Fatal(err)
		}
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND state='merged'`, merging) == 1
	})
	for range 3 {
		w.sync()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='chat.notice' AND payload->>'about'='base_moved'`, other); n != 1 {
		t.Fatalf("other task notices %d", n)
	}
	for _, id := range []string{merging, readOnly, done} {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='chat.notice' AND payload->>'about'='base_moved'`, id); n != 0 {
			t.Fatalf("excluded task %s got %d notices", id, n)
		}
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key LIKE 'base-moved:%'`, other); n != 1 {
		t.Fatalf("wakes %d", n)
	}
}

func TestIntegrationConflictRunsInternalReviewAndKeepsFeedback(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	before := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review'`, wi)
	w.gh.AdvanceBase("main", "Base moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.gh.Comment(1, "alice", "Please preserve this feedback too.")
	w.until("integration fixer", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='fix' AND pr_feedback::text LIKE '%Base integration%'`, wi) == 1
	})
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.until("reviewed integration back waiting", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review'`, wi) > before &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'`, wi) == 1
	})
	if w.escalationReason(wi) != "" {
		t.Fatal(w.escalationReason(wi))
	}
	if w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%preserve this feedback%' AND pr_feedback::text LIKE '%Base integration%'`, wi) != 1 {
		t.Fatal("feedback lost")
	}
	branch := w.gh.Pull(1).Head
	if out := gitOut(t, w.gh.Repo, "rev-list", "--count", branch+"..main"); out != "0" {
		t.Fatal("base not integrated", out)
	}
}

func TestIntegrationFailureOffersRetryAndTellOptsOut(t *testing.T) {
	for _, tell := range []bool{false, true} {
		t.Run(fmt.Sprint(tell), func(t *testing.T) {
			w := newWorld(t)
			w.toolsWithSync()
			wi := w.reviewing()
			if tell {
				mustExec(t, w.owner, `UPDATE forge_credentials SET settings='{"whenBehind":"tell"}' WHERE organization_id=$1`, w.org)
			}
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Crash: true} }
			w.gh.AdvanceBase("main", "Base moved")
			w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
			w.until("conflict escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
			var raw []byte
			if err := w.owner.QueryRow(context.Background(), `SELECT state->'escalation' FROM workflow_runs WHERE task_id=$1`, wi).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var e delivery.Escalation
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}
			want := "retry wait stop"
			if tell {
				want = "wait stop"
			}
			if strings.Join(e.Actions(), " ") != want {
				t.Fatalf("actions %v, want %s", e.Actions(), want)
			}
			attempts := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`, wi)
			if tell && attempts != 0 || !tell && attempts != 1 {
				t.Fatalf("attempts %d", attempts)
			}
			for range 3 {
				w.sync()
				w.pump()
			}
			if !tell {
				if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "retry"}); code != 200 {
					t.Fatal(code, body)
				}
				w.until("one explicit retry", func() bool {
					w.sync()
					return w.escalationReason(wi) == "pull_request_conflict" && w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`, wi) == 2
				})
			}
		})
	}
}

// whenBehind=tell leaves a conflict to a person; their "Resolve conflicts"
// on the pull request starts the one integration the setting withheld.
func TestResolveConflictsIsAPersonsExplicitIntegration(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings='{"whenBehind":"tell"}' WHERE organization_id=$1`, w.org)
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id=$1`, wi).Scan(&prID)
	me := w.person("owner")
	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{}); code != 409 {
		t.Fatalf("resolve on a clean pull request: %d %v", code, body)
	}
	w.gh.AdvanceBase("main", "Base moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("conflict escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	integrations := `SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`
	if n := w.count(integrations, wi); n != 0 {
		t.Fatalf("tell integrated by itself: %d", n)
	}
	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{}); code != 200 {
		t.Fatalf("resolve: %d %v", code, body)
	}
	w.until("integration started", func() bool { w.sync(); return w.count(integrations, wi) == 1 })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='pull_request.action'
		AND payload->>'action'='integration-started' AND payload->>'runId' LIKE 'run_%'`, wi); n != 1 {
		t.Fatalf("integration-started events %d", n)
	}
	if code, _ := w.callAs(me, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{}); code != 409 {
		t.Fatalf("a second resolve while integrating: %d", code)
	}
}

// The orchestrator dies after the integration Run is created and before its
// step commits: the replayed step finds that Run by its key and starts no
// second one for the pair. A base that moves again is a new pair and gets
// its own Run.
func TestAnIntegrationReplayedAfterARestartKeepsItsRun(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	var once sync.Once
	armed := w.gated(func(step, next string) {
		if step != "prFix" || next != "awaitPRFix" {
			return
		}
		once.Do(func() {
			mustExec(t, w.owner, `UPDATE workflow_runs SET locked_until = now() - interval '1 second' WHERE locked_by IS NOT NULL`)
		})
	})
	wi := w.reviewing()
	integrations := `SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`
	w.gh.AdvanceBase("main", "Base moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	armed.Store(true)
	w.until("the integration Run", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step <> 'awaitPullRequest' AND step <> 'prFix'`, wi) == 1 &&
			w.count(integrations, wi) >= 1
	})
	var lost bool
	once.Do(func() { lost = true })
	if lost {
		t.Fatal("the step never reached its commit after creating the Run")
	}
	if n := w.count(integrations, wi); n != 1 {
		t.Fatalf("integration Runs after a replay: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='pull_request.action' AND payload->>'action'='integration-started'`, wi); n != 1 {
		t.Fatalf("integration-started events after a replay: %d", n)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.until("back waiting", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'`, wi) == 1
	})
	w.gh.AdvanceBase("main", "Base moved again")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("a second pair's Run", func() bool { w.sync(); return w.count(integrations, wi) == 2 })
	if r := w.escalationReason(wi); r != "" {
		t.Fatalf("a new pair escalated: %s", r)
	}
}

// twoConflictingPullRequests delivers a task across target and web, then
// moves both bases so each pull request conflicts on FACTORY.md, read in one
// reconcile so both conflicts reach the workflow in one batch.
func (w *world) twoConflictingPullRequests() string {
	w.t.Helper()
	w.toolsWithSync()
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			b.Commit = map[string]string{"target:FACTORY.md": "task intent\n", "web:FACTORY.md": "task intent\n"}
		}
		return b
	}
	wi := w.task()
	w.addWeb(wi, "write")
	w.deliver(wi)
	w.until("a pull request in each repository", func() bool { return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })
	w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", "base intent\n", "Base change")
	w.commitFileIn(w.web.Repo, "main", "FACTORY.md", "base intent\n", "Base change")
	w.sync()
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND mergeable_state='conflicting'`, wi); n != 2 {
		w.t.Fatalf("conflicting pull requests read: %d", n)
	}
	return wi
}

func TestTwoConflictingPullRequestsAreIntegratedInTurn(t *testing.T) {
	w := newWorld(t)
	wi := w.twoConflictingPullRequests()
	integrations := `SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`
	w.until("both integrated and waiting", func() bool {
		w.sync()
		if r := w.escalationReason(wi); r != "" {
			t.Fatalf("escalated: %s", r)
		}
		return w.count(integrations, wi) == 2 &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'
				AND COALESCE(jsonb_array_length(state->'pendingConflicts'), 0) = 0`, wi) == 1
	})
	for _, repo := range []struct {
		name string
		gh   *fakegithub.Server
	}{{"target", w.gh}, {"web", w.web}} {
		if n := w.count(integrations+` AND pr_feedback::text LIKE '%in `+repo.name+`.%'`, wi); n != 1 {
			t.Errorf("%s integration Runs %d", repo.name, n)
		}
		branch := repo.gh.Pull(1).Head
		if out := gitOut(t, repo.gh.Repo, "rev-list", "--count", branch+"..main"); out != "0" {
			t.Errorf("%s: base not merged in (%s behind)", repo.name, out)
		}
		content := gitOut(t, repo.gh.Repo, "show", branch+":FACTORY.md")
		if !strings.Contains(content, "base intent") || !strings.Contains(content, "task intent") {
			t.Errorf("%s lost an intent: %q", repo.name, content)
		}
	}
}

// A sibling whose pull request merged while the first integration ran is
// not integrated: its queued conflict is dropped, and nothing escalates.
func TestAQueuedConflictOnAMergedSiblingIsSkipped(t *testing.T) {
	w := newWorld(t)
	wi := w.twoConflictingPullRequests()
	integrations := `SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`
	w.until("the first integration Run", func() bool { return w.count(integrations, wi) == 1 })
	sibling := w.web
	if w.count(integrations+` AND pr_feedback::text LIKE '%in web.%'`, wi) == 1 {
		sibling = w.gh
	}
	sibling.Merge(1)
	w.sync()
	w.until("back waiting with nothing queued", func() bool {
		w.sync()
		if r := w.escalationReason(wi); r != "" {
			t.Fatalf("escalated: %s", r)
		}
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND state='merged'`, wi) == 1 &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'
				AND COALESCE(jsonb_array_length(state->'pendingConflicts'), 0) = 0`, wi) == 1
	})
	for range 3 {
		w.sync()
		w.pump()
	}
	if n := w.count(integrations, wi); n != 1 {
		t.Fatalf("integration Runs %d, want only the open pull request's", n)
	}
	if n := len(w.gh.Pulls()) + len(w.web.Pulls()); n != 2 {
		t.Fatalf("pull requests %d: a merged repository got another", n)
	}
}

func TestSyncBaseReportsAPendingFetchWithinItsBound(t *testing.T) {
	w := newWorld(t)
	w.lux.HoldSyncs = true
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux,
		SyncBase: w.syncer.SyncBase, SyncBaseWait: time.Millisecond}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	status, body := w.callTool(tools.URL, string(w.lux.Runs()[0].Spec), "sync_base", `{}`)
	if status != 200 || !strings.Contains(body, `"status":"asked"`) || !strings.Contains(body, "HEAD..lux/main") {
		t.Fatalf("pending fetch: %d %s", status, body)
	}
}

// gitOut runs git in dir and answers what it printed, trimmed.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// An implementer at work calls sync_base: lux fetches main into its
// checkout as lux/main, the checkout does not move, and it is told how
// far behind it is and what to merge.
func TestSyncBaseFetchesTheBaseWithoutMovingTheCheckout(t *testing.T) {
	w := newWorld(t)
	tools := w.toolsWithSync()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	run := w.lux.Runs()[0]
	work, err := w.lux.Checkout(run.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	before := gitOut(t, work, "rev-parse", "HEAD")
	gitOut(t, work, "checkout", "-q", "-b", "task-work")
	moved := w.gh.AdvanceBase("main", "Someone else's work")

	status, body := w.callTool(tools, string(run.Spec), "sync_base", `{}`)
	if status != 200 {
		t.Fatalf("sync_base: %d %s", status, body)
	}
	var out struct {
		Repositories []struct {
			Repo, Base, Ref, Status, Next string
			Behind                        int
		}
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Repositories) != 1 {
		t.Fatalf("repositories: %s", body)
	}
	r := out.Repositories[0]
	if r.Repo != "target" || r.Base != "main" || r.Ref != "lux/main" || r.Status != "fetched" || r.Behind != 1 ||
		!strings.Contains(r.Next, "git merge lux/main") {
		t.Errorf("answer: %+v", r)
	}
	if got := gitOut(t, work, "rev-parse", "HEAD"); got != before {
		t.Errorf("the checkout moved: %s, was %s", got, before)
	}
	if got := gitOut(t, work, "rev-parse", "refs/remotes/lux/main"); got != moved {
		t.Errorf("lux/main at %s, want %s", got, moved)
	}
	// With the Run's push credentials, in fetch mode.
	syncs := run.Syncs
	if len(syncs) != 1 || len(syncs[0]) != 1 || syncs[0][0].Mode != "fetch" || syncs[0][0].Ref != "main" {
		t.Errorf("syncs asked: %+v", syncs)
	}
}

// Only the roles that commit to the task branch have it.
func TestSyncBaseIsTheWritersOnly(t *testing.T) {
	w := newWorld(t)
	tools := w.toolsWithSync()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	spec := string(w.lux.Runs()[0].Spec)
	for _, role := range []string{"reviewer", "qa_browser", "investigator"} {
		mustExec(t, w.owner, `UPDATE runs SET role = $2::agent_role WHERE task_id = $1`, wi, role)
		if status, body := w.callTool(tools, spec, "sync_base", `{}`); status != 404 {
			t.Errorf("a %s: %d %s", role, status, body)
		}
	}
	for _, role := range []string{"implementer", "simplifier", "conductor"} {
		mustExec(t, w.owner, `UPDATE runs SET role = $2::agent_role WHERE task_id = $1`, wi, role)
		if status, body := w.callTool(tools, spec, "sync_base", `{}`); status != 200 {
			t.Errorf("a %s: %d %s", role, status, body)
		}
	}
}

// A lux without sync modes cannot fetch without moving the checkout: the
// agent is told so, and nothing is synced.
func TestSyncBaseOnALuxWithoutSyncModesIsRefused(t *testing.T) {
	w := newWorld(t)
	w.lux.NoSyncModes = true
	tools := w.toolsWithSync()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	run := w.lux.Runs()[0]
	status, body := w.callTool(tools, string(run.Spec), "sync_base", `{}`)
	if status != 422 || !strings.Contains(body, "this lux has no sync modes") {
		t.Errorf("sync_base: %d %s", status, body)
	}
	if len(run.Syncs) != 0 {
		t.Errorf("synced: %+v", run.Syncs)
	}
}
