package chat_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// routedEpic is an epic routed from a mention in #support, with two tasks
// in the Backlog: the epic, its tasks in order, and the thread's root.
func (w *world) routedEpic() (epic string, tasks []string, root string) {
	w.t.Helper()
	w.model.queue(answer(map[string]any{"route": "epic", "confidence": 0.9, "reasoning": "two pieces", "project": "BIL",
		"new_epic": map[string]any{"title": "CSV exports", "description": "Make exports fast.", "tasks": []map[string]any{
			{"ref": "t1", "title": "Measure export time", "goal": "g", "blocked_by": []string{}},
			{"ref": "t2", "title": "Stream the response", "goal": "g", "blocked_by": []string{}}}}}))
	root = w.mention("Márcia", "C1", "", "open an epic for fast CSV exports")
	reqs := w.requests()
	if last := reqs[len(reqs)-1]; last.ResultKind != "epic" {
		w.t.Fatalf("routed to %+v", last)
	}
	epic = reqs[len(reqs)-1].ResultID
	for _, title := range []string{"Measure export time", "Stream the response"} {
		tasks = append(tasks, w.one(`SELECT id FROM tasks WHERE epic_id = $1 AND title = $2`, epic, title))
	}
	return epic, tasks, root
}

// epicNotices are dude's epic notices in thread, in the order posted.
func (w *world) epicNotices(channel, root string) []string {
	w.t.Helper()
	var out []string
	for _, m := range w.fake.BotMessages() {
		if m.Channel == channel && m.ThreadTS == root && (strings.HasPrefix(m.Text, "Started the epic ") ||
			strings.HasPrefix(m.Text, "Opened the first pull request for the epic ") || strings.HasPrefix(m.Text, "The epic ")) {
			out = append(out, m.Text)
		}
	}
	return out
}

func (w *world) moveRecorded(task, status, reason string) {
	w.t.Helper()
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		return delivery.RecordStatusTx(w.ctx, tx, w.org, w.project, task, status, reason)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// An epic routed from a thread tells it once when its first task starts,
// once when its first pull request opens, and once when every task has
// ended, with the counts; its tasks' own notices go on as they are. The
// moves written again and the loop run twice say nothing new.
func TestChatEpicNoticesPostStartedPROpenedAndFinishedOnceEachInOrder(t *testing.T) {
	w := setup(t)
	epic, tasks, root := w.routedEpic()
	for round := range 2 {
		w.move(tasks[0], "running", "planning with the conductor")
		w.moveRecorded(tasks[0], "running", "restarted")
		w.postNotices()
		w.postNotices()
		w.move(tasks[1], "running", "planning with the conductor")
		w.prOpened(tasks[1])
		w.prOpened(tasks[0])
		w.postNotices()
		w.move(tasks[0], "done", "pull requests merged")
		w.postNotices()
		if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); round == 0 && n != 0 {
			t.Fatalf("finished with a task still running")
		}
		w.move(tasks[1], "done", "pull requests merged")
		w.moveRecorded(tasks[1], "done", "pull requests merged")
		w.postNotices()
		w.postNotices()
	}
	want := []string{"Started the epic BIL › CSV exports.", "Opened the first pull request for the epic BIL › CSV exports.",
		"The epic BIL › CSV exports finished: 2 done, 0 failed, 0 cancelled."}
	if got := w.epicNotices("C1", root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the thread got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The tasks' own notices are unchanged: three each.
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = ANY($1) AND posted_at IS NOT NULL`, tasks); n != 6 {
		t.Errorf("%d task notices posted, want 6", n)
	}
	// Each epic notice opens the epic's page, never a repository.
	for _, m := range w.fake.BotMessages() {
		if strings.Contains(m.Text, "the epic ") && !strings.Contains(string(m.Blocks), "https://dude.example.com/#/o/acme/epic/"+epic) {
			t.Errorf("an epic notice without the epic's link: %s", m.Blocks)
		}
		if strings.Contains(string(m.Blocks), "github.com") {
			t.Errorf("a notice names the repository: %+v", m)
		}
	}
	// The epic's started notice follows its first task's.
	var order []string
	for _, m := range w.fake.BotMessages() {
		if m.ThreadTS == root && strings.HasPrefix(m.Text, "Started ") {
			order = append(order, m.Text)
		}
	}
	if len(order) != 3 || order[0] != "Started BIL-1 Measure export time." || order[1] != "Started the epic BIL › CSV exports." {
		t.Errorf("starts in the thread: %q", order)
	}
}

// One task done and one failed: the summary counts both, and nothing calls
// the epic a success.
func TestChatEpicNoticeOfAMixedOutcomeCountsEachEnd(t *testing.T) {
	w := setup(t)
	_, tasks, root := w.routedEpic()
	w.move(tasks[0], "running", "planning with the conductor")
	w.move(tasks[0], "done", "pull requests merged")
	w.move(tasks[1], "running", "planning with the conductor")
	w.move(tasks[1], "failed", "lux: exit 1")
	w.postNotices()
	got := w.epicNotices("C1", root)
	if len(got) != 2 || got[1] != "The epic BIL › CSV exports finished: 1 done, 1 failed, 0 cancelled." {
		t.Fatalf("the thread got %q", got)
	}
	for _, word := range []string{"success", "Done:", "completed"} {
		if strings.Contains(got[1], word) {
			t.Errorf("a mixed epic is called %q: %s", word, got[1])
		}
	}
}

// A task archived from the epic is not one of its tasks: it neither counts
// in the summary nor makes the epic finished.
func TestChatEpicNoticeCountsOnlyTheEpicsCurrentTasks(t *testing.T) {
	w := setup(t)
	epic, tasks, root := w.routedEpic()
	w.move(tasks[0], "running", "planning with the conductor")
	w.move(tasks[0], "aborted", "closed as won't do")
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); n != 0 {
		t.Fatalf("finished with a task in the Backlog")
	}
	w.exec(`UPDATE tasks SET resolution = 'wont_do', archived_at = now() WHERE id = $1`, tasks[0])
	w.move(tasks[1], "running", "planning with the conductor")
	w.move(tasks[1], "done", "pull requests merged")
	w.postNotices()
	got := w.epicNotices("C1", root)
	if len(got) != 2 || got[1] != "The epic BIL › CSV exports finished: 1 done, 0 failed, 0 cancelled." {
		t.Fatalf("the thread got %q", got)
	}
}

// An epic whose routed tasks are all cancelled says so, and an epic made
// with no tasks at all is never finished by anything.
func TestChatEpicNoticeOfAnEmptyEpicNeverFinishes(t *testing.T) {
	w := setup(t)
	empty := "epc_empty_" + w.org
	w.exec(`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES ($1, $2, $3, 'Empty',
		'{"provider":"slack","workspaceId":"T1","conversationId":"C1","conversationName":"#support","threadId":"1.0"}')`, empty, w.org, w.project)
	// A task outside it ending says nothing of it.
	task, _ := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	w.move(task, "done", "pull requests merged")
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1`, empty); n != 0 {
		t.Fatalf("%d notices for an empty epic", n)
	}
}

