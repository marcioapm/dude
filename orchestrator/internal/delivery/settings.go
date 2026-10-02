package delivery

// Settings in layers: a project's override, else its organization's
// default, else the factory's. Each layer stores only what it sets (a
// missing key is "inherited"), so each value is resolved on its own: a
// project that changes only the reviewer's effort keeps its organization's
// reviewer tier.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RoleSettings is how one agent role runs, resolved.
type RoleSettings struct {
	// The model tier's id, as the first layer names it; resolved to a tier
	// by Tiers.ForRole, which passes over ids that are gone.
	Tier string
	// How hard the model thinks (low, medium, high, max); "" leaves it to
	// the model.
	Effort string
	// Running time allowed per session, in minutes; 0 is no limit of the
	// role's own.
	TimeLimitMinutes int
	// Per-role notes, appended to the role's prompt.
	Context string
}

// roleLayer is one layer's config for a role, as stored: agent_models on a
// project, default_agent_models on an organization ({role -> config}).
type roleLayer struct {
	Tier             *string `json:"tier"`
	Effort           *string `json:"effort"`
	TimeLimitMinutes *int    `json:"timeLimitMinutes"`
	Context          *string `json:"context"`
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
	var rs RoleSettings
	chain := []string{role}
	if f, ok := modelFallback[role]; ok {
		chain = append(chain, f)
	}
	parsed := make([]map[string]roleLayer, 0, len(layers))
	for _, raw := range layers {
		var m map[string]roleLayer
		if json.Unmarshal(raw, &m) == nil {
			parsed = append(parsed, m)
		}
	}
	for _, r := range chain {
		for _, m := range parsed {
			l := m[r]
			if rs.Tier == "" && l.Tier != nil {
				rs.Tier = *l.Tier
			}
			if rs.Effort == "" && l.Effort != nil {
				rs.Effort = *l.Effort
			}
			if rs.TimeLimitMinutes == 0 && l.TimeLimitMinutes != nil {
				rs.TimeLimitMinutes = *l.TimeLimitMinutes
			}
			if rs.Context == "" && l.Context != nil {
				rs.Context = *l.Context
			}
		}
	}
	return rs
}

// ResolvePolicy is the delivery policy a task runs with: the factory's
// defaults, then its organization's, then its project's, then the task's
// own — each layer setting only what it names.
func ResolvePolicy(layers ...json.RawMessage) (Policy, error) {
	p := DefaultPolicy()
	for i, raw := range layers {
		if len(raw) == 0 {
			continue
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, fmt.Errorf("delivery policy layer %d: %w", i, err)
		}
	}
	return p, nil
}

// Prompts are the prompts one Run is told with: the organization's (nil
// for dude's built-in) and its project's (none, or added after the
// organization's, or in its place).
type Prompts struct {
	Org                  *string
	Project, ProjectMode string
}

// LoadPrompts reads the prompts a phase Run is told with. A Run already
// given to lux is told what it was first told — the versions recorded on
// it — so a resume does not change its instructions mid-conversation. A new
// one takes the current versions, and records them.
func LoadPrompts(ctx context.Context, tx pgx.Tx, runID, projectID, phase string) (Prompts, error) {
	var ps Prompts
	role := PromptRoleForPhase[phase]
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
