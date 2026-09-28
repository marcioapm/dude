package delivery

// Settings in layers: a project's override, else its organization's
// default, else the factory's. Each layer stores only what it sets (a
// missing key is "inherited"), so each value is resolved on its own: a
// project that changes only the reviewer's effort keeps its organization's
// reviewer model.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RoleSettings is how one agent role runs, resolved.
type RoleSettings struct {
	Model string
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
	Model            *string `json:"model"`
	Effort           *string `json:"effort"`
	TimeLimitMinutes *int    `json:"timeLimitMinutes"`
	Context          *string `json:"context"`
}

// modelFallback is where a role with no settings of its own takes them
// from: the fixer is the implementer, told something else.
var modelFallback = map[string]string{"fixer": "implementer"}

// ResolveRole resolves role's settings over the layers given, most
// specific first (a project's agent_models, then its organization's).
// A field no layer sets falls back to the role it follows, if any.
func ResolveRole(role string, layers ...json.RawMessage) RoleSettings {
	var rs RoleSettings
	var fallback *RoleSettings
	if f, ok := modelFallback[role]; ok {
		r := ResolveRole(f, layers...)
		fallback = &r
	}
	parsed := make([]roleLayer, 0, len(layers))
	for _, raw := range layers {
		var m map[string]roleLayer
		if json.Unmarshal(raw, &m) == nil {
			parsed = append(parsed, m[role])
		}
	}
	for _, l := range parsed {
		if rs.Model == "" && l.Model != nil {
			rs.Model = *l.Model
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
	if fallback != nil {
		if rs.Model == "" {
			rs.Model = fallback.Model
		}
		if rs.Effort == "" {
			rs.Effort = fallback.Effort
		}
		if rs.TimeLimitMinutes == 0 {
			rs.TimeLimitMinutes = fallback.TimeLimitMinutes
		}
		if rs.Context == "" {
			rs.Context = fallback.Context
		}
	}
	return rs
}

// SettingsRoleForPhase is whose settings a phase runs with: the fixer has
// its own, falling back to the implementer's.
func SettingsRoleForPhase(phase string) string {
	if phase == PhaseFix {
		return "fixer"
	}
	return RoleForPhase[phase]
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

// PromptSet is the prompts one Run is told with, and the versions they
// are: the organization's (nil for dude's built-in) and its project's
// (none, or added after the organization's, or in its place).
type PromptSet struct {
	OrgVersion, ProjectVersion *string
	Org                        *string
	Project, ProjectMode       string
}

// Apply puts the prompts into a phase's prompt input.
func (ps PromptSet) Apply(in *PromptInput) {
	in.OrgPrompt, in.ProjectPrompt, in.ProjectPromptMode = ps.Org, ps.Project, ps.ProjectMode
}

// LoadPrompts reads the prompts a phase Run is told with. A Run already
// given to lux is told what it was first told — the versions recorded on
// it — so a resume does not change its instructions mid-conversation. A new
// one takes the current versions, and records them.
func LoadPrompts(ctx context.Context, tx pgx.Tx, runID, projectID, phase string) (PromptSet, error) {
	var ps PromptSet
	role := PromptRoleForPhase[phase]
	if role == "" {
		return ps, nil
	}
	var submitted bool
	if err := tx.QueryRow(ctx, `SELECT lux_run_id IS NOT NULL, prompt_version_id, project_prompt_version_id
		FROM runs WHERE id = $1`, runID).Scan(&submitted, &ps.OrgVersion, &ps.ProjectVersion); err != nil {
		return ps, fmt.Errorf("load run prompts: %w", err)
	}
	if !submitted {
		// The latest of each is the current one. A project that uses its
		// organization's prompt as is saves an empty `add`.
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT id FROM prompt_versions WHERE role = $1 AND project_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 1),
			(SELECT id FROM prompt_versions WHERE role = $1 AND project_id = $2 ORDER BY created_at DESC, id DESC LIMIT 1)`,
			role, projectID).Scan(&ps.OrgVersion, &ps.ProjectVersion); err != nil {
			return ps, fmt.Errorf("current prompts: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET prompt_version_id = $2, project_prompt_version_id = $3 WHERE id = $1`,
			runID, ps.OrgVersion, ps.ProjectVersion); err != nil {
			return ps, fmt.Errorf("record prompts: %w", err)
		}
	}
	if ps.OrgVersion != nil {
		var body string
		if err := tx.QueryRow(ctx, `SELECT body FROM prompt_versions WHERE id = $1`, *ps.OrgVersion).Scan(&body); err != nil {
			return ps, fmt.Errorf("organization prompt: %w", err)
		}
		ps.Org = &body
	}
	if ps.ProjectVersion != nil {
		if err := tx.QueryRow(ctx, `SELECT body, mode FROM prompt_versions WHERE id = $1`, *ps.ProjectVersion).
			Scan(&ps.Project, &ps.ProjectMode); err != nil {
			return ps, fmt.Errorf("project prompt: %w", err)
		}
	}
	return ps, nil
}
