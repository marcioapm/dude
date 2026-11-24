package orchestrator_test

// Automatic task previews (migration 272): when dude opens a pull request
// for a task, or a person reopens one dude opened, the task's preview is
// prepared asleep — if the task (or, when it says nothing, its project)
// wants one. Through the real delivery store, PR syncer and preview
// service, against the fake GitHub and lux.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// autoWorld is a world whose previews wake, with a web server to start in
// previews, the delivery store and PR syncer wired to AutoPreview as the
// orchestrator's main wires them, and the project's setting as given.
func autoWorld(t *testing.T, projectOn bool) *world {
	w := readyWorld(t, "npm run dev")
	w.store.AutoPreview = w.previews.AutoPreview
	w.prs.AutoPreview = w.previews.AutoPreview
	mustExec(t, w.owner, `UPDATE projects SET auto_task_preview = $2 WHERE id = $1`, w.project, projectOn)
	return w
}

// taskOverride sets task's own choice: nil follows the project.
func (w *world) taskOverride(task string, on *bool) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE tasks SET auto_task_preview = $2 WHERE id = $1`, task, on)
}

// openTaskPR is the delivery's pull request step for task: its branch on
// the fake GitHub with one commit, opened and recorded by the store's own
// OpenPullRequests. The pull request's id and number.
func (w *world) openTaskPR(task string) (id string, number int) {
	w.t.Helper()
	ctx := context.Background()
	branch := delivery.BranchFor(task, 1)
	sha := w.gh.Branch(branch, "main", "work on "+task)
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error { return delivery.NameOnlyRepository(ctx, tx, task) }); err != nil {
		w.t.Fatal(err)
	}
	st := &delivery.State{TaskID: task, ProjectID: w.project, Branch: branch, Heads: map[string]string{"target": sha}}
	ids, err := w.store.OpenPullRequests(ctx, w.org, st, forge.Resolver{DB: w.app})
	if err != nil || len(ids) != 1 {
		w.t.Fatalf("open the pull request: %v %v", ids, err)
	}
	return ids[0], w.count(`SELECT number FROM pull_requests WHERE id = $1`, ids[0])
}

// replayOpen is the same pull request step again (a workflow replay).
func (w *world) replayOpen(task string) {
	w.t.Helper()
	branch := delivery.BranchFor(task, 1)
	st := &delivery.State{TaskID: task, ProjectID: w.project, Branch: branch, Heads: map[string]string{"target": w.gh.SHA(branch)}}
	if _, err := w.store.OpenPullRequests(context.Background(), w.org, st, forge.Resolver{DB: w.app}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) syncPRByID(id string) {
	w.t.Helper()
	if err := w.prs.Sync(context.Background(), w.org, id); err != nil {
		w.t.Fatal(err)
	}
}

// previews counts task's preview Runs, and its live ones.
func (w *world) taskPreviews(task string) (all, live int) {
	w.t.Helper()
	all = w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'preview'`, task)
	live = w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'preview' AND ended_at IS NULL
		AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, task)
	return all, live
}

// livePreviewID is task's one live preview.
func (w *world) livePreviewID(task string) string {
	return w.str(`SELECT id FROM runs WHERE task_id = $1 AND kind = 'preview' AND ended_at IS NULL
		AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, task)
}

func (w *world) noTaskPreview(task, why string) {
	w.t.Helper()
	for range 3 {
		w.pump()
	}
	if all, _ := w.taskPreviews(task); all != 0 {
		w.t.Fatalf("%s: %d previews, want none", why, all)
	}
}

// dormant: preview runID was made by the system, asleep: no wake asked
// for, no lux Run placed, no ready message — until its URL is opened.
func (w *world) dormant(runID string) {
	w.t.Helper()
	w.withServers(runID)
	for range 10 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wakeable AND wake_wanted_at IS NULL AND lux_run_id IS NULL`, runID); n != 1 {
		w.t.Fatalf("the automatic preview %s is not asleep", runID)
	}
	if n := len(w.luxRuns()); n != 0 {
		w.t.Fatalf("lux placed %d runs for a dormant preview", n)
	}
	if n := w.allReadyMessages(); n != 0 {
		w.t.Fatalf("%d ready messages for a dormant preview", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND actor_type = $2 AND actor_id = $3`,
		runID, ledger.ActorSystem, servers.AutoPreviewActor); n != 1 {
		w.t.Fatal("the automatic preview was not made by the system as auto-preview")
	}
}

// 1. Off by default: dude opening a task's pull request prepares nothing.
func TestAutoPreviewOffByDefaultPreparesNothing(t *testing.T) {
	w := readyWorld(t, "npm run dev")
	w.store.AutoPreview = w.previews.AutoPreview
	w.prs.AutoPreview = w.previews.AutoPreview
	if n := w.count(`SELECT count(*) FROM projects WHERE id = $1 AND NOT auto_task_preview`, w.project); n != 1 {
		t.Fatal("a project's automatic task previews are not off by default")
	}
	task := w.task()
	if n := w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND auto_task_preview IS NULL`, task); n != 1 {
		t.Fatal("a task's choice does not follow its project by default")
	}
	w.openTaskPR(task)
	w.noTaskPreview(task, "project off")
}

// 2. On: one dormant task preview, made after the pull request's record
// committed, which wakes when its URL is opened.
func TestAutoPreviewOnPreparesOneDormantTaskPreview(t *testing.T) {
	w := autoWorld(t, true)
	task := w.task()
	committed := -1
	w.store.AutoPreview = func(ctx context.Context, org, taskID string) error {
		// Read on another connection: only a committed record is seen.
		committed = w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1`, taskID)
		return w.previews.AutoPreview(ctx, org, taskID)
	}
	w.openTaskPR(task)
	if committed != 1 {
		t.Fatalf("the preview was prepared before the pull request's record committed (%d seen)", committed)
	}
	all, live := w.taskPreviews(task)
	if all != 1 || live != 1 {
		t.Fatalf("%d previews (%d live), want one", all, live)
	}
	runID := w.livePreviewID(task)
	if n := w.count(`SELECT count(*) FROM runs WHERE preview_branch IS NOT NULL AND project_id = $1`, w.project); n != 0 {
		t.Fatalf("%d branch previews for a task's pull request", n)
	}
	w.dormant(runID)
	w.open(w.serverID(runID, "web"))
	w.until("ready once opened", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND preview_ready_at IS NOT NULL`, runID) == 1
	})
	w.pump()
	if n := w.allReadyMessages(); n != 0 {
		t.Fatalf("%d ready messages for an automatic preview opened by a person", n)
	}
}

