package delivery

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// MentionQuoteMax bounds the words a mention's Waiting row and push quote,
// in characters.
const MentionQuoteMax = 280

// ShareForMention adds people to a session as chat members, accepted, as the
// owner's "Share and send" does with a message that mentions them. Only
// active members of the organisation are added; anyone already in it (or
// invited) is left as they are, and an id that names nobody is dropped
// without a word. Returns who was added. The caller checked the sender acts
// as owner and holds the session's lock.
func ShareForMention(ctx context.Context, tx pgx.Tx, ref RunRef, people []string, by, actorType, actorID string) ([]string, error) {
	if len(people) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at)
		SELECT $1, p.id, p.organization_id, 'chat', $3, now() FROM people p
		WHERE p.id = ANY($2) AND p.removed_at IS NULL
		ON CONFLICT (session_id, person_id) DO NOTHING RETURNING person_id`, ref.SessionID, people, by)
	if err != nil {
		return nil, err
	}
	added, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(added) == 0 {
		return added, err
	}
	slices.Sort(added)
	return added, SessionEvent(ctx, tx, ref, EvSessionShared, actorType, actorID,
		map[string]any{"people": added, "role": SessionChat, "accepted": true, "forMention": true})
}

// MentionInSession records that a message mentioned people: of those named,
// each
// who may open the session (session_role) and is not the sender gets a
// session_mentions row, which waits on them until they open the session;
// and those who do not have it open now are told by push
// (session.mentioned, which the notifier sends). Anyone else named is
// dropped without a word. Returns who was mentioned, in the order named.
func MentionInSession(ctx context.Context, tx pgx.Tx, ref RunRef, named []string, by, text, actorType, actorID string) ([]string, error) {
	var mentioned []string
	for _, id := range named {
		if id == by || slices.Contains(mentioned, id) {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT session_role($1, $2) IS NOT NULL`, ref.SessionID, id).Scan(&ok); err != nil {
			return nil, err
		}
		if ok {
			mentioned = append(mentioned, id)
		}
	}
	if len(mentioned) == 0 {
		return nil, nil
	}
	quote := []rune(text)
	if len(quote) > MentionQuoteMax {
		quote = append(quote[:MentionQuoteMax-1], '…')
	}
	byName, err := PersonName(ctx, tx, by)
	if err != nil {
		return nil, err
	}
	var title *string
	if err := tx.QueryRow(ctx, `SELECT title FROM sessions WHERE id = $1`, ref.SessionID).Scan(&title); err != nil {
		return nil, err
	}
	for _, id := range mentioned {
		var open bool
		if err := tx.QueryRow(ctx, `INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING EXISTS (SELECT 1 FROM session_people WHERE session_id = $2 AND person_id = $3 AND open_at > now() - interval '90 seconds')
			       OR EXISTS (SELECT 1 FROM session_viewers WHERE session_id = $2 AND person_id = $3 AND open_at > now() - interval '90 seconds')`,
			ref.Org, ref.SessionID, id, by, string(quote)).Scan(&open); err != nil {
			return nil, err
		}
		// Looking at it now: no push. The row waits on them only if they
		// leave before their page next says it is open (which marks it seen).
		if open {
			continue
		}
		payload := map[string]any{"person": id, "by": by, "byName": byName, "text": string(quote)}
		if title != nil {
			payload["title"] = *title
		}
		if err := SessionEvent(ctx, tx, ref, EvSessionMentioned, actorType, actorID, payload); err != nil {
			return nil, err
		}
	}
	return mentioned, nil
}
