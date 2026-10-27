package agenttools

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/images"
)

// The settings tool's per-role sections: roles, prompts, network,
// capabilities and machines, each resolved by the rule a Run is built with
// (ResolveRoleFrom, TierIn, Sizes.ForRole, images.RoleImageFrom,
// capabilities.Resolve) over rows read once for every scope.

// roleRefs are the organisation's tiers, sizes and images, read once.
type roleRefs struct {
	tiers  map[string]delivery.Tier
	sizes  delivery.Sizes
	images map[string]string
}

type refOut struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// A tier's: the model it requests; "" while it names none.
	Model *string `json:"model,omitempty"`
	// The id names nothing the organisation still has.
	Missing bool `json:"missing,omitempty"`
}

// roleValueOut is a role's setting: valueOut, and whether a fixer takes
// it from the implementer.
type roleValueOut struct {
	Value              any    `json:"value"`
	Source             string `json:"source"`
	FollowsImplementer bool   `json:"followsImplementer,omitempty"`
	Note               string `json:"note,omitempty"`
}

type roleOut struct {
	Role             string       `json:"role"`
	Label            string       `json:"label"`
	Tier             roleValueOut `json:"tier"`
	Harness          roleValueOut `json:"harness"`
	TimeLimitMinutes roleValueOut `json:"timeLimitMinutes"`
	MachineSize      roleValueOut `json:"machineSize"`
	Image            roleValueOut `json:"image"`
	// A project's notes for the role are set: their text is not shown.
	ProjectNotes *bool `json:"projectNotes,omitempty"`
}

// layerSource names where ResolveRoleFrom found a field, over the scope's
// layers (the project's then the organisation's, or the organisation's).
func layerSource(l settingsLayers, f delivery.Found) roleValueOut {
	out := roleValueOut{Source: sourceDude, FollowsImplementer: f.Follows}
	switch {
	case f.Layer < 0:
	case l.project != nil && f.Layer == 0:
		out.Source = sourceProject
	default:
		out.Source = sourceOrg
	}
	return out
}

// roleLayers are the agent_models layers of the scope, most specific first.
func (l settingsLayers) roleLayers() []json.RawMessage {
	if l.project != nil {
		return []json.RawMessage{l.project.AgentModels, l.org.AgentModels}
	}
	return []json.RawMessage{l.org.AgentModels}
}

func (l settingsLayers) projectModels() json.RawMessage {
	if l.project != nil {
		return l.project.AgentModels
	}
	return nil
}

func rolesOut(w wrapper, scope settingsLayers, refs roleRefs) []roleOut {
	out := make([]roleOut, 0, len(settingsRoles))
	for _, role := range settingsRoles {
		l := scope.ofRun(role)
		rs, from := delivery.ResolveRoleFrom(role, l.roleLayers()...)
		r := roleOut{Role: role, Label: delivery.RoleName(role)}
		r.Tier = layerSource(l, from.Tier)
		r.Harness = layerSource(l, from.Harness)
		if l.session != nil {
			// The session's choice in place of the organisation's, as
			// brainstormSpec builds its Run.
			rs = l.session.Over(rs)
			if l.session.Tier != nil {
				r.Tier.Source = sourceSession
			}
			if l.session.Harness != nil {
				r.Harness.Source = sourceSession
			}
		}
		if rs.Tier != "" {
			t, _, _ := delivery.TierIn(refs.tiers, role, rs)
			tier := refOut{ID: rs.Tier, Missing: t.ID == ""}
			if !tier.Missing {
				model := w.text(t.Model, 200)
				tier.Name, tier.Model = w.text(t.Name, 200), &model
			}
			r.Tier.Value = tier
		}
		r.Harness.Value = rs.HarnessName()
		r.TimeLimitMinutes = layerSource(l, from.TimeLimit)
		r.TimeLimitMinutes.Value = rs.TimeLimit()
		if role == settingsInvestigator && scope.session != nil {
			r.TimeLimitMinutes = roleValueOut{Source: sourceDude, Note: settingsText("session-child-limit", nil)}
		}
		r.MachineSize = machineOf(w, l, refs.sizes, role)
		known := func(id string) bool { _, ok := refs.images[id]; return ok }
		id, imgFrom := images.RoleImageFrom(role, l.projectModels(), l.org.AgentModels, known)
		r.Image = roleValueOut{Source: sourceOf(imgFrom)}
		if imgFrom == "implementer" {
			_, implFrom := images.RoleImageFrom("implementer", l.projectModels(), l.org.AgentModels, known)
			r.Image.Source, r.Image.FollowsImplementer = sourceOf(implFrom), true
		}
		if id != "" {
			r.Image.Value = refOut{ID: id, Name: w.text(refs.images[id], 200)}
		}
		if l.project != nil {
			set := from.Context.Layer == 0
			r.ProjectNotes = &set
		}
		out = append(out, r)
	}
	return out
}