// 3. The conductor's preview, live already, is the one: the same Run, one
// live preview, one ready message.
func TestAutoPreviewReusesTheConductorsPreview(t *testing.T) {
	w := autoWorld(t, true)
	task := w.task()
	conductor := w.conductorOf(task)
	runID, _ := w.agentPreviewWeb(task, conductor)
	w.until("ready announced", func() bool { return len(w.readyMessages(runID)) == 1 })
	w.openTaskPR(task)
	for range 5 {
		w.pump()
	}
	all, live := w.taskPreviews(task)
	if all != 1 || live != 1 || w.livePreviewID(task) != runID {
		t.Fatalf("%d previews (%d live), want the conductor's %s alone", all, live, runID)
	}
	if n := w.allReadyMessages(); n != 1 {
		t.Fatalf("%d ready messages, want the conductor's one", n)
	}
}

// 4. The task's own choice wins; null follows the project.
func TestAutoPreviewFollowsTheTaskThenTheProject(t *testing.T) {
	for _, c := range []struct {
		name      string
		project   bool
		task      *bool
		wantMaybe bool
	}{
		{"task off, project on", true, ptr(false), false},
		{"task on, project off", false, ptr(true), true},
		{"task null, project on", true, nil, true},
		{"task null, project off", false, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := autoWorld(t, c.project)
			task := w.task()
			w.taskOverride(task, c.task)
			w.openTaskPR(task)
			if !c.wantMaybe {
				w.noTaskPreview(task, c.name)
				return
			}
			if all, live := w.taskPreviews(task); all != 1 || live != 1 {
				t.Fatalf("%d previews (%d live), want one", all, live)
			}
			w.dormant(w.livePreviewID(task))
		})
	}
}

// 5. A task's Off is about automatic preparation only: a person and the
// conductor still start its preview.
func TestAutoPreviewTaskOffDoesNotBlockAnExplicitPreview(t *testing.T) {
	w := autoWorld(t, true)
	person := w.task()
	w.taskOverride(person, ptr(false))
	code, out := w.do("POST", "/internal/tasks/"+person+"/preview", nil)
	if code != 201 {
		t.Fatalf("a person's start = %d %v", code, out)
	}
	if _, live := w.taskPreviews(person); live != 1 {
		t.Fatal("a person's preview of a task that is off did not start")
	}
	agent := w.task()
	w.taskOverride(agent, ptr(false))
	runID := w.agentPreview(agent, w.conductorOf(agent))
	if _, live := w.taskPreviews(agent); live != 1 || runID == "" {
		t.Fatal("the conductor's preview of a task that is off did not start")
	}
	// It is the conductor's, awake: not made asleep by the setting.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL`, runID); n != 1 {
		t.Fatal("the conductor's preview did not wake as it is made")
	}
}

