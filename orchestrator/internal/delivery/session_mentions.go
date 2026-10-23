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
// active members of the organisation are added; an invitation they had not
// answered is accepted as chat (a handover's is left to Accept); anyone
// already in it is left as they are, and an id that names nobody is
// dropped without a word. Returns who was added. The caller checked the
// sender acts as owner and holds the session's lock.
func ShareForMention(ctx context.Context, tx pgx.Tx, ref RunRef, people []string, by, actorType, actorID string) ([]string, error) {
	if len(people) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at)
		SELECT $1, p.id, p.organization_id, 'chat', $3, now() FROM people p
		WHERE p.id = ANY($2) AND p.removed_at IS NULL
		ON CONFLICT (session_id, person_id) DO UPDATE SET accepted_at = now(), role = 'chat'
		WHERE session_people.accepted_at IS NULL AND NOT session_people.becomes_owner
		RETURNING person_id`, ref.SessionID, people, by)
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
// each who may open the session (session_role) and is not the sender gets a
// session_mentions row, which waits on them until they open the session;
// those who do not have it open now are told by push (session.mentioned,
// which the notifier sends). Anyone else named is dropped without a word.
// Returns who was mentioned, in the order named.
func MentionInSession(ctx context.Context, tx pgx.Tx, ref RunRef, named []string, by, text, actorType, actorID string) ([]string, error) {
	if len(named) == 0 {
		return nil, nil
	}
	quote := []rune(text)
	if len(quote) > MentionQuoteMax {
		quote = append(quote[:MentionQuoteMax-1], '…')
	}
	// One statement: who may open it, their rows, and whether each has it
	// open now (a heartbeat within 90 s), in the order named.
	rows, err := tx.Query(ctx, `WITH named AS (
			SELECT id, min(n) AS n FROM unnest($3::text[]) WITH ORDINALITY AS u(id, n) WHERE id <> $4 GROUP BY id),
		ins AS (
			INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text)
			SELECT $1, $2, named.id, $4, $5 FROM named WHERE session_role($2, named.id) IS NOT NULL
			RETURNING person_id)
		SELECT ins.person_id,
			EXISTS (SELECT 1 FROM session_people WHERE session_id = $2 AND person_id = ins.person_id AND open_at > now() - interval '90 seconds')
			OR EXISTS (SELECT 1 FROM session_viewers WHERE session_id = $2 AND person_id = ins.person_id AND open_at > now() - interval '90 seconds')
		FROM ins JOIN named ON named.id = ins.person_id ORDER BY named.n`,
		ref.Org, ref.SessionID, named, by, string(quote))
	if err != nil {
		return nil, err
	}
	type found struct {
		Person string
		Open   bool
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[found])
	if err != nil || len(got) == 0 {
		return nil, err
	}
	byName, err := PersonName(ctx, tx, by)
	if err != nil {
		return nil, err
	}
	var title *string
	if err := tx.QueryRow(ctx, `SELECT title FROM sessions WHERE id = $1`, ref.SessionID).Scan(&title); err != nil {
		return nil, err
	}
	mentioned := make([]string, 0, len(got))
	for _, f := range got {
		mentioned = append(mentioned, f.Person)
		// Looking at it now: no push. The row waits on them only if they
		// leave before their page next says it is open (which marks it seen).
		if f.Open {
			continue
		}
		payload := map[string]any{"person": f.Person, "by": by, "byName": byName, "text": string(quote)}
		if title != nil {
			payload["title"] = *title
		}
		if err := SessionEvent(ctx, tx, ref, EvSessionMentioned, actorType, actorID, payload); err != nil {
			return nil, err
		}
	}
	return mentioned, nil
}
