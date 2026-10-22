package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A reviewer's question (review_questions) is the task owner's to answer or
// withdraw; the conductor does both with answer_review_question, through
// the same delivery functions.
func (s *Server) reviewQuestionRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/review-questions/{id}/answer", s.auth(s.settleReviewQuestion(false)))
	mux.Handle("POST /internal/review-questions/{id}/withdraw", s.auth(s.settleReviewQuestion(true)))
}

func (s *Server) settleReviewQuestion(withdraw bool) handler {
	return func(w http.ResponseWriter, r *http.Request, org string) error {
		id := r.PathValue("id")
		var body struct {
			Answer string `json:"answer"`
			Reason string `json:"reason"`
		}
		if err := read(r, &body); err != nil {
			return err
		}
		p := principalOf(r)
		var out delivery.SettledReviewQuestion
		err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
			// A question's task never changes: read unlocked, for the owner.
			var taskID string
			if err := tx.QueryRow(r.Context(), `SELECT task_id FROM review_questions WHERE id = $1`, id).Scan(&taskID); err != nil {
				if db.IsNotFound(err) {
					return fail(http.StatusNotFound, "not_found", "reviewer question %s not found", id)
				}
				return err
			}
			if err := ownerOnly(r.Context(), tx, taskID, p.Person, "answer"); err != nil {
				return err
			}
			by := delivery.ReviewQuestionBy{Person: p.Person, ActorType: p.ActorType, ActorID: p.Actor}
			var err error
			if withdraw {
				out, err = delivery.WithdrawReviewQuestionTx(r.Context(), tx, org, id, body.Reason, by)
			} else {
				out, err = delivery.AnswerReviewQuestionTx(r.Context(), tx, org, id, body.Answer, by)
			}
			return reviewQuestionFailure(err)
		})
		if err != nil {
			return err
		}
		s.kick()
		write(w, http.StatusOK, map[string]any{"id": out.ID, "taskId": out.TaskID, "status": out.Status})
		return nil
	}
}

// reviewQuestionFailure answers a refused answer or withdrawal: 404, 409 or 400.
func reviewQuestionFailure(err error) error {
	var e delivery.ReviewQuestionError
	if !errors.As(err, &e) {
		return err
	}
	status := map[string]int{"not_found": http.StatusNotFound, "conflict": http.StatusConflict}[e.Kind]
	if status == 0 {
		status = http.StatusBadRequest
	}
	return fail(status, e.Kind, "%s", e.Msg)
}
