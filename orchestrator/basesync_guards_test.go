package orchestrator_test

// Guards of base sync that an earlier test reached only by accident: the
// changed paths of an integration Run, the pair bound on a person's path
// and before the pull request, the one-writer rule before the pull
// request, feedback beside a stale conflict, Resolve conflicts' refusals,
// the base-moved filters and the parked conductor's hold.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

const integrationRuns = `SELECT count(*) FROM runs WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%'`

// integrationPaths is what the task's integration Run published as its
// changed paths in target, once it has.
func (w *world) integrationPaths(wi string) string {
	w.t.Helper()
	var paths string
	w.until("the integration Run published", func() bool {
		w.sync()
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(heads->'target'->>'changedPaths', '') FROM runs
			WHERE task_id=$1 AND pr_feedback::text LIKE '%Base integration%' AND status='completed'`, wi).Scan(&paths)
		return paths != ""
	})
	return paths
}

// An integration Run's changed paths are its resolution, never the base's
// own files: main changes FACTORY.md, which conflicts, and BASE_ONLY.md.
// So with a task continuing another base (State.Bases).
func TestAnIntegrationRunsChangedPathsAreItsResolution(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		w := newWorld(t)
		w.toolsWithSync()
		wi := w.reviewing()
		w.commitFileIn(w.gh.Repo, "main", "BASE_ONLY.md", "the base's own\n", "Base only")
		w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", "base intent\n", "Base change")
		w.sync()
		if paths := w.integrationPaths(wi); paths != `["FACTORY.md"]` {
			t.Fatalf("changed paths %s, want the resolution's FACTORY.md only", paths)
		}
	})
	t.Run("a base the task continues", func(t *testing.T) {
		w := newWorld(t)
		w.toolsWithSync()
		wi := w.reviewing()
		gitOut(t, w.gh.Repo, "update-ref", "refs/heads/release", w.gh.SHA("main"))
		w.commitFileIn(w.gh.Repo, "release", "RELEASE_ONLY.md", "the release's own\n", "Release only")
		w.commitFileIn(w.gh.Repo, "release", "FACTORY.md", "release intent\n", "Release change")
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{bases}', '{"target": "release"}') WHERE task_id = $1`, wi)
		w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
		w.sync()
		if paths := w.integrationPaths(wi); paths != `["FACTORY.md"]` {
			t.Fatalf("changed paths %s, want the resolution's FACTORY.md only", paths)
		}
		if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND synced_bases->'target' ? 'release'`, wi); n != 1 {
			t.Fatalf("%d Runs fetched release", n)
		}
	})
}

// An integration fails; the owner chooses wait without fixing anything, and
// GitHub reports the same pair conflicting again: it escalates, with no
// second Run.
func TestAPersonsWaitOnAFailedIntegrationNeverLoopsOnItsPair(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Crash: true} }
	w.gh.AdvanceBase("main", "Base moved")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("the failed integration's escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	before := w.count(escalations, wi)
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "wait"}); code != 200 {
		t.Fatal(code, body)
	}
	w.until("waiting on the pull request", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'`, wi) == 1
	})
	// GitHub's reading of the same heads is news again (dude read it as
	// unknown in between, as after a push), still conflicting.
	mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state='unknown', updated_at=now() WHERE task_id=$1`, wi)
	w.until("the pair's escalation", func() bool { w.sync(); return w.count(escalations, wi) == before+1 })
	if raw := w.escalationJSON(wi); !strings.Contains(raw, `"cause": "pair_attempted"`) {
		t.Fatalf("escalation %s", raw)
	}
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Fatalf("integration Runs %d", n)
	}
}

func (w *world) escalationJSON(wi string) string {
	w.t.Helper()
	var raw string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'escalation', 'null')::text FROM workflow_runs WHERE task_id=$1`, wi).Scan(&raw)
	return raw
}

// Before the pull request: GitHub's merge conflicts, the integration Run
// fails, and the owner waits without moving the branch. The same pair
// escalates; no second Run.
func TestBeforePRAPersonsWaitNeverLoopsOnItsPair(t *testing.T) {
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
	before := w.count(escalations, wi)
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "wait"}); code != 200 {
		t.Fatal(code, body)
	}
	w.until("the pair's escalation", func() bool { return w.count(escalations, wi) == before+1 })
	if raw := w.escalationJSON(wi); !strings.Contains(raw, `"cause": "pair_attempted"`) {
		t.Fatalf("escalation %s", raw)
	}
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Fatalf("integration Runs %d", n)
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened on a conflicting branch", n)
	}
}

