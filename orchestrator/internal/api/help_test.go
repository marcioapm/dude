package api_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/guide"
)

// The Help page reads the guide this orchestrator embeds: its pages and
// their sections, and one page's Markdown; a page it lacks is a 404.
func TestHelpServesTheEmbeddedGuide(t *testing.T) {
	m := setupMemory(t)
	status, out := m.do(t, "GET", "/internal/help", "")
	pages, _ := out["pages"].([]any)
	if status != 200 || len(pages) != len(guide.Pages()) {
		t.Fatalf("list: %d %v", status, out)
	}
	var settings map[string]any
	for _, p := range pages {
		if p := p.(map[string]any); p["slug"] == "settings" {
			settings = p
		}
	}
	sections, _ := settings["sections"].([]any)
	sp, _ := guide.PageBy("settings")
	var first guide.Section
	for _, s := range sp.Sections {
		if s.Anchor != "" {
			first = s
			break
		}
	}
	if settings["title"] != sp.Title || len(sections) == 0 || first.Anchor == "" ||
		sections[0].(map[string]any)["anchor"] != first.Anchor || sections[0].(map[string]any)["heading"] != first.Heading {
		t.Errorf("settings page in the list: %v", settings)
	}

	status, out = m.do(t, "GET", "/internal/help/memory", "")
	p, _ := guide.PageBy("memory")
	got, _ := out["sections"].([]any)
	if status != 200 || out["slug"] != "memory" || out["title"] != p.Title || len(got) != len(p.Sections) {
		t.Fatalf("page: %d %v", status, out)
	}
	for i, s := range p.Sections {
		g := got[i].(map[string]any)
		if g["anchor"] != s.Anchor || g["text"] != s.Text {
			t.Errorf("section %d: %v, want %+v", i, g, s)
		}
	}
	if status, out = m.do(t, "GET", "/internal/help/nope", ""); status != 404 {
		t.Errorf("unknown page: %d %v", status, out)
	}
}
