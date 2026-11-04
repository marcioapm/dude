package delivery

// Settings in layers: a project's override, else its organization's
// default, else the factory's. Each layer stores only what it sets (a
// missing key is "inherited"), so each value is resolved on its own: a
// project that changes only the reviewer's time limit keeps its
// organization's reviewer tier.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RoleSettings is how one agent role runs, resolved.
type RoleSettings struct {
	// The model tier's id, as the first layer names it; resolved by
	// TierFor, which fails the Run when the tier is gone or names no model.
	Tier string
	// How long a phase Run of the role may go without progress before its
	// owner is told, in minutes (TimeLimitFor).
	TimeLimitMinutes int
	// Per-role notes, appended to the role's prompt.
	Context string
	// The coding agent its Runs run on (HarnessOpenCode when no layer
	// names one).
	Harness string
}

// The harnesses a role can run on, as settings and runs.harness name them.
const (
	HarnessOpenCode   = "opencode"
	HarnessClaudeCode = "claude-code"
	HarnessCodex      = "codex"
)

func IsHarness(h string) bool {
	switch h {
	case HarnessOpenCode, HarnessClaudeCode, HarnessCodex:
		return true
	default:
		return false
	}
}

// roleLayer is one layer's config for a role, as stored: agent_models on a
// project, default_agent_models on an organization ({role -> config}).
type roleLayer struct {
	Tier             *string `json:"tier"`
	TimeLimitMinutes *int    `json:"timeLimitMinutes"`
	Context          *string `json:"context"`
	Harness          *string `json:"harness"`
	// Resolved by Sizes.ForRole, which passes over ids that are gone.
	MachineSize *string `json:"machineSize"`
}

// modelFallback is where a role with no settings of its own takes them
// from: the fixer is the implementer, told something else.
var modelFallback = map[string]string{"fixer": "implementer"}

// ResolveRole resolves role's settings over the layers given, most
// specific first (a project's agent_models, then its organization's).
// A field no layer sets for the role comes from the role it follows, if
// any, over the same layers.
func ResolveRole(role string, layers ...json.RawMessage) RoleSettings {
	rs, _ := ResolveRoleFrom(role, layers...)
	return rs
}

// Found is where ResolveRoleFrom found one field: the index of its layer
// (-1 for none), and whether under the role this one follows.
type Found struct {
	Layer   int
	Follows bool
}

// RoleFrom is where each field of a resolved RoleSettings was found.
type RoleFrom struct {
	Tier, TimeLimit, Context, Harness Found
}

// ResolveRoleFrom is ResolveRole, saying where each field was found.
func ResolveRoleFrom(role string, layers ...json.RawMessage) (RoleSettings, RoleFrom) {
	var rs RoleSettings
	none := Found{Layer: -1}
	from := RoleFrom{none, none, none, none}
	chain := []string{role}
	if f, ok := modelFallback[role]; ok {
		chain = append(chain, f)
	}
	parsed := make([]map[string]roleLayer, len(layers))
	for i, raw := range layers {
		if json.Unmarshal(raw, &parsed[i]) != nil {
			parsed[i] = nil
		}
	}
	for _, r := range chain {
		for i, m := range parsed {
			l, here := m[r], Found{Layer: i, Follows: r != role}
			if rs.Tier == "" && l.Tier != nil {
				rs.Tier, from.Tier = *l.Tier, here
			}
			if rs.TimeLimitMinutes == 0 && l.TimeLimitMinutes != nil {
				rs.TimeLimitMinutes, from.TimeLimit = *l.TimeLimitMinutes, here
			}
			if rs.Context == "" && l.Context != nil {
				rs.Context, from.Context = *l.Context, here
			}
			if rs.Harness == "" && l.Harness != nil && IsHarness(*l.Harness) {
				rs.Harness, from.Harness = *l.Harness, here
			}
		}
	}
	return rs, from
}

// HarnessName is the harness the role runs on: its own, else OpenCode.
func (rs RoleSettings) HarnessName() string {
	if rs.Harness == "" {
		return HarnessOpenCode
	}
	return rs.Harness
}

// The no-progress limit: a role's default, and the least a stored one is
// read as (packages/domain timeLimitMinutesSchema).
const (
	DefaultTimeLimitMinutes = 120
	MinTimeLimitMinutes     = 30
)

// TimeLimit is the role's no-progress limit as it applies: its own,
// raised to the minimum, or the default.
func (rs RoleSettings) TimeLimit() int {
	if rs.TimeLimitMinutes == 0 {
		return DefaultTimeLimitMinutes
	}
	return max(rs.TimeLimitMinutes, MinTimeLimitMinutes)
}

