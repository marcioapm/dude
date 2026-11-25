package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// connectThroughCallback completes Connect GitHub for person through the
// control plane's real /connect and /callback routes over this test's
// database (testdata/connect-github.ts), GitHub's answers faked there. It
// may run on a workflow goroutine, so a failure is reported, not fatal.
func (w *world) connectThroughCallback(person, token string, githubID int) {
	_, file, _, _ := runtime.Caller(0)
	c := w.owner.Config()
	cmd := exec.Command("bun", "run", "orchestrator/testdata/connect-github.ts", w.org, person, token, strconv.Itoa(githubID))
	cmd.Dir = filepath.Join(filepath.Dir(file), "..")
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("DATABASE_URL=postgres://dude_app:dude_app@%s:%d/%s", c.Host, c.Port, c.Database))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || string(out) != "/?github=connected#/me/settings" {
		w.t.Errorf("Connect GitHub callback: %v\nstdout: %s\nstderr: %s", err, out, stderr.Bytes())
	}
}

// Connect completes while the publication step that found no authorization
// is still committing its park: the stored signal resumes it, once, as the
// frozen owner, without spending the workflow's retry budget.
func TestAConnectBeforeThePublicationParksResumesIt(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task := w.task()
	w.taskOwners(task, ana)
	var parking atomic.Bool
	attemptAtPark := -1
	armed := w.gated(func(step, next string) {
		if step == "openPullRequest" && next == "openPullRequest" && w.taskStatus(task) == "awaiting_input" && parking.CompareAndSwap(false, true) {
			attemptAtPark = w.attempts(task)
			w.connectThroughCallback(ana, "ana-token", 1)
		}
	})
	armed.Store(true)
	w.deliver(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	if !parking.Load() || attemptAtPark != 0 {
		t.Fatalf("parked %v at attempt %d", parking.Load(), attemptAtPark)
	}
	if got := w.gh.OpenAuth; len(got) != 1 || got[0] != "Bearer ana-token" {
		t.Fatalf("creation auth %v", got)
	}
	if n := w.consumedAuthorizations(task); n != 1 {
		t.Fatalf("%d consumed authorization signals", n)
	}
}

// "Let Deliver finish it" with openPullRequest confirmed authorizes the
// gate; the author is frozen then, not when the step later executes.
func TestAManualOpenConfirmationFreezesItsAuthorBeforeExecution(t *testing.T) {
	w := conducting(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	task := w.task()
	w.taskOwners(task, ana, bo)
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	w.reachGate(task)
	if status, out := w.postAsPerson("/internal/tasks/"+task+"/decider", ana, map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("hand back with confirmation: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana); n != 1 {
		t.Fatalf("confirmation froze %d Ana snapshots", n)
	}
	mustExec(t, w.owner, `UPDATE task_people SET position = CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id = $1`, task, bo)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if got := w.gh.OpenAuth; len(got) != 1 || got[0] != "Bearer ana-token" {
		t.Fatalf("authorization %v, want Ana frozen at confirmation", got)
	}
}

func (w *world) ownerPerson(name string) string {
	w.t.Helper()
	id := "per_" + strings.ToLower(name) + "_" + w.org
	mustExec(w.t, w.owner, `INSERT INTO people (id, organization_id, name, email) VALUES ($1, $2, $3, $4)`,
		id, w.org, name, strings.ToLower(name)+"@example.test")
	return id
}

func (w *world) connectOwner(person, token string, githubID int) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret'
		WHERE organization_id = $1`, w.org)
	mustExec(w.t, w.owner, `INSERT INTO github_user_authorizations (organization_id, person_id, credential_id,
		credential_version, github_user_id, github_login, access_token, refresh_token, expires_at, refresh_expires_at)
		SELECT $1, $2, id, oauth_version, $4, 'owner', $3, 'refresh', now() + interval '1 hour', now() + interval '1 day'
		FROM forge_credentials WHERE organization_id = $1
		ON CONFLICT (organization_id, person_id, credential_id) DO UPDATE SET access_token = EXCLUDED.access_token`,
		w.org, person, token, githubID)
}

func (w *world) taskOwners(task string, people ...string) {
	for i, p := range people {
		mustExec(w.t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ($1, $2, $3, $4)`, task, p, w.org, i)
	}
}

