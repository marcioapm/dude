package delivery_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A task's own choice before its pull request is over its project's, which
// is over the factory's Open it; only its owner changes it, and never once
// its current attempt has had a pull request.
func TestATasksPullRequestChoiceIsItsOwnersUntilAPullRequestOpens(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, delivery_policy)
		VALUES ('prj_c', $1, 'P', 'p', 'CHO', '{"pullRequest":"draft"}')`, org)
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_c', $1, 'prj_c', 'api', 'https://github.com/acme/api.git')`, org)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_c', $1, 'prj_c', 1, 'T', 'G')`, org)
	exec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_sam', $1, 'Sam Reyes'), ('per_alex', $1, 'Alex Kim')`, org)
	exec(t, owner, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ('wi_c', 'per_sam', $1, 0)`, org)

	in := func(f func(tx pgx.Tx) error) error { return app.InOrg(ctx, org, f) }
	choice := func() string {
		var got string
		if err := in(func(tx pgx.Tx) (err error) {
			got, err = delivery.PullRequestChoiceTx(ctx, tx, "wi_c")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	set := func(person, to string) error {
		return in(func(tx pgx.Tx) error {
			_, err := delivery.SetPullRequestChoiceTx(ctx, tx, org, "wi_c", to, delivery.Actor{ActorType: "human", ActorID: person, Person: person})
			return err
		})
	}
	code := func(err error) string {
		r, _ := delivery.IsChoiceRefusal(err)
		return r.Code
	}

	if got := choice(); got != delivery.PullRequestDraft {
		t.Fatalf("with no choice of its own = %q, want the project's draft", got)
	}
	if err := set("per_alex", "ask"); code(err) != "not_owner" {
		t.Fatalf("someone else's change: %v", err)
	}
	if err := set("per_sam", "ask"); err != nil {
		t.Fatal(err)
	}
	if got := choice(); got != delivery.PullRequestAsk {
		t.Fatalf("after the owner's ask = %q", got)
	}
	if err := in(func(tx pgx.Tx) error {
		_, p, err := delivery.PolicyFor(ctx, tx, "wi_c")
		if err == nil && p.PullRequest != delivery.PullRequestAsk {
			t.Errorf("PolicyFor's pullRequest = %q, want the task's ask", p.PullRequest)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM events WHERE task_id = 'wi_c' AND event_type = $1
		AND payload->>'to' = 'ask' AND payload->'from' = 'null'::jsonb`, delivery.EvPullRequestChoiceChanged); n != 1 {
		t.Errorf("%d change events", n)
	}
	if err := set("per_sam", "later"); code(err) != "bad_request" {
		t.Fatalf("an unknown choice: %v", err)
	}
	// Back to the project's.
	if err := set("per_sam", ""); err != nil || choice() != delivery.PullRequestDraft {
		t.Fatalf("back to the project's: %v, %q", err, choice())
	}
	store := &delivery.Store{DB: app}
	if got, err := store.FirstPullRequestChoice(ctx, org, "wi_c"); err != nil || got != delivery.PullRequestDraft {
		t.Fatalf("before any pull request the opening reads %q, %v", got, err)
	}

	exec(t, owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_c', $1, 'prj_c', 'wi_c', 'repo_c', 1, 'https://x/1', 'b', 'main', 'h1', 'T', 'closed')`, org)
	err := set("per_sam", "ask")
	if r, _ := delivery.IsChoiceRefusal(err); r.Code != "pull_request_open" || r.Msg != delivery.PullRequestOpenText {
		t.Fatalf("a change with a pull request on record: %v", err)
	}
	// Only the first opening reads the choice: a later one (another
	// repository's, a re-open) opens as Deliver always has.
	if got, err := store.FirstPullRequestChoice(ctx, org, "wi_c"); err != nil || got != "" {
		t.Fatalf("with a pull request on record the opening reads %q, %v", got, err)
	}
}

func countRows(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
