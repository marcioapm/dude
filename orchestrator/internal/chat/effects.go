package chat

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Why it could not be posted, when the provider refused it.
	Failed string `json:"failed,omitempty"`
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
// for the installation's health and skipped, the asker told privately where
// the provider lets dude, the mention marked failed instead of done, and
// the request recorded as failed.
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
	var failure string
	for i, e := range effects {
		if e.Done {
			if e.Failed != "" {
				failed, failure = true, e.Failed
			}
			continue
		}
		var posted string
		var err error
		switch e.Kind {
		case "reply":
			reply := Reply{Text: e.Text, LinkURL: e.LinkURL, LinkLabel: e.LinkLabel}
			if _, err = p.PostReply(ctx, inst, m.ConversationID, threadOf(m), reply); err != nil {
				if transient, _ := IsTransient(err); !transient {
					// Refused in the thread: the asker still gets the link, privately.
					reply.Text += fmt.Sprintf(" (%s did not take dude's reply in the thread.)", p.Info().Name)
					s.noteFailure(ctx, org, m.Provider, p.PostEphemeral(ctx, inst, m.ConversationID, threadOf(m), m.UserID, reply))
				}
			}
		case "ephemeral":
			err = p.PostEphemeral(ctx, inst, m.ConversationID, threadOf(m), m.UserID, Reply{Text: e.Text, LinkURL: e.LinkURL, LinkLabel: e.LinkLabel})
		case "question":
			if posted, err = p.PostQuestion(ctx, inst, m.ConversationID, threadOf(m), Question{Text: e.Text, Choices: e.Choices}); err != nil {
				if transient, _ := IsTransient(err); !transient {
					s.noteFailure(ctx, org, m.Provider, p.PostEphemeral(ctx, inst, m.ConversationID, threadOf(m), m.UserID,
						Reply{Text: "Sorry, dude couldn't ask its question here: " + e.Text}))
				}
			}
		case "update":
			err = p.UpdateQuestion(ctx, inst, m.ConversationID, e.Message, e.ChosenBy, e.Text)
		case "react":
			r := e.Reaction
			if failed && (r == ReactionDone || r == ReactionCleared) {
				r = ReactionFailed
			}
			if m.MessageID != "" {
				err = p.React(ctx, inst, m.ConversationID, m.MessageID, r)
			}
		}
		var failedHere string
		if err != nil && e.Kind == "react" {
			// A mark is cosmetic: its failure is noted and never retried or
			// counted against the request.
			s.noteFailure(ctx, org, m.Provider, err)
			s.log().Warn("marking a chat mention failed", "provider", m.Provider, "request", id, "error", err)
			err = nil
		}
		if err != nil {
			s.noteFailure(ctx, org, m.Provider, err)
			if transient, _ := IsTransient(err); transient {
				return err
			}
			if e.Kind != "react" && e.Kind != "update" {
				failedHere = clip(err.Error(), 300)
				failed, failure = true, failedHere
			}
		}
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			// Done, and stripped of what it said: a Connect link's state is
			// kept only as its hash once it was sent.
			_, err := tx.Exec(ctx, `UPDATE chat_requests SET effects = jsonb_set(effects, ARRAY[$2],
						jsonb_build_object('kind', $3::text, 'done', true) || CASE WHEN $5 = '' THEN '{}'::jsonb ELSE jsonb_build_object('failed', $5::text) END),
					question_message_id = COALESCE(NULLIF($4, ''), question_message_id) WHERE id = $1`,
				id, strconv.Itoa(i), e.Kind, posted, failedHere)
			return err
		}); err != nil {
			return err
		}
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if failed {
			// What it made stays; Requests says the person was never told.
			_, err := tx.Exec(ctx, `UPDATE chat_requests SET replied_at = now(), route = 'failed',
					asked_route = CASE WHEN route = 'failed' THEN asked_route ELSE COALESCE(asked_route, route) END,
					reasoning = left(btrim(reasoning || ' (its reply was not posted: ' || $2 || ')'), 2000)
				WHERE id = $1`, id, failure)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE chat_requests SET replied_at = now() WHERE id = $1`, id)
		return err
	})
}