func (w *world) authorizeAgain(task, person string) {
	var wf string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM workflow_runs WHERE task_id = $1`, task).Scan(&wf); err != nil {
		w.t.Fatal(err)
	}
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalGitHubAuthorized, map[string]string{"personId": person}, ""); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) attempts(task string) int {
	return w.count(`SELECT attempt FROM workflow_runs WHERE task_id = $1`, task)
}

func (w *world) chatAs(task, person, text string) (int, map[string]any) {
	return w.postAsPerson("/internal/tasks/"+task+"/chat", person, map[string]any{"text": text})
}

// postAsPerson posts to the internal API as the backend does for a signed-in person.
func (w *world) postAsPerson(path, person string, body any) (int, map[string]any) {
	w.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", w.api+path, bytes.NewReader(b))
	for k, v := range map[string]string{"Authorization": "Bearer svc", "X-Dude-Organization": w.org,
		"X-Dude-Person": person, "X-Dude-Actor": person, "X-Dude-Credential-Kind": "person", "Content-Type": "application/json"} {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// A fix may need a new PR: one for a repository it changed first, beside
// the open one. Missing authorization parks this secondary publication path
// without spending its fix budget. (Once every pull request has ended, no
// fix opens one: pr_ended_test.go.)
func TestAPRFixPublicationParksForTheFrozenOwner(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	scripted := w.lux.Decide
	var prOpen atomic.Bool
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		switch {
		case labels["dude.phase"] == "implement":
			// web is left for the pull request's fix to change first.
			b.Commit = map[string]string{"target:FACTORY.md": "work\n"}
		case labels["dude.phase"] == "fix" && prOpen.Load():
			b.Commit = map[string]string{"web:FIX.md": "fix\n"}
		}
		return b
	}
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	task := w.task()
	w.addWeb(task, "write")
	w.taskOwners(task, ana, bo)
	w.start(task, scriptedPolicy())
	w.until("the target pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	prOpen.Store(true)
	armed := w.gated(func(step, next string) {
		if step == "prFix" && next == "awaitPRFix" {
			mustExec(t, w.owner, `DELETE FROM github_user_authorizations WHERE person_id=$1`, ana)
			mustExec(t, w.owner, `UPDATE task_people SET position=CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id=$1`, task, bo)
		}
	})
	armed.Store(true)
	w.gh.ReviewSaying(1, "alice", "CHANGES_REQUESTED", "Please rename the helper.")
	w.sync()
	w.until("parked for Ana", func() bool { w.sync(); return w.taskStatus(task) == "awaiting_input" })
	armed.Store(false)
	if len(w.gh.Pulls()) != 1 || len(w.web.Pulls()) != 0 || w.attempts(task) != 0 {
		t.Fatalf("pulls %d+%d attempt %d", len(w.gh.Pulls()), len(w.web.Pulls()), w.attempts(task))
	}
	if w.web.SHA(delivery.BranchFor(task, 1)) == "" {
		t.Fatal("the fix's branch was not kept")
	}
	w.connectOwner(ana, "ana-again", 1)
	w.authorizeAgain(task, ana)
	// The fix reviewed and simplified on both pull requests: ready to
	// merge, the ready gate holding at both heads.
	w.until("the new pull request", func() bool {
		s := w.taskStatus(task)
		return len(w.web.Pulls()) == 1 && s == "ready_to_merge"
	})
	if got := w.web.OpenAuth; len(got) != 1 || got[0] != "Bearer ana-again" {
		t.Fatalf("new creation auth %v", got)
	}
	var iteration int
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE((state->>'prIteration')::int,0) FROM workflow_runs WHERE task_id=$1`, task).Scan(&iteration)
	if iteration != 1 {
		t.Fatalf("PR fix budget reset to %d", iteration)
	}
}