// Once finished, reopening a task or adding one and ending it again never
// gives a second finished notice, nor a second started one.
func TestChatEpicNoticeFinishedIsNeverRepeated(t *testing.T) {
	w := setup(t)
	epic, tasks, root := w.routedEpic()
	for _, task := range tasks {
		w.move(task, "running", "planning with the conductor")
		w.prOpened(task)
		w.move(task, "done", "pull requests merged")
	}
	w.postNotices()
	// Reopened and delivered again.
	w.exec(`UPDATE tasks SET status = 'received' WHERE id = $1`, tasks[0])
	w.move(tasks[0], "running", "planning with the conductor")
	w.move(tasks[0], "failed", "lux: exit 1")
	// A task added since, delivered.
	added := "wi_added_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin)
		SELECT $1, $2, $3, 9, $4, 'Added', 'received', origin FROM epics WHERE id = $4`, added, w.org, w.project, epic)
	w.move(added, "running", "planning with the conductor")
	w.prOpened(added)
	w.move(added, "aborted", "a person stopped the task")
	w.postNotices()
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1`, epic); n != 3 {
		t.Fatalf("%d epic notices recorded, want 3", n)
	}
	want := []string{"Started the epic BIL › CSV exports.", "Opened the first pull request for the epic BIL › CSV exports.",
		"The epic BIL › CSV exports finished: 2 done, 0 failed, 0 cancelled."}
	if got := w.epicNotices("C1", root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the thread got %q", got)
	}
}

