package agenttools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// chat_history (capabilities.ChatRead): a session started from chat reads
// more of the conversation it came from, a page at a time. The
// conversation is the session's origin's; the agent names only where to
// page from. Each read is checked in the call's transaction (the session's
// hourly reads), made with no transaction open, and recorded as a
// chat.read event with what was asked and how many messages came back,
// never their text. What people wrote is quoted as external text.

// EvChatRead is one read chat_history made.
const EvChatRead = "chat.read"

// chatReadsPerHour bounds a session's chat_history reads.
const chatReadsPerHour = 30

// chatReadsInHour counts a session's chat_history attempts of the last
// hour, along events_session_tool_attempts_idx (migration 197).
const chatReadsInHour = `SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'agent.tool.attempted'
	AND payload->>'tool' = 'chat_history' AND occurred_at > now() - interval '1 hour'`

// ChatReader reads a page of the conversation a session came from
// (chat.Service.ReadHistory).
type ChatReader interface {
	ReadHistory(ctx context.Context, org, session, before, after string) (chat.History, error)
}

type chatHistoryIn struct {
	// A message id from an earlier page.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type chatRead struct {
	Session, Before, After string
}

func checkChatRead(ctx context.Context, tx pgx.Tx, c Caller, in chatHistoryIn) (chatRead, error) {
	before, after := strings.TrimSpace(in.Before), strings.TrimSpace(in.After)
	if before != "" && after != "" {
		return chatRead{}, refuseText(rChatHistoryBeforeOrAfter)
	}
	var fromChat bool
	if err := tx.QueryRow(ctx, `SELECT origin->>'provider' IS NOT NULL FROM sessions WHERE id = $1`, c.SessionID).Scan(&fromChat); err != nil {
		return chatRead{}, err
	}
	if !fromChat {
		return chatRead{}, refuseText(rChatHistoryNotFromChat)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('chat_reads:' || $1))`, c.SessionID); err != nil {
		return chatRead{}, err
	}
	var reads int
	if err := tx.QueryRow(ctx, chatReadsInHour, c.SessionID).Scan(&reads); err != nil {
		return chatRead{}, err
	}
	if reads >= chatReadsPerHour {
		return chatRead{}, refuse(rChatHistoryAtMostReadsHour, chatReadsPerHour)
	}
	return chatRead{Session: c.SessionID, Before: before, After: after}, nil
}

func readChat(ctx context.Context, c Caller, read chatRead) (chat.History, error) {
	if c.env.chat == nil {
		return chat.History{}, refuseText(rChatHistoryNotInstalled)
	}
	h, err := c.env.chat.ReadHistory(ctx, c.Org, read.Session, read.Before, read.After)
	switch {
	case errors.Is(err, chat.ErrNotFromChat):
		return h, refuseText(rChatHistoryNotFromChat)
	case errors.Is(err, chat.ErrNotInstalled):
		return h, refuseText(rChatHistoryNotInstalled)
	case err != nil:
		return h, refuse(rChatHistoryUnavailable)
	}
	return h, nil
}

type chatMessageQuoted struct {
	ID   string `json:"id"`
	By   string `json:"by"`
	Text string `json:"text"`
}

func recordChatRead(ctx context.Context, tx pgx.Tx, c Caller, read chatRead, h chat.History) (map[string]any, error) {
	w := newWrapper()
	msgs := []chatMessageQuoted{}
	for _, m := range h.Messages {
		by := m.By
		switch {
		case m.App:
			by += " (an app)"
		case !m.InDude:
			by += " (not in dude)"
		}
		msgs = append(msgs, chatMessageQuoted{ID: m.ID, By: w.text(by, 200), Text: w.text(m.Text, 4000)})
	}
	payload := map[string]any{"tool": "chat_history", "scope": h.Scope, "messages": len(msgs)}
	if read.Before != "" {
		payload["before"] = read.Before
	}
	if read.After != "" {
		payload["after"] = read.After
	}
	if _, err := ledger.Append(ctx, tx, c.event(EvChatRead, payload)); err != nil {
		return nil, err
	}
	out := map[string]any{"external": chatReadNote(w.id, h.Provider), "from": fmt.Sprintf("%s, %s", h.Conversation, h.Scope),
		"messages": msgs, "more": h.More}
	return out, nil
}

// chatReadNote is what a chat_history result says of its quotes.
func chatReadNote(id, provider string) string {
	return chatHistoryText.Render(struct{ Provider, Quote string }{provider, delivery.ExternalText(id, "…")})
}

var chatHistoryText = delivery.MustText("results/chat-history/external")

var chatHistoryTool = around(defineSession("chat_history", checkChatRead).countingAttempts().withholdingResult(), readChat, recordChatRead)
