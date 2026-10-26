package chat_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

func TestChatASessionReadsMoreOfItsOwnConversationOnly(t *testing.T) {
	w := setup(t)
	for i := range 40 {
		w.fake.Post("C1", "", "U2", fmt.Sprintf("channel message %d", i))
	}
	w.fake.Post("G1", "", "U1", "a private channel's message")
	text, attachments, blocks := ciReport()
	w.fake.PostApp("C1", "", "BCI", "CI Bot", text, attachments, blocks)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	mention := w.mention("Márcia", "C1", "", "what failed?")
	w.fake.Post("C1", "", "U9", "a message after the mention")
	session := w.requests()[0].ResultID

	latest, err := w.svc.ReadHistory(w.ctx, w.org, session, "", "")
	if err != nil || latest.Scope != "channel" || len(latest.Messages) != chat.HistoryPageMax || !latest.More {
		t.Fatalf("latest page: %+v, %v", latest, err)
	}
	last := latest.Messages[len(latest.Messages)-1]
	if last.Text != "a message after the mention" || last.By != "Zed Outside" || last.InDude {
		t.Fatalf("the newest message %+v", last)
	}
	var sawBot, sawMention bool
	for _, m := range latest.Messages {
		sawBot = sawBot || (m.App && m.By == "CI Bot" && strings.Contains(m.Text, "actions/runs/812"))
		sawMention = sawMention || m.ID == mention
		if strings.Contains(m.Text, "private channel") {
			t.Fatal("another conversation's message was read")
		}
	}
	if !sawBot || !sawMention {
		t.Fatalf("bot %v, mention %v in %+v", sawBot, sawMention, latest.Messages)
	}
	// Paging back from the oldest of the page.
	older, err := w.svc.ReadHistory(w.ctx, w.org, session, latest.Messages[0].ID, "")
	if err != nil || len(older.Messages) == 0 || older.Messages[0].Text != "channel message 0" ||
		older.Messages[len(older.Messages)-1].ID >= latest.Messages[0].ID {
		t.Fatalf("older page: %+v, %v", older, err)
	}
	// And forward.
	newer, err := w.svc.ReadHistory(w.ctx, w.org, session, "", older.Messages[0].ID)
	if err != nil || len(newer.Messages) == 0 || newer.Messages[0].Text != "channel message 1" {
		t.Fatalf("newer page: %+v, %v", newer, err)
	}

	// A thread-bound session reads its thread.
	root := w.fake.Post("C1", "", "U2", "exports time out")
	w.fake.Post("C1", root, "U2", "same here")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", root, "look")
	threadSession := w.requests()[1].ResultID
	thread, err := w.svc.ReadHistory(w.ctx, w.org, threadSession, "", "")
	if err != nil || thread.Scope != "thread" || len(thread.Messages) != 3 || thread.Messages[0].Text != "exports time out" {
		t.Fatalf("thread: %+v, %v", thread, err)
	}
}

func TestChatHistoryIsRefusedOnceTheInstallationIsGone(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t"}))
	w.mention("Márcia", "C1", "", "hello")
	session := w.requests()[0].ResultID
	w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
	if _, err := w.svc.ReadHistory(w.ctx, w.org, session, "", ""); !errors.Is(err, chat.ErrNotInstalled) {
		t.Fatalf("uninstalled: %v", err)
	}
	w.exec(`UPDATE sessions SET origin = NULL WHERE id = $1`, session)
	if _, err := w.svc.ReadHistory(w.ctx, w.org, session, "", ""); !errors.Is(err, chat.ErrNotFromChat) {
		t.Fatalf("not from chat: %v", err)
	}
}
