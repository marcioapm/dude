package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A person's feedback, once submitted: what the backend forwards, since
// each changes what agents do. Drafting stays in the backend.

func (s *Server) feedbackRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/tasks/{id}/feedback/submit", s.auth(s.submitFeedback))
	mux.Handle("POST /internal/feedback/{id}/reply", s.auth(s.settleFeedback("reply")))
	mux.Handle("POST /internal/feedback/{id}/resolve", s.auth(s.settleFeedback("resolve")))
	mux.Handle("POST /internal/feedback/{id}/reopen", s.auth(s.settleFeedback("reopen")))
}

// feedbackFailure answers a refused feedback request with its code.
func feedbackFailure(err error) error {
	var fe delivery.FeedbackError
	if !errors.As(err, &fe) {
		return err
	}
	status := map[string]int{"bad_request": http.StatusBadRequest, "not_found": http.StatusNotFound,
		"forbidden": http.StatusForbidden}[fe.Kind]
	if status == 0 {
		status = http.StatusConflict
	}
	return fail(status, fe.Kind, "%s", fe.Msg)
}

func (s *Server) submitFeedback(w http.ResponseWriter, r *http.Request, org string) error {
	var body struct {
		DraftIDs      []string `json:"draftIds"`
		SubmissionKey string   `json:"submissionKey"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	p := principalOf(r)
	var out delivery.Submitted
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) (err error) {
		out, err = delivery.SubmitFeedbackTx(r.Context(), tx, s.Workflow, delivery.Submission{
			Org: org, TaskID: r.PathValue("id"), Person: p.Person, Key: body.SubmissionKey, DraftIDs: body.DraftIDs,
			Writer: delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person}})
		return err
	})
	if err != nil {
		return feedbackFailure(err)
	}
	s.kick()
	status := http.StatusCreated
	if out.Repeated {
		status = http.StatusOK
	}
	write(w, status, out)
	return nil
}

func (s *Server) settleFeedback(action string) handler {
	return func(w http.ResponseWriter, r *http.Request, org string) error {
		var body struct {
			Body string `json:"body"`
			Note string `json:"note"`
		}
		if err := read(r, &body); err != nil {
			return err
		}
		p := principalOf(r)
		in := delivery.PersonAction{Org: org, ItemID: r.PathValue("id"), Person: p.Person, Body: body.Body,
			Writer: delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person}}
		if action == "resolve" {
			in.Body = body.Note
		}
		out := map[string]any{"itemId": in.ItemID, "action": action}
		err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
			switch action {
			case "reply":
				return delivery.PersonReplyTx(r.Context(), tx, in)
			case "resolve":
				return delivery.PersonResolveTx(r.Context(), tx, in)
			}
			routed, err := delivery.PersonReopenTx(r.Context(), tx, s.Workflow, in)
			out["routedTo"], out["runId"], out["waiting"] = routed.RoutedTo, routed.RunID, routed.Waiting
			return err
		})
		if err != nil {
			return feedbackFailure(err)
		}
		s.kick()
		write(w, http.StatusOK, out)
		return nil
	}
}
