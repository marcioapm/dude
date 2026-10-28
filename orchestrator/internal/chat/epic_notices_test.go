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

// chatOrigin is an epic's or task's origin in #support's thread 1.0.
const chatOrigin = `{"provider":"slack","workspaceId":"T1","conversationId":"C1","conversationName":"#support","threadId":"1.0"}`

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
	w.t.Cleanup(func() {
		if w.t.Failed() {
			w.dumpNotices(epic, root)
		}
	})
	return epic, tasks, root
}

// dumpNotices logs the org's chat_notices rows and every bot message, so a
// failure tells a notice not recorded, not posted, or posted elsewhere.
func (w *world) dumpNotices(epic, root string) {
	w.t.Helper()
	rows, err := w.owner.Query(w.ctx, `SELECT COALESCE(epic_id, task_id), kind, COALESCE(posted_at::text, '-'), attempts,
			COALESCE(last_error, '-'), thread_id, next_attempt_at::text, COALESCE(gave_up_at::text, '-')
		FROM chat_notices WHERE organization_id = $1 ORDER BY next_attempt_at, id`, w.org)
	if err != nil {
		w.t.Logf("chat_notices: %v", err)
		return
	}
	defer rows.Close()
	w.t.Logf("epic %s, thread %s; chat_notices (subject, kind, posted_at, attempts, last_error, thread_id, next_attempt_at, gave_up_at):", epic, root)
	for rows.Next() {
		var subject, kind, posted, lastErr, thread, next, gaveUp string
		var attempts int
		if err := rows.Scan(&subject, &kind, &posted, &attempts, &lastErr, &thread, &next, &gaveUp); err != nil {
			w.t.Logf("scan: %v", err)
			return
		}
		w.t.Logf("  %s %s %s %d %s %s %s %s", subject, kind, posted, attempts, lastErr, thread, next, gaveUp)
	}
	w.t.Logf("the fake's bot messages (channel, thread, ts, text):")
	for _, m := range w.fake.BotMessages() {
		w.t.Logf("  %s %s %s %q", m.Channel, m.ThreadTS, m.TS, m.Text)
	}
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

// addedTask adds a task to epic in the Backlog, with the epic's origin, as
// one added after routing: its id.
func (w *world) addedTask(epic string) string {
	w.t.Helper()
	added := "wi_added_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin)
		SELECT $1, $2, $3, 9, $4, 'Added', 'received', origin FROM epics WHERE id = $4`, added, w.org, w.project, epic)
	return added
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
	added := w.addedTask(epic)
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
	for i := range 20 {
		epic := fmt.Sprintf("epc_race%d_%s", i, w.org)
		w.exec(`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES ($1, $2, $3, 'Race', $4::jsonb)`, epic, w.org, w.project, chatOrigin)
		var tasks []string
		for j := range 2 {
			id := fmt.Sprintf("wi_race%d_%d_%s", i, j, w.org)
			w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin)
				VALUES ($1, $2, $3, $4, $5, 'Racer', 'running', $6::jsonb)`, id, w.org, w.project, 100+i*2+j, epic, chatOrigin)
			tasks = append(tasks, id)
		}
		var (
			ready, wg sync.WaitGroup
			start     = make(chan struct{})
			moved     = make(chan struct{}, 2)
			errs      = make(chan error, 2)
			pids      [2]int
		)
		ready.Add(2)
		wg.Add(2)
		for j, task := range tasks {
			status := []string{"done", "failed"}[j]
			go func() {
				defer wg.Done()
				errs <- w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
					// Both transactions open before either moves.
					if err := tx.QueryRow(w.ctx, `SELECT pg_backend_pid()`).Scan(&pids[j]); err != nil {
						return err
					}
					ready.Done()
					<-start
					if _, err := delivery.SetTaskStatusTx(w.ctx, tx, w.org, w.project, task, "", status, "pull requests merged"); err != nil {
						return err
					}
					// The first to count holds its commit until the other is
					// seen waiting on a lock, or has counted too: unserialised,
					// both counts see the other still running.
					moved <- struct{}{}
					return w.waitBlocked(pids[1-j], 30*time.Second, func() bool { return len(moved) == 2 })
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

// An epic's last task ending while a person archives the epic: the move
// holds the task's row and its notice's foreign key then needs the epic's,
// while the archive holds the epic's and waits for the task's. Both finish,
// never a deadlock.
func TestChatEpicNoticeOfALastTaskEndingWhileTheEpicIsArchivedNeverDeadlocks(t *testing.T) {
	w := setup(t)
	for i := range 5 {
		epic := fmt.Sprintf("epc_arch%d_%s", i, w.org)
		w.exec(`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES ($1, $2, $3, 'Archived', $4::jsonb)`, epic, w.org, w.project, chatOrigin)
		var tasks []string
		for j, status := range []string{"done", "running"} {
			id := fmt.Sprintf("wi_arch%d_%d_%s", i, j, w.org)
			w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin)
				VALUES ($1, $2, $3, $4, $5, 'Archiver', $6, $7::jsonb)`, id, w.org, w.project, 200+i*2+j, epic, status, chatOrigin)
			tasks = append(tasks, id)
		}
		w.lockRace(i,
			// SetTaskStatusTx's row update, held until the archive waits on it.
			func(tx pgx.Tx) error {
				_, err := tx.Exec(w.ctx, `UPDATE tasks SET status = 'failed', updated_at = now() WHERE id = $1`, tasks[1])
				return err
			},
			func(tx pgx.Tx) error {
				return delivery.RecordStatusTx(w.ctx, tx, w.org, w.project, tasks[1], "failed", "lux: exit 1")
			},
			func(tx pgx.Tx) error {
				_, _, err := delivery.ArchiveEpicTx(w.ctx, tx, w.org, epic, true, delivery.Actor{ActorType: "system", ActorID: "test"},
					func(delivery.EpicTask) error { return nil })
				return err
			})
		if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); n != 1 {
			t.Fatalf("round %d: %d finished notices, want 1", i, n)
		}
	}
}

// An archived task moved out of its archived epic while a person unarchives
// the epic, an ended task moved in since still current in it: the move holds
// the task's row and its notice's foreign key then needs the epic's, while
// the unarchive holds the epic's and waits for the task's. Both finish,
// never a deadlock.
func TestChatEpicNoticeOfAnArchivedTaskMovedOutWhileTheEpicIsUnarchivedNeverDeadlocks(t *testing.T) {
	w := setup(t)
	const archivedAt = "2026-01-01 00:00:00+00"
	for i := range 5 {
		epic := fmt.Sprintf("epc_unarch%d_%s", i, w.org)
		w.exec(`INSERT INTO epics (id, organization_id, project_id, title, origin, archived_at)
			VALUES ($1, $2, $3, 'Unarchived', $4::jsonb, $5)`, epic, w.org, w.project, chatOrigin, archivedAt)
		archived := fmt.Sprintf("wi_unarch%d_0_%s", i, w.org)
		current := fmt.Sprintf("wi_unarch%d_1_%s", i, w.org)
		w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status, origin, archived_at)
			VALUES ($1, $2, $3, $4, $5, 'Archived with it', 'done', $6::jsonb, $7),
			       ($8, $2, $3, $9, $5, 'Moved in since', 'done', $6::jsonb, NULL)`,
			archived, w.org, w.project, 300+i*2, epic, chatOrigin, archivedAt, current, 301+i*2)
		w.lockRace(i,
			// PATCH /tasks/:id's row lock, held until the unarchive waits on it.
			func(tx pgx.Tx) error {
				_, err := tx.Exec(w.ctx, `SELECT id FROM tasks WHERE id = $1 FOR UPDATE`, archived)
				return err
			},
			func(tx pgx.Tx) error {
				if _, err := tx.Exec(w.ctx, `UPDATE tasks SET epic_id = NULL, updated_at = now() WHERE id = $1`, archived); err != nil {
					return err
				}
				_, err := tx.Exec(w.ctx, `SELECT chat_epic_notice($1, 'epic_finished', $2)`, epic, archived)
				return err
			},
			func(tx pgx.Tx) error {
				_, err := delivery.UnarchiveEpicTx(w.ctx, tx, w.org, epic, delivery.Actor{ActorType: "system", ActorID: "test"})
				return err
			})
		if got := w.one(`SELECT trigger_task_id || ' ' || detail FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); got != archived+" 1 done, 0 failed, 0 cancelled" {
			t.Fatalf("round %d: finished as %q", i, got)
		}
		if n := w.count(`SELECT count(*) FROM epics WHERE id = $1 AND archived_at IS NULL`, epic); n != 1 {
			t.Fatalf("round %d: the epic is still archived", i)
		}
	}
}

// lockRace runs hold in one transaction, then waiter in another; once the
// waiter's backend waits on a lock, the first goes on with rest. Both must
// commit within round: Postgres aborts one of a deadlocked pair.
func (w *world) lockRace(round int, hold, rest, waiter func(pgx.Tx) error) {
	w.t.Helper()
	held, blocked := make(chan struct{}), make(chan struct{})
	waiterPID := make(chan int, 1)
	errs := make(chan error, 2)
	go func() {
		errs <- w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			if err := hold(tx); err != nil {
				return err
			}
			close(held)
			<-blocked
			return rest(tx)
		})
	}()
	<-held
	go func() {
		errs <- w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			var pid int
			if err := tx.QueryRow(w.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			waiterPID <- pid
			return waiter(tx)
		})
	}()
	waited := w.waitBlocked(<-waiterPID, 30*time.Second, nil)
	close(blocked)
	for range 2 {
		if err := <-errs; err != nil {
			w.t.Fatalf("round %d: %v", round, err)
		}
	}
	if waited != nil {
		w.t.Fatalf("round %d: %v", round, waited)
	}
}

// waitBlocked waits until the backend pid waits on a lock, or until done
// (when non-nil) reports true: an error when neither within limit. It
// probes on a pool connection, so racing goroutines may call it at once.
func (w *world) waitBlocked(pid int, limit time.Duration, done func() bool) error {
	deadline := time.Now().Add(limit)
	for done == nil || !done() {
		var waiting bool
		if err := w.svc.DB.InOrg(w.ctx, w.org, func(probe pgx.Tx) error {
			return probe.QueryRow(w.ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, pid).Scan(&waiting)
		}); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("backend %d neither waited on a lock nor finished within %s", pid, limit)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

// archive archives task as a person's Archive does, closing it as won't do
// when it has not ended.
func (w *world) archive(task string) {
	w.t.Helper()
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.ArchiveTaskTx(w.ctx, tx, w.org, delivery.ArchiveInput{CloseInput: delivery.CloseInput{TaskID: task,
			Resolution: delivery.ResolutionWontDo, Actor: delivery.Actor{ActorType: "system", ActorID: "test"}}})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// The epic's last unfinished task archived: closed as won't do, it counts
// as cancelled and finishes the epic.
func TestChatEpicNoticeFinishesWhenItsLastUnfinishedTaskIsArchivedAndClosed(t *testing.T) {
	w := setup(t)
	_, tasks, root := w.routedEpic()
	w.move(tasks[0], "running", "planning with the conductor")
	w.move(tasks[0], "done", "pull requests merged")
	w.archive(tasks[1])
	w.postNotices()
	want := []string{"Started the epic BIL › CSV exports.", "The epic BIL › CSV exports finished: 1 done, 0 failed, 1 cancelled."}
	if got := w.epicNotices("C1", root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the thread got %q", got)
	}
}

// An ended task archived out of the epic, every other one ended: that
// finishes it with no status move, counting what is left. Every end the
// workflow writes asks itself, so here the end is written straight, as one
// recorded before epic notices existed.
func TestChatEpicNoticeFinishesWhenAnEndedTaskIsArchivedLeavingOnlyEndedOnes(t *testing.T) {
	w := setup(t)
	epic, tasks, root := w.routedEpic()
	added := w.addedTask(epic)
	w.move(tasks[0], "running", "planning with the conductor")
	w.move(tasks[0], "done", "pull requests merged")
	w.move(tasks[1], "running", "planning with the conductor")
	w.move(tasks[1], "done", "pull requests merged")
	w.exec(`UPDATE tasks SET status = 'aborted', resolution = 'wont_do' WHERE id = $1`, added)
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); n != 0 {
		t.Fatalf("finished before anything asked")
	}
	w.archive(added)
	w.postNotices()
	want := []string{"Started the epic BIL › CSV exports.", "The epic BIL › CSV exports finished: 2 done, 0 failed, 0 cancelled."}
	if got := w.epicNotices("C1", root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the thread got %q", got)
	}
	if got := w.one(`SELECT trigger_task_id FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_finished'`, epic); got != added {
		t.Errorf("finished recorded of %q, want %s", got, added)
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
// epic's project ledger, correlated with the epic, and one in the Chat of
// the task whose move recorded it (its events, as relationNotices reads).
func TestChatEpicNoticeGivesUpOnTheEpicsLedger(t *testing.T) {
	w := setup(t)
	epic, tasks, _ := w.routedEpic()
	w.move(tasks[0], "running", "planning with the conductor")
	if got := w.one(`SELECT trigger_task_id FROM chat_notices WHERE epic_id = $1 AND kind = 'epic_started'`, epic); got != tasks[0] {
		t.Fatalf("started recorded of %q, want %s", got, tasks[0])
	}
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
	text := w.one(`SELECT string_agg(payload->>'text', ' | ') FROM events WHERE event_type = 'chat.notice' AND task_id = $1
		AND payload->>'about' = 'thread_notice_failed' AND payload->>'epicId' = $2`, tasks[0], epic)
	if !strings.HasPrefix(text, "dude could not tell the Slack thread in #support of the epic BIL › CSV exports: ") || strings.Contains(text, " | ") {
		t.Errorf("the task's Chat says %q", text)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE event_type = 'chat.notice' AND task_id = $1 AND payload->>'about' = 'thread_notice_failed'`,
		tasks[1]); n != 0 {
		t.Errorf("%d give-up lines on the epic's other task", n)
	}
}
