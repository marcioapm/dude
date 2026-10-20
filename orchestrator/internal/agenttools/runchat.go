package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// run_chat (capabilities.RunsRead) reads another Run's conversation as its
// Chat shows it — what its agent said, and what people, the conductor and
// GitHub told it (agent.message, chat.message, run.steered,
// question.answered) — and its open question. A task's agent reads its own
// task's Runs, every attempt and child; a session's agent the Runs its
// session started. What people wrote is quoted as external text.

const (
	runChatDefault = 30
	runChatMax     = 100
)

// The events a Run's Chat shows as its conversation, in apps/web's
// conversation.ts terms: the agent's messages (but one with no words, no
// turn in its Chat either), and the person's, the conductor's and GitHub's
// messages to it. The WHERE of events_run_chat_idx (migration 165), written
// with literals, as a parameter would not prove the index's WHERE.
const (
	runChatShown = `run_id = $1 AND event_type IN ('agent.message', 'chat.message', 'run.steered', 'question.answered')
		AND (event_type <> 'agent.message' OR btrim(COALESCE(payload->>'text', '')) <> '')`
	runChatCount = `SELECT count(*) FROM events WHERE ` + runChatShown
)

type runChatIn struct {
	Run   string `json:"run"`
	Limit int    `json:"limit,omitempty"`
}

type chatRunOut struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Phase   string `json:"phase,omitempty"`
	Status  string `json:"status"`
	Attempt int    `json:"attempt,omitempty"`
}

