package chat_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

func TestChatATopLevelMentionReadsTheChannelBeforeIt(t *testing.T) {
	w := setup(t)
	w.fake.PostAged("C1", "U2", "a message from two days ago", 48*time.Hour)
	text, attachments, blocks := ciReport()
	w.fake.PostApp("C1", "", "BCI", "CI Bot", text, attachments, blocks)
	root := w.fake.Post("C1", "", "U9", "the exports thread")
	w.fake.Post("C1", root, "U2", "a reply inside a thread")
	w.fake.PostOwn("C1", "", "T1", "an earlier answer of dude's")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "investigate", "title": "Nightly"}))
	w.mention("Márcia", "C1", "", "can you investigate this night's failures above?")

	sent := w.model.last()
	if !strings.Contains(sent, `## The channel before it`) || strings.Contains(sent, `## The thread before it`) {
		t.Fatalf("sections: %s", sent)
	}
	for _, want := range []string{"CI Bot (an app): Nightly build failed", "https://github.com/acme/api/actions/runs/812", "Zed Outside (not in dude): the exports thread"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the routing input lacks %q: %s", want, sent)
		}
	}
	for _, not := range []string{"two days ago", "a reply inside a thread", "an earlier answer of dude's"} {
		if strings.Contains(sent, not) {
			t.Fatalf("the routing input has %q: %s", not, sent)
		}
	}
	// Oldest first: the report before the exports thread.
	if strings.Index(sent, "CI Bot (an app)") > strings.Index(sent, "the exports thread") {
		t.Fatalf("not oldest first: %s", sent)
	}
	got := w.requests()
	session := got[0].ResultID
	if b := w.one(`SELECT prompt FROM runs WHERE session_id = $1`, session); !strings.Contains(b, "Earlier in the channel, before this message") ||
		!strings.Contains(b, "https://github.com/acme/api/actions/runs/812") {
		t.Fatalf("briefing %s", b)
	}
	if c := w.one(`SELECT origin->'channel'->0->>'by' FROM sessions WHERE id = $1`, session); c != "CI Bot" {
		t.Fatalf("origin channel %s", c)
	}
	// Kept on the delivery for a retry, and in the context hash.
	if k := w.one(`SELECT decision->'channel'->'Messages'->0->>'by' FROM chat_events WHERE organization_id = $1`, w.org); k != "CI Bot" {
		t.Fatalf("kept %s", k)
	}
	if h := w.one(`SELECT context_hash FROM chat_requests WHERE organization_id = $1`, w.org); h == "" {
		t.Fatal("no context hash")
	}
	if n := w.fake.Calls("conversations.history"); n != 1 {
		t.Fatalf("%d history reads", n)
	}
}

func TestChatTheChannelIsCutFromItsOldestEnd(t *testing.T) {
	w := setup(t)
	long := strings.Repeat("x", 1900)
	for i := range 10 {
		w.fake.Post("C1", "", "U2", fmt.Sprintf("message %d %s", i, long))
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "what's going on?")
	sent := w.model.last()
	if !strings.Contains(sent, "earlier messages left out") || strings.Contains(sent, "message 0 ") || !strings.Contains(sent, "message 9 ") {
		t.Fatalf("not cut from the oldest end: %.600s", sent)
	}
}

func TestChatAThreadMentionReadsNoChannel(t *testing.T) {
	w := setup(t)
	w.fake.Post("C1", "", "U2", "something top-level")
	root := w.fake.Post("C1", "", "U9", "exports time out")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", root, "look at this")
	if n := w.fake.Calls("conversations.history"); n != 0 {
		t.Fatalf("a thread mention read the channel %d times", n)
	}
	if sent := w.model.last(); strings.Contains(sent, "The channel before it") || strings.Contains(sent, "something top-level") {
		t.Fatalf("sent %s", sent)
	}
}

func TestChatAFailedChannelReadRoutesAnyway(t *testing.T) {
	w := setup(t)
	w.fake.Post("C1", "", "U2", "something top-level")
	w.fake.FailNext("conversations.history", "missing_scope")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "what's going on?")
	got := w.requests()
	if len(got) != 1 || got[0].Route != "brainstorm" || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	if sent := w.model.last(); strings.Contains(sent, "The channel before it") {
		t.Fatalf("sent %s", sent)
	}
	if f := w.one(`SELECT last_failure FROM chat_installations WHERE organization_id = $1`, w.org); !strings.Contains(f, "conversations.history") {
		t.Fatalf("the failure was not noted: %q", f)
	}
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
