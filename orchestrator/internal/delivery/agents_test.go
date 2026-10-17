package delivery

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// Every role a phase runs as, and every role a person can edit, has its
// instructions file, read whole.
func TestEveryRoleHasItsInstructionsFile(t *testing.T) {
	roles, err := loadRoles(agentFiles)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{}, PromptRoles...)
	for _, role := range PromptRoleForPhase {
		want = append(want, role)
	}
	for _, role := range want {
		if len(roles[role]) == 0 || strings.TrimSpace(roles[role][0]) == "" {
			t.Errorf("agents/roles/%s.md is missing or empty", role)
		}
	}
	for role := range roles {
		if !slices.Contains(PromptRoles, role) {
			t.Errorf("agents/roles/%s.md is no role a person can edit", role)
		}
	}
}

// Every embedded template parses, and a file that does not is an error at
// load, never a template that renders empty.
func TestEveryAgentTemplateParses(t *testing.T) {
	parsed, err := parseAgentTemplates(agentFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Templates()) != len(agentTemplates.Templates()) {
		t.Errorf("parsed %d templates, the package holds %d", len(parsed.Templates()), len(agentTemplates.Templates()))
	}
	broken := fstest.MapFS{"agents/contracts/broken.md": {Data: []byte("Hello {{.Name\n")}}
	if _, err := parseAgentTemplates(broken); err == nil || !strings.Contains(err.Error(), "agents/contracts/broken.md") {
		t.Errorf("a template that does not parse loads: %v", err)
	}
	empty := fstest.MapFS{"agents/briefings/empty.md": {Data: []byte("\n")}}
	if _, err := parseAgentTemplates(empty); err == nil {
		t.Error("an empty template loads")
	}
	if _, err := loadRoles(fstest.MapFS{"agents/roles/fixer.md": {Data: []byte("  \n")}}); err == nil {
		t.Error("an empty role file loads")
	}
	including := fstest.MapFS{"agents/contracts/a.md": {Data: []byte("x{{if .}}{{template \"contracts/gone\"}}{{end}}\n")}}
	if _, err := parseAgentTemplates(including); err == nil || !strings.Contains(err.Error(), "agents/contracts/gone.md") {
		t.Errorf("a template including a missing one loads: %v", err)
	}
}

// A name Go renders that has no file panics, naming the file.
func TestAMissingTemplateNamesItsFile(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "agents/contracts/gone.md") {
			t.Errorf("mustTemplate of a missing name: %v", r)
		}
	}()
	mustTemplate("contracts/gone")
}
