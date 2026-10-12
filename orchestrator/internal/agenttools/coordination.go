package agenttools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The coordination tools (docs/design/agent-coordination.md): message a
// peer, wait for its reply, pin a note on the epic. Peers are task_peers'
// (delivery/peers.go); delivery and waiting reuse the steer's directives
// and the ask's wait (delivery/coordination.go, notes.go).

// Who may write to peers and pin notes: the roles that build or plan.
// Reviewers, the simplifier and QA read their peers, never write: a
// reviewer that messages would be deciding. A fixer runs as implementer.
var coordinators = []string{"implementer", "investigator", "conductor"}

var coordinationTools = []tool{
	define("message", messageDescription, coordinators, message),
	define("wait_for_reply", "End your turn until the reply to one of your messages comes. Use it only when the next thing "+
		"you must do depends on that answer and nothing else is left that doesn't. Your Run parks if no reply comes within "+
		"the project's grace (10 minutes by default); the reply resumes it. If the reply has already come, it is returned "+
		"at once and you carry on.", coordinators, waitForReply),
	define("note", "Pin a note on your epic: a short working fact every agent on the epic must follow — a contract, a "+
		"decision, where the docs are. Every agent on the epic is told at its next step, and every Run that starts there "+
		"reads it in its briefing, reviewers included. Notes are never edited: replaces N pins a new note in note N's place. "+
		"At most 10 current notes, at most 2,000 characters each; a replace never counts against the limit.",
		coordinators, note),
}

const messageDescription = "Send a short message to one of your peers (a task on your epic), delivered at its agent's " +
	"next step. Use it only when you need a decision or a fact that only that task's agent has. First look: list_tasks " +
	"with peers: true shows each peer's phase, branch, files and last words; run_diff with task reads its changes; the " +
	"epic notes in your briefing are what is settled. This never blocks: it returns at once and the reply lands at your " +
	"next step. Ask as early as you know you need the answer, then keep working on whatever doesn't depend on it. Call " +
	"wait_for_reply only at the point where you can't go on without the answer; a turn that ends with your question " +
	"unanswered waits on it too. If you go ahead on an assumption instead, reply to your own question saying what you " +
	"assumed. Never use it to report progress or to tell every task something: pin a note instead. A Run may send at " +
	"most 5 messages an hour."

// ---- message -------------------------------------------------------------------

type messageIn struct {
	To      string `json:"to" jsonschema:"the peer task's key, like ABS-2: one task, never a Run"`
	Text    string `json:"text" jsonschema:"what you need from it, or your answer"`
	ReplyTo string `json:"replyTo,omitempty" jsonschema:"the message this answers (pm_…); your own message, to close your question with what you assumed"`
}

func message(ctx context.Context, tx pgx.Tx, c Caller, in messageIn) (delivery.Sent, error) {
	key := strings.TrimSpace(in.To)
	if key == "" || strings.ContainsAny(key, ",*") || strings.EqualFold(key, "all") || strings.EqualFold(key, "epic") {
		return delivery.Sent{}, refuse("a message goes to one peer, by its key: to tell every task on the epic, pin a note")
	}
	peer, ok, err := delivery.PeerByKey(ctx, tx, c.TaskID, key)
	if err != nil {
		return delivery.Sent{}, err
	}
	if !ok {
		return delivery.Sent{}, refuseRecorded(delivery.EvMessageRefused, map[string]any{"reason": "not_a_peer", "to": key,
			"retry_at": nil}, delivery.NotAPeer(key))
	}
	sent, err := delivery.SendMessageTx(ctx, tx, c.run(), delivery.MessageInput{To: peer, Text: in.Text,
		ReplyTo: strings.TrimSpace(in.ReplyTo)})
	var refused delivery.RefusedMessage
	if errors.As(err, &refused) {
		var retry any
		if refused.RetryAt != nil {
			retry = refused.RetryAt.Format(time.RFC3339)
		}
		return sent, refuseRecorded(delivery.EvMessageRefused, map[string]any{"reason": refused.Reason, "to": peer.Key,
			"retry_at": retry}, refused.Msg)
	}
	return sent, err
}

