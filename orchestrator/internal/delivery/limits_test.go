package delivery_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// limits reads each open pull request's rerun claim as the rerun check does
// (RerunClaimedTx): claimed at its head only, a released claim not counting,
// and another pull request's claim not its own.
func TestLimitsReadsEachPullRequestsRerunClaimAsTheCheckDoes(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_l', $1, 'P', 'p', 'LIM')`, org)
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		('repo_a', $1, 'prj_l', 'api', 'https://github.com/acme/api.git'), ('repo_w', $1, 'prj_l', 'web', 'https://github.com/acme/web.git')`, org)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_l', $1, 'prj_l', 1, 'T', 'G')`, org)
	prs := []struct {
		id, repo string
		number   int
		head     string
	}{
		{"pr_claimed", "repo_a", 1, "h1"},        // claimed at its head
		{"pr_moved", "repo_a", 2, "h2new"},       // claimed at an older head
		{"pr_released", "repo_w", 1, "h3"},       // claimed, then released
		{"pr_other", "repo_w", 2, "h4"},          // the same head as another PR's claim
		{"pr_released_again", "repo_w", 3, "h5"}, // one claim released, a later one not
	}
	for i, p := range prs {
		exec(t, owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
				base_branch, head_sha, title, created_at)
			VALUES ($1, $2, 'prj_l', 'wi_l', $3, $4, 'https://x/' || $1, 'b', 'main', $5, 'T', now() - make_interval(mins => 10 - $6::int))`,
			p.id, org, p.repo, p.number, p.head, i)
	}
	claim := func(id, repo string, number int, head, released string) {
		payload := map[string]any{"action": delivery.ActionRerunChecks, "repo": repo, "number": number, "head": head}
		if released != "" {
			payload = map[string]any{"released": released}
		}
		exec(t, owner, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, $3, 'wi_l', 'agent', 'run_x', 'test', $4)`, id, org, delivery.EvPullRequestAction, payload)
	}
	claim("evt_c1", "api", 1, "h1", "")
	claim("evt_c2", "api", 2, "h2old", "")
	claim("evt_c3", "web", 1, "h3", "")
	claim("evt_r3", "", 0, "", "evt_c3")
	claim("evt_c4", "web", 9, "h4", "")
	claim("evt_c5a", "web", 3, "h5", "")
	claim("evt_r5a", "", 0, "", "evt_c5a")
	claim("evt_c5b", "web", 3, "h5", "")

	want := map[string]bool{"api#1": true, "api#2": false, "web#1": false, "web#2": false, "web#3": true}
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		l, err := delivery.TaskLimitsTx(ctx, tx, "wi_l")
		if err != nil {
			return err
		}
		if len(l.PullRequests) != len(want) {
			t.Errorf("%d pull requests, want %d: %+v", len(l.PullRequests), len(want), l.PullRequests)
		}
		for _, p := range l.PullRequests {
			key := p.Repo + "#" + string(rune('0'+p.Number))
			check, err := delivery.RerunClaimedTx(ctx, tx, "wi_l", p.Repo, p.Number, p.Head)
			if err != nil {
				return err
			}
			if p.RerunClaimed != want[key] || check != want[key] {
				t.Errorf("%s at %s: limits %v, the check %v, want %v", key, p.Head, p.RerunClaimed, check, want[key])
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
