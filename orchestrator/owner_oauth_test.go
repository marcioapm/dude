package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

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

// A fix may need a new PR after the original closes. Missing authorization
// parks this secondary publication path without spending its fix budget.
func TestAPRFixPublicationParksForTheFrozenOwner(t *testing.T) {
	w := newWorld(t)
	w.asApp()
	ana, bo := w.ownerPerson("Ana"), w.ownerPerson("Bo")
	w.connectOwner(ana, "ana-token", 1)
	w.connectOwner(bo, "bo-token", 2)
	task := w.task()
	w.taskOwners(task, ana, bo)
	w.deliver(task)
	w.until("the target pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
	armed := w.gated(func(step, next string) {
		if step == "prFix" && next == "awaitPRFix" {
			w.gh.Close(1)
			mustExec(t, w.owner, `UPDATE pull_requests SET state='closed' WHERE task_id=$1`, task)
			mustExec(t, w.owner, `DELETE FROM github_user_authorizations WHERE person_id=$1`, ana)
			mustExec(t, w.owner, `UPDATE task_people SET position=CASE person_id WHEN $2 THEN 0 ELSE 1 END WHERE task_id=$1`, task, bo)
		}
	})
	armed.Store(true)
	w.gh.ReviewSaying(1, "alice", "CHANGES_REQUESTED", "Please rename the helper.")
	w.sync()
	w.until("parked for Ana", func() bool { return w.taskStatus(task) == "awaiting_input" })
	armed.Store(false)
	if len(w.gh.Pulls()) != 1 || w.attempts(task) != 0 {
		t.Fatalf("pulls %d attempt %d", len(w.gh.Pulls()), w.attempts(task))
	}
	if w.gh.SHA(delivery.BranchFor(task, 1)) == "" {
		t.Fatal("the fix's branch was not kept")
	}
	w.connectOwner(ana, "ana-again", 1)
	w.authorizeAgain(task, ana)
	w.until("the new pull request", func() bool { return len(w.gh.Pulls()) == 2 && w.taskStatus(task) == "review" })
	if got := w.gh.OpenAuth; len(got) != 2 || got[1] != "Bearer ana-again" {
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
			if app.Installation(creds["target"]) != 71 {
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