// Two tasks of one epic ending at once, in separate transactions, give
// exactly one finished notice: never none, never two.
func TestChatEpicNoticeTwoTasksEndingConcurrentlyFinishOnce(t *testing.T) {
	w := setup(t)
	origin := `{"provider":"slack","workspaceId":"T1","conversationId":"C1","conversationName":"#support","threadId":"1.0"}`
	for i := range 20 {
		epic := fmt.Sprintf("epc_race%d_%s", i, w.org)
		w.exec(`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES ($1, $2, $3, 'Race', $4::jsonb)`, epic, w.org, w.project, origin)
		var tasks []string
		for j := range 2 {
			id := fmt.Sprintf("wi_race%d_%d_%s", i, j, w.org)
			w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin)
				VALUES ($1, $2, $3, $4, $5, 'Racer', 'running', $6::jsonb)`, id, w.org, w.project, 100+i*2+j, epic, origin)
			tasks = append(tasks, id)
		}
		var (
			ready, wg sync.WaitGroup
			start     = make(chan struct{})
			moved     = make(chan struct{}, 2)
			errs      = make(chan error, 2)
		)
		ready.Add(2)
		wg.Add(2)
		for j, task := range tasks {
			status := []string{"done", "failed"}[j]
			go func() {
				defer wg.Done()
				errs <- w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
					// Both transactions open before either moves.
					if _, err := tx.Exec(w.ctx, `SELECT 1`); err != nil {
						return err
					}
					ready.Done()
					<-start
					if _, err := delivery.SetTaskStatusTx(w.ctx, tx, w.org, w.project, task, "", status, "pull requests merged"); err != nil {
						return err
					}
					// Each holds its commit until the other has counted too, or
					// a while: unserialised counts both see the other running.
					moved <- struct{}{}
					deadline := time.After(300 * time.Millisecond)
					for len(moved) < 2 {
						select {
						case <-deadline:
							return nil
						case <-time.After(5 * time.Millisecond):
						}
					}
					return nil
				})
			}()
		}
		ready.Wait()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); n != 1 {
			t.Fatalf("round %d: %d finished notices, want 1", i, n)
		}
		if d := w.one(`SELECT detail FROM chat_notices WHERE epic_id = $1`, epic); d != "1 done, 1 failed, 0 cancelled" {
			t.Fatalf("round %d: finished as %q", i, d)
		}
	}
}

// An epic made in dude, with its tasks, says nothing anywhere.
func TestChatEpicNoticesNothingForAWebFiledEpic(t *testing.T) {
	w := setup(t)
	epic := "epc_exp_" + w.org
	task := "wi_web_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status) VALUES ($1, $2, $3, 9, $4, 'From the web', 'received')`,
		task, w.org, w.project, epic)
	w.move(task, "running", "planning with the conductor")
	w.prOpened(task)
	w.move(task, "done", "pull requests merged")
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE organization_id = $1`, w.org); n != 0 {
		t.Errorf("%d notices recorded", n)
	}
	if n := w.postNotices(); n != 0 || len(w.fake.BotMessages()) != 0 {
		t.Errorf("posted %d: %+v", n, w.fake.BotMessages())
	}
}

// An epic filed from a chat-born session's card (via the session) tells
// the session's thread.
func TestChatEpicNoticesOfAnEpicFiledFromAChatBornSessionPostInItsThread(t *testing.T) {
	w := setup(t)
	root, session := w.sessionMention()
	epic := "epc_filed_" + w.org
	w.exec(`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES ($1, $2, $3, 'Filed',
		jsonb_build_object('provider', 'slack', 'via', 'session', 'from', $4::text))`, epic, w.org, w.project, session)
	task := "wi_filed_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin) SELECT $1, $2, $3, 9, $4, 'Filed task',
		'received', origin FROM epics WHERE id = $4`, task, w.org, w.project, epic)
	w.move(task, "running", "planning with the conductor")
	w.move(task, "done", "pull requests merged")
	w.postNotices()
	want := []string{"Started the epic BIL › Filed.", "The epic BIL › Filed finished: 1 done, 0 failed, 0 cancelled."}
	if got := w.epicNotices("C1", root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the session's thread got %q", got)
	}
}

// A chat-born epic whose project is hidden from Slack since still posts,
// naming its own epic alone.
func TestChatEpicNoticesOfAnEpicWhoseProjectWasHiddenSinceStillPost(t *testing.T) {
	w := setup(t)
	_, tasks, root := w.routedEpic()
	other := "prj_web_" + w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Secret console', 'web', 'WEB')`, other, w.org)
	w.exec(`INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2), ($1, 'slack', $3)`,
		w.org, w.project, other)
	w.move(tasks[0], "running", "planning with the conductor")
	w.postNotices()
	got := w.epicNotices("C1", root)
	if len(got) != 1 || got[0] != "Started the epic BIL › CSV exports." {
		t.Fatalf("the thread got %q", got)
	}
	for _, m := range w.fake.BotMessages() {
		if strings.Contains(m.Text, "WEB") || strings.Contains(m.Text, "Secret console") {
			t.Errorf("a notice names another project: %s", m.Text)
		}
	}
}

// An epic's notice the provider refuses gives up, with a line on the
// epic's project ledger, correlated with the epic.
func TestChatEpicNoticeGivesUpOnTheEpicsLedger(t *testing.T) {
	w := setup(t)
	epic, tasks, _ := w.routedEpic()
	w.move(tasks[0], "running", "planning with the conductor")
	// The task's started notice posts; the epic's is refused.
	w.postNotices()
	w.exec(`UPDATE chat_notices SET posted_at = NULL, next_attempt_at = now() WHERE epic_id = $1`, epic)
	w.fake.FailNext("chat.postMessage", "is_archived")
	w.postNotices()
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1 AND gave_up_at IS NOT NULL AND last_error LIKE '%is_archived%'`, epic); n != 1 {
		t.Fatalf("the epic's notice did not give up")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE event_type = 'chat.notice' AND correlation_id = $1 AND task_id IS NULL
		AND project_id = $2 AND payload->>'about' = 'thread_notice_failed' AND payload->>'epicId' = $1`, epic, w.project); n != 1 {
		t.Errorf("%d give-up lines on the epic", n)
	}
}
