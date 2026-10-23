package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// setSessionAccess opens the session to its organisation: the owner, or an
// admin while its admins own it. One way: any other access is a 409, and
// so is a session already open (nothing to do is not an error: 200).
func (s *Server) setSessionAccess(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Access string `json:"access"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	id := r.PathValue("id")
	changed := false
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := lockedOwner(r.Context(), tx, id, p.Person, "change who can open it"); err != nil {
			return err
		}
		changed, err = delivery.OpenSession(r.Context(), tx, delivery.SessionRef(org, id), body.Access, p.writer().ActorType, p.Actor)
		return err
	})
	if errors.Is(err, delivery.ErrAccessOneWay) {
		return fail(http.StatusConflict, "one_way", "%s", err.Error())
	}
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id, "access": delivery.AccessOrganisation, "changed": changed})
	return nil
}

// takeSessionOwnership is an admin making themselves the owner of a session
// its admins own (its owner left the organisation): the flag clears and the
// owner before stays as they were, outside the organisation. 409 for a
// session its admins do not own; 404 for anyone who is not an admin.
func (s *Server) takeSessionOwnership(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := delivery.LockSession(r.Context(), tx, id); err != nil {
			return err
		}
		role, err := member(r.Context(), tx, id, p.Person)
		if err != nil {
			return err
		}
		_, byAdmins, err := delivery.SessionAccess(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !byAdmins {
			return fail(http.StatusConflict, "not_admin_owned", "this session has an owner; only they can hand it over")
		}
		if role != delivery.SessionOwner {
			return fail(http.StatusForbidden, "not_admin", "only an organisation admin can take over a session its admins own")
		}
		var from string
		if err := tx.QueryRow(r.Context(), `SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, id).
			Scan(&from); err != nil && !db.IsNotFound(err) {
			return err
		}
		// The owner before left the organisation: their row goes, and the
		// admin's own (if any) becomes the owner.
		if _, err := tx.Exec(r.Context(), `DELETE FROM session_people WHERE session_id = $1 AND role = 'owner'`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at)
			VALUES ($1, $2, $3, 'owner', $2, now())
			ON CONFLICT (session_id, person_id) DO UPDATE SET role = 'owner', accepted_at = COALESCE(session_people.accepted_at, now()),
				becomes_owner = false, handover_keep = NULL`, id, p.Person, org); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE sessions SET owned_by_admins = false, updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		name, err := delivery.PersonName(r.Context(), tx, p.Person)
		if err != nil {
			return err
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionOwnerTaken, p.writer().ActorType, p.Actor,
			map[string]any{"from": db.Nullable(from), "to": p.Person, "toName": name})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// listOpenSessions is every session open to the organisation, each saying
// whether the caller joined it (is one of its accepted people and has not
// archived it: an active one of theirs), joined first, whether its owner
// archived it (the screen's "Archived by owner"), and how many they joined.
// Never a word of the conversation. Only the Open to everyone screen reads
// it; the sidebar's count comes with listSessions.
func (s *Server) listOpenSessions(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var sessions json.RawMessage
	var joined int
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT COALESCE(json_agg(x ORDER BY x.joined DESC, x."lastActivityAt" DESC), '[]'),
				count(*) FILTER (WHERE x.joined) FROM (
			SELECT s.id, s.title, s.owned_by_admins AS "ownedByAdmins",
				(SELECT person_ref(o) FROM session_people so JOIN people o ON o.id = so.person_id
				 WHERE so.session_id = s.id AND so.role = 'owner') AS owner,
				EXISTS (SELECT 1 FROM session_people so WHERE so.session_id = s.id AND so.role = 'owner'
				  AND so.archived_at IS NOT NULL) AS "archivedByOwner",
				EXISTS (SELECT 1 FROM session_people me WHERE me.session_id = s.id AND me.person_id = $1
				  AND me.accepted_at IS NOT NULL AND me.archived_at IS NULL) AS joined,
				(SELECT count(*) FROM session_people sp JOIN people pp ON pp.id = sp.person_id
				 WHERE sp.session_id = s.id AND sp.accepted_at IS NOT NULL AND pp.removed_at IS NULL) AS people,
				`+sessionProjectsJSON+` AS projects,
				(SELECT r.status::text FROM runs r WHERE r.session_id = s.id ORDER BY r.created_at DESC LIMIT 1) AS "runStatus",
				COALESCE((SELECT max(e.occurred_at) FROM events e WHERE e.session_id = s.id), s.created_at) AS "lastActivityAt"
			FROM sessions s WHERE s.access = 'organisation' AND session_role(s.id, $1) IS NOT NULL) x`, p.Person).
			Scan(&sessions, &joined)
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"sessions": sessions, "joined": joined})
	return nil
}
