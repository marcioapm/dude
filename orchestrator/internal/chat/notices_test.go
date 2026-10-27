package chat_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// routedTask is a task routed from a mention in #support's thread root,
// in the Backlog: its id and the thread's root.
func (w *world) routedTask() (task, root string) {
	w.t.Helper()
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.95, "reasoning": "r", "project": "BIL",
		"title": "Fix the export timeout", "goal": "Exports time out after 30s on large accounts."}))
	root = w.mention("Márcia", "C1", "", "please fix the export timeout")
	reqs := w.requests()
	if last := reqs[len(reqs)-1]; last.ResultKind != "task" {
		w.t.Fatalf("routed to %+v", last)
	}
	return reqs[len(reqs)-1].ResultID, root
}

// move moves the task as delivery's writers do, in one transaction.
func (w *world) move(task, status, reason string) {
	w.t.Helper()
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.SetTaskStatusTx(w.ctx, tx, w.org, w.project, task, "", status, reason)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) prOpened(task, url string) {
	w.t.Helper()
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		return delivery.RecordPROpenedNoticeTx(w.ctx, tx, task, url)
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) postNotices() int {
	w.t.Helper()
	n, err := w.svc.PostNotices(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// threadNotices are dude's messages in thread after the routing reply.
func (w *world) threadNotices(channel, root string) []string {
	w.t.Helper()
	var out []string
	for _, m := range w.fake.BotMessages() {
		if m.Channel == channel && m.ThreadTS == root && !strings.HasPrefix(m.Text, "Filed ") {
			out = append(out, m.Text)
		}
	}
	return out
}

// A routed task's thread hears its delivery start, its pull request open
// and its end, once each and in order, however often the loop runs and a
// move is written again.
func TestChatNoticesPostStartedPROpenedAndDoneOnceEach(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	if n := w.postNotices(); n != 0 {
		t.Fatalf("%d notices for a task in the Backlog", n)
	}
	w.move(task, "running", "planning with the conductor")
	w.postNotices()
	w.postNotices()
	// A retried start, and a round back to running, say nothing new.
	w.move(task, "review", "reviewing")
	w.move(task, "running", "fixing review findings")
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		return delivery.RecordStatusTx(w.ctx, tx, w.org, w.project, task, "running", "restarted")
	}); err != nil {
		t.Fatal(err)
	}
	w.prOpened(task, "https://github.com/acme/billing/pull/7")
	w.prOpened(task, "https://github.com/acme/billing/pull/7")
	w.postNotices()
	w.move(task, "done", "pull requests merged")
	w.postNotices()
	w.postNotices()
	got := w.threadNotices("C1", root)
	want := []string{"Started BIL-1 Fix the export timeout.",
		"Opened a pull request for BIL-1 Fix the export timeout: https://github.com/acme/billing/pull/7",
		"Done: BIL-1 Fix the export timeout."}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the thread got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND posted_at IS NOT NULL AND message_id IS NOT NULL`, task); n != 3 {
		t.Errorf("%d notices recorded posted, want 3", n)
	}
}

// Text is escaped once, and a failure names its reason.
func TestChatNoticeOfAStoppedTaskSaysWhyEscapedOnce(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.exec(`UPDATE tasks SET title = 'Fix <export> & import' WHERE id = $1`, task)
	w.move(task, "aborted", "a person stopped the task")
	w.postNotices()
	got := w.threadNotices("C1", root)
	if len(got) != 1 || got[0] != "Stopped BIL-1 Fix &lt;export&gt; &amp; import: a person stopped the task." {
		t.Fatalf("the thread got %q", got)
	}
}

// A transient failure is tried again later and then posts; a permanent
// one gives up, recorded on the row and in the task's Chat.
func TestChatNoticeRetriesATransientFailureAndGivesUpOnAPermanentOne(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	w.fake.FailNext("chat.postMessage", "ratelimited")
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 0 {
		t.Fatalf("posted through a rate limit: %q", got)
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND attempts = 1 AND posted_at IS NULL AND gave_up_at IS NULL
		AND next_attempt_at > now() AND last_error LIKE '%429%'`, task); n != 1 {
		t.Fatalf("the rate-limited notice is not waiting to retry")
	}
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 0 {
		t.Fatalf("retried before its backoff: %q", got)
	}
	w.exec(`UPDATE chat_notices SET next_attempt_at = now() WHERE task_id = $1`, task)
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 1 || !strings.HasPrefix(got[0], "Started BIL-1") {
		t.Fatalf("after the backoff the thread got %q", got)
	}

	w.fake.FailNext("chat.postMessage", "is_archived")
	w.move(task, "done", "pull requests merged")
	w.postNotices()
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 1 {
		t.Fatalf("the thread got %q", got)
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND kind = 'done' AND gave_up_at IS NOT NULL AND posted_at IS NULL
		AND last_error LIKE '%is_archived%'`, task); n != 1 {
		t.Errorf("the refused notice is not given up with its error")
	}
	if said := w.one(`SELECT payload->>'text' FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'thread_notice_failed'`,
		task); !strings.Contains(said, "could not tell the Slack thread in #support") || !strings.Contains(said, "is_archived") {
		t.Errorf("the task's Chat says %q", said)
	}
}

// A task made in the web has no thread: nothing is recorded or posted.
func TestChatNoticesNothingForAWebFiledTask(t *testing.T) {
	w := setup(t)
	task := "wi_web_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES ($1, $2, $3, 9, 'From the web', 'received')`,
		task, w.org, w.project)
	w.move(task, "running", "planning with the conductor")
	w.move(task, "done", "pull requests merged")
	w.prOpened(task, "https://github.com/acme/billing/pull/9")
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE organization_id = $1`, w.org); n != 0 {
		t.Errorf("%d notices recorded", n)
	}
	if n := w.postNotices(); n != 0 || len(w.fake.BotMessages()) != 0 {
		t.Errorf("posted %d: %+v", n, w.fake.BotMessages())
	}
}

// A chat-born task whose project is hidden from Slack since keeps
// working, its notices included; a notice names its own task alone.
func TestChatNoticesOfATaskWhoseProjectWasHiddenSinceStillPost(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.exec(`INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, w.org, w.project)
	w.move(task, "running", "planning with the conductor")
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 1 || got[0] != "Started BIL-1 Fix the export timeout." {
		t.Fatalf("the thread got %q", got)
	}
}