func TestAnApprovedGateFreezesItsAuthorBeforeExecution(t *testing.T) {
	w := conducting(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	task := w.task()
	w.taskOwners(task, ana, bo)
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chatAs(task, ana, "Open"); status != 200 {
		t.Fatalf("Ana answers Open: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana); n != 1 {
		t.Fatalf("gate answer froze %d Ana snapshots", n)
	}
	mustExec(t, w.owner, `UPDATE task_people SET position = CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id = $1`, task, bo)
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if len(w.gh.OpenAuth) != 1 || w.gh.OpenAuth[0] != "Bearer ana-token" {
		t.Fatalf("authorization %v, want Ana frozen at the gate", w.gh.OpenAuth)
	}
}

func TestAnOrdinaryApprovedTransitionFreezesItsAuthorBeforeExecution(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	task := w.task()
	w.taskOwners(task, ana, bo)
	w.deliver(task)
	w.stepTo(task, "openPullRequest")
	if n := w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana); n != 1 {
		t.Fatalf("approved transition froze %d Ana snapshots", n)
	}
	mustExec(t, w.owner, `UPDATE task_people SET position = CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id = $1`, task, bo)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if len(w.gh.OpenAuth) != 1 || w.gh.OpenAuth[0] != "Bearer ana-token" {
		t.Fatalf("authorization %v, want Ana", w.gh.OpenAuth)
	}
}

func TestAFrozenOwnerWithoutAuthorizationIsNotReplacedByTheNewPrimary(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	task := w.task()
	w.taskOwners(task, ana, bo)
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error { return delivery.FreezePublicationTx(ctx, tx, task, delivery.BranchFor(task, 1)) }); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `DELETE FROM github_user_authorizations WHERE person_id = $1`, ana)
	mustExec(t, w.owner, `UPDATE task_people SET position = CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id = $1`, task, bo)
	st := &delivery.State{TaskID: task, ProjectID: w.project, Branch: delivery.BranchFor(task, 1), Heads: map[string]string{"target": "main"}}
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error { return delivery.NameOnlyRepository(ctx, tx, task) }); err != nil {
		t.Fatal(err)
	}
	_, err := (&delivery.Store{DB: w.app}).OpenPullRequests(ctx, w.org, st, forge.Resolver{DB: w.app})
	if !errorsAsOwner(err) || len(w.gh.OpenAuth) != 0 {
		t.Fatalf("missing frozen owner err=%v auth=%v", err, w.gh.OpenAuth)
	}
}

func errorsAsOwner(err error) bool {
	return errors.As(err, new(forge.OwnerAuthorizationError))
}

func TestARevokedOwnerTokenRequiresReconnectThenOpensOnce(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	task := w.task()
	w.taskOwners(task, ana)
	w.connectOwner(ana, "revoked-token", 1)
	w.gh.OpenRefuse = map[string]int{"revoked-token": 401}
	w.deliver(task)
	w.until("reconnect required", func() bool {
		return w.count(`SELECT count(*) FROM github_user_authorizations WHERE person_id = $1 AND reconnect_required`, ana) == 1 &&
			w.taskStatus(task) == "awaiting_input"
	})
	if w.attempts(task) != 0 || len(w.gh.Pulls()) != 0 {
		t.Fatalf("revocation spent retries or opened: attempt=%d pulls=%d", w.attempts(task), len(w.gh.Pulls()))
	}
	if got := w.credentialAsks(task); len(got) != 1 || got[0] != ana+" true" {
		t.Fatalf("credential asks %q, want one to reconnect for Ana", got)
	}
	mustExec(t, w.owner, `UPDATE github_user_authorizations SET access_token = 'fresh-token', reconnect_required = false WHERE person_id = $1`, ana)
	w.authorizeAgain(task, ana)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	var secrets int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE payload::text LIKE '%fresh-token%' OR payload::text LIKE '%revoked-token%'`).Scan(&secrets)
	leaked := w.count(`SELECT count(*) FROM workflow_runs WHERE COALESCE(last_error,'') LIKE '%-token%' OR state::text LIKE '%-token%'`)
	if secrets != 0 || leaked != 0 {
		t.Fatalf("token reached events=%d workflow=%d", secrets, leaked)
	}
	if got := w.gh.OpenAuth; len(got) != 2 || got[1] != "Bearer fresh-token" {
		t.Fatalf("creation auth %v", got)
	}
}

func TestALostCreateResponseIsAdoptedOnReplay(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	task := w.task()
	w.taskOwners(task, ana)
	w.connectOwner(ana, "ana-token", 1)
	w.gh.Set(func(s *fakegithub.Server) { s.LoseOpenResponses = 1 })
	w.deliver(task)
	w.until("adopted pull request", func() bool {
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1`, task) == 1
	})
	if len(w.gh.Pulls()) != 1 || len(w.gh.OpenAuth) != 1 || w.gh.OpenAuth[0] != "Bearer ana-token" {
		t.Fatalf("GitHub has %d pulls, creation auth %v", len(w.gh.Pulls()), w.gh.OpenAuth)
	}
}