// ---- wait_for_reply ------------------------------------------------------------

type waitIn struct {
	Message string `json:"message" jsonschema:"one of your own messages (pm_…) whose reply you cannot go on without"`
}

type waitOut struct {
	// The reply, when it had already come: carry on with it.
	Reply *replyOut `json:"reply,omitempty"`
	// Otherwise the wait: end your turn now.
	Waiting *delivery.Waiting `json:"waiting,omitempty"`
	Next    string            `json:"next"`
}

type replyOut struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

func waitForReply(ctx context.Context, tx pgx.Tx, c Caller, in waitIn) (waitOut, error) {
	id := strings.TrimSpace(in.Message)
	var fromRun, toTask, toKey string
	err := tx.QueryRow(ctx, `SELECT m.from_run_id, m.to_task_id, p.key_prefix || '-' || t.number FROM peer_messages m
		JOIN tasks t ON t.id = m.to_task_id JOIN projects p ON p.id = t.project_id WHERE m.id = $1`, id).Scan(&fromRun, &toTask, &toKey)
	if db.IsNotFound(err) || err == nil && fromRun != c.RunID {
		return waitOut{}, refuse("%s is not a message you sent: wait only on the reply to your own", id)
	}
	if err != nil {
		return waitOut{}, err
	}
	var r replyOut
	var self bool
	err = tx.QueryRow(ctx, `SELECT m.id, p.key_prefix || '-' || t.number, m.text, m.created_at, m.from_task_id <> $2
		FROM peer_messages m JOIN tasks t ON t.id = m.from_task_id JOIN projects p ON p.id = t.project_id
		WHERE m.reply_to = $1 ORDER BY m.created_at LIMIT 1`, id, toTask).Scan(&r.ID, &r.From, &r.Text, &r.At, &self)
	switch {
	case err == nil && self:
		return waitOut{}, refuse("you closed %s yourself, going ahead on what you assumed: there is nothing to wait for", id)
	case err == nil:
		return waitOut{Reply: &r, Next: "The reply had already come: carry on with it."}, nil
	case !db.IsNotFound(err):
		return waitOut{}, err
	}
	w, err := conducted(delivery.OpenWaitTx(ctx, tx, c.run(), id, "wait"))
	if err != nil {
		return waitOut{}, err
	}
	if w.Notice != "" {
		return waitOut{Next: w.Notice}, nil
	}
	return waitOut{Waiting: &w, Next: "End your turn now. " + toKey + "'s reply resumes you as your next message; if none " +
		"comes within the project's grace, your Run is parked until it does."}, nil
}

// ---- note ----------------------------------------------------------------------

type noteIn struct {
	Text     string `json:"text" jsonschema:"the note: a short working fact every agent on the epic must follow (at most 2,000 characters)"`
	Replaces int    `json:"replaces,omitempty" jsonschema:"the number of the current note this one replaces"`
}

func note(ctx context.Context, tx pgx.Tx, c Caller, in noteIn) (delivery.Pinned, error) {
	epic, err := delivery.TaskEpic(ctx, tx, c.TaskID)
	if err != nil {
		return delivery.Pinned{}, err
	}
	if epic == "" {
		return delivery.Pinned{}, refuse("your task is on no epic: there is nowhere to pin a note")
	}
	if in.Replaces < 0 {
		return delivery.Pinned{}, refuse("replaces is a note's number")
	}
	pinned, err := delivery.PinNoteTx(ctx, tx, c.Org, delivery.PinInput{EpicID: epic, Text: in.Text, Replaces: in.Replaces, Run: c.run()})
	var refused delivery.NoteRefusal
	if errors.As(err, &refused) {
		if refused.Reason == "limit" {
			return pinned, refuseRecorded(delivery.EvNoteRefused, map[string]any{"reason": "limit", "current": refused.Current,
				"epicId": epic}, refused.Msg)
		}
		return pinned, refuse("%s", refused.Msg)
	}
	return pinned, err
}
