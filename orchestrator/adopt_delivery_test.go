package orchestrator_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// adopting makes a task that starts from source (as task_sources keeps
// what the backend checked) and starts its delivery through the API.
func (w *world) adopting(kind string, number int, head, base string) string {
	w.t.Helper()
	wi := w.task()
	var n any
	if kind == "pr" {
		n = number
	}
	mustExec(w.t, w.owner, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, title, head_branch, base_branch)
		VALUES ($1, $2, $3, $4, $5, 'Taken over', $6, $7)`, wi, w.org, w.repoID, kind, n, head, base)
	mustExec(w.t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'write')`,
		w.org, wi, w.repoID)
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
		w.t.Fatalf("deliver: %d %v", status, body)
	}
	return wi
}

func TestAnAdoptedSourceChangedBeforeExecutionCannotRun(t *testing.T) {
	for _, change := range []string{"protection", "head", "base", "closed", "write access"} {
		t.Run(change, func(t *testing.T) {
			w := newWorld(t)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			wi := w.adopting("pr", n, "feature/greet", "main")
			pr := w.gh.Pull(n)
			w.gh.Set(func(s *fakegithub.Server) {
				switch change {
				case "protection":
					s.Protected = map[string]bool{"feature/greet": true}
				case "head":
					pr.Head = "main"
				case "base":
					pr.Base = "other-base"
				case "closed":
					pr.State = "closed"
				case "write access":
					s.ReceiveStatus = 403
				}
			})
			w.until("source refusal", func() bool { return w.taskStatus(wi) == "awaiting_input" })
			if got := w.phasesRun(wi); len(got) != 0 {
				t.Fatalf("changed source started phases: %v", got)
			}
		})
	}
}

func TestAStartWaitsForAnInFlightSourceReplacement(t *testing.T) {
	w := newWorld(t)
	w.gh.Branch("feature/old", "main", "Old")
	w.gh.Branch("feature/new", "main", "New")
	wi := w.task()
	mustExec(t, w.owner, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, head_branch, base_branch)
		VALUES ($1, $2, $3, 'branch', 'feature/old', 'main')`, wi, w.org, w.repoID)
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'write')`,
		w.org, wi, w.repoID)
	ctx := context.Background()
	tx, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR UPDATE`, wi); err != nil {
		t.Fatal(err)
	}
	started := make(chan int, 1)
	go func() {
		status, _ := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{})
		started <- status
	}()
	w.blockedBehind(tx)
	if _, err := tx.Exec(ctx, `UPDATE task_sources SET head_branch = 'feature/new' WHERE task_id = $1`, wi); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-started:
		if status != 201 {
			t.Fatalf("deliver: %d", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deliver did not finish after the source save committed")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->>'branch' = 'feature/new'`, wi); n != 1 {
		t.Fatalf("delivery did not take the committed source")
	}
}

// blockedBehind waits until another backend is waiting on a lock tx holds.
func (w *world) blockedBehind(tx pgx.Tx) {
	w.t.Helper()
	var pid int
	if err := tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		w.t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if w.count(`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, pid) > 0 {
			return
		}
	}
	w.t.Fatal("nothing waited on the held transaction")
}

// A start joining a live delivery (a first Chat message, a fired start)
// while an escalation decision holds the delivery and then takes the task
// waits for it rather than deadlocking.
func TestAStartBesideAnEscalationDecisionDoesNotDeadlock(t *testing.T) {
	w := newWorld(t)
	w.gh.Branch("feature/lock", "main", "Lock")
	wi := w.adopting("branch", 0, "feature/lock", "main")
	ctx := context.Background()
	tx, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if d, err := delivery.LoadDelivery(ctx, tx, wi); err != nil || d == nil {
		t.Fatalf("delivery: %v %v", d, err)
	}
	started := make(chan error, 1)
	go func() {
		started <- w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			_, err := delivery.StartTx(ctx, tx, w.runtime, delivery.StartInput{Org: w.org, TaskID: wi, How: delivery.HowDeliver})
			return err
		})
	}()
	w.blockedBehind(tx)
	if err := delivery.LockTaskTx(ctx, tx, wi); err != nil {
		t.Fatalf("the decision's task lock: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("start beside the decision: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the start did not finish after the decision committed")
	}
}

// An adopted pull request under the GitHub App needs no owner's connection:
// nothing is created, the delivery waits on that one pull request.
func TestAnAdoptedPullRequestUnderTheAppNeedsNoOwnerConnection(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret'
		WHERE organization_id = $1`, w.org)
	n := w.gh.OpenAsPerson("alice", "feature/app", "main", "App auth")
	wi := w.task()
	w.taskOwners(wi, ana)
	mustExec(t, w.owner, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, title, head_branch, base_branch)
		VALUES ($1, $2, $3, 'pr', $4, 'App auth', 'feature/app', 'main')`, wi, w.org, w.repoID, n)
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'write')`,
		w.org, wi, w.repoID)
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("deliver: %d %v", status, body)
	}
	w.until("waiting on the adopted pull request", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'`, wi) == 1
	})
	if len(w.gh.Pulls()) != 1 || len(w.gh.OpenAuth) != 0 {
		t.Fatalf("pulls=%d creation auth=%v", len(w.gh.Pulls()), w.gh.OpenAuth)
	}
}

