package agenttools

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// settings (capabilities.SettingsRead) is dude's settings as they apply to
// the caller, each value with the layer it is set in: the organisation's
// and its projects' rows read in one transaction under the caller's
// organisation, resolved by the functions the Runs are built with
// (delivery.PolicySources over ResolvePolicy, GitHubSettingsSetTx). It
// shows only what every member of the organisation sees on the settings
// pages, never a credential, secret or prompt text. What people typed is
// quoted as external text.

type settingsIn struct {
	Project string `json:"project,omitempty"`
	Task    string `json:"task,omitempty"`
	Section string `json:"section,omitempty"`
}

// The sections settings answers, in its output's order.
var settingsSections = []string{"delivery", "roles", "prompts", "github", "network", "capabilities", "machines"}

// Where a value is set, as settings says it.
const (
	sourceDude    = "dude"
	sourceOrg     = "organization"
	sourceProject = "project"
	sourceSession = "session"
)

// valueOut is one setting: its value and the layer it is set in.
type valueOut struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// settingsLayers are the rows a scope resolves over: the organisation's,
// and a project's when the scope is one.
type settingsLayers struct {
	org     orgRow
	project *projectRow
	// A brainstorm caller's session's tier and harness, over the
	// organisation's Brainstorm setting, and the size its agent raised.
	session *delivery.SessionModel
	raised  delivery.Raises
}

// of is the layers role's Runs are built over: a brainstorm's are its
// organisation's and its session's, never a project's (brainstormSpec).
func (l settingsLayers) of(role string) settingsLayers {
	if role == delivery.RoleBrainstorm {
		return settingsLayers{org: l.org, session: l.session, raised: l.raised}
	}
	return settingsLayers{org: l.org, project: l.project}
}

type orgRow struct {
	Name               string
	AgentModels        json.RawMessage
	DeliveryPolicy     json.RawMessage
	Egress             []string
	BrainstormAnywhere bool
}

type projectRow struct {
	ID, Key, Name  string
	AgentModels    json.RawMessage
	DeliveryPolicy json.RawMessage
	Egress         []string
	EgressMode     string
}

type scopeOut struct {
	Project  *projectRefOut         `json:"project,omitempty"`
	Delivery map[string]valueOut    `json:"delivery,omitempty"`
	Roles    []roleOut              `json:"roles,omitempty"`
	Secrets  []secretOut            `json:"secrets,omitempty"`
	Prompts  []promptOut            `json:"prompts,omitempty"`
	Network  *networkOut            `json:"network,omitempty"`
	Machines map[string]machineRole `json:"machines,omitempty"`
}

type projectRefOut struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type settingsOut struct {
	External     string            `json:"external"`
	Sources      string            `json:"sources"`
	Organization string            `json:"organization"`
	Role         string            `json:"yourRole"`
	Scopes       []scopeOut        `json:"settings"`
	Task         *taskViewOut      `json:"task,omitempty"`
	GitHub       any               `json:"github,omitempty"`
	Capabilities []capabilitiesOut `json:"capabilities,omitempty"`
	MachineSizes []sizeOut         `json:"machineSizes,omitempty"`
}

// settingsRoles are the roles the settings pages list, in their order.
var settingsRoles = []string{delivery.RoleConductor, delivery.RoleBrainstorm, "investigator", "implementer", "reviewer", "fixer",
	"simplifier", "qa_browser"}

