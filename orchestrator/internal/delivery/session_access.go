package delivery

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// A session's general access (sessions.access): its people alone, or every
// active member of its organisation, who may then chat in it. Who has which
// role under it is session_role (migration 181), which SessionRole reads.
const (
	AccessPrivate      = "private"
	AccessOrganisation = "organisation"
)

// Ledger events of a session's access and ownership, and of a mention.
const (
	EvSessionAccess     = "session.access_changed"
	EvSessionOwnerLeft  = "session.owner_left"
	EvSessionOwnerTaken = "session.owner_taken"
	EvSessionMentioned  = "session.mentioned"
)

// JoinedVia is session.joined's via when someone joined an organisation
// session by writing in it.
const JoinedVia = "organisation"

// ErrAccessOneWay refuses any access but organisation: a session is opened
// to its organisation once and for good (the database refuses it too).
var ErrAccessOneWay = errors.New("a session can only be opened to its organisation; that cannot be undone")

// SessionAccess is the session's access and whether its admins own it.
func SessionAccess(ctx context.Context, tx pgx.Tx, sessionID string) (access string, ownedByAdmins bool, err error) {
	err = tx.QueryRow(ctx, `SELECT access, owned_by_admins FROM sessions WHERE id = $1`, sessionID).Scan(&access, &ownedByAdmins)
	return access, ownedByAdmins, err
}

// OpenSession opens the session to its organisation: true when it changed,
// false when it already was. Any other access is ErrAccessOneWay. The caller
// checked the person acts as its owner.
func OpenSession(ctx context.Context, tx pgx.Tx, ref RunRef, access, actorType, actorID string) (bool, error) {
	if access != AccessOrganisation {
		return false, ErrAccessOneWay
	}
	tag, err := tx.Exec(ctx, `UPDATE sessions SET access = 'organisation', updated_at = now() WHERE id = $1 AND access <> 'organisation'`,
		ref.SessionID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	return true, SessionEvent(ctx, tx, ref, EvSessionAccess, actorType, actorID, map[string]any{"access": AccessOrganisation})
}

// JoinByWriting makes person one of an organisation session's people, as
// chat, when they write in it without being one: their first message is
// their joining (session.joined, via organisation). An invitation they had
// not answered is accepted as it stood (a handover's is left to Accept); a
// member is left as they are. Nothing for a private session. Under the
// session's lock.
func JoinByWriting(ctx context.Context, tx pgx.Tx, ref RunRef, person, actorType, actorID string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		SELECT s.id, $2, s.organization_id, 'chat', now() FROM sessions s
		WHERE s.id = $1 AND s.access = 'organisation'
		ON CONFLICT (session_id, person_id) DO UPDATE SET accepted_at = now()
		WHERE session_people.accepted_at IS NULL AND NOT session_people.becomes_owner`, ref.SessionID, person)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return SessionEvent(ctx, tx, ref, EvSessionJoined, actorType, actorID, map[string]any{"person": person, "via": JoinedVia})
}