// The provider is called with no transaction open: while Slack holds the
// post, no transaction of the test database is open and the notice's row
// can be locked at once.
func TestChatNoticePostsWithNoTransactionOpen(t *testing.T) {
	w := setup(t)
	task, _ := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
			once.Do(func() { close(held) })
			<-release
		}
		w.fake.ServeHTTP(rw, r)
	}))
	t.Cleanup(slow.Close)
	svc := &chat.Service{DB: w.svc.DB, PublicURL: w.svc.PublicURL, Providers: chat.Registry{"slack": slack.New(slack.Config{
		SigningSecret: signingSecret, ClientID: "cid", ClientSecret: "csecret", APIURL: slow.URL + "/api"})}}
	done := make(chan error, 1)
	go func() {
		_, err := svc.PostNotices(w.ctx)
		done <- err
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the notice never reached Slack")
	}
	open := w.count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()
		AND state LIKE 'idle in transaction%'`)
	locked := pgx.BeginFunc(w.ctx, w.owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(w.ctx, `SELECT 1 FROM chat_notices WHERE task_id = $1 FOR UPDATE NOWAIT`, task)
		return err
	})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if open != 0 || locked != nil {
		t.Errorf("while Slack held the notice: %d transactions open; locking its row: %v", open, locked)
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND posted_at IS NOT NULL`, task); n != 1 {
		t.Errorf("%d posted", n)
	}
}