type chatMessageOut struct {
	// "agent" (the Run's own), "person", "conductor" or "github".
	From string `json:"from"`
	// Who, for a person or GitHub: their name or login, quoted.
	Who string `json:"who,omitempty"`
	// message, steer (a message sent while it worked) or answer (to its question).
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

type chatChoiceOut struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type chatQuestionOut struct {
	Header   string          `json:"header,omitempty"`
	Question string          `json:"question"`
	Choices  []chatChoiceOut `json:"choices,omitempty"`
	Multiple bool            `json:"multiple,omitempty"`
}

type openQuestionOut struct {
	ID        string            `json:"id"`
	Prompt    string            `json:"prompt"`
	Questions []chatQuestionOut `json:"questions"`
	// Who answers it: one member, by name, or the task's or session's people.
	For     string    `json:"for"`
	AskedAt time.Time `json:"askedAt"`
}

// runChatScope says the Run is one the caller may read: a session agent's,
// one its session started; any other's, one of its task's.
func runChatScope(ctx context.Context, tx pgx.Tx, c Caller, runID string) (chatRunOut, bool, error) {
	var r chatRunOut
	err := tx.QueryRow(ctx, `SELECT r.id, COALESCE(r.role::text, ''), COALESCE(r.phase::text, ''), r.status::text, COALESCE(r.attempt, 0)
		FROM runs r WHERE r.id = $1 AND r.kind = 'agent'
		  AND CASE WHEN $4 THEN $2 <> '' AND r.source_kind = 'session' AND r.parent_session_id = $2
		           ELSE $3 <> '' AND r.task_id = $3 END`, runID, c.SessionID, c.TaskID, sessionAgent(c.Role)).
		Scan(&r.ID, &r.Role, &r.Phase, &r.Status, &r.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func runChat(ctx context.Context, tx pgx.Tx, c Caller, in runChatIn) (json.RawMessage, error) {
	runID := strings.TrimSpace(in.Run)
	limit := in.Limit
	switch {
	case runID == "":
		return nil, refuseText(rNameRunWhoseChatRead)
	case limit < 0 || limit > runChatMax:
		return nil, refuse(rLimit1Messages, runChatMax)
	case limit == 0:
		limit = runChatDefault
	}
	run, ok, err := runChatScope(ctx, tx, c, runID)
	if err != nil {
		return nil, err
	}
	if !ok {
		// The same words for a Run that is not there and another's: which
		// Runs exist elsewhere is not the caller's to learn.
		if sessionAgent(c.Role) {
			return nil, refuse(rNotRunThisSessionStarted, runID)
		}
		return nil, refuse(rNotRunYourTaskYouRead, runID)
	}
	// The latest limit, oldest first.
	var total int
	if err := tx.QueryRow(ctx, runChatCount, runID).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.event_type, e.actor_type, COALESCE(e.actor_id, ''),
			COALESCE((SELECT p.name FROM people p WHERE p.id = e.actor_id),
				(SELECT p.name FROM api_keys k JOIN people p ON p.id = k.person_id WHERE k.id = e.actor_id), ''),
			e.payload, e.occurred_at
		FROM (SELECT * FROM events WHERE `+runChatShown+` ORDER BY cursor DESC LIMIT $2) e ORDER BY e.cursor`, runID, limit)
	if err != nil {
		return nil, err
	}
	w := newWrapper()
	messages, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (chatMessageOut, error) {
		var typ, actorType, actorID, name string
		var payload map[string]any
		var at time.Time
		if err := r.Scan(&typ, &actorType, &actorID, &name, &payload, &at); err != nil {
			return chatMessageOut{}, err
		}
		return w.chatMessage(typ, actorType, actorID, name, payload, at), nil
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"external": runChatNote(w.id), "run": run, "messages": messages, "total": total}
	if total > len(messages) {
		out["more"] = runChatText("more", struct{ Shown, Total int }{len(messages), total})
	}
	q, err := w.openQuestion(ctx, tx, c, runID)
	if err != nil {
		return nil, err
	}
	if q != nil {
		out["openQuestion"] = q
	}
	// Within the GitHub reads' result budget, by leaving out the oldest
	// messages first and saying so.
	for {
		raw, err := delivery.GitHubReadJSON(out)
		switch {
		case err != nil:
			return nil, err
		case len(raw) <= resultCap:
			return raw, nil
		case len(messages) <= 1:
			return w.fit(out)
		}
		messages = messages[1:]
		out["messages"] = messages
		out["more"] = runChatText("more", struct{ Shown, Total int }{len(messages), total})
	}
}

// chatMessage is one Chat turn: the agent's own words cut to textCap, and
// anyone else's quoted too, but for the conductor's, which an agent wrote.
func (w wrapper) chatMessage(typ, actorType, actorID, name string, p map[string]any, at time.Time) chatMessageOut {
	text, _ := p["text"].(string)
	m := chatMessageOut{Kind: "message", At: at}
	if typ == delivery.EvRunSteered {
		m.Kind = "steer"
	}
	gh, _ := p["github"].(map[string]any)
	login, _ := gh["login"].(string)
	switch {
	case typ == "agent.message":
		m.From, m.Text = "agent", w.own(text)
		return m
	case p["by"] == delivery.RoleConductor:
		m.From, m.Text = "conductor", w.own(text)
		return m
	case typ == delivery.EvChatMessage && actorType == ledger.ActorAgent:
		// The Run's own reply on GitHub (reply_on_pull_request, by "fixer").
		m.From, m.Text = "agent", w.own(text)
		return m
	case typ == "question.answered":
		m.Kind = "answer"
		answer, _ := p["answer"].(string)
		if note, _ := p["note"].(string); strings.TrimSpace(note) != "" {
			answer = strings.TrimSpace(answer + "\n\n" + note)
		}
		text = answer
	}
	m.From = "person"
	if login != "" || strings.HasPrefix(actorID, "github:") {
		m.From = "github"
		name = login
		if name == "" {
			name = strings.TrimPrefix(actorID, "github:")
		}
	}
	m.Who, m.Text = w.text(name, 100), w.text(text, textCap)
	return m
}

// own is an agent's text cut to textCap, saying so, and not quoted.
func (w wrapper) own(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= textCap {
		return s
	}
	kept := cutBytes(s, textCap)
	return kept + githubText("text-cut", struct {
		Cut         string
		Kept, Total int
	}{cutMark, len(kept), len(s)})
}

// openQuestion is the Run's question waiting for an answer; nil for none.
func (w wrapper) openQuestion(ctx context.Context, tx pgx.Tx, c Caller, runID string) (*openQuestionOut, error) {
	var q openQuestionOut
	var to string
	var items []delivery.QuestionItem
	err := tx.QueryRow(ctx, `SELECT q.id, q.prompt, q.items, COALESCE((SELECT p.name FROM people p WHERE p.id = q.to_person), ''), q.asked_at
		FROM questions q WHERE q.run_id = $1 AND q.status = 'open' ORDER BY q.asked_at DESC LIMIT 1`, runID).
		Scan(&q.ID, &q.Prompt, &items, &to, &q.AskedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.Prompt = w.own(q.Prompt)
	q.Questions = []chatQuestionOut{}
	for _, it := range items {
		cq := chatQuestionOut{Header: w.own(it.Header), Question: w.own(it.Question), Multiple: it.Multiple}
		for _, ch := range it.Choices {
			cq.Choices = append(cq.Choices, chatChoiceOut{Label: w.own(ch.Label), Description: w.own(ch.Description)})
		}
		q.Questions = append(q.Questions, cq)
	}
	q.For = runChatText("for", struct{ To string }{w.text(to, 100)})
	if to == "" && sessionAgent(c.Role) {
		q.For = runChatText("for-session", nil)
	}
	return &q, nil
}

// runChatNote is what a run_chat result says of its quotes under id.
func runChatNote(id string) string { return runChatText("external", delivery.ExternalText(id, "…")) }

// runChatText renders results/run-chat.md's text name.
func runChatText(name string, data any) string { return runChatTexts[name].Render(data) }

var runChatTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"external", "more", "for", "for-session"} {
		out[name] = delivery.MustText("results/run-chat/" + name)
	}
	return out
}()
