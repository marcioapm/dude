package orchestrator_test

// Keeping a task current with its base: sync_base, the integration Run a
// conflict starts, the merge before a pull request opens, and what a merge
// of the base counts as.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
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
	return tools.URL
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
