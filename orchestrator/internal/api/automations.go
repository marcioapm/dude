package api

// Automations' endpoints that need GitHub, lux or the orchestrator's
// matcher; the settings and the run lists are the backend's
// (routes/automations.ts). The backend checks who may run an automation
// before it asks.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/automations"
	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// eventSubscriptions is the events this organisation's GitHub sends dude,
// for the event picker, and where that was read.
func (s *Server) eventSubscriptions(w http.ResponseWriter, r *http.Request, org string) error {
	sub, err := automations.ReadSubscriptions(r.Context(), s.DB, s.Forges, org, time.Now())
	if err != nil {
		return err
	}
	write(w, http.StatusOK, sub)
	return nil
}

type testEvent struct {
	Event      string          `json:"event"`
	Action     *string         `json:"action"`
	DeliveryID string          `json:"deliveryId"`
	Body       json.RawMessage `json:"body"`
}

// runNow fires an automation for a person: {} on a schedule, {deliveryId}
// or {testEvent} on GitHub events. An event its filters would skip is
// refused (422) naming the filter.
func (s *Server) automationRunNow(w http.ResponseWriter, r *http.Request, org string) error {
	if s.Automations == nil {
		return fail(http.StatusServiceUnavailable, "unavailable", "automations are not running here")
	}
	var body struct {
		DeliveryID string     `json:"deliveryId"`
		TestEvent  *testEvent `json:"testEvent"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var test *automations.Payload
	if body.TestEvent != nil {
		var obj map[string]any
		if json.Unmarshal(body.TestEvent.Body, &obj) != nil || obj == nil {
			return fail(http.StatusBadRequest, "bad_request", "a test event's body is a JSON object")
		}
		test = &automations.Payload{DeliveryID: body.TestEvent.DeliveryID, Event: body.TestEvent.Event, Body: body.TestEvent.Body}
	}
	runID, verdict, err := s.Automations.RunNow(r.Context(), org, r.PathValue("id"), principalOf(r).Person, body.DeliveryID, test)
	switch {
	case errors.Is(err, automations.ErrBadRequest):
		return fail(http.StatusBadRequest, "bad_request", "%s", err.Error())
	case errors.Is(err, automations.ErrNotFound), db.IsNotFound(err):
		return fail(http.StatusNotFound, "not_found", "%s", err.Error())
	case err != nil:
		return err
	case !verdict.Matches:
		write(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "filtered",
			"message": "it would be skipped because of " + verdict.Filter + ": " + verdict.Because}, "verdict": verdict})
		return nil
	}
	s.kick()
	write(w, http.StatusAccepted, map[string]string{"runId": runID})
	return nil
}

// evaluateTestEvent checks a written event against an automation's filters,
// the way a live delivery is matched: {matches} or {matches:false, filter, because}.
func (s *Server) evaluateTestEvent(w http.ResponseWriter, r *http.Request, org string) error {
	if s.Automations == nil {
		return fail(http.StatusServiceUnavailable, "unavailable", "automations are not running here")
	}
	var body struct {
		Body json.RawMessage `json:"body"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var obj map[string]any
	if json.Unmarshal(body.Body, &obj) != nil || obj == nil {
		return fail(http.StatusBadRequest, "bad_request", "a test event's body is a JSON object")
	}
	var verdict automations.Verdict
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		a, err := automations.Load(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if a.Trigger.Kind != "github" {
			return fail(http.StatusBadRequest, "bad_request", "a scheduled automation takes no event")
		}
		verdict, err = s.Automations.Evaluate(r.Context(), tx, a, automations.ReadEvent(a.Trigger.Event, body.Body))
		return err
	})
	if db.IsNotFound(err) {
		return fail(http.StatusNotFound, "not_found", "no automation %s", r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	write(w, http.StatusOK, verdict)
	return nil
}

// startFresh lets go of a resumable automation's kept session now.
func (s *Server) automationStartFresh(w http.ResponseWriter, r *http.Request, org string) error {
	if err := automations.StartFresh(r.Context(), s.DB, s.Lux, org, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