// What people already said on an adopted pull request is recorded from
// GitHub before its first reviewer is created, so that reviewer's context
// (AdoptedContextTx, read when it is submitted) has it.
func TestTheFirstReviewerOfAnAdoptedPullRequestSeesItsFeedback(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/said", "main", "Already discussed")
	w.gh.Comment(n, "jerv", "Please keep the old greeting as a fallback.")
	wi := w.adopting("pr", n, "feature/said", "main")
	w.until("the first reviewer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	if n := w.count(`SELECT count(*) FROM events e WHERE e.task_id = $1 AND e.event_type = 'pull_request.commented'
		AND e.payload->>'body' = 'Please keep the old greeting as a fallback.'
		AND e.occurred_at <= (SELECT min(created_at) FROM runs WHERE task_id = $1 AND phase = 'review')`, wi); n != 1 {
		t.Fatalf("existing feedback recorded before the first reviewer: %d", n)
	}
	var adopted *delivery.AdoptedContext
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		adopted, err = delivery.AdoptedContextTx(context.Background(), tx, wi, 1)
		return err
	}); err != nil || adopted == nil || len(adopted.Said) != 1 || adopted.Said[0].Author != "jerv" {
		t.Fatalf("reviewer context %+v: %v", adopted, err)
	}
}

// phasesRun lists the phase of each Run lux was given, in order.
func (w *world) phasesRun(wi string) []string {
	rows, err := w.owner.Query(context.Background(), `SELECT phase::text FROM runs WHERE task_id = $1 AND phase IS NOT NULL ORDER BY created_at`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

// A pull request a person opened is taken over: dude reviews it (nothing
// is implemented), fixes what review found on the same branch by
// fast-forward, and waits on that pull request — never opening another.
// Feedback on it wakes a fixer whose commit lands on the same branch, and
// the reviewer whose review that commit made stale is asked again.
func TestAnAdoptedPullRequestIsReviewedAndFixedInPlace(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet in Portuguese")
	start := w.gh.SHA("feature/greet")
	w.gh.ReviewSaying(n, "jerv", "APPROVED", "")

	wi := w.adopting("pr", n, "feature/greet", "main")
	w.until("waiting on the pull request", func() bool {
		return w.taskStatus(wi) == "review" && w.fixes(wi) == 0 &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'`, wi) == 1
	})

	if got := w.phasesRun(wi); !slices.Equal(got, []string{"review", "fix", "review", "simplify"}) {
		t.Fatalf("phases %v, want review, fix, re-review, simplify (no implement)", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests, want the one taken over", len(w.gh.Pulls()))
	}
	// The review loop's commits are on the person's branch, on top of theirs.
	log := w.gh.Log("feature/greet")
	if len(log) < 4 || !strings.HasPrefix(log[0], "Simplify") || !strings.HasPrefix(log[1], "Address review findings") ||
		!slices.Contains(log, "Start Greet in Portuguese") {
		t.Fatalf("feature/greet history %v", log)
	}
	if out, err := exec.Command("git", "-C", w.gh.Repo, "merge-base", "--is-ancestor", start, "refs/heads/feature/greet").CombinedOutput(); err != nil {
		t.Fatalf("the person's commit %s is not under feature/greet's head: %v %s", start, err, out)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND adopted AND number = 1 AND base_branch = 'main'`, wi); n != 1 {
		t.Fatalf("adopted pull request recorded %d times", n)
	}
	// jerv reviewed the person's head: the loop's push made it stale.
	if reqs := w.reviewRequests(); len(reqs) != 1 || !slices.Equal(reqs[0].Logins, []string{"jerv"}) {
		t.Fatalf("review requests %v, want jerv asked again once", reqs)
	}

	// Feedback on it is fixed on the same branch, by fast-forward.
	jervsHead := w.gh.SHA("feature/greet")
	w.gh.ReviewSaying(n, "jerv", "CHANGES_REQUESTED", "Please rename the greeting.")
	head := w.prFixPushed(wi)
	if head == jervsHead || len(w.gh.Pulls()) != 1 {
		t.Fatalf("fix head %s (was %s), %d pull requests", head, jervsHead, len(w.gh.Pulls()))
	}
	if out := w.gh.Log("feature/greet"); !strings.HasPrefix(out[0], "Address review findings") {
		t.Fatalf("feature/greet after the PR fix: %v", out)
	}
	if e := w.rerequests(wi); len(e) != 2 || e[1]["head"] != head || !slices.Equal(strs(e[1]["reviewersRequested"]), []string{"jerv"}) {
		t.Fatalf("re-requests %v, want jerv asked for the fix's head", e)
	}
	if reqs := w.reviewRequests(); len(reqs) != 2 {
		t.Fatalf("review requests %v", reqs)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests after the fix", len(w.gh.Pulls()))
	}
}

// A task whose PR fixer was aborted released its adopted branch; once a
// second task adopts it, no way of picking the first back up may publish
// on it, and the second task's delivery stays as it was.
func TestAStoppedAdoptedTaskCannotTakeBackABranchAnotherTaskAdopted(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/shared", "main", "Shared")
	first := w.adopting("pr", n, "feature/shared", "main")
	w.until("waiting on the pull request", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'`, first) == 1
	})
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.gh.Comment(n, "octo", "Please rename it")
	w.until("a fixer for the feedback", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`, first) == 1
	})
	var fix string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`,
		first).Scan(&fix)
	if status, body := w.call("/internal/runs/"+fix+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	w.until("the first task stopped", func() bool { return w.taskStatus(first) == "aborted" })

	second := w.adopting("pr", n, "feature/shared", "main")
	var secondWF, secondStatus string
	_ = w.owner.QueryRow(context.Background(), `SELECT id, status::text FROM workflow_runs WHERE task_id = $1`, second).
		Scan(&secondWF, &secondStatus)
	snapshot := func() string {
		var s string
		_ = w.owner.QueryRow(context.Background(), `SELECT t.status::text || ' ' || d.status::text || ' ' || d.step ||
			' ' || (SELECT count(*) FROM runs WHERE task_id = t.id)
			FROM tasks t JOIN workflow_runs d ON d.task_id = t.id WHERE t.id = $1`, first).Scan(&s)
		return s
	}
	before := snapshot()
	actions, _ := w.options(first)
	if !slices.Contains(actions, "retry") || !slices.Contains(actions, "restart") {
		t.Fatalf("recovery options %v", actions)
	}
	for _, action := range actions {
		status, body := w.call("/internal/tasks/"+first+"/recover", map[string]any{"action": action})
		if status != 409 || body["error"].(map[string]any)["code"] != "branch_taken" {
			t.Fatalf("%s beside the branch's new holder: %d %v", action, status, body)
		}
		if after := snapshot(); after != before {
			t.Fatalf("refused %s changed the first task: %s -> %s", action, before, after)
		}
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND id = $2 AND status::text = $3`,
		second, secondWF, secondStatus); n != 1 {
		t.Fatalf("the second task's delivery changed")
	}
}

// A pull request retargeted before its adoption ran stops the task, saying
// the two ways on that work: put it back and Try again, or stop and adopt
// it in a new task.
func TestARetargetedAdoptedPullRequestOffersWaysOnThatWork(t *testing.T) {
	retargeted := func(t *testing.T) (*world, *fakegithub.Pull, int, string) {
		w := newWorld(t)
		w.gh.Branch("stack/base", "main", "The base")
		n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
		pr := w.gh.Pull(n)
		w.gh.Set(func(*fakegithub.Server) { pr.Base = "stack/base" })
		first := w.adopting("pr", n, "feature/greet", "main")
		w.until("source refusal", func() bool { return w.taskStatus(first) == "awaiting_input" })
		var reason string
		_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail'->>'reason' FROM events
			WHERE task_id = $1 AND event_type = 'question.asked' ORDER BY occurred_at DESC LIMIT 1`, first).Scan(&reason)
		if want := "GitHub's pull request (or branch) no longer matches what this task adopted: it was feature/greet → main, " +
			"and is now feature/greet → stack/base. Put it back on GitHub and Try again, or stop this task and adopt it in a new one."; reason != want {
			t.Fatalf("source_changed says %q", reason)
		}
		return w, pr, n, first
	}
	t.Run("put back and Try again", func(t *testing.T) {
		w, pr, _, first := retargeted(t)
		w.gh.Set(func(*fakegithub.Server) { pr.Base = "main" })
		if status, body := w.call("/internal/tasks/"+first+"/decide", map[string]any{"action": "retry"}); status != 200 {
			t.Fatalf("try again: %d %v", status, body)
		}
		w.until("review of the restored pull request", func() bool { return slices.Contains(w.phasesRun(first), "review") })
	})
	t.Run("stop and adopt in a new task", func(t *testing.T) {
		w, _, n, first := retargeted(t)
		if status, body := w.call("/internal/tasks/"+first+"/decide", map[string]any{"action": "stop"}); status != 200 {
			t.Fatalf("stop: %d %v", status, body)
		}
		w.until("the first task stopped", func() bool { return w.taskStatus(first) == "aborted" })
		second := w.adopting("pr", n, "feature/greet", "stack/base")
		w.until("review of the retargeted pull request", func() bool { return slices.Contains(w.phasesRun(second), "review") })
	})
}

