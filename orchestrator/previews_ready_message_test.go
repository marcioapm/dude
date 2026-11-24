package orchestrator_test

// "Ready for your look": a preview an agent started announces its first
// ready, once, in the conversation of the agent that started it.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// conductorOf gives task a live conductor Run, as Chat would.
func (w *world) conductorOf(task string) string {
	w.t.Helper()
	id := fmt.Sprintf("run_cond_%d", time.Now().UnixNano())
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status)
		VALUES ($1, $2, $3, $4, 1, 'conductor', 'agent', 'running')`, id, w.org, w.project, task)
	return id
}

// brainstormer is a session with its agent's live Run.
func (w *world) brainstormer() (session, run string) {
	w.t.Helper()
	n := time.Now().UnixNano()
	session, run = fmt.Sprintf("ses_ready_%d", n), fmt.Sprintf("run_bs_%d", n)
	mustExec(w.t, w.owner, `INSERT INTO sessions (id, organization_id, title) VALUES ($1, $2, 'Ideas')`, session, w.org)
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, role, kind, status)
		VALUES ($1, $2, $3, 1, 'brainstorm', 'agent', 'running')`, run, w.org, session)
	return session, run
}

// agentPreview is start_preview by agent run of its task.
func (w *world) agentPreview(task, run string) string {
	w.t.Helper()
	out, err := servers.AgentPreviews{Service: w.previews.Service}.StartPreview(context.Background(), w.org, task, run, ledger.ActorAgent, "")
	if err != nil {
		w.t.Fatal(err)
	}
	return out.ID
}

// withServers waits for preview id's servers to exist in lux.
func (w *world) withServers(id string) {
	w.t.Helper()
	w.until("the preview's servers", func() bool {
		return w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND deleted_at IS NULL`, id) > 0
	})
}

// unopened: no URL request asked preview id to wake (lux's
// server.wake_requested moves wake_event_id).
func (w *world) unopened(id string) {
	w.t.Helper()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_event_id = 0`, id); n != 1 {
		w.t.Fatalf("preview %s was woken by a request", id)
	}
}

// readyMessages are the "Ready for your look" messages of preview runID.
func (w *world) readyMessages(runID string) []readyMessage {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT COALESCE(run_id, ''), COALESCE(task_id, ''), COALESCE(session_id, ''),
			actor_type, payload->>'text' FROM events
		WHERE organization_id = $1 AND event_type = 'agent.message' AND payload->>'previewReady' = $2 ORDER BY cursor`, w.org, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []readyMessage
	for rows.Next() {
		var m readyMessage
		if err := rows.Scan(&m.run, &m.task, &m.session, &m.actor, &m.text); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

type readyMessage struct{ run, task, session, actor, text string }

// taskReadyText is a task preview's message, as its template renders it,
// for its server lines.
func taskReadyText(lines ...string) string {
	return "Ready for your look:\n- " + strings.Join(lines, "\n- ") + "\n\nCheck this task's change there, on its branch as it is now."
}

// quietAgentPreview is an agent's preview of task, started by run, whose
// servers exist but never become ready by themselves: the test says when
// (feedState).
func (w *world) quietAgentPreview(task, run string) (runID, web string) {
	w.t.Helper()
	runID = w.agentPreview(task, run)
	w.withServers(runID)
	return runID, w.serverID(runID, "web")
}

func (w *world) allReadyMessages() int {
	return w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'agent.message'
		AND payload ? 'previewReady'`, w.org)
}

func (w *world) feedState(serverID, state string, id int64) {
	w.t.Helper()
	if err := w.newFeed().Apply(context.Background(), lux.FeedEvent{ID: id, Type: "server.state", ServerID: serverID,
		Data: map[string]any{"state": state}}); err != nil {
		w.t.Fatal(err)
	}
}

