package agenttools

// reply_in_thread (capabilities.ChatReply): a session started from a chat
// thread posts a short message in that thread, as the organisation's
// installation. The destination is the session's origin, never the agent's
// argument. Its text was redacted with the Run's secret values before the
// call reached the tool (Server.call).
//
// Three steps, as post_to_chat's: checkReply, in the call's transaction,
// reads the origin and the installation, checks the text and the turn's
// budget and writes the attempt; postInThread calls the provider with no
// transaction open; recordThreadReply records the post.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

const (
	// repliesPerTurn bounds a session agent's thread posts after one
	// person's input.
	repliesPerTurn = 3
	// replyTextMax is one thread post's length, in characters: what one
	// Block Kit section takes.
	replyTextMax = blockMax
	// EvThreadReplied records a post the session's agent made in its thread.
	EvThreadReplied = "chat.reply.posted"
	// EvThreadReplyRefused records a thread post refused after its checks
	// began: by dude (budget, text, no installation) or by the provider.
	EvThreadReplyRefused = "chat.reply.refused"
)

// broadcast matches the provider markup that notifies a whole conversation.
var broadcast = regexp.MustCompile(`<!(channel|here|everyone)(\|[^>]*)?>`)

// repliesThisTurn counts the session's reply_in_thread attempts after the
// latest input a person gave it (a message, or an answer to its question),
// both read along events_session_cursor_idx.
const repliesThisTurn = `SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'agent.tool.attempted'
	AND payload->>'tool' = 'reply_in_thread'
	AND cursor > COALESCE((SELECT max(cursor) FROM events WHERE session_id = $1
		AND event_type IN ('chat.message', 'question.answered') AND actor_type IN ('person', 'human')), 0)`

type replyInThreadIn struct {
	Text string `json:"text"`
}

// threadReply is a checked reply and where it goes.
type threadReply struct {
	text     string
	origin   chat.Origin
	inst     chat.Installation
	provider chat.Provider
}

type threadPosted struct{ ts, permalink string }

// replyRefused is a refusal recorded on the session as EvThreadReplyRefused.
func replyRefused(o chat.Origin, msg string) error {
	return refuseRecorded(EvThreadReplyRefused, map[string]any{"tool": "reply_in_thread", "provider": o.Provider,
		"channel": o.ConversationName, "conversationId": o.ConversationID, "error": msg}, msg)
}

// sessionOrigin is the chat thread a session was started from; false for
// a session no chat thread started.
func sessionOrigin(ctx context.Context, tx pgx.Tx, session string) (chat.Origin, bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT origin FROM sessions WHERE id = $1`, session).Scan(&raw)
	if db.IsNotFound(err) || err == nil && raw == nil {
		return chat.Origin{}, false, nil
	}
	if err != nil {
		return chat.Origin{}, false, err
	}
	var o chat.Origin
	if json.Unmarshal(raw, &o) != nil || o.Provider == "" || o.ConversationID == "" {
		return chat.Origin{}, false, nil
	}
	return o, true, nil
}

func checkReply(ctx context.Context, tx pgx.Tx, c Caller, in replyInThreadIn) (threadReply, error) {
	o, ok, err := sessionOrigin(ctx, tx, c.SessionID)
	if err != nil {
		return threadReply{}, err
	}
	if !ok {
		return threadReply{}, refuseText(rReplyNotFromChat)
	}
	if err := textWithin(in.Text, replyTextMax, rReplyTextLength); err != nil {
		return threadReply{}, replyRefused(o, err.Error())
	}
	if m := broadcast.FindString(in.Text); m != "" {
		return threadReply{}, replyRefused(o, refuse(rReplyBroadcast, m).Error())
	}
	// Serialises the session's replies: two calls at once cannot both take
	// the turn's last post.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('thread_replies:' || $1))`, c.SessionID); err != nil {
		return threadReply{}, err
	}
	var made int
	if err := tx.QueryRow(ctx, repliesThisTurn, c.SessionID).Scan(&made); err != nil {
		return threadReply{}, err
	}
	if made >= repliesPerTurn {
		return threadReply{}, replyRefused(o, refuse(rReplyPerTurn, repliesPerTurn).Error())
	}
	inst, installed, err := chat.Installed(ctx, tx, o.Provider)
	if err != nil {
		return threadReply{}, err
	}
	p, registered := c.env.chatProviders.Get(o.Provider)
	if !installed || !registered || inst.WorkspaceID != o.WorkspaceID {
		return threadReply{}, replyRefused(o, refuseText(rReplyNotConnected).Error())
	}
	return threadReply{text: in.Text, origin: o, inst: inst, provider: p}, nil
}

// replyThread is the thread a reply goes in: the origin's, or none for a
// direct message's top level, where dude's own reply went too.
func replyThread(o chat.Origin) string {
	if o.ConversationKind == chat.KindDM && o.TopLevel {
		return ""
	}
	return o.ThreadID
}

// postInThread posts, with no transaction open.
func postInThread(ctx context.Context, c Caller, r threadReply) (threadPosted, error) {
	thread := replyThread(r.origin)
	ts, err := r.provider.PostReply(ctx, r.inst, r.origin.ConversationID, thread, chat.Reply{Text: r.text, Markup: true})
	if err != nil {
		if c.env.db != nil {
			chat.NoteFailure(ctx, c.env.db, nil, c.Org, r.origin.Provider, err)
		}
		var transient *chat.TransientError
		switch {
		case revokedCodes[apiCode(err)]:
			return threadPosted{}, replyRefused(r.origin, refuseText(rReplyNotConnected).Error())
		case errors.As(err, &transient):
			return threadPosted{}, replyRefused(r.origin, refuse(rReplyBusy, err.Error()).Error())
		}
		return threadPosted{}, replyRefused(r.origin, refuse(rReplyRefused, err.Error()).Error())
	}
	return threadPosted{ts: ts, permalink: r.provider.Permalink(r.inst, r.origin.ConversationID, ts, thread)}, nil
}

func recordThreadReply(ctx context.Context, tx pgx.Tx, c Caller, r threadReply, done threadPosted) (map[string]any, error) {
	payload := map[string]any{"provider": r.origin.Provider, "channel": r.origin.ConversationName,
		"conversationId": r.origin.ConversationID, "threadId": replyThread(r.origin), "ts": done.ts, "permalink": done.permalink}
	if _, err := ledger.Append(ctx, tx, c.event(EvThreadReplied, payload)); err != nil {
		return nil, err
	}
	return map[string]any{"channel": r.origin.ConversationName, "ts": done.ts, "permalink": done.permalink}, nil
}

var replyInThreadTool = around(defineSession("reply_in_thread", checkReply).countingAttempts(), postInThread, recordThreadReply)
