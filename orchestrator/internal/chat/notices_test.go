package chat_test

import (
	"context"
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

func (w *world) prOpened(task string) {
	w.t.Helper()
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		return delivery.RecordPROpenedNoticeTx(w.ctx, tx, task)
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
	// Blocked: queued is not a start, and says nothing.
	w.move(task, "queued", "blocked by BIL-9")
	if n := w.postNotices(); n != 0 || len(w.threadNotices("C1", root)) != 0 {
		t.Fatalf("queued posted %d: %q", n, w.threadNotices("C1", root))
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("queued recorded %d notices", n)
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
	w.prOpened(task)
	w.prOpened(task)
	w.postNotices()
	w.move(task, "done", "pull requests merged")
	w.postNotices()
	w.postNotices()
	got := w.threadNotices("C1", root)
	// The pull request's URL names the repository: the thread gets dude's link alone.
	want := []string{"Started BIL-1 Fix the export timeout.",
		"Opened a pull request for BIL-1 Fix the export timeout.",
		"Done: BIL-1 Fix the export timeout."}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the thread got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, m := range w.fake.BotMessages() {
		if strings.Contains(m.Text, "github.com") || strings.Contains(string(m.Blocks), "github.com") {
			t.Errorf("a notice names the repository: %+v", m)
		}
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

// A failed or stopped notice says only a known reason, as a fixed phrase;
// any other reason (an error's text) becomes "it failed" or "it was
// cancelled", and never reaches the thread.
func TestChatNoticeOfAnEndedTaskSaysOnlyAKnownReason(t *testing.T) {
	for _, c := range []struct{ status, reason, want string }{
		{"aborted", "a person stopped it", "a person stopped the task"},
		{"aborted", "pull request closed without merging", "pull request closed without merging"},
		{"aborted", "closed as won't do by Ana", "closed as won't do by Ana"},
		{"aborted", "closed as duplicate", "closed as duplicate"},
		{"aborted", "pq: connection refused to 10.0.3.7:5432 (sk-acme-token)", "it was cancelled"},
		{"failed", "lux: git clone github.com/acme/secret-repo: exit 128", "it failed"},
		{"aborted", "closed as won't do by Ana <!channel> https://evil.example", "it was cancelled"},
	} {
		t.Run(c.reason, func(t *testing.T) {
			w := setup(t)
			task, root := w.routedTask()
			w.exec(`UPDATE tasks SET status = 'running' WHERE id = $1`, task)
			w.move(task, c.status, c.reason)
			w.postNotices()
			got := w.threadNotices("C1", root)
			if want := "Stopped BIL-1 Fix the export timeout: " + c.want + "."; len(got) != 1 || got[0] != want {
				t.Fatalf("the thread got %q, want %q", got, want)
			}
			if c.want == "it failed" || c.want == "it was cancelled" {
				if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND detail = $2`, task, c.want); n != 1 {
					t.Errorf("the row keeps another detail")
				}
			}
		})
	}
}

// A transient failure is tried again later and then posts; a permanent
// one gives up, recorded on the row and in the task's Chat. A given-up
// notice is never posted, even once due again.
func TestChatNoticeRetriesATransientFailureAndGivesUpOnAPermanentOne(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	// Retry-After (120 s) is told apart from the first backoff (5 s).
	w.fake.FailRetryAfter("chat.postMessage", "ratelimited", 1, 120)
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 0 {
		t.Fatalf("posted through a rate limit: %q", got)
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND attempts = 1 AND posted_at IS NULL AND gave_up_at IS NULL
		AND next_attempt_at > now() + interval '100 seconds' AND next_attempt_at < now() + interval '130 seconds' AND last_error LIKE '%429%'`, task); n != 1 {
		t.Fatalf("the rate-limited notice does not wait for its Retry-After: %s",
			w.one(`SELECT (next_attempt_at - now())::text FROM chat_notices WHERE task_id = $1`, task))
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
	// Due again (past its claim's lease): a given-up notice is not tried.
	w.exec(`UPDATE chat_notices SET next_attempt_at = now() WHERE task_id = $1`, task)
	before := w.fake.Calls("chat.postMessage")
	w.postNotices()
	if got := w.threadNotices("C1", root); len(got) != 1 || w.fake.Calls("chat.postMessage") != before {
		t.Errorf("a given-up notice was tried again: %q", got)
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND kind = 'done' AND attempts = 1`, task); n != 1 {
		t.Errorf("a given-up notice was attempted again")
	}
}

// Transient failures keep a notice pending, backing off, until 6 hours
// after it was recorded; the next failure past that gives up, once, with
// one line in the task's Chat.
func TestChatNoticeGivesUpOnTransientFailuresSixHoursAfterItWasRecorded(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	w.fake.FailTimes("chat.postMessage", "ratelimited", 1000)
	for i := range 12 {
		w.exec(`UPDATE chat_notices SET next_attempt_at = now() WHERE task_id = $1`, task)
		w.postNotices()
		if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND attempts = $2 AND gave_up_at IS NULL AND posted_at IS NULL
			AND next_attempt_at > now()`, task, i+1); n != 1 {
			t.Fatalf("after %d transient failures the notice is not pending", i+1)
		}
	}
	// Backoff doubles from 5 s and stops at 10 minutes.
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND next_attempt_at BETWEEN now() + interval '590 seconds' AND now() + interval '610 seconds'`,
		task); n != 1 {
		t.Errorf("the 12th backoff is not 10 minutes: %s", w.one(`SELECT (next_attempt_at - now())::text FROM chat_notices WHERE task_id = $1`, task))
	}
	w.exec(`UPDATE chat_notices SET created_at = now() - interval '5 hours 59 minutes', next_attempt_at = now() WHERE task_id = $1`, task)
	w.postNotices()
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND gave_up_at IS NULL`, task); n != 1 {
		t.Fatalf("gave up inside the window")
	}
	w.exec(`UPDATE chat_notices SET created_at = now() - interval '6 hours 1 minute', next_attempt_at = now() WHERE task_id = $1`, task)
	w.postNotices()
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND gave_up_at IS NOT NULL AND attempts = 14 AND posted_at IS NULL`, task); n != 1 {
		t.Fatalf("not given up past the window")
	}
	w.exec(`UPDATE chat_notices SET next_attempt_at = now() WHERE task_id = $1`, task)
	w.postNotices()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'thread_notice_failed'`, task); n != 1 {
		t.Errorf("%d thread_notice_failed lines, want 1", n)
	}
	if got := w.threadNotices("C1", root); len(got) != 0 {
		t.Errorf("the thread got %q", got)
	}
}

// The window is time, not a try count: a notice already past it gives up on
// its first transient failure.
func TestChatNoticePastItsWindowGivesUpOnItsFirstTransientFailure(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	w.exec(`UPDATE chat_notices SET created_at = now() - interval '6 hours 1 minute' WHERE task_id = $1`, task)
	w.fake.FailTimes("chat.postMessage", "ratelimited", 1)
	w.postNotices()
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND gave_up_at IS NOT NULL AND attempts = 1 AND posted_at IS NULL`, task); n != 1 {
		t.Fatalf("not given up on the first try past the window: %s",
			w.one(`SELECT row_to_json(n)::text FROM chat_notices n WHERE task_id = $1`, task))
	}
	if got := w.threadNotices("C1", root); len(got) != 0 {
		t.Errorf("the thread got %q", got)
	}
}

// cancelAfterPost cancels the caller's context once a post was taken.
type cancelAfterPost struct {
	chat.Provider
	cancel context.CancelFunc
}

func (p cancelAfterPost) PostReply(ctx context.Context, inst chat.Installation, conversation, thread string, r chat.Reply) (string, error) {
	ts, err := p.Provider.PostReply(ctx, inst, conversation, thread, r)
	p.cancel()
	return ts, err
}

// A post Slack took is recorded even when the loop's context is cancelled
// right after it (a shutdown), so it is not posted again.
func TestChatNoticeIsRecordedWhenCancelledAfterThePost(t *testing.T) {
	w := setup(t)
	task, root := w.routedTask()
	w.move(task, "running", "planning with the conductor")
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	p := cancelAfterPost{Provider: w.svc.Providers["slack"], cancel: cancel}
	svc := &chat.Service{DB: w.svc.DB, PublicURL: w.svc.PublicURL, Providers: chat.Registry{"slack": p}}
	if _, err := svc.PostNotices(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("the context was never cancelled")
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1 AND posted_at IS NOT NULL AND message_id IS NOT NULL`, task); n != 1 {
		t.Fatalf("the post was not recorded")
	}
	if got := w.threadNotices("C1", root); len(got) != 1 {
		t.Fatalf("the thread got %q", got)
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
	w.prOpened(task)
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
