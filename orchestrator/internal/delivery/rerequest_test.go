package delivery

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A step replayed after its re-request (a crash before the transition
// committed) asks nobody again for the same head, even one GitHub refused
// (jerv is still not requested then); a later head is asked about afresh.
func TestAReplayedRerequestAsksOncePerHead(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()

	bare := filepath.Join(t.TempDir(), "target.git")
	seed := filepath.Join(t.TempDir(), "seed")
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"init", "-q", "-b", "feature", seed},
		{"-C", seed, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"-C", seed, "push", "-q", bare, "feature"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gh := fakegithub.New(bare, "acme/target")
	srv := httptest.NewServer(gh.Handler())
	t.Cleanup(srv.Close)

	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_q', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_q', $1, 'prj_q', 1, 'Greet', 'greet', 'review')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_q', $1, 'prj_q', 'target', 'https://github.com/acme/target.git', 'main')`,
		`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, body, state, checks)
			VALUES ('pr_q', $1, 'prj_q', 'wi_q', 'repo_q', 1, 'https://github.com/acme/target/pull/1', 'feature', 'main',
			'', 'Greet', '', 'open', 'unknown')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	client := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})
	if _, err := client.OpenPullRequest(ctx, forge.OpenPullRequest{Slug: "acme/target", Title: "Greet", Head: "feature", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	gh.Review(1, "jerv", "COMMENTED")
	st := &State{TaskID: "wi_q", ProjectID: "prj_q", PullRequestIDs: []string{"pr_q"}}
	s := &Store{DB: app}
	asks := func() int {
		n := 0
		gh.Set(func(s *fakegithub.Server) { n = len(s.ReviewRequests) })
		return n
	}

	head := gh.CommitOnTop("feature", "dude's fix")
	gh.Set(func(s *fakegithub.Server) { s.ReviewRequestStatus = 403 })
	for range 2 {
		if err := s.RerequestReviews(ctx, org, st, client, map[string]string{"target": head}); err != nil {
			t.Fatal(err)
		}
	}
	if n := asks(); n != 1 {
		t.Fatalf("%d review requests for one head, want 1", n)
	}

	gh.Set(func(s *fakegithub.Server) { s.ReviewRequestStatus = 0 })
	next := gh.CommitOnTop("feature", "dude's next fix")
	if err := s.RerequestReviews(ctx, org, st, client, map[string]string{"target": next}); err != nil {
		t.Fatal(err)
	}
	if n := asks(); n != 2 {
		t.Errorf("%d review requests after a second head, want 2", n)
	}
	var events int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE task_id = 'wi_q' AND event_type = $1`,
		EvPullRequestReviewRerequested).Scan(&events)
	if events != 2 {
		t.Errorf("%d re-request events, want one per head", events)
	}
}