func readSettings(ctx context.Context, tx pgx.Tx, c Caller, in settingsIn) (settingsOut, error) {
	var out settingsOut
	section := strings.TrimSpace(in.Section)
	if section != "" && !slices.Contains(settingsSections, section) {
		return out, refuse(rSettingsSection, strings.Join(settingsSections, ", "))
	}
	want := func(s string) bool { return section == "" || section == s }
	w := newWrapper()
	taskID, taskProject, err := settingsTask(ctx, tx, c, in.Task)
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(in.Task) != "" && strings.TrimSpace(in.Project) == "" {
		// A task named alone is read in its own project.
		in.Project = taskProject
	}
	org, projects, err := settingsScopes(ctx, tx, c, in)
	if err != nil {
		return out, err
	}
	if taskID != "" {
		if out.Task, err = taskView(ctx, tx, w, taskID); err != nil {
			return out, err
		}
	}
	out.External, out.Sources = settingsText("external", delivery.ExternalText(w.id, "…")), settingsText("sources", nil)
	out.Organization, out.Role = w.text(org.Name, 200), c.Role
	var session *delivery.SessionModel
	var raised delivery.Raises
	if c.SessionID != "" && (want("roles") || want("machines")) {
		m, err := delivery.LoadSessionModel(ctx, tx, c.SessionID)
		if err != nil {
			return out, err
		}
		session = &m
		if raised, err = delivery.LoadSessionRaise(ctx, tx, c.SessionID, delivery.RoleBrainstorm); err != nil {
			return out, err
		}
	}
	scopes := []settingsLayers{{org: org, session: session, raised: raised}}
	if len(projects) > 0 {
		scopes = scopes[:0]
		for i := range projects {
			scopes = append(scopes, settingsLayers{org: org, project: &projects[i], session: session, raised: raised})
		}
	}
	grants := map[string]capabilities.Grants{}
	for _, role := range settingsRoles {
		if grants[role], err = capabilities.Resolve(ctx, tx, capabilities.Agent{Role: settingsToolRole(role), Org: c.Org}); err != nil {
			return out, err
		}
	}
	// Everything the sections resolve over, each read once for all scopes.
	var refs roleRefs
	var secrets map[string][]secretOut
	if want("roles") || want("machines") {
		if refs.sizes, err = delivery.LoadSizes(ctx, tx); err != nil {
			return out, err
		}
	}
	if want("roles") {
		if refs.tiers, err = delivery.LoadTiers(ctx, tx); err != nil {
			return out, err
		}
		if refs.images, err = imageNames(ctx, tx); err != nil {
			return out, err
		}
		if secrets, err = projectSecrets(ctx, tx, projects); err != nil {
			return out, err
		}
	}
	var prompts map[promptKey]promptRow
	if want("prompts") {
		if prompts, err = latestPrompts(ctx, tx, projects); err != nil {
			return out, err
		}
	}
	for _, l := range scopes {
		s := scopeOut{}
		if l.project != nil {
			s.Project = &projectRefOut{Key: l.project.Key, Name: w.text(l.project.Name, 200)}
		}
		if want("delivery") {
			if s.Delivery, err = deliveryOut(w, l); err != nil {
				return out, err
			}
		}
		if want("roles") {
			s.Roles = rolesOut(w, l, refs)
			if l.project != nil {
				s.Secrets = secrets[l.project.ID]
			}
		}
		if want("prompts") {
			s.Prompts = promptsOut(w, l, prompts)
		}
		if want("network") {
			s.Network = networkOf(w, l, grants)
		}
		if want("machines") {
			s.Machines = map[string]machineRole{}
			for _, role := range settingsRoles {
				s.Machines[role] = machineOf(w, l.of(role), refs.sizes, role)
			}
		}
		out.Scopes = append(out.Scopes, s)
	}
	if want("github") {
		gh, set, err := delivery.GitHubSettingsSetTx(ctx, tx)
		if err != nil {
			return out, err
		}
		out.GitHub = githubOut(w, gh, set)
	}
	if want("capabilities") {
		for _, role := range settingsRoles {
			cs := capabilitiesOut{Role: role, Label: delivery.RoleName(role), Yours: role == c.Role, Granted: []capabilityOut{}}
			for _, cap := range grants[role].Granted() {
				cs.Granted = append(cs.Granted, capabilityOut{Key: string(cap.Key), Description: cap.Description})
			}
			out.Capabilities = append(out.Capabilities, cs)
		}
	}
	if want("machines") {
		out.MachineSizes = sizesOut(w, refs.sizes)
	}
	return out, nil
}

// settingsToolRole is the role a settings role's Runs are given tools as:
// the fixer is the implementer's Run (instructions.go's toolRoleOf).
func settingsToolRole(role string) string {
	if role == "fixer" {
		return "implementer"
	}
	return role
}

