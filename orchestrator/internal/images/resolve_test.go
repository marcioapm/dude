package images

import (
	"encoding/json"
	"testing"
)

func TestPickTakesTheFirstLibraryIdThenTheFirstTypedImage(t *testing.T) {
	full := Site{Role: "img_role", PreviewID: "img_prev", PreviewTyped: "typed:prev", RuntimeID: "img_run",
		RuntimeTyped: "typed:run", DefaultID: "img_def", Fallback: "agent:fallback"}
	for _, c := range []struct {
		name      string
		site      func(Site) Site
		id, typed string
	}{
		{"the role's image beats the preview's, the runtime's and the default", func(s Site) Site { return s }, "img_role", ""},
		{"the preview's id beats the runtime's", func(s Site) Site { s.Role = ""; return s }, "img_prev", ""},
		{"the runtime's id beats the default", func(s Site) Site { s.Role, s.PreviewID = "", ""; return s }, "img_run", ""},
		{"the default base beats any typed image", func(s Site) Site { s.Role, s.PreviewID, s.RuntimeID = "", "", ""; return s }, "img_def", ""},
		{"any library id beats a typed preview image", func(s Site) Site {
			return Site{RuntimeID: "img_run", PreviewTyped: "typed:prev", Fallback: s.Fallback}
		}, "img_run", ""},
		{"a typed preview image beats a typed runtime image", func(s Site) Site {
			return Site{PreviewTyped: "typed:prev", RuntimeTyped: "typed:run", Fallback: s.Fallback}
		}, "", "typed:prev"},
		{"a typed runtime image", func(s Site) Site { return Site{RuntimeTyped: "typed:run", Fallback: s.Fallback} }, "", "typed:run"},
		{"nothing set is the fallback", func(s Site) Site { return Site{Fallback: s.Fallback} }, "", "agent:fallback"},
	} {
		id, typed := c.site(full).Pick()
		if id != c.id || typed != c.typed {
			t.Errorf("%s: Pick = (%q, %q), want (%q, %q)", c.name, id, typed, c.id, c.typed)
		}
	}
}

// The same cases as packages/domain's resolveRoleImage test: the backend
// shows what the orchestrator runs.
func TestRoleImageFollowsTheProjectThenTheOrganizationAndTheFixerTheImplementer(t *testing.T) {
	known := func(id string) bool { return id != "img_gone" }
	for _, c := range []struct {
		name, role, project, org, want string
	}{
		{"the project's beats the organization's", "reviewer",
			`{"reviewer":{"image":"img_p"}}`, `{"reviewer":{"image":"img_o"}}`, "img_p"},
		{"the organization's when the project names none", "reviewer",
			`{"reviewer":{"model":"m"}}`, `{"reviewer":{"image":"img_o"}}`, "img_o"},
		{"the fixer's own beats the implementer's", "fixer",
			`{"fixer":{"image":"img_f"},"implementer":{"image":"img_i"}}`, `{}`, "img_f"},
		{"the fixer's own on the organization beats the project's implementer", "fixer",
			`{"implementer":{"image":"img_i"}}`, `{"fixer":{"image":"img_of"}}`, "img_of"},
		{"the fixer follows the project's implementer", "fixer",
			`{"implementer":{"image":"img_i"}}`, `{"implementer":{"image":"img_oi"}}`, "img_i"},
		{"the fixer follows the organization's implementer", "fixer",
			`{}`, `{"implementer":{"image":"img_oi"}}`, "img_oi"},
		{"only the fixer follows the implementer", "reviewer",
			`{"implementer":{"image":"img_i"}}`, `{}`, ""},
		{"an image that is gone is skipped", "reviewer",
			`{"reviewer":{"image":"img_gone"}}`, `{"reviewer":{"image":"img_o"}}`, "img_o"},
		{"invalid JSON on the project falls through to the organization", "reviewer",
			`not json`, `{"reviewer":{"image":"img_o"}}`, "img_o"},
		{"invalid JSON on the organization leaves the project's", "reviewer",
			`{"reviewer":{"image":"img_p"}}`, `{"reviewer":`, "img_p"},
		{"none anywhere", "implementer", `{}`, `null`, ""},
	} {
		if got := RoleImage(c.role, json.RawMessage(c.project), json.RawMessage(c.org), known); got != c.want {
			t.Errorf("%s: RoleImage = %q, want %q", c.name, got, c.want)
		}
	}
}
