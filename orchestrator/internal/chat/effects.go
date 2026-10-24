package chat

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// effect is one thing dude posts for a request, recorded with the request
// in the transaction that decided it and posted after that commits, in
// order, each marked done once posted: a retry posts only what is left.
type effect struct {
	// reply, ephemeral, question, update (a question's buttons replaced
	// with who chose what) or react.
	Kind      string       `json:"kind"`
	Text      string       `json:"text,omitempty"`
	LinkURL   string       `json:"linkUrl,omitempty"`
	LinkLabel string       `json:"linkLabel,omitempty"`
	Choices   []Choice     `json:"choices,omitempty"`
	Message   string       `json:"message,omitempty"`
	ChosenBy  ExternalUser `json:"chosenBy,omitzero"`
	Reaction  Reaction     `json:"reaction,omitempty"`
	Done      bool         `json:"done,omitempty"`
}

func replyEffect(r Reply) effect {
	return effect{Kind: "reply", Text: r.Text, LinkURL: r.LinkURL, LinkLabel: r.LinkLabel}
}

func ephemeralEffect(r Reply) effect {
	return effect{Kind: "ephemeral", Text: r.Text, LinkURL: r.LinkURL, LinkLabel: r.LinkLabel}
}

func reactEffect(r Reaction) effect { return effect{Kind: "react", Reaction: r} }

// deliver posts what the delivery's request has left to post. A transient
// failure stops it, for the delivery to be tried again; any other is noted
// for the installation's health and skipped, and the mention marked failed
// instead of done.
func (s *Service) deliver(ctx context.Context, org string, p Provider, inst Installation, eventID string) error {
	var (
		id      string
		m       Mention
		effects []effect
		found   bool
	)
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var mRaw, eRaw []byte
		err := tx.QueryRow(ctx, `SELECT id, mention, effects FROM chat_requests WHERE provider = $1 AND event_id = $2 AND replied_at IS NULL`,
			p.Info().ID, eventID).Scan(&id, &mRaw, &eRaw)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if err := json.Unmarshal(mRaw, &m); err != nil {
			return err
		}
		return json.Unmarshal(eRaw, &effects)
	}); err != nil || !found {
		return err
	}
	failed := false
	for i, e := range effects {
		if e.Done {
			continue
		}
		var posted string
		var err error
		switch e.Kind {
		case "reply":
			_, err = p.PostReply(ctx, inst, m.ConversationID, threadOf(m), Reply{Text: e.Text, LinkURL: e.LinkURL, LinkLabel: e.LinkLabel})
		case "ephemeral":
			err = p.PostEphemeral(ctx, inst, m.ConversationID, threadOf(m), m.UserID, Reply{Text: e.Text, LinkURL: e.LinkURL, LinkLabel: e.LinkLabel})
		case "question":
			posted, err = p.PostQuestion(ctx, inst, m.ConversationID, threadOf(m), Question{Text: e.Text, Choices: e.Choices})
		case "update":
			err = p.UpdateQuestion(ctx, inst, m.ConversationID, e.Message, e.ChosenBy, e.Text)
		case "react":
			r := e.Reaction
			if failed && r == ReactionDone {
				r = ReactionFailed
			}
			if m.MessageID != "" {
				err = p.React(ctx, inst, m.ConversationID, m.MessageID, r)
			}
		}
		if err != nil {
			s.noteFailure(ctx, org, m.Provider, err)
			if transient, _ := IsTransient(err); transient {
				return err
			}
			if e.Kind != "react" && e.Kind != "update" {
				failed = true
			}
		}
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			// Done, and stripped of what it said: a Connect link's state is
			// kept only as its hash once it was sent.
			_, err := tx.Exec(ctx, `UPDATE chat_requests SET effects = jsonb_set(effects, ARRAY[$2], jsonb_build_object('kind', $3::text, 'done', true)),
					question_message_id = COALESCE(NULLIF($4, ''), question_message_id) WHERE id = $1`,
				id, strconv.Itoa(i), e.Kind, posted)
			return err
		}); err != nil {
			return err
		}
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE chat_requests SET replied_at = now() WHERE id = $1`, id)
		return err
	})
}
