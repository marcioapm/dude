package orchestrator_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// refreshWorld is a conductor's task in review on its pull request, whose
// checks dude last read as pending.
func refreshWorld(t *testing.T) (*world, string) {
	t.Helper()
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.gh.SetChecks("pending")
	w.sync()
	if got := field(w.must(task, "pull_requests", `{}`), "pullRequests", 0, "checks"); got != "pending" {
		t.Fatalf("dude's last poll: checks %v", got)
	}
	return w, task
}

// pull_requests answers dude's last poll, saying when that was; CI that
// finished since is shown by refresh_pull_requests, which reads GitHub now,
// is one of the task's GitHub reads, and leaves the stored pull request as
// GitHub has it.
func TestRefreshingThePullRequestsReadsGitHubNow(t *testing.T) {
	w, task := refreshWorld(t)
	w.gh.SetChecks("success")
	stale := w.must(task, "pull_requests", `{}`)
	if got := field(stale, "pullRequests", 0, "checks"); got != "pending" {
		t.Errorf("before a refresh: checks %v, want dude's last poll", got)
	}
	polled, _ := field(stale, "pullRequests", 0, "lastPolledAt").(string)
	at, err := time.Parse(time.RFC3339Nano, polled)
	if err != nil || time.Since(at) > time.Minute {
		t.Errorf("lastPolledAt %q (%v)", polled, err)
	}

	before := w.requests()
	out := w.must(task, "refresh_pull_requests", `{}`)
	if got := field(out, "pullRequests", 0, "checks"); got != "passing" {
		t.Errorf("refreshed: checks %v, want passing: %v", got, out)
	}
	if again, _ := field(out, "pullRequests", 0, "lastPolledAt").(string); again == "" || again <= polled {
		t.Errorf("lastPolledAt after the refresh %q, before %q", again, polled)
	}
	if field(out, "refreshed") != float64(1) || w.requests() == before {
		t.Errorf("refreshed %v, GitHub requests %d → %d", out["refreshed"], before, w.requests())
	}
	if got := field(w.must(task, "pull_requests", `{}`), "pullRequests", 0, "checks"); got != "passing" {
		t.Errorf("the stored pull request after a refresh: checks %v", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1`+githubReadsHour, task); n != 1 {
		t.Errorf("%d GitHub reads this hour, want the refresh", n)
	}
	if reads := field(w.limitsOf(task), "githubReadsThisHour"); num(field(reads, "used")) != 1 {
		t.Errorf("limits' GitHub reads: %v", reads)
	}
}

// A session's agent refreshes a linked project's task's pull requests by
// the task's key, against the session's GitHub reads; a task of a project
// not linked is refused, and GitHub is not asked.
func TestABrainstormRefreshesALinkedTasksPullRequests(t *testing.T) {
	s, run := brainstorming(t)
	task := s.reviewing()
	s.gh.SetChecks("pending")
	s.sync()
	s.gh.SetChecks("success")
	var key string
	if err := s.owner.QueryRow(context.Background(), `SELECT 'BL-' || number FROM tasks WHERE id = $1`, task).Scan(&key); err != nil {
		t.Fatal(err)
	}
	out := s.read(run, "refresh_pull_requests", `{"task":"`+key+`"}`)
	if got := field(out, "pullRequests", 0, "checks"); got != "passing" || field(out, "pullRequests", 0, "lastPolledAt") == nil {
		t.Errorf("refreshed: %v", out)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1`+githubReadsHour, s.sessionOf(run)); n != 1 {
		t.Errorf("%d of the session's GitHub reads, want the refresh", n)
	}
	before := s.requests()
	if status, out := s.tool(run, "refresh_pull_requests", `{"project":"WC","task":"WC-1"}`); status != 422 ||
		!strings.Contains(out["error"].(string), "not linked") {
		t.Errorf("another project's task: %d %v", status, out)
	}
	if s.requests() != before {
		t.Error("a refused refresh reached GitHub")
	}
}

// githubReadsHour is the hourly reads' predicate, as agenttools counts it.
const githubReadsHour = ` AND event_type = 'agent.tool.attempted' AND payload->>'tool' IN ('github_search', 'github_issue',
	'github_pull_request', 'github_check_log', 'refresh_pull_requests') AND occurred_at > now() - interval '1 hour'`

// spendGitHubReads records n of the task's GitHub reads ten minutes ago,
// as evt_spent_1…n.
func (w *world) spendGitHubReads(task string, n int) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO events (id, organization_id, event_type, task_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_spent_' || n, $1, 'agent.tool.attempted', $2, now() - interval '10 minutes', 'agent', 'run_x', 'runner',
			jsonb_build_object('tool', 'github_issue') FROM generate_series(1, $3::int) n`, w.org, task, n)
}

// With the hour's 60 reads spent, a refresh is refused before GitHub is asked.
func TestARefreshPastTheHoursReadsIsRefused(t *testing.T) {
	w, task := refreshWorld(t)
	w.spendGitHubReads(task, 60)
	before := w.requests()
	w.refused(task, "refresh_pull_requests", `{}`, "at most 60 GitHub reads an hour")
	if w.requests() != before {
		t.Error("a refused refresh reached GitHub")
	}
}

// secondPullRequest gives the task another open pull request, #2 of the same
// repository, which GitHub does not have.
func (w *world) secondPullRequest(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO pull_requests SELECT (jsonb_populate_record(NULL::pull_requests,
		to_jsonb(pr) || jsonb_build_object('id', pr.id || '_2', 'number', 2, 'node_id', NULL, 'url', pr.url || '-2'))).*
		FROM pull_requests pr WHERE task_id = $1`, task)
}