// Before the pull request opens, GitHub is not asked to merge the base
// while a phase Run is still writing the task branch.
func TestBeforePRNoMergeWhileAWriterIsAtWork(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.task()
	w.deliver(wi)
	w.until("implemented", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='implement' AND status='completed'`, wi) == 1
	})
	mustExec(t, w.owner, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,phase,role)
		VALUES('run_writer_beforepr',$1,$2,$3,1,'paused','fix','implementer')`, w.org, w.project, wi)
	var merges atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/merges") {
				merges.Add(1)
			}
			return 0
		}
	})
	w.gh.AdvanceBase("main", "Base moved")
	w.until("before the pull request", func() bool {
		return merges.Load() > 0 || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='openPullRequest'`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := merges.Load(); n != 0 || len(w.gh.Pulls()) != 0 {
		t.Fatalf("%d merges asked, %d pull requests, while a writer was at work", n, len(w.gh.Pulls()))
	}
	mustExec(t, w.owner, `UPDATE runs SET status='aborted' WHERE id='run_writer_beforepr'`)
	w.until("the pull request after the writer", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := merges.Load(); n != 1 {
		t.Fatalf("merges asked %d", n)
	}
}

// A comment that came with a conflict GitHub no longer reports is fixed:
// one fix Run carries it, and it is not an integration.
func TestAStaleConflictsFeedbackIsFixed(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.gh.Comment(1, "alice", "Please rename the helper.")
	w.sync()
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	fixes := `SELECT count(*) FROM runs WHERE task_id=$1 AND phase='fix' AND pr_feedback::text LIKE '%rename the helper%'`
	w.until("the comment's fix", func() bool { return w.count(fixes, wi) == 1 })
	if n := w.count(fixes+` AND pr_feedback::text LIKE '%Base integration%'`, wi); n != 0 {
		t.Fatalf("%d integrations for a stale conflict", n)
	}
}

// Resolve conflicts is refused, saying why, unless the owner asks about a
// conflicting open pull request the task's workflow waits on or escalated.
func TestResolveConflictsRefusesWhatItCannotDo(t *testing.T) {
	setup := func(t *testing.T) (*world, string, string) {
		w := newWorld(t)
		w.toolsWithSync()
		wi := w.reviewing()
		mustExec(t, w.owner, `UPDATE forge_credentials SET settings='{"whenBehind":"tell"}' WHERE organization_id=$1`, w.org)
		mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state='conflicting' WHERE task_id=$1`, wi)
		var prID string
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id=$1`, wi).Scan(&prID)
		return w, wi, prID
	}
	resolve := func(w *world, actor, prID string) (int, string) {
		code, body := w.callAs(actor, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{})
		e, _ := body["error"].(map[string]any)
		msg, _ := e["message"].(string)
		return code, msg
	}
	for name, c := range map[string]struct {
		prepare func(w *world, wi string) string
		code    int
		says    string
	}{
		"someone not the owner": {func(w *world, wi string) string {
			ana, bo := w.person("Ana"), w.person("Bo")
			w.assignOwner(wi, ana)
			return bo
		}, 403, "only Ana"},
		"a merged pull request": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE pull_requests SET state='merged' WHERE task_id=$1`, wi)
			return w.person("owner")
		}, 409, "the pull request is merged"},
		"a delivery that ended": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET status='completed' WHERE task_id=$1`, wi)
			return w.person("owner")
		}, 409, "dude is not waiting on this pull request"},
		"another escalation": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{escalation}',
				'{"reason": "ci_stuck", "detail": {"repo": "target", "number": 1}}') WHERE task_id=$1`, wi)
			return w.person("owner")
		}, 409, "a decision about something else"},
	} {
		t.Run(name, func(t *testing.T) {
			w, wi, prID := setup(t)
			actor := c.prepare(w, wi)
			if code, msg := resolve(w, actor, prID); code != c.code || !strings.Contains(msg, c.says) {
				t.Fatalf("%d %q, want %d saying %q", code, msg, c.code, c.says)
			}
			for range 3 {
				w.pump()
			}
			if n := w.count(integrationRuns, wi); n != 0 {
				t.Fatalf("%d integrations after a refusal", n)
			}
		})
	}
	t.Run("a failed update integration is retried", func(t *testing.T) {
		w := newWorld(t)
		w.toolsWithSync()
		wi := w.reviewing()
		w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Crash: true} }
		w.gh.AdvanceBase("main", "Base moved")
		w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
		w.until("the failed integration's escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
		var prID string
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id=$1`, wi).Scan(&prID)
		if code, msg := resolve(w, w.person("owner"), prID); code != 200 {
			t.Fatalf("resolve: %d %s", code, msg)
		}
		w.until("a second integration Run", func() bool { w.sync(); return w.count(integrationRuns, wi) == 2 })
	})
}

// A merged pull request tells each other task writing to its base with a
// conductor, once: not the merging task, nor one with no conductor, nor
// one on another base, however often the merge is recorded.
func TestBaseMovedNoticeFiltersAndDedupes(t *testing.T) {
	w := newWorld(t)
	merging := w.reviewing()
	other, unconducted, onRelease := w.task(), w.task(), w.task()
	for _, id := range []string{other, unconducted, onRelease} {
		mustExec(t, w.owner, `INSERT INTO task_repositories(task_id, repository_id, access, organization_id) VALUES($1,$2,'write',$3)`, id, w.repoID, w.org)
		state := `{}`
		if id == onRelease {
			state = `{"bases": {"target": "release"}}`
		}
		mustExec(t, w.owner, `INSERT INTO workflow_runs(id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
			VALUES($1,$2,$3,$1,$4,'waiting','awaitPullRequest',$5::jsonb)`, "wf_"+id, w.org, delivery.WorkflowType, id, state)
	}
	for _, id := range []string{other, onRelease, merging} {
		mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind)
			VALUES($1,$2,$3,$4,1,'paused','conductor','agent')`, "run_c_"+id, w.org, w.project, id)
	}
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id=$1`, merging).Scan(&prID)
	w.gh.Merge(1)
	w.until("merge recorded", func() bool {
		if err := w.prs.Sync(context.Background(), w.org, prID); err != nil {
			t.Fatal(err)
		}
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND state='merged'`, merging) == 1
	})
	// Recorded again, as a replayed sync would.
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NotifyBaseMovedTx(context.Background(), tx, w.org, prID)
	}); err != nil {
		t.Fatal(err)
	}
	notices := `SELECT count(*) FROM events WHERE task_id=$1 AND event_type='chat.notice' AND payload->>'about'='base_moved'`
	for id, want := range map[string]int{other: 1, merging: 0, unconducted: 0, onRelease: 0} {
		if n := w.count(notices, id); n != want {
			t.Errorf("task %s: %d notices, want %d", id, n, want)
		}
		if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key LIKE 'base-moved:%'`, id); n != want {
			t.Errorf("task %s: %d wakes, want %d", id, n, want)
		}
	}
}

// A parked conductor holding only news that a base moved is neither
// selected to wake (Wakeable) nor resumed when woken (WakeConductorTx);
// one with a real reason beside it is.
func TestAParkedConductorIsNotWokenForABaseThatMoved(t *testing.T) {
	w := newWorld(t)
	held, real := w.task(), w.task()
	for _, id := range []string{held, real} {
		mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind, dude_pause)
			VALUES($1,$2,$3,$4,1,'paused','conductor','agent','conductor')`, "run_c_"+id, w.org, w.project, id)
		mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
			VALUES($1,$2,$3,'checkout','base-moved:pr_x','main moved.', now() - interval '1 hour')`, "cwk_held_"+id, w.org, id)
	}
	mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES('cwk_real',$1,$2,'decision','decision:next','A decision waits.', now() - interval '1 hour')`, w.org, real)
	var selected []string
	if err := w.app.InSystem(context.Background(), "test", func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT DISTINCT c.task_id FROM conductor_wakes c WHERE `+delivery.Wakeable+` ORDER BY 1`, 0.0)
		if err != nil {
			return err
		}
		selected, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(selected) != fmt.Sprint([]string{real}) {
		t.Fatalf("selected to wake %v, want only %s", selected, real)
	}
	var woken string
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		if err = delivery.LockChat(context.Background(), tx, held); err != nil {
			return err
		}
		woken, err = delivery.WakeConductorTx(context.Background(), tx, w.org, held, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if woken != "" || w.count(`SELECT count(*) FROM directives WHERE run_id=$1`, "run_c_"+held) != 0 {
		t.Fatalf("the parked conductor was woken (%q) for a base that moved", woken)
	}
}