// 6. Reopened: a pull request dude opened, closed and reopened, ensures the
// task's one preview — kept if live, made again if it ended. A sync of an
// open pull request, a push to it, and a replay of the step make nothing.
func TestAutoPreviewOnReopenEnsuresOnePreview(t *testing.T) {
	w := autoWorld(t, true)
	task := w.task()
	id, number := w.openTaskPR(task)
	first := w.livePreviewID(task)

	// Still live: closed and reopened, the same preview.
	w.gh.Close(number)
	w.syncPRByID(id)
	w.gh.Reopen(number)
	w.syncPRByID(id)
	if all, live := w.taskPreviews(task); all != 1 || live != 1 || w.livePreviewID(task) != first {
		t.Fatalf("%d previews (%d live) after a reopen with one live, want %s alone", all, live, first)
	}

	// Ended while open: a plain sync, a push and a replay make nothing.
	if _, err := w.previews.StopPreview(context.Background(), w.org, task, "person", ledger.ActorHuman, ""); err != nil {
		t.Fatal(err)
	}
	w.syncPRByID(id)
	w.gh.CommitOnTop(delivery.BranchFor(task, 1), "a person's push")
	w.syncPRByID(id)
	w.syncPRByID(id)
	w.replayOpen(task)
	if all, live := w.taskPreviews(task); all != 1 || live != 0 {
		t.Fatalf("%d previews (%d live) after syncs, a push and a replay, want the ended one alone", all, live)
	}

	// Ended, closed and reopened: a new one, asleep.
	w.gh.Close(number)
	w.syncPRByID(id)
	if all, _ := w.taskPreviews(task); all != 1 {
		t.Fatal("closing the pull request prepared a preview")
	}
	w.gh.Reopen(number)
	w.syncPRByID(id)
	w.syncPRByID(id) // open again, and synced again: nothing more
	all, live := w.taskPreviews(task)
	if all != 2 || live != 1 || w.livePreviewID(task) == first {
		t.Fatalf("%d previews (%d live) after reopening, want a new one beside the ended", all, live)
	}
	w.dormant(w.livePreviewID(task))
}

// 7. A pull request dude took over from a person (adopted) is not one dude
// opened: its reopening prepares nothing. Nor does a branch preview of
// the project, a brainstorm's, make a task preview.
func TestAutoPreviewNotForAdoptedPullRequestsOrBranches(t *testing.T) {
	w := autoWorld(t, true)
	task := w.task()
	number := w.gh.OpenAsPerson("someone", "feature/theirs", "main", "Their work")
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		head_branch, base_branch, head_sha, title, state, adopted)
		VALUES ('pr_adopted', $1, $2, $3, $4, $5, 'https://github.com/acme/target/pull/1', 'feature/theirs', 'main', $6, 'Their work',
		'closed', true)`, w.org, w.project, task, w.repoID, number, w.gh.SHA("feature/theirs"))
	w.syncPRByID("pr_adopted")
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE id = 'pr_adopted' AND state = 'open'`); n != 1 {
		t.Fatal("the adopted pull request's reopening was not recorded")
	}
	w.noTaskPreview(task, "an adopted pull request reopened")

	w.gh.Branch("feature/look", "main", "a look")
	_, agent := w.brainstormer()
	w.agentBranchPreview("feature/look", agent)
	if n := w.count(`SELECT count(*) FROM runs WHERE kind = 'preview' AND task_id IS NOT NULL AND project_id = $1`, w.project); n != 0 {
		t.Fatalf("%d task previews from a branch preview", n)
	}
}

// A finished task, and a project with nothing to start in previews, are
// quiet no-ops: no preview, no error, the pull request recorded.
func TestAutoPreviewSkipsQuietlyWithoutAServerOrForAFinishedTask(t *testing.T) {
	w := autoWorld(t, true)
	done := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, done)
	if err := w.previews.AutoPreview(context.Background(), w.org, done); err != nil {
		t.Fatalf("a finished task: %v", err)
	}
	w.noTaskPreview(done, "a finished task")

	mustExec(t, w.owner, `UPDATE project_servers SET autostart_in_previews = false WHERE project_id = $1`, w.project)
	task := w.task()
	w.openTaskPR(task)
	w.noTaskPreview(task, "no server to start in previews")
	if n := len(w.luxRuns()); n != 0 {
		t.Fatalf("%d lux runs placed for a project with nothing to start", n)
	}
}

// syncBuffer is a log sink a test reads while the loops write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// 8. A failing preparation is logged and fails nothing: the pull request
// is recorded on open, and its reopening synced.
func TestAutoPreviewFailureLeavesThePullRequestRecorded(t *testing.T) {
	w := autoWorld(t, true)
	var logs syncBuffer
	svc := *w.previews.Service
	svc.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// The real preparation, on a context already over: its database work fails.
	failing := func(ctx context.Context, org, taskID string) error {
		over, cancel := context.WithCancel(ctx)
		cancel()
		return svc.AutoPreview(over, org, taskID)
	}
	w.store.AutoPreview, w.prs.AutoPreview = failing, failing
	task := w.task()
	id, number := w.openTaskPR(task) // fails the test if OpenPullRequests errs
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvPullRequestOpened); n != 1 {
		t.Fatal("the pull request's opening was not recorded")
	}
	w.gh.Close(number)
	w.syncPRByID(id)
	w.gh.Reopen(number)
	if err := w.prs.Sync(context.Background(), w.org, id); err != nil {
		t.Fatalf("a failed preparation failed the sync: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND state = 'open'`, id); n != 1 {
		t.Fatal("the reopening was not recorded")
	}
	if got := strings.Count(logs.String(), "automatic task preview failed"); got != 2 {
		t.Fatalf("%d failures logged, want 2:\n%s", got, logs.String())
	}
	if all, _ := w.taskPreviews(task); all != 0 {
		t.Fatalf("%d previews from a failed preparation", all)
	}
}
