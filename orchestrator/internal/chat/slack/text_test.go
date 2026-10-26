package slack_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

// ciReport is a CI bot's nightly report as such bots post it: a short
// text, the run's link only in an attachment, and a section block that
// repeats the text.
func ciReport() (text string, attachments, blocks []any) {
	text = "Nightly build failed"
	attachments = []any{map[string]any{"pretext": "acme/api nightly", "title": "Run #812 failed",
		"title_link": "https://github.com/acme/api/actions/runs/812", "text": "2 jobs failed: integration, lint",
		"fields": []any{map[string]any{"title": "Branch", "value": "main"}}, "footer": "GitHub Actions"}}
	blocks = []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "Nightly build failed"}},
		map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": "triggered by schedule"}}},
		map[string]any{"type": "rich_text", "elements": []any{map[string]any{"type": "rich_text_section", "elements": []any{
			map[string]any{"type": "text", "text": "logs: "}, map[string]any{"type": "link", "url": "https://ci.example.com/812"}}}}},
		map[string]any{"type": "video", "title": map[string]any{"type": "plain_text", "text": "never read"}},
		map[string]any{"type": "actions", "elements": []any{map[string]any{"type": "button", "text": map[string]any{"type": "plain_text",
			"text": "Open run"}, "url": "https://github.com/acme/api/actions/runs/812"}}},
	}
	return
}

func TestHistoryReadsTheChannelBeforeAMessageWithinItsWindow(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	old := fake.PostAged("C1", "U2", "two days ago", 48*time.Hour)
	first := fake.Post("C1", "", "U2", "first")
	fake.Post("C1", first, "U1", "a reply in its thread")
	second := fake.Post("C1", "", "U1", "second")
	mention := fake.Post("C1", "", "U1", "<@UBT1> look above")
	fake.Post("C1", "", "U2", "after")
	got, err := p.FetchHistory(ctx, inst, "C1", mention, 24*time.Hour, 30)
	if err != nil || len(got) != 2 || got[0].ID != second || got[1].ID != first {
		t.Fatalf("history %+v, %v (old %s)", got, err, old)
	}
	if got, err = p.FetchHistory(ctx, inst, "C1", mention, 24*time.Hour, 1); err != nil || len(got) != 1 || got[0].ID != second {
		t.Fatalf("limited %+v, %v", got, err)
	}
	if _, err := p.FetchHistory(ctx, inst, "C9", mention, time.Hour, 30); err == nil {
		t.Fatal("a conversation that is not there was read")
	}
}

func TestAPageIsTheMessagesNearestItsBoundOldestFirst(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	var ts []string
	for i := range 7 {
		ts = append(ts, fake.Post("C1", "", "U2", fmt.Sprintf("m%d", i)))
	}
	reply := fake.Post("C1", ts[0], "U1", "in the thread")
	texts := func(ms []chat.ThreadMessage) string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Text)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		thread string
		page   chat.Page
		want   string
		more   bool
	}{
		{"", chat.Page{Limit: 3}, "m4,m5,m6", true},
		{"", chat.Page{Before: ts[4], Limit: 3}, "m1,m2,m3", true},
		{"", chat.Page{Before: ts[2], Limit: 3}, "m0,m1", false},
		{"", chat.Page{After: ts[1], Limit: 3}, "m2,m3,m4", true},
		{ts[0], chat.Page{Limit: 30}, "m0,in the thread", false},
		{ts[0], chat.Page{After: ts[0], Limit: 30}, "in the thread", false},
	} {
		got, more, err := p.FetchPage(ctx, inst, "C1", c.thread, c.page)
		if err != nil || texts(got) != c.want || more != c.more {
			t.Errorf("%+v in %q: %q more=%v (%v); want %q more=%v", c.page, c.thread, texts(got), more, err, c.want, c.more)
		}
	}
	_ = reply
}

func TestABotsAttachmentsAndBlocksAreReadOnce(t *testing.T) {
	fake, p, inst := world(t)
	text, attachments, blocks := ciReport()
	root := fake.PostApp("C1", "", "BCI", "CI Bot", text, attachments, blocks)
	fake.Post("C1", root, "U1", "<@UBT1> look")
	got, err := p.FetchThread(context.Background(), inst, "C1", root, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("thread %+v, %v", got, err)
	}
	bot := got[0]
	if !bot.Bot || bot.BotName != "CI Bot" {
		t.Fatalf("the bot's message %+v", bot)
	}
	for _, want := range []string{"Nightly build failed", "acme/api nightly", "Run #812 failed https://github.com/acme/api/actions/runs/812",
		"2 jobs failed: integration, lint", "Branch: main", "GitHub Actions", "triggered by schedule", "logs: <https://ci.example.com/812>"} {
		if !strings.Contains(bot.Text, want) {
			t.Fatalf("the bot's text lacks %q:\n%s", want, bot.Text)
		}
	}
	if n := strings.Count(bot.Text, "Nightly build failed"); n != 1 {
		t.Fatalf("the text repeated by a block appears %d times:\n%s", n, bot.Text)
	}
	if strings.Contains(bot.Text, "never read") {
		t.Fatalf("an unknown block was read:\n%s", bot.Text)
	}
	if got[1].Bot || got[1].UserID != "U1" {
		t.Fatalf("the person's message %+v", got[1])
	}
}
