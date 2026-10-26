package automations

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// givenTo is what a run of the automation is given, by name, as its spec
// would declare it.
func (h *harness) givenTo(id string) map[string]string {
	h.t.Helper()
	ctx := context.Background()
	var a Automation
	if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) {
		a, err = Load(ctx, tx, id)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	in, why, err := h.runner.specInput(ctx, a, "arn_"+id, nil, nil, false)
	if err != nil || why != "" {
		h.t.Fatalf("%s: %s %v", id, why, err)
	}
	out := map[string]string{}
	for _, s := range in.Secrets {
		if s.As != "env" {
			h.t.Errorf("%s: %s given as %q", id, s.Name, s.As)
		}
		out[s.Name] = s.Value
	}
	return out
}

func equalSecrets(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// An own agent, a command and a server get exactly the secrets they pick,
// at either level; an organisation pick whose name the project holds is
// the project's value at run start. A dude-role preset gets what its role
// is ticked for; the Investigator is no recipient and gets none.
func TestAutomationRunsGetTheirPickedOrRoleSecrets(t *testing.T) {
	h := newHarness(t)
	exec(t, h.owner, `INSERT INTO organization_secrets (organization_id, name, value, hint, recipients) VALUES
		($1, 'GH_TOKEN', 'org-gh', 'o-gh', '{implementer}'), ($1, 'NPM_TOKEN', 'org-npm', 'onpm', '{tester}'),
		($1, 'SENTRY_TOKEN', 'org-sentry', 'osen', '{tester,reviewer}'), ($1, 'STATUSPAGE_KEY', 'org-status', 'ostat', '{}')`, h.org)
	exec(t, h.owner, `INSERT INTO project_secrets (organization_id, project_id, name, value, hint, recipients) VALUES
		($1, $2, 'GH_TOKEN', 'project-gh', 'p-gh', '{tester}'), ($1, $2, 'DB_PASS', 'project-db', 'p-db', '{previews}')`, h.org, h.project)
	schedule := map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"}
	own := map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Go"}}
	preset := func(role string) map[string]any {
		return map[string]any{"preset": map[string]any{"role": role, "overrides": map[string]any{"tierId": h.tier}}, "brief": ""}
	}
	h.automation("aut_org", "agent", schedule, map[string]any{"project_id": nil, "agent": own})
	h.automation("aut_proj", "command", schedule, map[string]any{"command": "true"})
	h.automation("aut_none", "command", schedule, map[string]any{"command": "true"})
	h.automation("aut_proj_only", "command", schedule, map[string]any{"command": "true"})
	h.automation("aut_qa", "agent", schedule, map[string]any{"agent": preset("qa_browser")})
	h.automation("aut_qa_org", "agent", schedule, map[string]any{"project_id": nil, "agent": preset("qa_browser")})
	h.automation("aut_inv", "agent", schedule, map[string]any{"agent": preset("investigator")})
	exec(t, h.owner, `UPDATE automations SET secrets = '[{"source":"organization","name":"STATUSPAGE_KEY"},{"source":"organization","name":"GH_TOKEN"}]' WHERE id = 'aut_org'`)
	exec(t, h.owner, `UPDATE automations SET secrets = '[{"source":"organization","name":"GH_TOKEN"},{"source":"organization","name":"NPM_TOKEN"},
		{"source":"project","name":"DB_PASS"}]' WHERE id = 'aut_proj'`)
	// A project pick of a name only the organisation holds: the source is part of the pick, so it gets nothing.
	exec(t, h.owner, `UPDATE automations SET secrets = '[{"source":"project","name":"STATUSPAGE_KEY"}]' WHERE id = 'aut_proj_only'`)

	for id, want := range map[string]map[string]string{
		// Ticked for nobody, it still reaches an automation that picks it.
		"aut_org": {"STATUSPAGE_KEY": "org-status", "GH_TOKEN": "org-gh"},
		// The organisation's GH_TOKEN pick resolves to the project's.
		"aut_proj":      {"GH_TOKEN": "project-gh", "NPM_TOKEN": "org-npm", "DB_PASS": "project-db"},
		"aut_proj_only": {},
		"aut_none":      {},
		// Tester: tester-ticked at both levels, the project's GH_TOKEN winning the name.
		"aut_qa":     {"GH_TOKEN": "project-gh", "NPM_TOKEN": "org-npm", "SENTRY_TOKEN": "org-sentry"},
		"aut_qa_org": {"NPM_TOKEN": "org-npm", "SENTRY_TOKEN": "org-sentry"},
		"aut_inv":    {},
	} {
		if got := h.givenTo(id); !equalSecrets(got, want) {
			t.Errorf("%s was given %v, want %v", id, got, want)
		}
	}

	// A project secret added later under a picked organisation name takes its place at the next run start.
	exec(t, h.owner, `INSERT INTO project_secrets (organization_id, project_id, name, value, hint, recipients) VALUES ($1, $2, 'NPM_TOKEN', 'project-npm', 'pnpm', '{previews}')`, h.org, h.project)
	if got := h.givenTo("aut_proj"); got["NPM_TOKEN"] != "project-npm" {
		t.Errorf("after the project's own NPM_TOKEN: %v", got)
	}
}
