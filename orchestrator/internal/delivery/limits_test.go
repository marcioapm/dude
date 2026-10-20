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

// An ended delivery's limits are what the checks still read of it: its
// rerun grants, which RerunClaimedTx honours on a head, and none of its
// per-round grants or spent rounds, which a new delivery starts without;
// its fix rounds per review round are today's policy, not the one it froze.
func TestAnEndedDeliverysLimitsKeepOnlyItsRerunGrants(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, delivery_policy)
		VALUES ('prj_e', $1, 'P', 'p', 'END', '{"maxPrFixIterations":4}')`, org)
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_e', $1, 'prj_e', 'api', 'https://github.com/acme/api.git')`, org)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_e', $1, 'prj_e', 1, 'T', 'G')`, org)
	exec(t, owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
		VALUES ('pr_e', $1, 'prj_e', 'wi_e', 'repo_e', 1, 'https://x/1', 'b', 'main', 'h1', 'T')`, org)
	exec(t, owner, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_e', $1, 'wi_e', 'task.delivery', 'ended-fixture', 'completed', 'done',
		'{"taskId":"wi_e","projectId":"prj_e","prIteration":2,"prFixesByRepo":{"api":2},"policy":{"maxPrFixIterations":2},"limitGrants":[
		  {"limit":"rerun","extra":1,"repo":"api","number":1,"head":"h1","by":"p_ana","byName":"Ana","questionId":"q1","at":"2026-01-01T00:00:00Z"},
		  {"limit":"perReviewRound","extra":1,"by":"p_cy","byName":"Cy","questionId":"q2","at":"2026-01-01T00:00:00Z"}]}')`, org)
	exec(t, owner, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		VALUES ('evt_e1', $1, $2, 'wi_e', 'agent', 'run_x', 'test', $3)`, org, delivery.EvPullRequestAction,
		map[string]any{"action": delivery.ActionRerunChecks, "repo": "api", "number": 1, "head": "h1"})

	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		l, err := delivery.TaskLimitsTx(ctx, tx, "wi_e")
		if err != nil {
			return err
		}
		_, now, err := delivery.PolicyFor(ctx, tx, "wi_e")
		if err != nil {
			return err
		}
		if l.Delivery != delivery.DeliveryEnded || len(l.Grants) != 1 || l.Grants[0].Limit != delivery.LimitRerun {
			t.Errorf("delivery %q, grants %+v", l.Delivery, l.Grants)
		}
		if l.RoundFixLimit() != now.MaxPRFixIterations || now.MaxPRFixIterations != 4 || l.PRIteration != 0 || l.RoundFixesSpent() {
			t.Errorf("fix rounds per review round: limit %d (policy now %d), spent %d", l.RoundFixLimit(), now.MaxPRFixIterations, l.PRIteration)
		}
		claimed, err := delivery.RerunClaimedTx(ctx, tx, "wi_e", "api", 1, "h1")
		if err != nil {
			return err
		}
		if len(l.PullRequests) != 1 {
			t.Fatalf("pull requests %+v", l.PullRequests)
		}
		p := l.PullRequests[0]
		if p.Reruns != 1 || p.RerunsAllowed != 2 || p.RerunClaimed || claimed {
			t.Errorf("api#1 at h1: limits %+v, the check claimed %v; want 1 of 2, not claimed", p, claimed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
