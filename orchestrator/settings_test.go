package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// specOf is the spec lux was given for a task's phase.
func (w *world) specOf(phase string) *lux.Spec {
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.phase"] == phase {
			return &spec
		}
	}
	return nil
}

// An organization's settings reach the agent: the tier its role names, with
// that tier's effort, its time limit, and its prompt in place of dude's —
// with the project's added after it — and the Run records the versions it
// ran with, so its history can say who was told what.
func TestAnOrganizationsSettingsAndPromptsReachTheAgent(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// The implementer is a real model so its spec carries a real prompt;
	// the implementer's tier is the organization's to say.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models - 'implementer' WHERE id = $1`, w.project)
	mustExec(t, w.owner, `INSERT INTO model_tiers (id, organization_id, name, model, effort) VALUES ('mtr_org_impl_' || $1, $1, 'Org coder', 'llm-impl', 'high')`, w.org)
	mustExec(t, w.owner, `UPDATE organizations SET default_agent_models =
		jsonb_build_object('implementer', jsonb_build_object('tier', 'mtr_org_impl_' || $1, 'timeLimitMinutes', 45)) WHERE id = $1`, w.org)
	mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, role, body, created_by, created_at)
		VALUES ('pv_old', $1, 'implementer', 'OLD ORG PROMPT', 'key_x', now() - interval '1 hour'),
		       ('pv_org', $1, 'implementer', 'ORG PROMPT', 'key_x', now())`, w.org)
	mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, created_by)
		VALUES ('pv_proj', $1, $2, 'implementer', 'add', 'PROJECT ADDITION', 'key_x')`, w.org, w.project)

	wi := w.task()
	w.deliver(wi)
	var spec *lux.Spec
	w.until("the implementer to reach lux", func() bool { spec = w.specOf("implement"); return spec != nil })

	if spec.Labels["dude.model"] != "llm-impl" || spec.Labels["dude.model_tier"] != "Org coder" || spec.Labels["dude.effort"] != "high" {
		t.Errorf("model %q tier %q effort %q: want the organization's tier and its effort",
			spec.Labels["dude.model"], spec.Labels["dude.model_tier"], spec.Labels["dude.effort"])
	}
	// The role's time limit is when its owner is told of no progress, not
	// lux's: lux is sent the hard limit.
	if spec.Timeout != phases.DefaultTimeout {
		t.Errorf("timeout = %q, want the hard limit %s", spec.Timeout, phases.DefaultTimeout)
	}
	var config struct {
		Provider map[string]struct {
			Models map[string]struct {
				Options map[string]any `json:"options"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil ||
		config.Provider["llm-openai"].Models["llm-impl"].Options["reasoningEffort"] != "high" {
		t.Errorf("opencode config lacks the tier's effort: %s", spec.Env["OPENCODE_CONFIG_CONTENT"])
	}
	p := spec.Workload.Prompt
	if !strings.Contains(p, "ORG PROMPT") || strings.Contains(p, "OLD ORG PROMPT") || strings.Contains(p, "handing over code that does not build") {
		t.Errorf("the organization's current prompt should replace dude's:\n%s", p)
	}
	if strings.Index(p, "PROJECT ADDITION") < strings.Index(p, "ORG PROMPT") || !strings.Contains(p, "Greet people") {
		t.Errorf("the project's addition should follow the organization's, with the task:\n%s", p)
	}

	var orgVersion, projectVersion string
	if err := w.owner.QueryRow(ctx, `SELECT prompt_version_id, project_prompt_version_id FROM runs
		WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&orgVersion, &projectVersion); err != nil {
		t.Fatal(err)
	}
	if orgVersion != "pv_org" || projectVersion != "pv_proj" {
		t.Errorf("recorded versions %q %q", orgVersion, projectVersion)
	}
}

// An organization that never saved a prompt runs dude's, and its Runs
// record none.
func TestAnOrganizationThatNeverEditsRunsTheBuiltInPrompt(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", "llm-impl")
	wi := w.task()
	w.deliver(wi)
	var spec *lux.Spec
	w.until("the implementer to reach lux", func() bool { spec = w.specOf("implement"); return spec != nil })
	if !strings.HasPrefix(spec.Workload.Prompt, delivery.BuiltinPrompt("implementer")) {
		t.Errorf("prompt:\n%s", spec.Workload.Prompt)
	}
	if spec.Labels["dude.effort"] != "" {
		t.Errorf("effort %q, want none", spec.Labels["dude.effort"])
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND (prompt_version_id IS NOT NULL OR project_prompt_version_id IS NOT NULL)`, wi); n != 0 {
		t.Errorf("%d runs recorded a prompt version", n)
	}
}

// An organization's delivery policy is what its projects deliver with,
// where they set nothing themselves.
func TestAnOrganizationsDeliveryPolicyAppliesUnderItsProjects(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE organizations SET delivery_policy = '{"simplify":false,"maxReviewIterations":2}'::jsonb WHERE id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = '{"maxReviewIterations":4}'::jsonb WHERE id = $1`, w.project)
	wi := w.task()
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("deliver: %d %v", status, body)
	}
	var simplify bool
	var rounds int
	if err := w.owner.QueryRow(context.Background(), `SELECT (state->'policy'->>'simplify')::bool, (state->'policy'->>'maxReviewIterations')::int
		FROM workflow_runs WHERE task_id = $1`, wi).Scan(&simplify, &rounds); err != nil {
		t.Fatal(err)
	}
	if simplify || rounds != 4 {
		t.Errorf("simplify=%v rounds=%d: want the organization's simplify and the project's rounds", simplify, rounds)
	}
}
