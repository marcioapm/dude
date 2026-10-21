package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Close, Reopen, Archive and Unarchive (docs/design/tracking-work.md,
// Epic 1), forwarded by the backend like Mark done so the guard and the
// status write stay in this process. A task's owner or an organisation
// admin only; an agent or a service, 403.

func (s *Server) lifecycleRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/tasks/{id}/close", s.auth(s.closeTask))
	mux.Handle("POST /internal/tasks/{id}/reopen", s.auth(s.reopenTask))
	mux.Handle("POST /internal/tasks/{id}/archive", s.auth(s.archiveTask))
	mux.Handle("POST /internal/tasks/{id}/unarchive", s.auth(s.unarchiveTask))
	mux.Handle("POST /internal/epics/{id}/archive", s.auth(s.archiveEpic))
	mux.Handle("POST /internal/epics/{id}/unarchive", s.auth(s.unarchiveEpic))
}

type closeBody struct {
	Resolution       string `json:"resolution"`
	DuplicateOf      string `json:"duplicateOf"`
	Note             string `json:"note"`
	RemoveRelations  bool   `json:"removeRelations"`
	ClosePullRequest bool   `json:"closePullRequest"`
}

func (b closeBody) input(taskID string, a delivery.Actor) delivery.CloseInput {
	return delivery.CloseInput{TaskID: taskID, Resolution: b.Resolution, DuplicateOf: b.DuplicateOf, Note: b.Note,
		RemoveRelations: b.RemoveRelations, ClosePullRequest: b.ClosePullRequest, Actor: a}
}

// lifecycleActor is the person deciding, refused 403 for anyone else.
func lifecycleActor(ctx context.Context, tx pgx.Tx, r *http.Request) (delivery.Actor, error) {
	p := principalOf(r)
	if p.Person == "" {
		return delivery.Actor{}, fail(http.StatusForbidden, "not_owner", "only a person closes or archives work")
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM people WHERE id = $1`, p.Person).Scan(&name); err != nil {
		return delivery.Actor{}, err
	}
	return delivery.Actor{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person, Name: name}, nil
}

// mayDecide passes the task's owner or an admin, in the menu's words.
func mayDecide(ctx context.Context, tx pgx.Tx, taskID string, p principal) error {
	if p.Admin {
		return nil
	}
	ownerID, ownerName, err := taskOwner(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if ownerID != nil && *ownerID == p.Person {
		return nil
	}
	who := "the task's owner"
	if ownerName != nil {
		who = firstWord(*ownerName) + ", the owner,"
	}
	return fail(http.StatusForbidden, "not_owner", "Only %s or an admin can do this", who)
}

func firstWord(s string) string {
	for i, c := range s {
		if c == ' ' {
			return s[:i]
		}
	}
	return s
}

// lifecycleFailure answers a refused lifecycle write with its code.
func lifecycleFailure(err error) error {
	var lr delivery.LifecycleRefusal
	if !errors.As(err, &lr) {
		return err
	}
	status := http.StatusConflict
	switch lr.Code {
	case "bad_request":
		status = http.StatusBadRequest
	case "not_found":
		status = http.StatusNotFound
	}
	he := &httpError{status: status, code: lr.Code, message: lr.Msg}
	if lr.Details != nil {
		he.details = lr.Details
	}
	return he
}

// taskDecision runs a lifecycle write on a task, its owner's or an admin's;
// a Close it made that asked for the task's pull requests closed closes
// them on GitHub once committed.
func (s *Server) taskDecision(w http.ResponseWriter, r *http.Request, org string, do func(tx pgx.Tx, a delivery.Actor) (any, error)) error {
	taskID := r.PathValue("id")
	var out any
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		a, err := lifecycleActor(r.Context(), tx, r)
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1)`, taskID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fail(http.StatusNotFound, "not_found", "task %s not found", taskID)
		}
		if err := mayDecide(r.Context(), tx, taskID, principalOf(r)); err != nil {
			return err
		}
		out, err = do(tx, a)
		return err
	})
	if err != nil {
		return lifecycleFailure(err)
	}
	var closed *delivery.Closed
	switch o := out.(type) {
	case delivery.Closed:
		closed = &o
	case delivery.Archived:
		closed = o.Closed
	}
	if closed != nil && len(closed.PullRequests) > 0 {
		// Each outcome in its own transaction: the close is committed whatever GitHub says.
		s.closeOnForge(r.Context(), org, closed.PullRequests, closed.Comment, true, func(c closedOnForge) {
			if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
				return delivery.RecordPullRequestCloseTx(r.Context(), tx, org, closed.Actor, c.ProjectID, c.TaskID, c.Repo, c.Number, c.Err)
			}); err != nil && s.Log != nil {
				s.Log.Warn("recording a pull request's close failed", "pr", c.Number, "repo", c.Repo, "error", err)
			}
		})
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}

