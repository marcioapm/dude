package delivery

import (
	"context"
	"strconv"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// recordAdopted asks for the task's automatic preview only when this call
// announced the adoption and the pull request is open (or a draft): a
// replay, which finds it announced, asks for none, and neither does one
// already merged or closed when it was taken over. (The adopt step itself
// escalates source_gone for those before recording; this keeps the record
// from depending on it.) The preview itself is the root package's
// TestAutoPreviewForAnAdoptedPullRequest.
func TestRecordAdoptedPreparesAPreviewOnlyWhenItAnnouncesAnOpenOne(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', $1, 'P', 'p', 'PA')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES
			('repo_a', $1, 'prj_a', 'target', 'https://github.com/acme/target.git', 'main')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES
			('wi_open', $1, 'prj_a', 1, 'T', 'g', 'running'),
			('wi_draft', $1, 'prj_a', 2, 'T', 'g', 'running'),
			('wi_merged', $1, 'prj_a', 3, 'T', 'g', 'running'),
			('wi_closed', $1, 'prj_a', 4, 'T', 'g', 'running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	asked := map[string]int{}
	s := &Store{DB: app, AutoPreview: func(_ context.Context, _ string, taskID string) error {
		asked[taskID]++
		return nil
	}}
	repo := Repository{ID: "repo_a", Name: "target", URL: "https://github.com/acme/target.git", DefaultBranch: "main"}
	record := func(task string, number int, state string) {
		t.Helper()
		st := &State{TaskID: task, ProjectID: "prj_a", Source: &Source{Kind: SourcePR, RepositoryID: repo.ID,
			Repository: repo.Name, Number: number, Title: "Theirs", HeadBranch: "feature/" + task, BaseBranch: "main"}}
		pr := forge.Status{PullRequestRef: forge.PullRequestRef{Number: number, State: state, HeadSHA: "abc",
			URL: "https://github.com/acme/target/pull/" + strconv.Itoa(number)}}
		if _, err := s.recordAdopted(ctx, org, st, repo, pr); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		task   string
		number int
		state  string
		want   int
	}{
		{"wi_open", 1, forge.StateOpen, 1},
		{"wi_draft", 2, forge.StateDraft, 1},
		{"wi_merged", 3, forge.StateMerged, 0},
		{"wi_closed", 4, forge.StateClosed, 0},
	} {
		record(c.task, c.number, c.state)
		if asked[c.task] != c.want {
			t.Fatalf("%s adopted %s: AutoPreview asked %d times, want %d", c.task, c.state, asked[c.task], c.want)
		}
		// The adoption is announced once, whether or not a preview was asked.
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`,
			c.task, EvPullRequestOpened).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d pull_request.opened events (%v), want one", c.task, n, err)
		}
		// A replay finds it announced (its upsert still affects the row).
		record(c.task, c.number, c.state)
		if asked[c.task] != c.want {
			t.Fatalf("%s: a replay of the adoption asked AutoPreview again (%d)", c.task, asked[c.task])
		}
	}
}