// settingsScopes reads the organisation's row and the projects the call is
// about: the one named, else the caller's own project, else a session's
// linked projects; none is the organisation alone.
func settingsScopes(ctx context.Context, tx pgx.Tx, c Caller, in settingsIn) (orgRow, []projectRow, error) {
	var org orgRow
	if err := tx.QueryRow(ctx, `SELECT name, default_agent_models, delivery_policy, agent_egress, brainstorm_anywhere
		FROM organizations WHERE id = $1`, c.Org).Scan(&org.Name, &org.AgentModels, &org.DeliveryPolicy, &org.Egress,
		&org.BrainstormAnywhere); err != nil {
		return org, nil, err
	}
	const cols = `SELECT p.id, p.key_prefix, p.name, p.agent_models, p.delivery_policy, p.agent_egress, p.agent_egress_mode FROM projects p `
	var rows pgx.Rows
	var err error
	name := strings.TrimSpace(in.Project)
	switch {
	case name != "":
		rows, err = tx.Query(ctx, cols+`WHERE p.id = $1 OR upper(p.key_prefix) = upper($1) OR lower(p.name) = lower($1)
			ORDER BY p.id = $1 DESC, upper(p.key_prefix) = upper($1) DESC LIMIT 1`, name)
	case c.ProjectID != "":
		rows, err = tx.Query(ctx, cols+`WHERE p.id = $1`, c.ProjectID)
	case c.SessionID != "":
		rows, err = tx.Query(ctx, cols+`JOIN session_projects sp ON sp.project_id = p.id WHERE sp.session_id = $1
			ORDER BY p.key_prefix, p.name`, c.SessionID)
	default:
		return org, nil, nil
	}
	if err != nil {
		return org, nil, err
	}
	projects, err := pgx.CollectRows(rows, pgx.RowToStructByPos[projectRow])
	if err != nil {
		return org, nil, err
	}
	if name != "" && len(projects) == 0 {
		return org, nil, refuse(rSettingsNoProject, name)
	}
	return org, projects, nil
}

// sourceOf names a delivery.SetIn… layer as settings says it.
func sourceOf(setIn string) string {
	switch setIn {
	case delivery.SetInProject:
		return sourceProject
	case delivery.SetInOrganization:
		return sourceOrg
	}
	return sourceDude
}

// deliveryOut is every key of the scope's delivery policy, with its layer.
// The conditional reviewers' path globs are people's when a layer sets
// them.
func deliveryOut(w wrapper, l settingsLayers) (map[string]valueOut, error) {
	var project json.RawMessage
	if l.project != nil {
		project = l.project.DeliveryPolicy
	}
	p, from, err := delivery.PolicySources(l.org.DeliveryPolicy, project)
	if err != nil {
		return nil, err
	}
	out := map[string]valueOut{}
	for k, v := range policyValues(p) {
		out[k] = valueOut{Value: v, Source: sourceOf(from[k])}
	}
	if src := out["conditionalReviewers"].Source; src != sourceDude {
		rules := make([]delivery.ReviewerRule, len(p.ConditionalReviewers))
		for i, r := range p.ConditionalReviewers {
			rules[i] = delivery.ReviewerRule{Category: w.text(r.Category, 100), WhenPathsMatch: w.labels(r.WhenPathsMatch)}
		}
		out["conditionalReviewers"] = valueOut{Value: rules, Source: src}
	}
	return out, nil
}

// policyValues are p's fields by their stored keys.
func policyValues(p delivery.Policy) map[string]any {
	v := reflect.ValueOf(p)
	out := map[string]any{}
	for i, k := range delivery.PolicyKeys() {
		out[k] = v.Field(i).Interface()
	}
	return out
}

// githubOut is the organisation's GitHub settings (forge.Settings), each
// with its layer: the organisation's where it stored the key. The
// connection itself — its credential, webhook secret, OAuth client — is
// not a setting here.
func githubOut(w wrapper, s forge.Settings, set map[string]bool) map[string]valueOut {
	v := reflect.ValueOf(s)
	out := map[string]valueOut{}
	for i, k := range forge.SettingKeys() {
		val := v.Field(i).Interface()
		if k == "reviewLogins" {
			val = w.labels(s.ReviewLogins)
		}
		src := sourceDude
		if set[k] {
			src = sourceOrg
		}
		out[k] = valueOut{Value: val, Source: src}
	}
	return out
}

// settingsText renders results/settings.md's text name.
func settingsText(name string, data any) string { return settingsTexts[name].Render(data) }

var settingsTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"external", "sources", "frozen", "reviewers"} {
		out[name] = delivery.MustText("results/settings/" + name)
	}
	return out
}()
