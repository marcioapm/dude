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
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

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
	w.conflictWithMovedBase()
	w.until("the failed integration's escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	before := w.count(escalations, wi)
	if code, body := w.decide(w.person("owner"), wi, "wait", ""); code != 200 {
		t.Fatal(code, body)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(wi) })
	// GitHub's reading of the same heads is news again (dude read it as
	// unknown in between, as after a push), still conflicting.
	mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state='unknown', updated_at=now() WHERE task_id=$1`, wi)
	w.until("the pair's escalation", func() bool { w.sync(); return w.count(escalations, wi) == before+1 })
	if raw, _ := w.escalationOf(wi); !strings.Contains(string(raw), `"cause": "pair_attempted"`) {
		t.Fatalf("escalation %s", raw)
	}
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Fatalf("integration Runs %d", n)
	}
}

// Before the pull request: GitHub's merge conflicts, the integration Run
// fails, and the owner waits without moving the branch. The same pair
// escalates; no second Run.
func TestBeforePRAPersonsWaitNeverLoopsOnItsPair(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.task()
	w.deliver(wi)
	w.untilImplemented(wi)
	w.gh.AdvanceBase("main", "Base moved")
	w.mergeBaseConflictsAndItsIntegrationFails()
	w.until("failed integration", func() bool { return w.escalationReason(wi) == "pull_request_conflict" })
	before := w.count(escalations, wi)
	if code, body := w.decide(w.person("owner"), wi, "wait", ""); code != 200 {
		t.Fatal(code, body)
	}
	w.until("the pair's escalation", func() bool { return w.count(escalations, wi) == before+1 })
	if raw, _ := w.escalationOf(wi); !strings.Contains(string(raw), `"cause": "pair_attempted"`) {
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
	w.untilImplemented(wi)
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

// Busy delivery must accept the owner's request without starting another writer.
func TestResolveConflictsQueuesDuringCandidateReview(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.tell()
	w.conflictWithMovedBase()
	w.sync()
	mustExec(t, w.owner, `UPDATE workflow_runs SET step='awaitReview', awaiting_signals='[]',
		state=state-'escalation' WHERE task_id=$1`, wi)
	prID := w.prOf(wi)
	owner := w.person("owner")
	for range 2 {
		code, body := w.callAs(owner, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{})
		if code != 200 || body["status"] != "queued" {
			t.Fatalf("busy resolve: %d %v; want success with queued status", code, body)
		}
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND completed_at IS NULL`, wi); n != 1 {
		t.Fatalf("pending requests %d, want one", n)
	}
	if n := w.count(integrationRuns, wi); n != 0 {
		t.Fatalf("queued request started %d parallel integration Runs", n)
	}
	// A step writing its old snapshot cannot overwrite the independent row.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state=state-'pendingBaseSync' WHERE task_id=$1`, wi)
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND completed_at IS NULL`, wi); n != 1 {
		t.Fatalf("transition dropped request: %d", n)
	}
	w.gh.AdvanceBase("main", "Another base change on the same PR head")
	code, body := w.callAs(owner, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{})
	if code != 200 {
		t.Fatalf("new base: %d %v", code, body)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND completed_at IS NULL`, wi); n != 2 {
		t.Fatalf("new base on same head requests %d, want two", n)
	}
}

