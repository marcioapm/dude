package chat_test

import (
	"strings"
	"testing"
)

// ciReport is a CI bot's nightly report: the run's link only in an
// attachment, and a section block repeating the text.
func ciReport() (text string, attachments, blocks []any) {
	text = "Nightly build failed"
	attachments = []any{map[string]any{"title": "Run #812 failed", "title_link": "https://github.com/acme/api/actions/runs/812",
		"text": "2 jobs failed: integration, lint"}}
	blocks = []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "Nightly build failed"}}}
	return
}

func TestChatAThreadKeepsOtherAppsAndDropsDudesOwn(t *testing.T) {
	w := setup(t)
	text, attachments, blocks := ciReport()
	root := w.fake.PostApp("C1", "", "BCI", "CI Bot", text, attachments, blocks)
	w.fake.PostOwn("C1", root, "T1", "an earlier reply of dude's")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "investigate", "title": "Nightly"}))
	w.mention("Márcia", "C1", root, "can you investigate this?")

	sent := w.model.last()
	for _, want := range []string{"CI Bot (an app): Nightly build failed", "https://github.com/acme/api/actions/runs/812",
		"2 jobs failed: integration, lint"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the routing input lacks %q: %s", want, sent)
		}
	}
	if strings.Contains(sent, "an earlier reply of dude's") {
		t.Fatalf("dude's own message was quoted: %s", sent)
	}
	if n := strings.Count(sent, "Nightly build failed"); n != 1 {
		t.Fatalf("the repeated block was quoted %d times: %s", n, sent)
	}
	session := w.requests()[0].ResultID
	origin := w.one(`SELECT origin::text FROM sessions WHERE id = $1`, session)
	if !strings.Contains(origin, `"by": "CI Bot"`) || !strings.Contains(origin, `"app": true`) || strings.Contains(origin, `"person"`) {
		t.Fatalf("origin %s", origin)
	}
}
