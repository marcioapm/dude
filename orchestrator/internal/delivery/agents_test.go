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

// A file that does not parse, or is empty, is an error at load, never a
// template that renders empty.
func TestEveryAgentTemplateParses(t *testing.T) {
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
}

// A file that includes a missing one is an error at load, naming both,
// wherever in the file the {{template}} sits.
func TestAMissingIncludeNamesItsFile(t *testing.T) {
	const gone = `{{template "contracts/gone"}}`
	for _, c := range []struct{ name, src, includer string }{
		{"if", `x{{if .}}` + gone + `{{end}}`, "contracts/a"},
		{"if-else", `x{{if .}}{{else}}` + gone + `{{end}}`, "contracts/a"},
		{"range", `x{{range .}}` + gone + `{{end}}`, "contracts/a"},
		{"range-else", `x{{range .}}{{else}}` + gone + `{{end}}`, "contracts/a"},
		{"with", `x{{with .}}` + gone + `{{end}}`, "contracts/a"},
		{"with-else", `x{{with .}}{{else}}` + gone + `{{end}}`, "contracts/a"},
		// define and block bodies are templates of their own in the set.
		{"define", `x{{define "inner"}}` + gone + `{{end}}`, "inner"},
		{"block", `x{{block "inner" .}}` + gone + `{{end}}`, "inner"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseAgentTemplates(fstest.MapFS{"agents/contracts/a.md": {Data: []byte(c.src + "\n")}})
			want := "agents/" + c.includer + ".md includes agents/contracts/gone.md, which is missing"
			if err == nil || err.Error() != want {
				t.Errorf("%s: got %v, want %q", c.src, err, want)
			}
		})
	}
}

// A key Go needs under a prefix that has no file panics, naming the file.
func TestAMissingAgentTextNamesItsFile(t *testing.T) {
	set, err := parseAgentTemplates(fstest.MapFS{"agents/contracts/review-range/simplify.md": {Data: []byte("Simplify.\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := agentTextsIn(set, "contracts/review-range/", "simplify"); got["simplify"] != "Simplify." {
		t.Errorf("agentTexts of a present key: %q", got)
	}
	defer func() {
		if r := recover(); fmt.Sprint(r) != "agents/contracts/review-range/pull-request-fix.md is missing" {
			t.Errorf("agentTexts without a wanted key: %v", r)
		}
	}()
	agentTextsIn(set, "contracts/review-range/", "simplify", "pull-request-fix")
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

// A {{define}} Go renders that its file lacks panics, naming the file.
func TestAMissingDefineNamesItsFile(t *testing.T) {
	defer func() {
		if r := fmt.Sprint(recover()); r != `agents/briefings/replay-lines.md defines no "briefings/replay-lines/gone"` {
			t.Errorf("mustTemplate of a missing define: %v", r)
		}
	}()
	mustTemplate("briefings/replay-lines/gone")
}