func TestQueuedBaseSyncTargetsWriterAndRetainsUntilAncestry(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.tell()
	w.conflictWithMovedBase()
	w.sync()
	mustExec(t, w.owner, `UPDATE workflow_runs SET step='awaitFix',awaiting_signals='[]',state=state-'escalation' WHERE task_id=$1`, wi)
	mustExec(t, w.owner, `INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,role,kind,phase)
 VALUES('run_queue_writer',$1,$2,$3,1,'running','implementer','agent','fix')`, w.org, w.project, wi)
	pr := w.prOf(wi)
	owner := w.person("owner")
	for range 2 {
		code, body := w.callAs(owner, "/internal/pull-requests/"+pr+"/resolve-conflicts", map[string]any{})
		if code != 200 {
			t.Fatalf("resolve: %d %v", code, body)
		}
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id='run_queue_writer' AND text LIKE '%call sync_base%merge lux/main%Do not rebase%'`); n != 1 {
		t.Fatalf("writer directives %d", n)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND completed_at IS NULL`, wi); n != 1 {
		t.Fatalf("directive consumed request: %d", n)
	}
	// A forge reading of the still-unintegrated public head is not completion.
	if err := w.prs.Sync(context.Background(), w.org, pr); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND completed_at IS NULL`, wi); n != 1 {
		t.Fatalf("unintegrated request retired: %d", n)
	}
	// The next writer's submitted prompt carries the request, claimed for
	// it (ClaimBaseSyncPromptTx), and no reviewer is told to write.
	mustExec(t, w.owner, `UPDATE runs SET status='completed' WHERE id='run_queue_writer'`)
	mustExec(t, w.owner, `UPDATE workflow_runs SET status='waiting', awaiting_signals='["queue_test_hold"]', wake_at=NULL WHERE task_id=$1`, wi)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = '{}'::jsonb WHERE id = $1`, w.project)
	w.onModel("implementer", "claude-impl")
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	next := w.freshFixer(wi, "queue-next-writer")
	w.until("the next writer submitted", func() bool { return w.luxRunOf(next) != "" })
	if prompt := w.submittedPrompt(w.luxRunOf(next)); !strings.Contains(prompt, "Queued conflict resolution") ||
		!strings.Contains(prompt, "call sync_base") || !strings.Contains(prompt, "merge lux/main") {
		t.Errorf("next writer's prompt lacks the request:\n%s", prompt)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND writer_run_id=$2 AND via='prompt'`, wi, next); n != 1 {
		t.Fatalf("request not claimed by the next writer's prompt: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id=d.run_id WHERE r.task_id=$1
		AND r.phase NOT IN ('implement','fix','simplify') AND d.text LIKE '%Queued conflict resolution%'`, wi); n != 0 {
		t.Fatalf("a non-writer was told to write: %d", n)
	}
	// A trusted forge comparison closes it only once the requested base is
	// actually an ancestor of the public head (not a candidate assertion).
	pull := w.gh.Pull(1)
	w.commitFileIn(w.gh.Repo, pull.Head, "queue-proof.txt", "proof\n", "proof")
	// Resetting this test public branch to main is a trusted ancestry fixture,
	// not an agent report; no workflow promotion is exercised here.
	gitOut(t, w.gh.Repo, "branch", "-f", pull.Head, "main")
	if err := w.prs.Sync(context.Background(), w.org, pr); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE task_id=$1 AND completed_at IS NOT NULL`, wi); n != 1 {
		t.Fatalf("ancestry did not complete request: %d", n)
	}
}

// Resolve conflicts is refused, saying why, unless the owner asks about a
// conflicting open pull request the task's workflow waits on or escalated.
func TestResolveConflictsRefusesWhatItCannotDo(t *testing.T) {
	setup := func(t *testing.T) (*world, string, string) {
		w := newWorld(t)
		w.toolsWithSync()
		wi := w.reviewing()
		w.tell()
		mustExec(t, w.owner, `UPDATE pull_requests SET mergeable_state='conflicting' WHERE task_id=$1`, wi)
		return w, wi, w.prOf(wi)
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
		"a stopped task with a stale live delivery": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET status='aborted', state=jsonb_set(state,'{halt}','{}') WHERE task_id=$1`, wi)
			return w.person("owner")
		}, 409, "dude is not waiting on this pull request"},
		"another escalation": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{escalation}',
				'{"reason": "ci_stuck", "detail": {"repo": "target", "number": 1}}') WHERE task_id=$1`, wi)
			return w.person("owner")
		}, 409, "a decision about something else"},
		// A pull request of an earlier attempt: this delivery waits on others.
		"a pull request the delivery does not wait on": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{pullRequestIds}', '["pr_earlier"]') WHERE task_id=$1`, wi)
			return w.person("owner")
		}, 409, "dude is not waiting on this pull request"},
		// A conflict escalation about the task's other pull request.
		"a conflict escalation about another pull request": {func(w *world, wi string) string {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{escalation}',
				'{"reason": "pull_request_conflict", "detail": {"repo": "other", "number": 1}}') WHERE task_id=$1`, wi)
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
		w.conflictWithMovedBase()
		w.until("the failed integration's escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
		if code, msg := resolve(w, w.person("owner"), w.prOf(wi)); code != 200 {
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
	prID := w.prOf(merging)
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

// A conductor running between turns holding only a riding reason (a
// checkout off the task branch) is neither selected to wake nor woken; held
// news alone still is, and a real reason takes the riding line with it.
func TestARidingReasonAloneWakesNoRunningConductor(t *testing.T) {
	w := newWorld(t)
	ride, held, real := w.task(), w.task(), w.task()
	for _, id := range []string{ride, held, real} {
		mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind, turn_done_at)
			VALUES($1,$2,$3,$4,1,'running','conductor','agent', now())`, "run_c_"+id, w.org, w.project, id)
	}
	for _, id := range []string{ride, real} {
		mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
			VALUES($1,$2,$3,'checkout',$4,'target: off the task branch.', now() - interval '1 hour')`,
			"cwk_ride_"+id, w.org, id, delivery.RideWakeKey("checkout:sync-1:target"))
	}
	mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES('cwk_held',$1,$2,'checkout','base-moved:pr_x','main moved.', now() - interval '1 hour')`, w.org, held)
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
	if want := []string{held, real}; !slices.Contains(selected, held) || !slices.Contains(selected, real) || slices.Contains(selected, ride) {
		t.Fatalf("selected to wake %v, want %v", selected, want)
	}
	wake := func(task string) string {
		var woken string
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
			if err = delivery.LockChat(context.Background(), tx, task); err != nil {
				return err
			}
			woken, err = delivery.WakeConductorTx(context.Background(), tx, w.org, task, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return woken
	}
	if woken := wake(ride); woken != "" || w.count(`SELECT count(*) FROM directives WHERE run_id=$1`, "run_c_"+ride) != 0 {
		t.Fatalf("the conductor was woken (%q) for a riding line alone", woken)
	}
	if woken := wake(real); woken != "run_c_"+real {
		t.Fatalf("a real reason woke %q", woken)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND strpos(text, 'off the task branch') > 0
		AND strpos(text, 'A decision waits') > 0`, "run_c_"+real); n != 1 {
		t.Errorf("the real reason's note does not carry the riding line")
	}
}