func TestAnAppPullRequestWaitsForTheFrozenOwnerThenOpensOnceAsThem(t *testing.T) {
	w := newWorld(t)
	app := w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret'
		WHERE organization_id = $1`, w.org)
	wi := w.task()
	mustExec(t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		VALUES ($1, $2, $4, 0), ($1, $3, $4, 1)`, wi, ana, bo, w.org)
	w.deliver(wi)
	w.until("the owner authorization pause", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if pulls := len(w.gh.Pulls()); pulls != 0 {
		t.Fatalf("%d pull requests opened without owner authorization", pulls)
	}
	if n := w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, wi, ana); n != 1 {
		t.Fatalf("owner snapshot count %d", n)
	}
	for _, auth := range w.gh.OpenAuth {
		if auth != "" {
			t.Fatalf("PR creation attempted before connection with %q", auth)
		}
	}
	pushes := 0
	for _, r := range w.lux.Runs() {
		for _, creds := range r.PushCredentials {
			pushes++
			if app.Installation(creds["target"]) != 60000071 {
				t.Fatalf("push did not use installation token: %v", creds)
			}
		}
	}
	if pushes == 0 {
		t.Fatal("branch was not published before the pause")
	}

	mustExec(t, w.owner, `UPDATE task_people SET position = CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id = $1`, wi, bo)
	w.connectOwner(bo, "bo-token", 2)
	w.connectOwner(ana, "ana-token", 1)
	var wf string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM workflow_runs WHERE task_id = $1`, wi).Scan(&wf); err != nil {
		t.Fatal(err)
	}
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalGitHubAuthorized, map[string]string{"personId": ana}, "connect:"+ana); err != nil {
		t.Fatal(err)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if len(w.gh.OpenAuth) != 1 || w.gh.OpenAuth[0] != "Bearer ana-token" {
		t.Fatalf("PR creation authorization %v, want only Ana", w.gh.OpenAuth)
	}
	if _, _, err := w.runtime.Start(context.Background(), workflow.StartOptions{Type: delivery.WorkflowType, OrganizationID: w.org,
		IdempotencyKey: "delivery:" + wi, TaskID: wi}); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if len(w.gh.Pulls()) != 1 || len(w.gh.OpenAuth) != 1 {
		t.Fatalf("replay opened again: pulls=%d auth=%v", len(w.gh.Pulls()), w.gh.OpenAuth)
	}
}

func TestAPATPullRequestStillUsesThePAT(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if len(w.gh.OpenAuth) != 1 || w.gh.OpenAuth[0] != "Bearer ghp_test" {
		t.Fatalf("PAT PR creation authorization %v", w.gh.OpenAuth)
	}
	if n := w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1`, wi); n != 0 {
		t.Fatalf("PAT recorded %d App publication snapshots", n)
	}
}

func TestAnOwnersRepositoryRefusalIsShownWithoutFallback(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	w.gh.OpenRefuse = map[string]int{"ana-token": 403}
	wi := w.task()
	mustExec(t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		VALUES ($1, $2, $4, 0), ($1, $3, $4, 1)`, wi, ana, bo, w.org)
	w.deliver(wi)
	w.until("GitHub refusal", func() bool {
		var last string
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(last_error, '') FROM workflow_runs WHERE task_id = $1`, wi).Scan(&last)
		return strings.Contains(last, "Resource not accessible by integration")
	})
	for _, auth := range w.gh.OpenAuth {
		if auth != "Bearer ana-token" {
			t.Fatalf("fallback authorization %q", auth)
		}
	}
	if len(w.gh.Pulls()) != 0 {
		t.Fatal("refused owner opened a pull request")
	}
}

// credentialAsks are the task's question.asked of kind credential, each
// as "personId reconnect".
func (w *world) credentialAsks(task string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'personId' || ' ' || (payload->>'reconnect')
		FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'credential'
		  AND payload->>'reason' = 'owner_github' ORDER BY cursor`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	asks, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return asks
}

// waitingOnAuthorization: the delivery is parked at openPullRequest for
// github.authorized.
func (w *world) waitingOnAuthorization(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'waiting'
		AND step = 'openPullRequest' AND awaiting_signals ? $2`, task, delivery.SignalGitHubAuthorized) == 1
}