// machineOf is the size role's Runs start on in the scope (Sizes.ForRole,
// over a session's raised size for its brainstorm). A fixer's taken from
// the implementer is set where the implementer's is, over the same layers.
func machineOf(w wrapper, l settingsLayers, sizes delivery.Sizes, role string) roleValueOut {
	m, ok := sizes.ForRole(role, l.raised, l.projectModels(), l.org.AgentModels)
	out := roleValueOut{Source: sourceOf(m.From)}
	if m.Raised != nil {
		out.Source = sourceSession
	}
	if m.From == "implementer" {
		impl, _ := sizes.ForRole("implementer", nil, l.projectModels(), l.org.AgentModels)
		out.Source, out.FollowsImplementer = sourceOf(impl.From), true
	}
	if ok {
		out.Value = refOut{ID: m.SizeID, Name: w.text(m.Name, 200)}
	}
	return out
}

type sizeOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CPUs      float64 `json:"cpus"`
	MemoryMiB int64   `json:"memoryMiB"`
	DiskGiB   int64   `json:"diskGiB"`
	Default   bool    `json:"default,omitempty"`
}

// sizesOut are the organisation's machine sizes, smallest first.
func sizesOut(w wrapper, sizes delivery.Sizes) []sizeOut {
	out := []sizeOut{}
	for _, m := range sizes.ByID {
		out = append(out, sizeOut{ID: m.SizeID, Name: w.text(m.Name, 200), CPUs: m.CPUs, MemoryMiB: m.MemoryMiB, DiskGiB: m.DiskGiB,
			Default: m.SizeID == sizes.Default})
	}
	slices.SortFunc(out, func(a, b sizeOut) int {
		return cmp.Or(cmp.Compare(a.CPUs, b.CPUs), cmp.Compare(a.MemoryMiB, b.MemoryMiB), cmp.Compare(a.DiskGiB, b.DiskGiB),
			cmp.Compare(a.ID, b.ID))
	})
	return out
}

// imageNames are the organisation's images' names, by id.
func imageNames(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, name FROM images`)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var id, name string
	_, err = pgx.ForEachRow(rows, []any{&id, &name}, func() error {
		out[id] = name
		return nil
	})
	return out, err
}

// ---- prompts --------------------------------------------------------------

type promptKey struct{ role, project string }

// promptRow is a layer's current prompt version, without its text.
type promptRow struct {
	Mode    string
	Empty   bool
	SavedAt time.Time
	SavedBy string
}

type savedOut struct {
	SavedAt string `json:"savedAt"`
	SavedBy string `json:"savedBy,omitempty"`
}

type promptOut struct {
	Role  string `json:"role"`
	Label string `json:"label"`
	// "dude" for its built-in prompt, "saved", or "per-run" for a session's
	// investigator, which runs on the prompt its brainstorm gives it.
	Organization string    `json:"organization"`
	OrgSaved     *savedOut `json:"organizationSaved,omitempty"`
	// A project's: "inherit" (the organisation's as it is), "add" (after
	// it) or "replace" (in its place).
	Project      string    `json:"project,omitempty"`
	ProjectSaved *savedOut `json:"projectSaved,omitempty"`
	Note         string    `json:"note,omitempty"`
}

// latestPrompts are the current prompt versions of the organisation and of
// projects, in one statement: who saved each, never its text.
func latestPrompts(ctx context.Context, tx pgx.Tx, projects []projectRow) (map[promptKey]promptRow, error) {
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (v.role, v.project_id) v.role, COALESCE(v.project_id, ''), COALESCE(v.mode, ''),
			v.body ~ '^\s*$', v.created_at, COALESCE(p.name, k.name, '')
		FROM prompt_versions v LEFT JOIN api_keys k ON k.id = v.created_by LEFT JOIN people p ON p.id = COALESCE(k.person_id, v.created_by)
		WHERE v.project_id IS NULL OR v.project_id = ANY($1)
		ORDER BY v.role, v.project_id, v.created_at DESC, v.id DESC`, ids)
	if err != nil {
		return nil, err
	}
	out := map[promptKey]promptRow{}
	var k promptKey
	var r promptRow
	_, err = pgx.ForEachRow(rows, []any{&k.role, &k.project, &r.Mode, &r.Empty, &r.SavedAt, &r.SavedBy}, func() error {
		out[k] = r
		return nil
	})
	return out, err
}

