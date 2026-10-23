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
// its admins own (its owner left the organisation): the flag clears, the
// departed owner's row goes, and the agent is told who owns it now. 409 for
// a session its admins do not own; 403 for someone with a role who is not
// an admin; 404 for someone with none.
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
		// The owner row is the departed owner's; one still in the
		// organisation is never removed here. FOR SHARE waits for an
		// invitation's accept bringing them back, then reads them as active.
		var from string
		var gone bool
		if err := tx.QueryRow(r.Context(), `SELECT sp.person_id, p.removed_at IS NOT NULL OR p.organization_id <> sp.organization_id
			FROM session_people sp JOIN people p ON p.id = sp.person_id WHERE sp.session_id = $1 AND sp.role = 'owner'
			FOR SHARE OF p`, id).
			Scan(&from, &gone); err != nil && !db.IsNotFound(err) {
			return err
		}
		if from != "" && !gone {
			return fail(http.StatusConflict, "owner_active", "this session's owner is still in the organisation")
		}
		if ok, err := activePerson(r.Context(), tx, p.Person); err != nil {
			return err
		} else if !ok {
			return fail(http.StatusForbidden, "not_admin", "only an organisation admin can take over a session its admins own")
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM session_people WHERE session_id = $1 AND role = 'owner'`, id); err != nil {
			return err
		}
		// A handover the departed owner offered is void with them.
		if _, err := tx.Exec(r.Context(), `UPDATE session_people SET becomes_owner = false, handover_keep = NULL
			WHERE session_id = $1 AND becomes_owner AND person_id <> $2`, id, p.Person); err != nil {
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
		// What was put to the departed owner by name nobody else may answer.
		if from != "" {
			if err := delivery.WithdrawQuestionsTo(r.Context(), tx, org, id, from); err != nil {
				return err
			}
		}
		name, err := delivery.PersonName(r.Context(), tx, p.Person)
		if err != nil {
			return err
		}
		ref := delivery.SessionRef(org, id)
		if err := delivery.SessionEvent(r.Context(), tx, ref, delivery.EvSessionOwnerTaken, p.writer().ActorType, p.Actor,
			map[string]any{"from": db.Nullable(from), "to": p.Person, "toName": name}); err != nil {
			return err
		}
		fromName := ""
		if from != "" {
			if fromName, err = delivery.PersonName(r.Context(), tx, from); err != nil {
				return err
			}
		}
		return delivery.TellBrainstorm(r.Context(), tx, org, id, sessionOwnerTold.Render(struct {
			To, From   string
			Read, Left bool
		}{name, fromName, false, true}))
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

var (
	sessionOwnerLeftTold     = delivery.MustText("notices/told/session-owner-left")
	sessionOwnerReturnedTold = delivery.MustText("notices/told/session-owner-returned")
)

// personSessions is the backend telling the orchestrator, after it
// committed, that a person left the organisation ("left") or came back
// ("returned"), with the sessions that changed hands (`sessions`). Its own
// call, not a person's: a principal naming one is refused.
//
// left: each listed session still owned by its admins with them as owner of
// record has its live agent told; then every open question put to them by
// name in any session is withdrawn (delivery.WithdrawQuestionsTo), so no
// session waits on someone who can no longer answer. returned: each listed
// session they own again has its agent told. One transaction per session,
// under its lock; a session that changed since is skipped.
func (s *Server) personSessions(w http.ResponseWriter, r *http.Request, org string) error {
	if p := principalOf(r); p.Person != "" {
		return fail(http.StatusForbidden, "not_system", "only the backend tells the orchestrator who left")
	}
	var body struct {
		Change   string   `json:"change"`
		Sessions []string `json:"sessions"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if body.Change != "left" && body.Change != "returned" {
		return fail(http.StatusBadRequest, "bad_request", "change is left or returned")
	}
	person := r.PathValue("person")
	ctx := r.Context()
	var removed *bool
	var name string
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT removed_at IS NOT NULL, name FROM people WHERE id = $1`, person).Scan(&removed, &name)
	}); err != nil && !db.IsNotFound(err) {
		return err
	}
	if removed == nil {
		return fail(http.StatusNotFound, "not_found", "person %s not found", person)
	}
	if *removed != (body.Change == "left") {
		return fail(http.StatusConflict, "conflict", "%s is not %s", person, body.Change)
	}
	told := []string{}
	for _, id := range body.Sessions {
		ok := false
		err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			if err := delivery.LockSession(ctx, tx, id); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions s JOIN session_people sp ON sp.session_id = s.id
				WHERE s.id = $1 AND sp.person_id = $2 AND sp.role = 'owner' AND s.owned_by_admins = $3)`,
				id, person, body.Change == "left").Scan(&ok); err != nil || !ok {
				return err
			}
			text := sessionOwnerReturnedTold.Render(name)
			if body.Change == "left" {
				text = sessionOwnerLeftTold.Render(name)
			}
			return delivery.TellBrainstorm(ctx, tx, org, id, text)
		})
		if err != nil {
			return err
		}
		if ok {
			told = append(told, id)
		}
	}
	withdrawn := []string{}
	if body.Change == "left" {
		var asked []string
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT DISTINCT r.session_id FROM questions q JOIN runs r ON r.id = q.run_id
				WHERE q.to_person = $1 AND q.status = 'open' AND r.session_id IS NOT NULL ORDER BY 1`, person)
			if err != nil {
				return err
			}
			asked, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		}); err != nil {
			return err
		}
		for _, id := range asked {
			if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
				if err := delivery.LockSession(ctx, tx, id); err != nil {
					return err
				}
				return delivery.WithdrawQuestionsTo(ctx, tx, org, id, person)
			}); err != nil {
				return err
			}
			withdrawn = append(withdrawn, id)
		}
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"told": told, "withdrawn": withdrawn})
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