// consumedAuthorizations is the number of the task's github.authorized
// signals its workflow consumed.
func (w *world) consumedAuthorizations(task string) int {
	return w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1 AND s.name = $2 AND s.consumed_at IS NOT NULL`, task, delivery.SignalGitHubAuthorized)
}

// resweepPublication runs the delivery's parked publication step again,
// as a sweep of the step with no new signal, and waits for it to park
// again.
func (w *world) resweepPublication(task string) {
	w.t.Helper()
	tag, err := w.owner.Exec(context.Background(), `UPDATE workflow_runs SET status = 'running', wake_at = NULL
		WHERE task_id = $1 AND status = 'waiting' AND step = 'openPullRequest'`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	if n := tag.RowsAffected(); n != 1 {
		w.t.Fatalf("the sweep woke %d parked publications, want one", n)
	}
	w.until("the step swept again", func() bool { return w.waitingOnAuthorization(task) })
}

// A pull request waiting on its owner's GitHub is the owner's item: one
// question.asked of kind credential, addressed to them, which a sweep of
// the step while they still have not connected does not repeat. Connecting
// opens the pull request, and the wait is over.
func TestAnOwnerWithoutGitHubIsAskedOnceThenThePullRequestOpens(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task := w.task()
	w.taskOwners(task, ana)
	w.deliver(task)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) && w.taskStatus(task) == "awaiting_input" })
	if got := w.credentialAsks(task); len(got) != 1 || got[0] != ana+" false" {
		t.Fatalf("credential asks %q, want one for Ana", got)
	}
	var branch string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'branch' FROM events WHERE task_id = $1 AND payload->>'kind' = 'credential'`, task).Scan(&branch)
	if branch != delivery.BranchFor(task, 1) {
		t.Fatalf("the ask names branch %q", branch)
	}
	// The step runs again with Ana still not connected: nobody is told twice.
	w.resweepPublication(task)
	if got := w.credentialAsks(task); len(got) != 1 {
		t.Fatalf("a second sweep asked again: %q", got)
	}
	// No conductor decides it: nobody to tell, and nothing fails for it.
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("%d conductor wakes for a delivery no conductor decides", n)
	}
	if len(w.gh.Pulls()) != 0 || w.attempts(task) != 0 {
		t.Fatalf("pulls %d attempt %d", len(w.gh.Pulls()), w.attempts(task))
	}
	w.connectOwner(ana, "ana-token", 1)
	w.authorizeAgain(task, ana)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	if w.waitingOnAuthorization(task) {
		t.Fatal("still waiting on the authorization")
	}
	if got := w.gh.OpenAuth; len(got) != 1 || got[0] != "Bearer ana-token" {
		t.Fatalf("creation auth %v", got)
	}
	if got := w.credentialAsks(task); len(got) != 1 {
		t.Fatalf("credential asks after opening %q", got)
	}
}

