package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

// limitsWorld is a conductor on the fixture's task with one open pull
// request on repo target, and the organisation's GitHub settings.
func limitsWorld(t *testing.T, settings string) (*fixture, string) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url)
		VALUES ('repo_t', $1, $2, 'target', 'https://github.com/acme/target.git')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title) VALUES ('pr_t', $1, $2, $3, 'repo_t', 1, 'https://x/1', 'b', 'main', 'abc', 'T')`, f.org, f.project, f.item)
	mustExec(t, f.owner, `INSERT INTO forge_credentials (id, organization_id, auth, secret, api_base_url, settings)
		VALUES ('fc_t', $1, 'pat', 'x', 'http://127.0.0.1:1', $2::jsonb)`, f.org, settings)
	return f, token
}

// deliver gives the task a delivery in status that froze policy and has
// spent round fixes this review round and perPR on target.
func (f *fixture) deliver(t *testing.T, status, policy string, round, perPR int) {
	t.Helper()
	state, _ := json.Marshal(map[string]any{"attempt": 1, "prIteration": round, "prFixesByRepo": map[string]int{"target": perPR}})
	mustExec(t, f.owner, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_l', $1, $2, 'task.delivery', 'l', $3::workflow_run_status, 'conductorDecision',
			jsonb_set($4::jsonb, '{policy}', (SELECT jsonb_build_object('maxPrFixIterations', 0)) || $5::jsonb))`,
		f.org, f.item, status, string(state), policy)
}

func (f *fixture) limitsOf(t *testing.T, token string) map[string]any {
	t.Helper()
	var out map[string]any
	if status := f.postAs(t, token, "limits", `{}`, &out); status != 200 {
		t.Fatalf("limits: %d %v", status, out)
	}
	return out
}

func asInt(v any) int { f, _ := v.(float64); return int(f) }

// refreshing is a fixture with a GitHub connection whose syncs record the
// pull request they were asked for in synced and answer result.
func refreshing(t *testing.T, result error) (f *fixture, synced *[]string) {
	synced = &[]string{}
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: t.Name(), APIBaseURL: "http://127.0.0.1:1"})
	f = setupWith(t, &agenttools.Server{Forges: forgesOf{gh}, SyncPullRequest: func(_ context.Context, _, id string) error {
		*synced = append(*synced, id)
		return result
	}})
	return f, synced
}

// attemptsCharged are the task's agent.tool.attempted events: the GitHub
// reads a refresh charged.
func (f *fixture) attemptsCharged(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.tool.attempted'`,
		f.item).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A refresh charges only pull requests on GitHub, and one another sync
// recorded first (prs.ErrRaced) was refreshed, not failed.
func TestARefreshChargesGitHubPullRequestsAndCountsARacedSyncAsRefreshed(t *testing.T) {
	f, synced := refreshing(t, prs.ErrRaced)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		('repo_t', $1, $2, 'target', 'https://github.com/acme/target.git'), ('repo_local', $1, $2, 'local', '/srv/git/local.git')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title) VALUES
		('pr_t', $1, $2, $3, 'repo_t', 1, 'https://x/1', 'b', 'main', 'abc', 'T'),
		('pr_local', $1, $2, $3, 'repo_local', 1, 'https://x/l1', 'b', 'main', 'abc', 'L')`, f.org, f.project, f.item)
	var out map[string]any
	if status := f.postAs(t, token, "refresh_pull_requests", `{}`, &out); status != 200 {
		t.Fatalf("refresh: %d %v", status, out)
	}
	if strings.Join(*synced, ",") != "pr_t" || asInt(out["refreshed"]) != 1 || out["notRefreshed"] != nil {
		t.Errorf("synced %v; refreshed %v, notRefreshed %v", *synced, out["refreshed"], out["notRefreshed"])
	}
	if reads := f.attemptsCharged(t); reads != 1 {
		t.Errorf("%d GitHub reads charged, want 1", reads)
	}
}

// A task with no open pull request, and one whose open pull requests are
// all on repositories not on GitHub, are refused in their own words; GitHub
// is not asked and no read is charged.
func TestARefreshWithNothingOnGitHubIsRefused(t *testing.T) {
	f, synced := refreshing(t, nil)
	token := f.conductor(t)
	refused := func(want string) {
		t.Helper()
		var out map[string]any
		if status := f.postAs(t, token, "refresh_pull_requests", `{}`, &out); status != 422 || out["error"] != want {
			t.Errorf("refresh: %d %v, want %q", status, out, want)
		}
	}
	refused("the task has no open pull request to refresh: pull_requests lists the ones it had")
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url)
		VALUES ('repo_local', $1, $2, 'local', '/srv/git/local.git')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title) VALUES ('pr_local', $1, $2, $3, 'repo_local', 1, 'https://x/l1', 'b', 'main', 'abc', 'L')`,
		f.org, f.project, f.item)
	refused("none of the task's open pull requests is on GitHub: pull_requests lists them as dude has them")
	if len(*synced) != 0 {
		t.Errorf("synced %v", *synced)
	}
	if reads := f.attemptsCharged(t); reads != 0 {
		t.Errorf("%d GitHub reads charged, want 0", reads)
	}
}

