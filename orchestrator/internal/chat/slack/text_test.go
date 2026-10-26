package slack_test

import (
	"context"
	"strings"
	"testing"
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