func TestAConductorStartedPreviewSaysReadyOnceInItsTask(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.task()
	conductor := w.conductorOf(task)
	runID := w.agentPreview(task, conductor)
	w.withServers(runID)
	web := w.serverID(runID, "web")

	// Not ready: starting, waking, failed, stopped say nothing.
	for i, state := range []string{"starting", "waking", "failed", "stopped"} {
		w.feedState(web, state, int64(1<<41+i))
	}
	if n := w.allReadyMessages(); n != 0 {
		t.Fatalf("%d ready messages before ready", n)
	}

	// Nobody opens it: the agent's preview starts by itself.
	w.until("ready announced", func() bool { return len(w.readyMessages(runID)) == 1 })
	w.unopened(runID)
	got := w.readyMessages(runID)[0]
	url := w.str(`SELECT url FROM preview_servers WHERE run_id = $1 AND name = 'web'`, runID)
	if got.run != conductor || got.task != task || got.session != "" || got.actor != ledger.ActorAgent {
		t.Fatalf("said on run %q task %q session %q by %q, want the conductor %s in task %s", got.run, got.task, got.session, got.actor, conductor, task)
	}
	if want := taskReadyText("web — " + url); url == "" || got.text != want {
		t.Fatalf("text %q, want %q", got.text, want)
	}

	// Seen again: a replayed or later ready, another start_preview, a
	// park and a wake of the same preview.
	w.feedState(web, lux.ServerReady, 1<<42)
	w.feedState(web, lux.ServerReady, 1<<42+1)
	if again := w.agentPreview(task, conductor); again != runID {
		t.Fatalf("a repeated start_preview made %s, not %s", again, runID)
	}
	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	// Parked, it stays asleep: only its first start was automatic.
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wake_wanted_at IS NULL`, runID); n != 1 {
		t.Fatal("a parked agent preview woke by itself")
	}
	w.open(web)
	w.pump()
	if n := len(w.readyMessages(runID)); n != 1 || w.allReadyMessages() != 1 {
		t.Fatalf("%d ready messages for the preview, %d in all, want 1", n, w.allReadyMessages())
	}

	// A new preview of the task, once the first ended, starts and says its own.
	if _, err := w.previews.StopPreview(context.Background(), w.org, task, conductor, ledger.ActorAgent, ""); err != nil {
		t.Fatal(err)
	}
	w.until("the first preview over", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND ended_at IS NOT NULL`, runID) == 1
	})
	next := w.agentPreview(task, conductor)
	if next == runID {
		t.Fatal("no new preview")
	}
	w.until("the new preview announced", func() bool { return len(w.readyMessages(next)) == 1 })
	w.unopened(next)
	w.pump()
	if n := w.allReadyMessages(); n != 2 || len(w.readyMessages(runID)) != 1 {
		t.Fatalf("%d ready messages in all, want one per preview", n)
	}
}

