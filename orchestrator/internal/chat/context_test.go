package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// The channel is read once, asking for at most 30 messages: of 40 before
// the mention, the newest 30 are quoted.
func TestChatTheChannelIsAtMostThirtyMessages(t *testing.T) {
	w := setup(t)
	for i := range 40 {
		w.fake.Post("C1", "", "U2", fmt.Sprintf("note %02d", i))
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "what's going on?")
	args := w.fake.Args("conversations.history")
	if len(args) != 1 || args[0]["limit"] != "30" {
		t.Fatalf("history reads %v", args)
	}
	sent := w.model.last()
	if strings.Contains(sent, "note 09") || !strings.Contains(sent, "note 10") || !strings.Contains(sent, "note 39") {
		t.Fatalf("not the newest 30: %s", sent)
	}
}

// The context hash covers the channel: the same message in the same
// conversation hashes apart once the channel before it differs.
func TestChatTheContextHashCoversTheChannel(t *testing.T) {
	w := setup(t)
	w.fake.Post("C1", "", "U2", "the export failed at 02:00")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "what's going on?")
	first := w.one(`SELECT context_hash FROM chat_requests WHERE organization_id = $1`, w.org)
	w.fake.Post("C1", "", "U2", "and the import at 03:00")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "what's going on?")
	second := w.one(`SELECT context_hash FROM chat_requests WHERE organization_id = $1 AND context_hash <> $2`, w.org, first)
	if first == "" || second == "" || first == second {
		t.Fatalf("hashes %q %q", first, second)
	}
}

// A retry acting on a kept decision quotes the channel it was decided
// from, without reading it again, though the channel changed meanwhile.
func TestChatARetryReusesTheKeptChannel(t *testing.T) {
	w := setup(t)
	w.fake.Post("C1", "", "U2", "the export failed at 02:00")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "Exports"}))
	fails := 1
	w.svc.SetBeforeMake(func(context.Context) error {
		if fails > 0 {
			fails--
			return &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
		}
		return nil
	})
	w.receive("Márcia", "C1", "", "look into it")
	w.once()
	if n := w.fake.Calls("conversations.history"); n != 1 {
		t.Fatalf("%d history reads", n)
	}
	w.fake.Post("C1", "", "U2", "written after the decision")
	w.due()
	w.once()
	if n := w.fake.Calls("conversations.history"); n != 1 {
		t.Fatalf("the retry read the channel again: %d reads", n)
	}
	got := w.requests()
	if len(got) != 1 || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	origin := w.one(`SELECT origin::text FROM sessions WHERE id = $1`, got[0].ResultID)
	if !strings.Contains(origin, "the export failed at 02:00") || strings.Contains(origin, "written after the decision") {
		t.Fatalf("origin %s", origin)
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
	for _, code := range []string{"missing_scope", "not_in_channel"} {
		w.fake.FailNext("conversations.history", code)
		w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
		w.mention("Márcia", "C1", "", "what's going on? "+code)
		if sent := w.model.last(); strings.Contains(sent, "The channel before it") {
			t.Fatalf("%s: sent %s", code, sent)
		}
		if f := w.one(`SELECT last_failure FROM chat_installations WHERE organization_id = $1`, w.org); !strings.Contains(f, code) {
			t.Fatalf("%s: the failure was not noted: %q", code, f)
		}
	}
	got := w.requests()
	if len(got) != 2 || got[0].Route != "brainstorm" || got[0].ResultKind != "session" || got[1].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
}

