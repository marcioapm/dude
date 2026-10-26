package slack_test

import (
	"context"
	"errors"
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

// With Slack paging (here 7 messages a call), a page that starts where
// Slack's order starts is one call of Limit+1; one at the far end follows
// the cursor; one past what a read scans is refused, never a page that is
// not next to its bound.
func TestAPageReadsOnlyWhatDecidesItAndRefusesOneTooFar(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	fake.SetPageSize(7)
	var ts []string
	for i := range 60 {
		ts = append(ts, fake.Post("C1", "", "U2", fmt.Sprintf("c%02d", i)))
	}
	root := ts[0]
	var replies []string
	for i := range 60 {
		replies = append(replies, fake.Post("C1", root, "U1", fmt.Sprintf("r%02d", i)))
	}
	ids := func(ms []chat.ThreadMessage) string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Text)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		name, thread string
		page         chat.Page
		first, last  string
		more         bool
		calls        int
		limit        string
	}{
		// Newest first on a channel: the latest page, and before, are its first call.
		{"channel latest", "", chat.Page{Limit: 5}, "c55", "c59", true, 1, "6"},
		{"channel before", "", chat.Page{Before: ts[30], Limit: 5}, "c25", "c29", true, 1, "6"},
		// Oldest first in a thread: after is its first call. Slack puts the
		// root first in it, counted in the limit, so it asks for Limit+2.
		{"thread after", root, chat.Page{After: replies[10], Limit: 5}, "r11", "r15", true, 1, "7"},
		{"thread after, one more left", root, chat.Page{After: replies[53], Limit: 5}, "r54", "r58", true, 1, "7"},
		// The far end: the cursor is followed to the bound (the channel's
		// 19 after c40, the thread's root and 50 replies before r50).
		{"channel after", "", chat.Page{After: ts[40], Limit: 5}, "c41", "c45", true, 3, "200"},
		{"thread before", root, chat.Page{Before: replies[50], Limit: 5}, "r45", "r49", true, 8, "200"},
	} {
		before := fake.Calls("conversations.history") + fake.Calls("conversations.replies")
		got, more, err := p.FetchPage(ctx, inst, "C1", c.thread, c.page)
		calls := fake.Calls("conversations.history") + fake.Calls("conversations.replies") - before
		method := "conversations.history"
		if c.thread != "" {
			method = "conversations.replies"
		}
		args := fake.Args(method)
		if err != nil || len(got) != 5 || got[0].Text != c.first || got[4].Text != c.last || more != c.more || calls != c.calls ||
			args[len(args)-calls]["limit"] != c.limit {
			t.Errorf("%s: %q more=%v (%v), %d calls, limit %s; want %s..%s more=%v, %d calls, limit %s",
				c.name, ids(got), more, err, calls, args[len(args)-calls]["limit"], c.first, c.last, c.more, c.calls, c.limit)
		}
	}
	// The end of the channel: the latest page past the last message is empty, with nothing more.
	if got, more, err := p.FetchPage(ctx, inst, "C1", "", chat.Page{After: ts[59], Limit: 5}); err != nil || len(got) != 0 || more {
		t.Errorf("after the last message: %q more=%v (%v)", ids(got), more, err)
	}
	// Further than a read scans: refused, not a page from elsewhere.
	fake.SetPageSize(0)
	for i := range 1100 {
		fake.Post("C1", "", "U2", fmt.Sprintf("n%04d", i))
	}
	before := fake.Calls("conversations.history")
	if got, _, err := p.FetchPage(ctx, inst, "C1", "", chat.Page{After: ts[2], Limit: 5}); !errors.Is(err, chat.ErrPageTooFar) {
		t.Errorf("a bound 1100 messages from the far end: %q (%v), want ErrPageTooFar", ids(got), err)
	}
	if n := fake.Calls("conversations.history") - before; n != 5 {
		t.Errorf("the scan made %d calls, want 5 of 200", n)
	}
}

// Slack may answer a call with fewer messages than its limit while more
// remain: a page read from its bound is filled by one more call, and when
// even that leaves it short it still says there is more.
func TestAPageSlackAnswersShortStillSaysThereIsMore(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	var ts []string
	for i := range 40 {
		ts = append(ts, fake.Post("C1", "", "U2", fmt.Sprintf("c%02d", i)))
	}
	root := ts[0]
	var replies []string
	for i := range 40 {
		replies = append(replies, fake.Post("C1", root, "U1", fmt.Sprintf("r%02d", i)))
	}
	texts := func(ms []chat.ThreadMessage) string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Text)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		name, thread string
		pageSize     int
		page         chat.Page
		want         string
	}{
		// Four a call: the second call fills the page of five.
		{"channel latest", "", 4, chat.Page{Limit: 5}, "c35,c36,c37,c38,c39"},
		{"channel before", "", 4, chat.Page{Before: ts[20], Limit: 5}, "c15,c16,c17,c18,c19"},
		{"thread after", root, 4, chat.Page{After: replies[10], Limit: 5}, "r11,r12,r13,r14,r15"},
		// Two a call: two calls leave it short, and more remain (a thread's
		// first call spends one on its root).
		{"channel latest, short", "", 2, chat.Page{Limit: 5}, "c36,c37,c38,c39"},
		{"channel before, short", "", 2, chat.Page{Before: ts[20], Limit: 5}, "c16,c17,c18,c19"},
		{"thread after, short", root, 2, chat.Page{After: replies[10], Limit: 5}, "r11,r12,r13"},
	} {
		fake.SetPageSize(c.pageSize)
		before := fake.Calls("conversations.history") + fake.Calls("conversations.replies")
		got, more, err := p.FetchPage(ctx, inst, "C1", c.thread, c.page)
		calls := fake.Calls("conversations.history") + fake.Calls("conversations.replies") - before
		if err != nil || texts(got) != c.want || !more || calls != 2 {
			t.Errorf("%s: %q more=%v (%v), %d calls; want %q more=true, 2 calls", c.name, texts(got), more, err, calls, c.want)
		}
	}
	// A rate-limited second call leaves the first call's page, short, with more.
	fake.SetPageSize(4)
	fake.FailAfter("conversations.history", "ratelimited", 1, 1)
	got, more, err := p.FetchPage(ctx, inst, "C1", "", chat.Page{Limit: 5})
	if err != nil || texts(got) != "c36,c37,c38,c39" || !more {
		t.Errorf("a refill rate-limited: %q more=%v (%v); want the first call's four, more=true, no error", texts(got), more, err)
	}
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
