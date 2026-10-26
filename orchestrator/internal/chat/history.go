package chat

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// A session started from chat reads more of the conversation it came from:
// its thread, or for a top-level mention the channel. Only that one
// conversation, named by the session's origin, never by the caller.

// HistoryPageMax is how many messages one read returns at most.
const HistoryPageMax = 30

var (
	// ErrNotFromChat is a session that no chat started.
	ErrNotFromChat = errors.New("this session was not started from chat")
	// ErrNotInstalled is a session whose chat installation is gone (removed,
	// revoked, or another workspace's now).
	ErrNotInstalled = errors.New("the chat this session came from is no longer installed")
	// ErrPageTooFar is a page whose bound is further from where the
	// provider lists from than a read scans: a nearer message is needed.
	ErrPageTooFar = errors.New("that message is too far back or ahead to page to from here")
)

// HistoryMessage is one message read, quoted: its id (to page from), its
// author as people read them, and its text.
type HistoryMessage struct {
	ID     string `json:"id"`
	By     string `json:"by"`
	InDude bool   `json:"inDude"`
	App    bool   `json:"app,omitempty"`
	Text   string `json:"text"`
}

// History is a page of a session's conversation, oldest first.
type History struct {
	Provider, Conversation string
	// "thread" or "channel".
	Scope    string
	Messages []HistoryMessage
	More     bool
}

// ReadHistory reads a page of the conversation session came from: its
// thread when it was made in one, else the channel's top level. Before and
// After are message ids (one of them, or neither for the latest page).
func (s *Service) ReadHistory(ctx context.Context, org, session, before, after string) (History, error) {
	var origin Origin
	var inst Installation
	var installed bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT origin FROM sessions WHERE id = $1`, session).Scan(&raw); err != nil {
			return err
		}
		if raw == nil {
			return ErrNotFromChat
		}
		if err := json.Unmarshal(raw, &origin); err != nil || origin.Provider == "" || origin.ConversationID == "" {
			return ErrNotFromChat
		}
		var err error
		inst, installed, err = loadInstallation(ctx, tx, origin.Provider)
		return err
	}); err != nil {
		if db.IsNotFound(err) {
			return History{}, ErrNotFromChat
		}
		return History{}, err
	}
	if !installed || inst.WorkspaceID != origin.WorkspaceID {
		return History{}, ErrNotInstalled
	}
	p, err := s.provider(origin.Provider)
	if err != nil {
		return History{}, ErrNotInstalled
	}
	h := History{Provider: p.Info().Name, Conversation: origin.ConversationName, Scope: "channel"}
	thread := ""
	if !origin.TopLevel && origin.ThreadID != "" {
		thread, h.Scope = origin.ThreadID, "thread"
	}
	msgs, more, err := p.FetchPage(ctx, inst, origin.ConversationID, thread, Page{Before: before, After: after, Limit: HistoryPageMax})
	if errors.Is(err, ErrPageTooFar) {
		return History{}, err
	}
	if err != nil {
		s.noteFailure(ctx, org, origin.Provider, err)
		return History{}, err
	}
	quoted, err := s.quoteMessages(ctx, org, p, inst, msgs, "")
	if err != nil {
		return History{}, err
	}
	h.More = more
	for _, q := range quoted {
		h.Messages = append(h.Messages, HistoryMessage{ID: q.ID, By: q.By, InDude: q.InDude, App: q.App, Text: q.Text})
	}
	return h, nil
}