// A refresh spends one GitHub read per open pull request: with fewer left
// than it has open, it syncs none and says how many it needs and has; with
// enough, its attempt counts them all, in the hourly count and in limits.
func TestARefreshSpendsAReadPerPullRequest(t *testing.T) {
	w, task := refreshWorld(t)
	w.secondPullRequest(task)
	w.spendGitHubReads(task, 59)
	before := w.requests()
	w.refused(task, "refresh_pull_requests", `{}`, "one GitHub read for each open pull request, 2 here, and 1 of the hour's 60 are left")
	if w.requests() != before {
		t.Error("a refresh short of reads reached GitHub")
	}
	mustExec(t, w.owner, `DELETE FROM events WHERE id = 'evt_spent_1'`)
	w.must(task, "refresh_pull_requests", `{}`)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1`+githubReadsHour, task); n != 60 {
		t.Errorf("%d GitHub reads this hour, want 58 and the refresh's 2", n)
	}
	if reads := field(w.limitsOf(task), "githubReadsThisHour"); num(field(reads, "used")) != 60 || num(field(reads, "remaining")) != 0 {
		t.Errorf("limits' GitHub reads: %v", reads)
	}
}

// The refresh reads GitHub with no transaction open: while GitHub holds its
// answer, no backend is idle in a transaction, the task's GitHub-reads lock
// is free, and the conductor's other tools answer.
func TestARefreshHoldsNoTransactionWhileGitHubAnswers(t *testing.T) {
	w, task := refreshWorld(t)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/1") {
				once.Do(func() { close(held); <-release })
			}
			return 0
		}
	})
	done := make(chan map[string]any)
	go func() {
		_, out := w.as(task, "refresh_pull_requests", `{}`)
		done <- out
	}()
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the refresh never asked GitHub")
	}
	// No backend of this database sits in a transaction while GitHub holds
	// the answer: one seen idle in the same transaction across samples a
	// tenth of a second apart is one held open over the request.
	inTx := func() map[string]bool {
		rows, err := w.owner.Query(context.Background(), `SELECT pid || '@' || xact_start || ': ' || left(query, 200) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid() AND state LIKE 'idle in transaction%'`)
		if err != nil {
			t.Fatal(err)
		}
		open := map[string]bool{}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			open[s] = true
		}
		return open
	}
	idle := inTx()
	for range 3 {
		time.Sleep(100 * time.Millisecond)
		now := inTx()
		for s := range idle {
			if !now[s] {
				delete(idle, s)
			}
		}
	}
	for s := range idle {
		t.Errorf("a transaction is held while GitHub answers: %s", s)
	}
	// The budget: the check's GitHub-reads lock is free, so another read
	// of the task's is not held behind the refresh.
	tx := w.begin()
	var free bool
	if err := tx.QueryRow(context.Background(), `SELECT pg_try_advisory_xact_lock(hashtext('github_reads:' || $1))`, "task:"+task).Scan(&free); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(context.Background())
	if !free {
		t.Error("the task's GitHub-reads lock is held while GitHub answers")
	}
	answered := make(chan struct{})
	go func() { w.must(task, "limits", `{}`); close(answered) }()
	select {
	case <-answered:
	case <-time.After(10 * time.Second):
		t.Error("the conductor's limits waited on the refresh")
	}
	close(release)
	if out := <-done; field(out, "refreshed") != float64(1) {
		t.Errorf("the held refresh: %v", out)
	}
}
