package automations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A webhook that answers 5xx is tried once more, then the run fails with
// the reason and its owner is told.
func TestAutomationWebhookFailsAfterItsRetry(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		tries++
		mu.Unlock()
		w.WriteHeader(502)
	}))
	t.Cleanup(srv.Close)
	h.automation("aut_hook502", "webhook", map[string]any{"kind": "github", "event": "release"},
		map[string]any{"webhook": map[string]any{"url": srv.URL}, "webhook_secret": "s3cret-signing-key"})
	h.deliver("r-1", "release", map[string]any{"action": "published", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("the run fails", func() bool { r := h.runs("aut_hook502"); return len(r) == 1 && r[0].Status == "failed" })
	r := h.runs("aut_hook502")[0]
	mu.Lock()
	defer mu.Unlock()
	if r.Reason != "the webhook did not answer 2xx" || tries != 2 {
		t.Errorf("run %+v after %d tries", r, tries)
	}
	var to []string
	if err := h.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(payload->'to')) FROM events
		WHERE event_type = 'automation.failed' AND payload->>'automationId' = 'aut_hook502'`).Scan(&to); err != nil || !slices.Equal(to, []string{h.person}) {
		t.Errorf("failure told %v %v", to, err)
	}
}

// A preset's values follow its role's settings as they are when a run
// starts (Q15): a later change reaches the next spec.
func TestAutomationPresetFollowsItsRolesLaterChange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	exec(t, h.owner, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ($1 || '_big', $1, 'Big', 'acme-large')`, h.org)
	setTier := func(tier string) {
		models, _ := json.Marshal(map[string]any{"qa_browser": map[string]any{"tier": tier, "timeLimitMinutes": 60}})
		exec(t, h.owner, `UPDATE organizations SET default_agent_models = $2 WHERE id = $1`, h.org, string(models))
	}
	setTier(h.tier)
	h.automation("aut_follow", "agent", map[string]any{"kind": "schedule", "cron": "0 * * * *", "tz": "UTC"},
		map[string]any{"agent": map[string]any{"preset": map[string]any{"role": "qa_browser", "overrides": map[string]any{}}, "brief": "Check"}})
	model := func() string {
		var a Automation
		if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) { a, err = Load(ctx, tx, "aut_follow"); return err }); err != nil {
			t.Fatal(err)
		}
		spec, why, err := h.runner.spec(ctx, a, "arn_follow", nil, nil)
		if err != nil || why != "" {
			t.Fatalf("spec: %s %v", why, err)
		}
		return spec.Labels["dude.model"]
	}
	if m := model(); m != "fake/scripted" {
		t.Fatalf("before: %s", m)
	}
	setTier(h.org + "_big")
	if m := model(); m != "acme-large" {
		t.Errorf("after the role's change: %s", m)
	}
}

// What a run checks out: a project automation's all / none / named
// repositories; an organisation schedule's, those its filter covers; an
// organisation event's, the event's repository when covered, else none.
func TestAutomationReposCheckedOut(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := h.org + "_svc"
	exec(t, h.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'svc', 'svc', 'SVC')`, other, h.org)
	exec(t, h.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		($1 || '_api', $2, $1, 'svc-api', 'https://github.com/acme/svc-api.git'),
		($1 || '_ui', $2, $1, 'svc-ui', 'https://github.com/acme/svc-ui.git')`, other, h.org)
	push := func(repo string) []Payload {
		b, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": repo}})
		return []Payload{{Event: "push", Body: b}}
	}
	only := &RepoFilter{Mode: "only", Repos: []string{"acme/svc-*"}}
	for _, c := range []struct {
		name     string
		a        Automation
		payloads []Payload
		want     []string
	}{
		{"project all", Automation{ProjectID: other, Repos: json.RawMessage(`"all"`)}, nil, []string{"svc-api", "svc-ui"}},
		{"project none", Automation{ProjectID: other, Repos: json.RawMessage(`"none"`)}, nil, nil},
		{"project named", Automation{ProjectID: other, Repos: json.RawMessage(`["` + other + `_ui"]`)}, nil, []string{"svc-ui"}},
		{"project named elsewhere", Automation{ProjectID: other, Repos: json.RawMessage(`["` + h.project + `_r"]`)}, nil, nil},
		{"org schedule, filter", Automation{RepoFilter: only}, nil, []string{"svc-api", "svc-ui"}},
		{"org schedule, all", Automation{}, nil, []string{"web", "svc-api", "svc-ui"}},
		{"org event, covered", Automation{RepoFilter: only}, push("acme/svc-api"), []string{"svc-api"}},
		{"org event, filtered out", Automation{RepoFilter: only}, push("acme/web"), nil},
	} {
		c.a.Org = h.org
		var got []string
		if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) error {
			repos, err := automationRepos(ctx, tx, c.a, c.payloads)
			for _, r := range repos {
				got = append(got, r.Name)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v, want %v", c.name, got, want)
		}
	}
}
