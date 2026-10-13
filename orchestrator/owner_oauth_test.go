package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

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
	time.Sleep(50 * time.Millisecond)
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
