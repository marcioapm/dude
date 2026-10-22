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
	if settings["title"] != "Settings" || len(sections) == 0 || sections[0].(map[string]any)["anchor"] != "delivery" ||
		sections[0].(map[string]any)["heading"] != "Delivery" {
		t.Errorf("settings page in the list: %v", settings)
	}

	status, out = m.do(t, "GET", "/internal/help/memory", "")
	p, _ := guide.PageBy("memory")
	if status != 200 || out["slug"] != "memory" || out["title"] != p.Title || out["markdown"] != p.Markdown {
		t.Errorf("page: %d %v", status, out)
	}
	if status, out = m.do(t, "GET", "/internal/help/nope", ""); status != 404 {
		t.Errorf("unknown page: %d %v", status, out)
	}
}
