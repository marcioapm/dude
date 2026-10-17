package delivery

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func must(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Promotion's record of merging a candidate's head is its own: the same
// commit in another repository, another round's candidate, or a later
// round of the same branch is not taken as attempted.
func TestAPromotionsMergeAttemptIsItsCandidatesOwn(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	must(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_m', $1, 'P', 'p', 'P')`, org)
	must(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		VALUES ('wi_m', $1, 'prj_m', 1, 'Greet', 'greet', 'running')`, org)
	s := &Store{DB: app}
	const commit = "c0ffee0000000000000000000000000000000000"
	at := func(round int, branch string) *State {
		return &State{TaskID: "wi_m", ProjectID: "prj_m", Branch: "dude/wi_m/attempt-1",
			Candidate: &Candidate{Round: round, Branch: branch}}
	}
	recorded := at(1, "dude/wi_m/attempt-1-candidate")
	if err := s.fixMergeAttemptTx(ctx, org, recorded, "target", commit); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name         string
		st           *State
		repo, commit string
		want         bool
	}{
		{"the round that recorded it", recorded, "target", commit, true},
		{"another repository", recorded, "web", commit, false},
		{"another commit", recorded, "target", "0ddba11000000000000000000000000000000000", false},
		{"a later round on the same branch", at(2, "dude/wi_m/attempt-1-candidate"), "target", commit, false},
		{"another attempt's candidate", at(1, "dude/wi_m/attempt-2-candidate"), "target", commit, false},
	} {
		got, err := s.fixMergeAttempted(ctx, org, c.st, c.repo, c.commit)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: attempted %v, want %v", c.name, got, c.want)
		}
	}
}

// Promotion judges a pull request's branch against what the task's branch
// merges into: an adopted source's base where the task keeps one, else the
// repository's default branch.
func TestAPromotionJudgesAgainstTheTasksBase(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	must(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_n', $1, 'P', 'p', 'P')`, org)
	must(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_n1', $1, 'prj_n', 'target', 'git://x/target.git', 'main'),
		       ('repo_n2', $1, 'prj_n', 'web', 'git://x/web.git', 'trunk')`, org)
	must(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		VALUES ('wi_n', $1, 'prj_n', 1, 'Greet', 'greet', 'running')`, org)
	must(t, owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access)
		VALUES ('wi_n', 'repo_n1', $1, 'write'), ('wi_n', 'repo_n2', $1, 'write')`, org)
	got, err := (&Store{DB: app}).baseBranches(ctx, org, &State{TaskID: "wi_n", Bases: map[string]string{"target": "release/2"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["target"] != "release/2" || got["web"] != "trunk" {
		t.Errorf("bases %v, want target's release/2 and web's default trunk", got)
	}
}