// A conducted delivery whose pull request waits on its owner's GitHub
// tells its conductor so, once, in words it can pass on: it must not
// answer "what's next?" with "nothing is needed from you".
func TestAConductorIsToldThePullRequestWaitsOnItsOwnersGitHub(t *testing.T) {
	w := conducting(t)
	w.asApp()
	ana := w.ownerPerson("Ana Lima")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task := w.task()
	w.taskOwners(task, ana)
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chatAs(task, ana, "Open"); status != 200 {
		t.Fatalf("Ana answers Open: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) })
	if got := w.credentialAsks(task); len(got) != 1 || got[0] != ana+" false" {
		t.Fatalf("credential asks %q", got)
	}
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = $2`,
		task, delivery.WakeOwnerGitHub).Scan(&line); err != nil {
		t.Fatalf("the conductor was not told: %v", err)
	}
	if !strings.Contains(line, "until Ana connects GitHub") || !strings.Contains(line, delivery.BranchFor(task, 1)) ||
		!strings.Contains(line, "nothing is redone") {
		t.Fatalf("the conductor's line: %q", line)
	}
	w.resweepPublication(task)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeOwnerGitHub); n != 1 {
		t.Fatalf("the conductor was told %d times", n)
	}
}

// A conducted delivery's owner, asked to connect, connects with a token
// GitHub then refuses: they are asked again, to reconnect, and the
// conductor is told that too. A further sweep asks nobody again.
func TestAnOwnerWhoConnectsThenIsRefusedIsAskedToReconnect(t *testing.T) {
	w := conducting(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task := w.task()
	w.taskOwners(task, ana)
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chatAs(task, ana, "Open"); status != 200 {
		t.Fatalf("Ana answers Open: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) })
	if got := w.credentialAsks(task); len(got) != 1 || got[0] != ana+" false" {
		t.Fatalf("credential asks %q, want one to connect for Ana", got)
	}
	wakes := func() int {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeOwnerGitHub)
	}
	if n := wakes(); n != 1 {
		t.Fatalf("the conductor was told %d times", n)
	}
	// Ana connects; GitHub refuses the token as the pull request is created.
	w.gh.OpenRefuse = map[string]int{"bad-token": 401}
	w.connectOwner(ana, "bad-token", 1)
	w.authorizeAgain(task, ana)
	w.until("the refusal swept", func() bool {
		return w.consumedAuthorizations(task) == 1 && w.waitingOnAuthorization(task) &&
			w.count(`SELECT count(*) FROM github_user_authorizations WHERE person_id = $1 AND reconnect_required`, ana) == 1
	})
	if got := w.credentialAsks(task); len(got) != 2 || got[0] != ana+" false" || got[1] != ana+" true" {
		t.Fatalf("credential asks %q, want connect then reconnect for Ana", got)
	}
	if n := wakes(); n != 2 {
		t.Fatalf("the conductor was told %d times, want twice", n)
	}
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = $2
		ORDER BY created_at DESC, id DESC LIMIT 1`, task, delivery.WakeOwnerGitHub).Scan(&line); err != nil || !strings.Contains(line, "reconnects GitHub") {
		t.Fatalf("the conductor's second line: %q %v", line, err)
	}
	// Swept again with Ana still to reconnect: nobody is asked a third time.
	w.resweepPublication(task)
	if got := w.credentialAsks(task); len(got) != 2 {
		t.Fatalf("a further sweep asked again: %q", got)
	}
	if n := wakes(); n != 2 {
		t.Fatalf("a further sweep told the conductor again: %d", n)
	}
	if len(w.gh.Pulls()) != 0 || w.attempts(task) != 0 {
		t.Fatalf("pulls %d attempt %d", len(w.gh.Pulls()), w.attempts(task))
	}
}

// Each Connect that GitHub then refuses asks the owner again: asked to
// connect, refused, asked to reconnect, refused again, asked to reconnect
// again, and the conductor told each time. A sweep with no new Connect
// asks nobody.
func TestAnOwnerRefusedAgainAfterReconnectingIsAskedAgain(t *testing.T) {
	w := conducting(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task := w.task()
	w.taskOwners(task, ana)
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chatAs(task, ana, "Open"); status != 200 {
		t.Fatalf("Ana answers Open: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) })
	wakes := func() int {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeOwnerGitHub)
	}
	refused := func() bool {
		return w.count(`SELECT count(*) FROM github_user_authorizations WHERE person_id = $1 AND reconnect_required`, ana) == 1
	}
	w.gh.OpenRefuse = map[string]int{"bad-token": 401, "bad-again": 401}
	// Ana connects; GitHub refuses the token as the pull request is created.
	w.connectThroughCallback(ana, "bad-token", 1)
	w.until("the refusal swept", func() bool { return w.consumedAuthorizations(task) == 1 && w.waitingOnAuthorization(task) && refused() })
	// She reconnects; GitHub refuses that token too.
	w.connectThroughCallback(ana, "bad-again", 1)
	w.until("the second refusal swept", func() bool { return w.consumedAuthorizations(task) == 2 && w.waitingOnAuthorization(task) && refused() })
	want := []string{ana + " false", ana + " true", ana + " true"}
	if got := w.credentialAsks(task); !slices.Equal(got, want) {
		t.Fatalf("credential asks %q, want %q", got, want)
	}
	if n := wakes(); n != 3 {
		t.Fatalf("the conductor was told %d times, want three", n)
	}
	// Swept again with no new Connect of hers, once on someone else's
	// github.authorized and once on nothing: nobody is asked a fourth time.
	w.authorizeAgain(task, bo)
	w.until("the step swept on Bo's", func() bool { return w.consumedAuthorizations(task) == 3 && w.waitingOnAuthorization(task) })
	w.resweepPublication(task)
	if got := w.credentialAsks(task); len(got) != 3 {
		t.Fatalf("a further sweep asked again: %q", got)
	}
	if n := wakes(); n != 3 {
		t.Fatalf("a further sweep told the conductor again: %d", n)
	}
	if len(w.gh.Pulls()) != 0 || w.attempts(task) != 0 {
		t.Fatalf("pulls %d attempt %d", len(w.gh.Pulls()), w.attempts(task))
	}
	// She disconnects, which signals nobody: swept again, she is asked to
	// connect, not to reconnect, and that is a new question.
	mustExec(t, w.owner, `DELETE FROM github_user_authorizations WHERE person_id = $1`, ana)
	w.resweepPublication(task)
	want = append(want, ana+" false")
	if got := w.credentialAsks(task); !slices.Equal(got, want) {
		t.Fatalf("credential asks after disconnecting %q, want %q", got, want)
	}
}

// A Connect that lands while the publication step is calling GitHub with
// the previous token is not credited with that token's refusal: the step
// asks to reconnect for the Connect it ran on, then runs on the new one,
// and its refusal asks again.
func TestAConnectDuringARefusedPublicationIsAskedAboutWhenRefused(t *testing.T) {
	w := conducting(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task := w.task()
	w.taskOwners(task, ana)
	w.reachGate(task)
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chatAs(task, ana, "Open"); status != 200 {
		t.Fatalf("Ana answers Open: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) })
	refused := func(token string) bool {
		return w.count(`SELECT count(*) FROM github_user_authorizations WHERE person_id = $1 AND access_token = $2
			AND reconnect_required`, ana, token) == 1
	}
	var reconnected atomic.Bool
	w.gh.Set(func(s *fakegithub.Server) {
		s.OpenRefuse = map[string]int{"bad-token": 401, "bad-again": 401}
		// While GitHub weighs the first token, Ana presses Connect again.
		s.Intercept = func(r *http.Request) int {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") &&
				r.Header.Get("Authorization") == "Bearer bad-token" && reconnected.CompareAndSwap(false, true) {
				w.connectThroughCallback(ana, "bad-again", 1)
			}
			return 0
		}
	})
	w.connectThroughCallback(ana, "bad-token", 1)
	w.until("both refusals swept", func() bool {
		return w.consumedAuthorizations(task) == 2 && w.waitingOnAuthorization(task) && refused("bad-again")
	})
	if !reconnected.Load() {
		t.Fatal("the second Connect never ran during the first token's request")
	}
	var auth []string
	w.gh.Set(func(s *fakegithub.Server) { auth = slices.Clone(s.OpenAuth) })
	if want := []string{"Bearer bad-token", "Bearer bad-again"}; !slices.Equal(auth, want) {
		t.Fatalf("creation auth %q, want %q", auth, want)
	}
	want := []string{ana + " false", ana + " true", ana + " true"}
	if got := w.credentialAsks(task); !slices.Equal(got, want) {
		t.Fatalf("credential asks %q, want %q", got, want)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeOwnerGitHub); n != 3 {
		t.Fatalf("the conductor was told %d times, want three", n)
	}
	if len(w.gh.Pulls()) != 0 || w.attempts(task) != 0 {
		t.Fatalf("pulls %d attempt %d", len(w.gh.Pulls()), w.attempts(task))
	}
}

// An owner's github.authorized on another delivery is not an authorization
// this one has seen: swept again, this delivery asks nobody.
func TestAnotherDeliverysConnectDoesNotAskAgain(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana := w.ownerPerson("Ana")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	first, other := w.task(), w.task()
	w.taskOwners(first, ana)
	w.taskOwners(other, ana)
	w.deliver(first)
	w.deliver(other)
	for _, task := range []string{first, other} {
		w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) && w.taskStatus(task) == "awaiting_input" })
	}
	w.authorizeAgain(other, ana)
	w.until("the other delivery swept", func() bool {
		return w.waitingOnAuthorization(other) && w.consumedAuthorizations(other) == 1
	})
	w.resweepPublication(first)
	if got := w.credentialAsks(first); !slices.Equal(got, []string{ana + " false"}) {
		t.Fatalf("credential asks %q, want one for Ana", got)
	}
	// Nor is another signal of this run's that names her.
	mustExec(t, w.owner, `INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, consumed_at)
		SELECT 'sig_other_' || id, organization_id, id, $2, jsonb_build_object('personId', $3::text), now()
		FROM workflow_runs WHERE task_id = $1`, first, delivery.SignalConductorDecision, ana)
	w.resweepPublication(first)
	if got := w.credentialAsks(first); !slices.Equal(got, []string{ana + " false"}) {
		t.Fatalf("credential asks after another signal %q, want one for Ana", got)
	}
}

// takeOver makes person the task's owner through the control plane's real
// PATCH /v1/tasks/:id over this test's database (testdata/take-over.ts).
func (w *world) takeOver(task, person string) {
	w.t.Helper()
	_, file, _, _ := runtime.Caller(0)
	c := w.owner.Config()
	cmd := exec.Command("bun", "run", "orchestrator/testdata/take-over.ts", w.org, person, task)
	cmd.Dir = filepath.Join(filepath.Dir(file), "..")
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("DATABASE_URL=postgres://dude_app:dude_app@%s:%d/%s", c.Host, c.Port, c.Database))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || string(out) != "200" {
		w.t.Fatalf("take over: %v\nstdout: %s\nstderr: %s", err, out, stderr.Bytes())
	}
}

// The owner switching, while the pull request waits on the old owner's
// GitHub, to someone who has connected opens it as them, at once. A switch
// to someone who has not connected changes nothing.
func TestTakingOverAWaitingPullRequestOpensItAsTheNewOwner(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana, bo, cy := w.ownerPerson("Ana"), w.ownerPerson("Bo"), w.ownerPerson("Cy")
	task := w.task()
	w.taskOwners(task, ana)
	w.connectOwner(bo, "bo-token", 2)
	w.deliver(task)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) && w.taskStatus(task) == "awaiting_input" })
	w.takeOver(task, cy)
	w.pump()
	if !w.waitingOnAuthorization(task) || len(w.gh.Pulls()) != 0 ||
		w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana) != 1 {
		t.Fatal("a switch to someone not connected moved the publication")
	}
	w.takeOver(task, bo)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	if got := w.gh.OpenAuth; len(got) != 1 || got[0] != "Bearer bo-token" {
		t.Fatalf("creation auth %v, want Bo's", got)
	}
	if w.attempts(task) != 0 {
		t.Fatalf("the switch spent attempt %d", w.attempts(task))
	}
}