// commitFile commits path with content on top of branch in the fake's
// repository, as a person's push does; its SHA.
func (w *world) commitFile(branch, path, content, message string) string {
	w.t.Helper()
	index := filepath.Join(w.t.TempDir(), "index")
	git := func(stdin string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", w.gh.Repo}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_INDEX_FILE="+index, "GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com",
			"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			w.t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "read-tree", "refs/heads/"+branch)
	blob := git(content, "hash-object", "-w", "--stdin")
	git("", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	tree := git("", "write-tree")
	sha := git("", "commit-tree", tree, "-p", "refs/heads/"+branch, "-m", message)
	git("", "update-ref", "refs/heads/"+branch, sha)
	return sha
}

// A stacked branch's first review is of what it adds to its base: a file
// only the base changed is not among the paths reviewers are picked by.
func TestAStackedBranchIsReviewedForItsOwnChangesOnly(t *testing.T) {
	w := newWorld(t)
	w.gh.Branch("stack/base", "main", "The base")
	w.commitFile("stack/base", "migrations/001_base.sql", "CREATE TABLE base (id int);\n", "Base migration")
	w.gh.Branch("stack/top", "stack/base", "The top")
	w.commitFile("stack/top", "web/Top.tsx", "export const Top = 1;\n", "Top component")

	wi := w.adopting("branch", 0, "stack/top", "stack/base")
	w.until("the first review", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	var changed []string
	if err := w.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(state->'changedPaths'))
		FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, wi).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changed, []string{"target/web/Top.tsx"}) {
		t.Fatalf("changed paths %v, want only the top's file", changed)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT COALESCE(category, '') FROM runs WHERE task_id = $1 AND phase = 'review'
		ORDER BY category`, wi)
	if err != nil {
		t.Fatal(err)
	}
	categories, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(categories, "frontend") || slices.Contains(categories, "database") {
		t.Fatalf("first reviewers %v, want frontend for the top's file and no database for the base's", categories)
	}
}

// A branch is taken over: reviewed and fixed on itself, then the person's
// Open opens its pull request into the base they chose — a stacked branch,
// not main.
func TestAnAdoptedBranchOpensItsPullRequestIntoItsBase(t *testing.T) {
	w := newWorld(t)
	w.gh.Branch("stack/base", "main", "The base")
	w.gh.Branch("stack/top", "stack/base", "The top")

	wi := w.adopting("branch", 0, "stack/top", "stack/base")
	w.until("the person's Open or Draft", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	for range 3 {
		w.pump()
	}
	if len(w.gh.Pulls()) != 0 {
		t.Fatal("Deliver opened a person's branch without their Open or Draft")
	}
	if status, out := w.handBack(wi, map[string]any{"decider": "policy"}); status != 409 || !gateRefused(out) {
		t.Fatalf("hand-back without Open or Draft: %d %v", status, out)
	}
	if status, out := w.handBack(wi, map[string]any{"decider": "policy", "openPullRequest": true, "draft": true}); status != 200 {
		t.Fatalf("Draft: %d %v", status, out)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })

	pr := w.gh.Pulls()[0]
	if pr.Head != "stack/top" || pr.Base != "stack/base" || !pr.Draft {
		t.Fatalf("pull request %s into %s draft=%v, want draft stack/top into stack/base", pr.Head, pr.Base, pr.Draft)
	}
	if got := w.phasesRun(wi); !slices.Equal(got, []string{"review", "fix", "review", "simplify"}) {
		t.Fatalf("phases %v, want no implement", got)
	}
	log := w.gh.Log("stack/top")
	if !slices.Contains(log, "The top") || !slices.Contains(log, "The base") || !strings.HasPrefix(log[0], "Simplify") {
		t.Fatalf("stack/top history %v", log)
	}
	// The pull request is recorded into the base, as dude's own.
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND NOT adopted AND base_branch = 'stack/base'`, wi); n != 1 {
		t.Fatalf("pull request recorded %d times into stack/base", n)
	}
}