func promptsOut(w wrapper, scope settingsLayers, rows map[promptKey]promptRow) []promptOut {
	saved := func(r promptRow) *savedOut {
		return &savedOut{SavedAt: r.SavedAt.UTC().Format(time.RFC3339), SavedBy: w.text(r.SavedBy, 100)}
	}
	out := make([]promptOut, 0, len(settingsRoles))
	for _, role := range settingsRoles {
		l := scope.of(role)
		p := promptOut{Role: role, Label: delivery.RoleName(role), Organization: sourceDude}
		if role == settingsInvestigator && scope.session != nil {
			p.Organization, p.Note = "per-run", settingsText("session-child-prompt", nil)
			out = append(out, p)
			continue
		}
		if r, ok := rows[promptKey{role, ""}]; ok {
			p.Organization, p.OrgSaved = "saved", saved(r)
		}
		if l.project != nil {
			p.Project = "inherit"
			// An empty addition is the organisation's as it is (projectMode).
			if r, ok := rows[promptKey{role, l.project.ID}]; ok {
				if r.Mode != "add" || !r.Empty {
					p.Project = r.Mode
				}
				p.ProjectSaved = saved(r)
			}
		}
		out = append(out, p)
	}
	return out
}

// ---- network --------------------------------------------------------------

type webOut struct {
	Role      string `json:"role"`
	WebSearch bool   `json:"webSearch"`
	WebFetch  bool   `json:"webFetch"`
}

type networkOut struct {
	OrganizationEgress valueOut  `json:"organizationEgress"`
	ProjectEgress      *valueOut `json:"projectEgress,omitempty"`
	// A project's: "add" (its list after the organisation's) or "only".
	Mode               *valueOut `json:"mode,omitempty"`
	BrainstormAnywhere valueOut  `json:"brainstormAnywhere"`
	// Each role's harness web search and fetch, as its capabilities grant.
	Web []webOut `json:"web"`
}

// networkOf is the scope's egress lists as the Network page shows them.
// A list or mode at its column's default is dude's.
func networkOf(w wrapper, l settingsLayers, grants map[string]capabilities.Grants) *networkOut {
	list := func(hosts []string, set string) valueOut {
		v := valueOut{Value: w.labels(hosts), Source: sourceDude}
		if len(hosts) > 0 {
			v.Source = set
		}
		return v
	}
	out := &networkOut{OrganizationEgress: list(l.org.Egress, sourceOrg),
		BrainstormAnywhere: valueOut{Value: l.org.BrainstormAnywhere, Source: sourceDude}}
	if l.org.BrainstormAnywhere {
		out.BrainstormAnywhere.Source = sourceOrg
	}
	if l.project != nil {
		pe := list(l.project.Egress, sourceProject)
		mode := valueOut{Value: l.project.EgressMode, Source: sourceDude}
		if l.project.EgressMode != "add" {
			mode.Source = sourceProject
		}
		out.ProjectEgress, out.Mode = &pe, &mode
	}
	for _, role := range settingsRoles {
		g := grants[role]
		out.Web = append(out.Web, webOut{Role: role, WebSearch: g.Native(capabilities.NativeWebSearch),
			WebFetch: g.Native(capabilities.NativeWebFetch)})
	}
	return out
}

// ---- secrets --------------------------------------------------------------

// secretOut is a project's own secret as its Secrets page lists it to
// members: its name and which of the project's Runs and previews receive
// it, never its value or hint. The organisation's secrets the page lists
// after them are not here (the tool's doc says so).
type secretOut struct {
	Name       string   `json:"name"`
	Recipients []string `json:"recipients"`
}

// projectSecrets are the named projects' secrets, by project id.
func projectSecrets(ctx context.Context, tx pgx.Tx, projects []projectRow) (map[string][]secretOut, error) {
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	rows, err := tx.Query(ctx, `SELECT project_id, name, recipients FROM project_secrets WHERE project_id = ANY($1) ORDER BY name`, ids)
	if err != nil {
		return nil, err
	}
	out := map[string][]secretOut{}
	var project string
	var s secretOut
	_, err = pgx.ForEachRow(rows, []any{&project, &s.Name, &s.Recipients}, func() error {
		out[project] = append(out[project], s)
		return nil
	})
	return out, err
}

// ---- capabilities ---------------------------------------------------------

type capabilityOut struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
}

type capabilitiesOut struct {
	Role    string          `json:"role"`
	Label   string          `json:"label"`
	Yours   bool            `json:"yours,omitempty"`
	Granted []capabilityOut `json:"granted"`
}
