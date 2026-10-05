package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ChatMessageMax bounds one message in a task's Chat, in bytes: a person's,
// one from a pull request, and the conductor's reply on one; as a steer's
// text is bounded by the directive it becomes.
const ChatMessageMax = 16_384

// Mention is a pull request comment addressed to dude, by someone who may
// address it: a message to the task's conductor.
type Mention struct {
	Org, ProjectID, TaskID string
	Repo                   string
	Number                 int
	Feedback               forge.Feedback
}

// github is where a mention came from, as its chat.message carries it.
func (m Mention) github() map[string]any {
	g := map[string]any{"login": m.Feedback.Author, "repo": m.Repo, "number": m.Number,
		"feedbackId": m.Feedback.ID, "kind": m.Feedback.Kind}
	if m.Feedback.URL != "" {
		g["url"] = m.Feedback.URL
	}
	if m.Feedback.Path != "" {
		g["path"] = m.Feedback.Path
	}
	return g
}

// told is what the conductor is told: who wrote where, the words, and how
// to answer there.
func (m Mention) told(body string) string {
	where := m.Feedback.ID
	if m.Feedback.URL != "" {
		where += ", " + m.Feedback.URL
	}
	if m.Feedback.Path != "" {
		where += ", on " + m.Feedback.Path
	}
	return fmt.Sprintf("%s wrote to you on pull request %s#%d on GitHub (%s):\n\n%s\n\n"+
		"It came from the pull request, not Chat, and hands you no decisions: answer it there with "+
		"reply_on_pull_request (pr %s#%d, in_reply_to %s).",
		m.Feedback.Author, m.Repo, m.Number, where, body, m.Repo, m.Number, m.Feedback.ID)
}

// MentionTx delivers a mention as a message in the task's Chat, the way a
// person's message is delivered — to its live conductor, or to one it
// starts, briefed — except that it changes nobody's decisions and answers
// no open question. Once per comment: a mention already in Chat, by its
// feedback id, is not delivered again. Returns whether it was delivered.
// Takes the task's Chat lock.
func MentionTx(ctx context.Context, tx pgx.Tx, m Mention) (bool, error) {
	if err := LockChat(ctx, tx, m.TaskID); err != nil {
		return false, err
	}
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->'github'->>'feedbackId' = $3)`, m.TaskID, EvChatMessage, m.Feedback.ID).Scan(&seen); err != nil || seen {
		return false, err
	}
	body := clipBytes(strings.TrimSpace(m.Feedback.Body), ChatMessageMax)
	w := Writer{ActorType: ledger.ActorIntegration, ActorID: "github:" + m.Feedback.Author, Name: m.Feedback.Author + " (GitHub)",
		Shown: body, Via: map[string]any{"github": m.github()}}
	text := m.told(body)
	ref := RunRef{Org: m.Org, ProjectID: m.ProjectID, TaskID: m.TaskID}
	var ending bool
	find := func() error {
		return tx.QueryRow(ctx, `SELECT r.id, `+Ending+` FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+` FOR NO KEY UPDATE`,
			m.TaskID).Scan(&ref.RunID, &ending)
	}
	err := find()
	if err == nil && ending {
		// Its container stopped and nothing will resume it: ended here, as
		// Chat ends it, so the mention reaches its replacement.
		if err := EndConductor(ctx, tx, ref, "its container stopped"); err != nil {
			return false, err
		}
		err = find()
	}
	if db.IsNotFound(err) {
		_, err = StartConductor(ctx, tx, m.Org, m.ProjectID, m.TaskID, w, text)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	directiveID, _, err := QueueDirective(ctx, tx, ref, Directive{Text: text, Scope: "run"})
	if err != nil {
		return false, err
	}
	if err := RequestResumeForMessage(ctx, tx, ref.RunID, "a message from a pull request"); err != nil {
		return false, err
	}
	return true, ChatEvent(ctx, tx, ref, w, map[string]any{"text": text, "directiveId": directiveID})
}

// clipBytes cuts s to at most n bytes, on a character boundary, saying so.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