// takenOverByUnconnectedCy parks a delivery on Ana's GitHub, then Cy, who
// has not connected, takes the task over: the publication stays Ana's.
func takenOverByUnconnectedCy(t *testing.T) (w *world, task, ana, cy string) {
	w = newWorld(t)
	w.asApp()
	ana, cy = w.ownerPerson("Ana"), w.ownerPerson("Cy")
	mustExec(t, w.owner, `UPDATE forge_credentials SET oauth_client_id = 'client', oauth_client_secret = 'secret' WHERE organization_id = $1`, w.org)
	task = w.task()
	w.taskOwners(task, ana)
	w.deliver(task)
	w.until("the owner authorization wait", func() bool { return w.waitingOnAuthorization(task) && w.taskStatus(task) == "awaiting_input" })
	w.takeOver(task, cy)
	w.pump()
	if !w.waitingOnAuthorization(task) || len(w.gh.Pulls()) != 0 ||
		w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana) != 1 {
		t.Fatal("a take-over by someone not connected moved the publication")
	}
	return w, task, ana, cy
}

// The new owner who took over before connecting opens the pull request as
// they connect: the publication passes from Ana to them.
func TestATakenOverPullRequestOpensAsTheNewOwnerWhenTheyConnect(t *testing.T) {
	w, task, ana, cy := takenOverByUnconnectedCy(t)
	w.connectThroughCallback(cy, "cy-token", 3)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	if got := w.gh.OpenAuth; len(got) != 1 || got[0] != "Bearer cy-token" {
		t.Fatalf("creation auth %v, want Cy's", got)
	}
	if w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, cy) != 1 ||
		w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana) != 0 {
		t.Fatal("the publication is still frozen to Ana")
	}
	if w.attempts(task) != 0 {
		t.Fatalf("connecting spent attempt %d", w.attempts(task))
	}
}

// The new owner, connected after taking over, presses Take over again (the
// card's "Take over and open it as me"): the owner is unchanged, and the
// pull request opens as them.
func TestTakingOverAgainOnceConnectedOpensThePullRequest(t *testing.T) {
	w, task, ana, cy := takenOverByUnconnectedCy(t)
	w.connectOwner(cy, "cy-token", 3)
	w.pump()
	if !w.waitingOnAuthorization(task) || len(w.gh.Pulls()) != 0 {
		t.Fatal("connecting outside the callback opened it")
	}
	w.takeOver(task, cy)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	if got := w.gh.OpenAuth; len(got) != 1 || got[0] != "Bearer cy-token" {
		t.Fatalf("creation auth %v, want Cy's", got)
	}
	if w.count(`SELECT count(*) FROM pr_publications WHERE task_id = $1 AND owner_person_id = $2`, task, ana) != 0 {
		t.Fatal("the publication is still frozen to Ana")
	}
}