// ResolvePolicy is the delivery policy a task runs with: the factory's
// defaults, then its organization's, then its project's, then the task's
// own — each layer setting only what it names. The first layer is the
// organisation's: a project-only key stored there (ProjectOnlyPolicyKeys)
// is passed over.
func ResolvePolicy(layers ...json.RawMessage) (Policy, error) {
	p := DefaultPolicy()
	for i, raw := range layers {
		if len(raw) == 0 {
			continue
		}
		if i == 0 {
			raw = withoutProjectOnlyKeys(raw)
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, fmt.Errorf("delivery policy layer %d: %w", i, err)
		}
		if err := refuseNullReviewerKeys(raw); err != nil {
			return p, fmt.Errorf("delivery policy layer %d: %w", i, err)
		}
	}
	if err := p.checkPullRequest(); err != nil {
		return p, err
	}
	err := p.checkReviewerKeys()
	return p, err
}

// withoutProjectOnlyKeys is an organisation's stored policy with the keys
// only a project sets removed; raw as it is when it is not an object.
func withoutProjectOnlyKeys(raw json.RawMessage) json.RawMessage {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return raw
	}
	found := false
	for _, k := range ProjectOnlyPolicyKeys {
		if _, ok := keys[k]; ok {
			delete(keys, k)
			found = true
		}
	}
	if !found {
		return raw
	}
	out, err := json.Marshal(keys)
	if err != nil {
		return raw
	}
	return out
}

// checkPullRequest refuses a pullRequest value that is not one of its three.
func (p *Policy) checkPullRequest() error {
	if !IsPullRequestChoice(p.PullRequest) {
		return fmt.Errorf("pullRequest: %q is not open, draft or ask", p.PullRequest)
	}
	return nil
}

// Prompts are the prompts one Run is told with: the organization's (nil
// for dude's built-in) and its project's (none, or added after the
// organization's, or in its place).
type Prompts struct {
	Org                  *string
	Project, ProjectMode string
}

// LoadPrompts reads the prompts a Run is told with, by its prompt role
// (PromptRoleFor). A Run already
// given to lux is told what it was first told — the versions recorded on
// it — so a resume does not change its instructions mid-conversation. A new
// one takes the current versions, and records them.
func LoadPrompts(ctx context.Context, tx pgx.Tx, runID, projectID, role string) (Prompts, error) {
	var ps Prompts
	var override *string
	if err := tx.QueryRow(ctx, `SELECT child_prompt FROM runs WHERE id=$1`, runID).Scan(&override); err != nil {
		return ps, err
	}
	if override != nil {
		ps.Org = override
		return ps, nil
	}
	if role == "" {
		return ps, nil
	}
	var submitted bool
	var orgVersion, projectVersion *string
	if err := tx.QueryRow(ctx, `SELECT lux_run_id IS NOT NULL, prompt_version_id, project_prompt_version_id
		FROM runs WHERE id = $1`, runID).Scan(&submitted, &orgVersion, &projectVersion); err != nil {
		return ps, fmt.Errorf("load run prompts: %w", err)
	}
	if !submitted {
		// The latest of each is the current one. A project that uses its
		// organization's prompt as is saves an empty `add`.
		if err := tx.QueryRow(ctx, `UPDATE runs SET
			prompt_version_id = (SELECT id FROM prompt_versions WHERE role = $2 AND project_id IS NULL
				ORDER BY created_at DESC, id DESC LIMIT 1),
			project_prompt_version_id = (SELECT id FROM prompt_versions WHERE role = $2 AND project_id = $3
				ORDER BY created_at DESC, id DESC LIMIT 1)
			WHERE id = $1 RETURNING prompt_version_id, project_prompt_version_id`,
			runID, role, projectID).Scan(&orgVersion, &projectVersion); err != nil {
			return ps, fmt.Errorf("record prompts: %w", err)
		}
	}
	if orgVersion == nil && projectVersion == nil {
		return ps, nil
	}
	var project, mode *string
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT body FROM prompt_versions WHERE id = $1),
		(SELECT body FROM prompt_versions WHERE id = $2),
		(SELECT mode FROM prompt_versions WHERE id = $2)`, orgVersion, projectVersion).
		Scan(&ps.Org, &project, &mode); err != nil {
		return ps, fmt.Errorf("load prompts: %w", err)
	}
	if project != nil && mode != nil {
		ps.Project, ps.ProjectMode = *project, *mode
	}
	return ps, nil
}