func (s *Server) closeTask(w http.ResponseWriter, r *http.Request, org string) error {
	var body closeBody
	if err := read(r, &body); err != nil {
		return err
	}
	return s.taskDecision(w, r, org, func(tx pgx.Tx, a delivery.Actor) (any, error) {
		return delivery.CloseTx(r.Context(), tx, org, body.input(r.PathValue("id"), a))
	})
}

func (s *Server) reopenTask(w http.ResponseWriter, r *http.Request, org string) error {
	return s.taskDecision(w, r, org, func(tx pgx.Tx, a delivery.Actor) (any, error) {
		id := r.PathValue("id")
		return map[string]any{"taskId": id, "status": "received"}, delivery.ReopenTx(r.Context(), tx, org, id, a)
	})
}

func (s *Server) archiveTask(w http.ResponseWriter, r *http.Request, org string) error {
	var body closeBody
	if err := read(r, &body); err != nil {
		return err
	}
	return s.taskDecision(w, r, org, func(tx pgx.Tx, a delivery.Actor) (any, error) {
		return delivery.ArchiveTaskTx(r.Context(), tx, org, delivery.ArchiveInput{CloseInput: body.input(r.PathValue("id"), a)})
	})
}

func (s *Server) unarchiveTask(w http.ResponseWriter, r *http.Request, org string) error {
	return s.taskDecision(w, r, org, func(tx pgx.Tx, a delivery.Actor) (any, error) {
		id := r.PathValue("id")
		return map[string]any{"taskId": id}, delivery.UnarchiveTaskTx(r.Context(), tx, org, id, a)
	})
}

// archiveEpic archives an epic and its tasks, or with withEndedTasks its
// ended tasks alone while some haven't ended. An admin, or the owner of
// every task it archives.
func (s *Server) archiveEpic(w http.ResponseWriter, r *http.Request, org string) error {
	var body struct {
		WithEndedTasks bool `json:"withEndedTasks"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	epicID := r.PathValue("id")
	var out map[string]any
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		a, err := lifecycleActor(r.Context(), tx, r)
		if err != nil {
			return err
		}
		p := principalOf(r)
		archived, tasks, err := delivery.ArchiveEpicTx(r.Context(), tx, org, epicID, body.WithEndedTasks, a, func(t delivery.EpicTask) error {
			if p.Admin || t.Owner == p.Person {
				return nil
			}
			return fail(http.StatusForbidden, "not_owner", "Only the owner of %s, or an admin, can archive it", t.Key)
		})
		out = map[string]any{"epicId": epicID, "archived": archived, "tasks": db.NonNil(tasks)}
		return err
	})
	if err != nil {
		return lifecycleFailure(err)
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}

func (s *Server) unarchiveEpic(w http.ResponseWriter, r *http.Request, org string) error {
	epicID := r.PathValue("id")
	var out map[string]any
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		a, err := lifecycleActor(r.Context(), tx, r)
		if err != nil {
			return err
		}
		if !principalOf(r).Admin {
			// The tasks it brings back are those the archive took: their
			// owners' (taskOwner's: the first active person) or an admin's.
			var others int
			if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM tasks t JOIN epics e ON e.id = t.epic_id
				WHERE e.id = $1 AND t.archived_at = e.archived_at AND `+delivery.TaskOwnerOf+` IS DISTINCT FROM $2`,
				epicID, a.Person).Scan(&others); err != nil {
				return err
			}
			if others > 0 {
				return fail(http.StatusForbidden, "not_owner", "Only the owners of its tasks, or an admin, can unarchive it")
			}
		}
		tasks, err := delivery.UnarchiveEpicTx(r.Context(), tx, org, epicID, a)
		out = map[string]any{"epicId": epicID, "tasks": db.NonNil(tasks)}
		return err
	})
	if err != nil {
		return lifecycleFailure(err)
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}