// A rate-limited channel read is not routed blind: the delivery is retried
// no sooner than Slack's Retry-After, as a thread read's is, and the retry
// reads the channel.
func TestChatARateLimitedChannelReadIsRetried(t *testing.T) {
	w := setup(t)
	w.fake.Post("C1", "", "U2", "the export failed at 02:00")
	w.fake.FailRetryAfter("conversations.history", "ratelimited", 1, 30)
	_, ev := w.receive("Márcia", "C1", "", "what's going on?")
	w.once()
	if e := w.event(ev); e.Processed || e.Decision || e.NextIn < 25*time.Second || !strings.Contains(e.LastError, "conversations.history") {
		t.Fatalf("after a rate-limited channel read: %+v", e)
	}
	if w.model.calls() != 0 || len(w.requests()) != 0 {
		t.Fatalf("routed without the channel: %d calls, %+v", w.model.calls(), w.requests())
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.due()
	w.once()
	if sent := w.model.last(); !strings.Contains(sent, "the export failed at 02:00") {
		t.Fatalf("the retry did not read the channel: %s", sent)
	}
}

// What is stored of the channel is what the model was shown: the newest
// messages within 12 000 characters, on the kept decision, the session's
// origin and a task's origin and goal alike.
func TestChatOnlyTheChannelShownIsStored(t *testing.T) {
	w := setup(t)
	long := strings.Repeat("y", 1900)
	for i := range 10 {
		w.fake.Post("C1", "", "U2", fmt.Sprintf("note %d %s", i, long))
	}
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.95, "reasoning": "r", "project": "BIL", "title": "Look",
		"goal": "Look at the nightly notes above."}))
	w.mention("Márcia", "C1", "", "file it")
	got := w.requests()
	if len(got) != 1 || got[0].ResultKind != "task" {
		t.Fatalf("requests %+v", got)
	}
	for what, q := range map[string]string{
		"decision":    `SELECT decision->'channel'->'Messages' FROM chat_events WHERE organization_id = $1`,
		"task origin": `SELECT origin->'channel' FROM tasks WHERE organization_id = $1 AND origin ? 'channel'`,
	} {
		stored := w.one(`SELECT (`+q+`)::text`, w.org)
		if strings.Contains(stored, "note 0 ") || !strings.Contains(stored, "note 9 ") || len(stored) > 14000 {
			t.Errorf("%s stores %d characters (note 0 kept: %v)", what, len(stored), strings.Contains(stored, "note 0 "))
		}
	}
	goal := w.one(`SELECT goal FROM tasks WHERE organization_id = $1 AND origin ? 'channel'`, w.org)
	if strings.Contains(goal, "note 0 ") || !strings.Contains(goal, "earlier messages left out") {
		t.Errorf("goal %.300s", goal)
	}
}

// Slack's text arrives mrkdwn-escaped, with links as <url|label>: the
// thread and the channel reach the routing call as the words shown.
func TestChatTheThreadAndChannelAreQuotedAsTheirWords(t *testing.T) {
	w := setup(t)
	w.fake.Post("C1", "", "U2", "R&amp;D's notes: <https://example.com/n|the notes> &lt;draft&gt;")
	root := w.fake.Post("C1", "", "U9", "Q&amp;A at <https://example.com/qa|the board>")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", root, "look at this")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "and this")
	var thread, channel map[string]any
	w.model.mu.Lock()
	thread, channel = w.model.sent[0], w.model.sent[1]
	w.model.mu.Unlock()
	for _, c := range []struct {
		sent     map[string]any
		want     string
		notWants []string
	}{
		{thread, "Q&A at the board (https://example.com/qa)", []string{"&amp;", "<https://"}},
		{channel, "R&D's notes: the notes (https://example.com/n) <draft>", []string{"&amp;", "&lt;", "<https://"}},
	} {
		raw, _ := json.Marshal(c.sent)
		var decoded struct {
			Messages []struct{ Content any } `json:"messages"`
		}
		_ = json.Unmarshal(raw, &decoded)
		var all strings.Builder
		for _, m := range decoded.Messages {
			fmt.Fprint(&all, m.Content, "\n")
		}
		if !strings.Contains(all.String(), c.want) {
			t.Errorf("the routing input lacks %q:\n%s", c.want, all.String())
		}
		for _, not := range c.notWants {
			if strings.Contains(all.String(), not) {
				t.Errorf("the routing input holds %q:\n%s", not, all.String())
			}
		}
	}
}

// A mention as Slack sends it ("R&D" arrives as "R&amp;D") starts a
// session titled with the words typed, and the reply escapes them once.
func TestChatAMentionWithAnAmpersandIsTitledAndRepliedAsTyped(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": ""}))
	w.mention("Márcia", "C1", "", "look at R&amp;D's build")
	got := w.requests()
	if len(got) != 1 || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	if title := w.one(`SELECT title FROM sessions WHERE id = $1`, got[0].ResultID); title != "look at R&D's build" {
		t.Fatalf("title %q", title)
	}
	bot := w.fake.BotMessages()
	if len(bot) != 1 {
		t.Fatalf("bot messages %+v", bot)
	}
	var blocks []struct {
		Text struct{ Type, Text string } `json:"text"`
	}
	_ = json.Unmarshal(bot[0].Blocks, &blocks)
	if len(blocks) == 0 || blocks[0].Text.Type != "mrkdwn" || !strings.Contains(blocks[0].Text.Text, "“look at R&amp;D's build”") ||
		strings.Contains(string(bot[0].Blocks), "&amp;amp;") || strings.Count(blocks[0].Text.Text, "R&amp;D") != 1 {
		t.Fatalf("reply %s", bot[0].Blocks)
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
