package agenttools

// reply_in_thread (capabilities.ChatReply): a session started from a chat
// thread posts a short message, as the organisation's installation, in the
// conversation and thread the turn's latest person input came from (its
// chat.message's recorded source), never the agent's argument. A turn
// whose input was typed in dude gets no thread post. Its text was redacted
// with the Run's secret values before the call reached the tool
// (Server.call), and is escaped once when posted.
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
	"time"

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

// broadcast matches every Slack special mention (<!channel>, <!here>,
// <!subteam^…>, …). The text is escaped when posted, so these never reach
// Slack as markup; refusing them tells the agent so.
var broadcast = regexp.MustCompile(`<![^>]*>`)

// latestInput is the session's latest input from a person (a message, or
// an answer to its question): the turn's boundary, and where it was said.
// A chat.message written by the chat path carries its conversation and
// thread (chat.writerFrom); one typed in dude carries none.
const latestInput = `SELECT cursor, occurred_at, payload FROM events WHERE session_id = $1
	AND event_type IN ('chat.message', 'question.answered') AND actor_type IN ('person', 'human')
	ORDER BY cursor DESC LIMIT 1`

// repliesThisTurn counts the session's reply_in_thread attempts after that
// input: cursor > $3 is the exact boundary. occurred_at is stamped by each
// writer's clock, so its range starts a minute before the input's ($2) and
// only narrows the read to the turn along events_session_tool_attempts_idx
// (session_id, occurred_at). "cursor + 0" keeps the planner off cursor's
// own indexes, whose range is every tenant's events since the turn began.
const repliesThisTurn = `SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'agent.tool.attempted'
	AND payload->>'tool' = 'reply_in_thread' AND occurred_at >= $2::timestamptz - interval '1 minute' AND cursor + 0 > $3`

type replyInThreadIn struct {
	Text string `json:"text"`
}

// threadReply is a checked reply and where it goes: the conversation and
// thread of the turn's input.
type threadReply struct {
	text     string
	to       chat.Origin
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

// turnInput is the turn's boundary and, when its input came through the
// chat, where it was said (ok false for one typed in dude, a legacy
// message, or a session no person has written to).
type turnInput struct {
	cursor int64
	at     time.Time
	from   chat.Origin
	ok     bool
}

func readTurnInput(ctx context.Context, tx pgx.Tx, session string) (turnInput, error) {
	var in turnInput
	var raw []byte
	err := tx.QueryRow(ctx, latestInput, session).Scan(&in.cursor, &in.at, &raw)
	if db.IsNotFound(err) {
		return turnInput{}, nil
	}
	if err != nil {
		return turnInput{}, err
	}
	in.ok = json.Unmarshal(raw, &in.from) == nil && in.from.Provider != "" && in.from.WorkspaceID != "" && in.from.ConversationID != ""
	return in, nil
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
	turn, err := readTurnInput(ctx, tx, c.SessionID)
	if err != nil {
		return threadReply{}, err
	}
	if !turn.ok {
		return threadReply{}, replyRefused(o, refuseText(rReplyFromDude).Error())
	}
	to := turn.from
	var made int
	if err := tx.QueryRow(ctx, repliesThisTurn, c.SessionID, turn.at, turn.cursor).Scan(&made); err != nil {
		return threadReply{}, err
	}
	if made >= repliesPerTurn {
		return threadReply{}, replyRefused(to, refuse(rReplyPerTurn, repliesPerTurn).Error())
	}
	inst, installed, err := chat.Installed(ctx, tx, to.Provider)
	if err != nil {
		return threadReply{}, err
	}
	p, registered := c.env.chatProviders.Get(to.Provider)
	if !installed || !registered || inst.WorkspaceID != to.WorkspaceID {
		return threadReply{}, replyRefused(to, refuseText(rReplyNotConnected).Error())
	}
	return threadReply{text: in.Text, to: to, inst: inst, provider: p}, nil
}

// postInThread posts, with no transaction open.
func postInThread(ctx context.Context, c Caller, r threadReply) (threadPosted, error) {
	ts, err := r.provider.PostReply(ctx, r.inst, r.to.ConversationID, r.to.ThreadID, chat.Reply{Text: r.text})
	if err != nil {
		if c.env.db != nil {
			chat.NoteFailure(ctx, c.env.db, nil, c.Org, r.to.Provider, err)
		}
		var transient *chat.TransientError
		switch {
		case revokedCodes[apiCode(err)]:
			return threadPosted{}, replyRefused(r.to, refuseText(rReplyNotConnected).Error())
		case errors.As(err, &transient):
			return threadPosted{}, replyRefused(r.to, refuse(rReplyBusy, err.Error()).Error())
		}
		return threadPosted{}, replyRefused(r.to, refuse(rReplyRefused, err.Error()).Error())
	}
	return threadPosted{ts: ts, permalink: r.provider.Permalink(r.inst, r.to.ConversationID, ts, r.to.ThreadID)}, nil
}

func recordThreadReply(ctx context.Context, tx pgx.Tx, c Caller, r threadReply, done threadPosted) (map[string]any, error) {
	payload := map[string]any{"provider": r.to.Provider, "channel": r.to.ConversationName,
		"conversationId": r.to.ConversationID, "threadId": r.to.ThreadID, "ts": done.ts, "permalink": done.permalink}
	if _, err := ledger.Append(ctx, tx, c.event(EvThreadReplied, payload)); err != nil {
		return nil, err
	}
	return map[string]any{"channel": r.to.ConversationName, "ts": done.ts, "permalink": done.permalink}, nil
}

var replyInThreadTool = around(defineSession("reply_in_thread", checkReply).countingAttempts(), postInThread, recordThreadReply)
