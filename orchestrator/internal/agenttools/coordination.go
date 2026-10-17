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
	define("message", coordinators, message),
	define("wait_for_reply", coordinators, waitForReply),
	define("note", coordinators, note),
}

// ---- message -------------------------------------------------------------------

type messageIn struct {
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"replyTo,omitempty"`
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
	Message string `json:"message"`
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
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR KEY SHARE`, c.RunID); err != nil {
		return waitOut{}, err
	}
	var fromRun, toTask, toKey string
	err := tx.QueryRow(ctx, `SELECT m.from_run_id, m.to_task_id, p.key_prefix || '-' || t.number FROM peer_messages m
		JOIN tasks t ON t.id = m.to_task_id JOIN projects p ON p.id = t.project_id WHERE m.id = $1 FOR NO KEY UPDATE OF m`, id).Scan(&fromRun, &toTask, &toKey)
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
		return waitOut{Reply: &r, Next: replyCameNext}, nil
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
	return waitOut{Waiting: &w, Next: waitNext(toKey)}, nil
}

// waitNext is wait_for_reply's answer when it waits on peer's reply.
func waitNext(peer string) string { return waitResult.Render(peer) }

// ---- note ----------------------------------------------------------------------

type noteIn struct {
	Text     string `json:"text"`
	Replaces int    `json:"replaces,omitempty"`
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