// A delivery holds the policy it started with: a person changing the
// policy after shows as policyNow, with the layer it is set in now, while
// the limit stays the frozen one and carries no layer of its own.
func TestLimitsHoldTheFrozenPolicyAndSayWhereTodaysIsSet(t *testing.T) {
	f, token := limitsWorld(t, `{"fixRoundsPerPr":8}`)
	f.deliver(t, "waiting", `{"maxPrFixIterations":3}`, 1, 1)
	round := func() map[string]any { m, _ := field(f.limitsOf(t, token), "fixRounds").(map[string]any); return m }

	if r := round(); asInt(field(r, "perReviewRound", "limit")) != 3 || field(r, "perReviewRound", "setIn") != "default" || r["policyNow"] != nil {
		t.Errorf("unchanged policy: %v", r)
	}
	mustExec(t, f.owner, `UPDATE organizations SET delivery_policy = '{"maxPrFixIterations":4}' WHERE id = $1`, f.org)
	if r := round(); asInt(field(r, "perReviewRound", "limit")) != 3 || field(r, "perReviewRound", "setIn") != nil ||
		asInt(field(r, "policyNow", "limit")) != 4 || field(r, "policyNow", "setIn") != "organization" {
		t.Errorf("set in the organisation after: %v", r)
	}
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"maxPrFixIterations":6}' WHERE id = $1`, f.project)
	if r := round(); asInt(field(r, "perReviewRound", "limit")) != 3 || asInt(field(r, "perReviewRound", "remaining")) != 2 ||
		asInt(field(r, "policyNow", "limit")) != 6 || field(r, "policyNow", "setIn") != "project" {
		t.Errorf("set in the project too: %v", r)
	}
	// The project's value equal to the frozen one: no policyNow, the layer beside the limit.
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"maxPrFixIterations":3}' WHERE id = $1`, f.project)
	if r := round(); r["policyNow"] != nil || field(r, "perReviewRound", "setIn") != "project" {
		t.Errorf("the project's equal to the frozen: %v", r)
	}
	if d, _ := f.limitsOf(t, token)["delivery"].(string); !strings.Contains(d, "held to") {
		t.Errorf("a live delivery: %q", d)
	}
}

// Which bound stops a pull request's next fix first, each spent to 0
// where the fix's check refuses, and an unlimited fixRoundsPerPr.
func TestLimitsNameTheBindingBound(t *testing.T) {
	for _, c := range []struct {
		name, settings string
		max, round     int
		perPR          int
		roundLeft      int
		prLeft         any
		binding        string
	}{
		{"equal", `{"fixRoundsPerPr":4}`, 3, 1, 2, 2, float64(2), "both"},
		{"the pull request's first", `{"fixRoundsPerPr":4}`, 5, 1, 3, 4, float64(1), "organisation GitHub settings"},
		{"the round's first", `{"fixRoundsPerPr":8}`, 3, 2, 2, 1, float64(6), "delivery policy"},
		{"both exhausted", `{"fixRoundsPerPr":2}`, 3, 3, 2, 0, float64(0), "both"},
		{"round past its bound", `{"fixRoundsPerPr":8}`, 2, 4, 1, 0, float64(7), "delivery policy"},
		{"no per pull request bound", `{"fixRoundsPerPr":0}`, 3, 3, 40, 0, nil, "delivery policy"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, token := limitsWorld(t, c.settings)
			f.deliver(t, "running", `{"maxPrFixIterations":`+string(rune('0'+c.max))+`}`, c.round, c.perPR)
			out := f.limitsOf(t, token)
			pr, _ := field(out, "fixRounds", "perPullRequest", "pullRequests", 0).(map[string]any)
			if left := asInt(field(out, "fixRounds", "perReviewRound", "remaining")); left != c.roundLeft {
				t.Errorf("round remaining %d, want %d", left, c.roundLeft)
			}
			if pr["remaining"] != c.prLeft || pr["binding"] != c.binding || asInt(pr["used"]) != c.perPR {
				t.Errorf("pull request %v, want remaining %v binding %q", pr, c.prLeft, c.binding)
			}
		})
	}
}

// With no delivery, or one that ended, limits is the policy a delivery
// would start with now, nothing spent, and says which.
func TestLimitsWithoutALiveDelivery(t *testing.T) {
	f, token := limitsWorld(t, `{"fixRoundsPerPr":5}`)
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"maxPrFixIterations":4}' WHERE id = $1`, f.project)
	out := f.limitsOf(t, token)
	if d, _ := out["delivery"].(string); !strings.Contains(d, "no delivery yet") ||
		asInt(field(out, "fixRounds", "perReviewRound", "limit")) != 4 || field(out, "fixRounds", "perReviewRound", "setIn") != "project" ||
		asInt(field(out, "fixRounds", "perReviewRound", "used")) != 0 {
		t.Errorf("no delivery: %v", out)
	}
	f.deliver(t, "completed", `{"maxPrFixIterations":2}`, 2, 3)
	out = f.limitsOf(t, token)
	if d, _ := out["delivery"].(string); !strings.Contains(d, "has ended") || strings.Contains(d, "held to") ||
		asInt(field(out, "fixRounds", "perReviewRound", "limit")) != 4 || asInt(field(out, "fixRounds", "perReviewRound", "used")) != 0 ||
		field(out, "fixRounds", "policyNow") != nil {
		t.Errorf("an ended delivery: %v", out)
	}
}
