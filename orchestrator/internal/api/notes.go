package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A person's epic notes (docs/design/agent-coordination.md): pinning,
// replacing and retiring one tells every live agent on the epic, so the
// orchestrator writes them; the control plane reads them itself. People
// never write in a thread between agents: there is no route for it.
func (s *Server) noteRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/epics/{id}/notes", s.auth(s.pinNote))
	mux.Handle("POST /internal/epics/{id}/notes/{n}/retire", s.auth(s.retireNote))
}

// notePerson is the person a note is by: a request with none (the service
// alone) may not pin or retire.
func notePerson(r *http.Request) (principal, error) {
	p := principalOf(r)
	if p.Person == "" {
		return p, fail(http.StatusForbidden, "not_a_person", "a note is pinned or retired by a person")
	}
	return p, nil
}

// noteRefused is a refused note as the API answers it: 409 past the limit
// or on a note no longer current, 404 for no such note, 422 otherwise.
func noteRefused(err error) error {
	var refused delivery.NoteRefusal
	if !errors.As(err, &refused) {
		return err
	}
	switch refused.Reason {
	case "limit", "replaced", "retired":
		return fail(http.StatusConflict, "note_"+refused.Reason, "%s", refused.Msg)
	case "no_note":
		return fail(http.StatusNotFound, "not_found", "%s", refused.Msg)
	}
	return fail(http.StatusUnprocessableEntity, "invalid", "%s", refused.Msg)
}

func (s *Server) pinNote(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := notePerson(r)
	if err != nil {
		return err
	}
	var body struct {
		Text     string `json:"text"`
		Replaces int    `json:"replaces"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if body.Replaces < 0 {
		return fail(http.StatusBadRequest, "bad_request", "replaces is a note's number")
	}
	epic := r.PathValue("id")
	var out delivery.Pinned
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		out, err = delivery.PinNoteTx(r.Context(), tx, org, delivery.PinInput{EpicID: epic, Text: body.Text, Replaces: body.Replaces,
			Person: p.Person, ActorType: p.ActorType, Actor: p.Actor})
		if db.IsNotFound(err) {
			return fail(http.StatusNotFound, "not_found", "epic %s not found", epic)
		}
		return err
	})
	var refused delivery.NoteRefusal
	if errors.As(err, &refused) && refused.Reason == "limit" {
		// Recorded as the person's request, in a transaction of its own:
		// the pin's rolled back.
		if logged := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
			return delivery.NoteRefusedTx(r.Context(), tx, org, epic, delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor}, refused)
		}); logged != nil {
			s.Log.Error("recording a refused note failed", "epic", epic, "error", logged)
		}
	}
	if err != nil {
		return noteRefused(err)
	}
	s.kick()
	write(w, http.StatusCreated, out)
	return nil
}

func (s *Server) retireNote(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := notePerson(r)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return fail(http.StatusNotFound, "not_found", "no note %s", r.PathValue("n"))
	}
	epic := r.PathValue("id")
	var out delivery.Note
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		out, err = delivery.RetireNoteTx(r.Context(), tx, org, epic, n, p.Person,
			delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor})
		if db.IsNotFound(err) {
			return fail(http.StatusNotFound, "not_found", "epic %s not found", epic)
		}
		return noteRefused(err)
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}