func TestABrainstormStartedBranchPreviewSaysReadyInItsSession(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/look", "main", "a look")
	session, agent := w.brainstormer()
	// A task of the project with its conductor: not where the message goes.
	w.conductorOf(w.task())
	gh, err := w.previews.Forges.For(context.Background(), w.org)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.previews.StartBranchPreview(context.Background(), gh, w.org, w.project, "feature/look", agent, ledger.ActorAgent, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := w.allReadyMessages(); n != 0 {
		t.Fatalf("%d ready messages before ready", n)
	}
	// Nobody opens it.
	w.until("ready announced", func() bool { return len(w.readyMessages(b.ID)) == 1 })
	w.unopened(b.ID)
	got := w.readyMessages(b.ID)[0]
	if got.run != agent || got.session != session || got.task != "" {
		t.Fatalf("said on run %q session %q task %q, want %s in session %s", got.run, got.session, got.task, agent, session)
	}
	url := w.str(`SELECT url FROM preview_servers WHERE run_id = $1 AND name = 'web'`, b.ID)
	if !strings.Contains(url, previewDomain) {
		t.Fatalf("web's URL %q", url)
	}
	want := "Ready for your look:\n- web — " + url + "\n\nCheck `feature/look` there, as the branch is now."
	if got.text != want {
		t.Fatalf("text %q, want %q", got.text, want)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'agent.message' AND task_id IS NOT NULL`, w.org); n != 0 {
		t.Fatalf("%d agent messages in a task", n)
	}
	// A repeated preview_branch is the same preview: nothing more.
	if _, err := w.previews.StartBranchPreview(context.Background(), gh, w.org, w.project, "feature/look", agent, ledger.ActorAgent, ""); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if n := w.allReadyMessages(); n != 1 {
		t.Fatalf("%d ready messages, want 1", n)
	}
}

// A preview a person starts sleeps until opened, and says nothing when
// ready.
func TestAPersonStartedPreviewSleepsUntilOpenedAndSaysNothing(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	w.conductorOf(task) // a conductor there, which did not start it
	for range 10 {
		w.pump()
		time.Sleep(10 * time.Millisecond)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wake_wanted_at IS NULL
		AND lux_run_id IS NULL AND preview_ready_at IS NULL`, runID); n != 1 || len(w.luxRuns()) != 0 {
		t.Fatalf("a person's unopened preview woke (%d lux runs)", len(w.luxRuns()))
	}
	if n := w.allReadyMessages(); n != 0 {
		t.Fatalf("%d ready messages before it was opened", n)
	}
	w.open(w.serverID(runID, "web"))
	w.until("ready", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND preview_ready_at IS NOT NULL`, runID) == 1
	})
	w.pump()
	if n := w.allReadyMessages(); n != 0 {
		t.Fatalf("%d ready messages for a person's preview", n)
	}
}

// Two followers (two orchestrators) apply the same ready at once: one
// message.
func TestTwoFollowersOfOneReadySayItOnce(t *testing.T) {
	w := newWorld(t)
	// Wakeable, with no follower running: the test is the followers.
	w.lux.PreviewDomain = previewDomain
	w.lux.IdleCheck = time.Hour
	w.previews.PreviewDomain = previewDomain
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.task()
	runID := w.agentPreview(task, w.conductorOf(task))
	w.withServers(runID)
	web := w.serverID(runID, "web")
	const followers = 4
	var wg sync.WaitGroup
	errs := make(chan error, followers)
	start := make(chan struct{})
	for i := range followers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- w.newFeed().Apply(context.Background(), lux.FeedEvent{ID: 1 << 43, Type: "server.state", ServerID: web,
				Data: map[string]any{"state": lux.ServerReady}})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.readyMessages(runID)); n != 1 {
		t.Fatalf("%d ready messages, want 1", n)
	}
}

// A preview that failed or ended before its servers say ready says
// nothing: only a live preview is announced.
func TestAnEndedPreviewSaysNothingWhenReady(t *testing.T) {
	for _, end := range []string{"failed", "stopped"} {
		t.Run(end, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("web", 3000, "fakelux-never-ready", "", nil, true)
			task := w.task()
			conductor := w.conductorOf(task)
			runID, web := w.quietAgentPreview(task, conductor)
			if end == "failed" {
				mustExec(t, w.owner, `UPDATE runs SET status = 'failed', ended_at = now() WHERE id = $1`, runID)
			} else if _, err := w.previews.StopPreview(context.Background(), w.org, task, conductor, ledger.ActorAgent, ""); err != nil {
				t.Fatal(err)
			}
			w.feedState(web, lux.ServerReady, 1<<44)
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND preview_ready_at IS NOT NULL`, runID); n != 1 {
				t.Fatal("the ready was not applied")
			}
			if n := w.allReadyMessages(); n != 0 {
				t.Fatalf("%d ready messages for a %s preview", n, end)
			}
		})
	}
}

// The conductor that started a task's preview ended and the task has a
// newer one before the preview is ready: the latest says it, in the task.
func TestAPreviewReadyAfterItsConductorEndedIsSaidByTheLatest(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "fakelux-never-ready", "", nil, true)
	task := w.task()
	starter := w.conductorOf(task)
	runID, web := w.quietAgentPreview(task, starter)
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, starter)
	latest := w.conductorOf(task)
	w.feedState(web, lux.ServerReady, 1<<44)
	got := w.readyMessages(runID)
	if len(got) != 1 {
		t.Fatalf("%d ready messages, want 1", len(got))
	}
	if got[0].run != latest || got[0].task != task || got[0].session != "" {
		t.Fatalf("said on run %q task %q session %q, want the latest conductor %s (not %s) in task %s",
			got[0].run, got[0].task, got[0].session, latest, starter, task)
	}
}

// A server deleted from the preview is not listed.
func TestADeletedPreviewServerIsNotListed(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "fakelux-never-ready", "", nil, true)
	w.recipe("api", 4000, "fakelux-never-ready", "api", nil, true)
	task := w.task()
	runID, web := w.quietAgentPreview(task, w.conductorOf(task))
	w.until("both servers", func() bool {
		return w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND deleted_at IS NULL`, runID) == 2
	})
	mustExec(t, w.owner, `UPDATE preview_servers SET deleted_at = now() WHERE run_id = $1 AND name = 'api'`, runID)
	w.feedState(web, lux.ServerReady, 1<<44)
	got := w.readyMessages(runID)
	url := w.str(`SELECT url FROM preview_servers WHERE run_id = $1 AND name = 'web'`, runID)
	if want := taskReadyText("web — " + url); len(got) != 1 || got[0].text != want {
		t.Fatalf("messages %+v, want one %q", got, want)
	}
}
