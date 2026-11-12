package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Supersede: a person confirmed that the live task holding a pull request
// or branch stops for a task of theirs on it. In one transaction the old
// task is stopped as their Stop stops it (its Runs ended, its delivery
// halted, the branch released; its pull request stays open), the new task
// is made (or an existing one not yet started is named) with the source
// attached, and the two are linked (supersedes) with an event on each. The
// holder is read again under the repository's lock: another than the one
// confirmed is refused, and nothing changes. Only a person's request comes
// here; nothing automatic supersedes.

type supersedeBody struct {
	// The holder the person confirmed, by id.
	Holder string          `json:"holder"`
	Source delivery.Source `json:"source"`
	// An existing task not yet started; else Create makes one.
	TaskID string         `json:"taskId"`
	Create *supersedeTask `json:"create"`
}

type supersedeTask struct {
	Title              string   `json:"title"`
	Goal               string   `json:"goal"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	EpicID             string   `json:"epicId"`
	Repositories       []struct {
		ID     string `json:"id"`
		Access string `json:"access"`
	} `json:"repositories"`
	PullRequestChoice *string `json:"pullRequestChoice"`
}

func (s *Server) createSuperseding(w http.ResponseWriter, r *http.Request, org string) error {
	var body supersedeBody
	if err := read(r, &body); err != nil {
		return err
	}
	projectID, src := r.PathValue("id"), body.Source
	if body.Holder == "" || src.RepositoryID == "" || src.HeadBranch == "" || (body.TaskID == "") == (body.Create == nil) {
		return fail(http.StatusBadRequest, "bad_request", "name the holder confirmed, the source, and taskId or create")
	}
	p := principalOf(r)
	if p.Person == "" {
		return fail(http.StatusForbidden, "not_owner", "only a person supersedes a task")
	}
	ctx := r.Context()
	out := map[string]any{}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		holder, err := delivery.HolderUnderLockTx(ctx, tx, src.RepositoryID, src.HeadBranch, body.TaskID, body.Holder)
		if err != nil {
			return err
		}
		if holder == nil {
			return fail(http.StatusConflict, "supersede_stale", "%s is no longer held by a task in progress: start this one without superseding", src.HeadBranch)
		}
		if err := mayDecide(ctx, tx, holder.ID, p); err != nil {
			return err
		}
		taskID := body.TaskID
		if taskID != "" {
			var started bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workflow_runs WHERE task_id = $1)
				FROM tasks WHERE id = $1 AND project_id = $2 FOR UPDATE`, taskID, projectID).Scan(&started); err != nil {
				return fail(http.StatusNotFound, "not_found", "task %s not found in this project", taskID)
			}
			if started {
				return fail(http.StatusConflict, "started", "where a task starts from is fixed once its delivery has started")
			}
		} else {
			c := body.Create
			if taskID, _, err = delivery.CreateTaskTx(ctx, tx, org, projectID, delivery.Filer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person},
				c.EpicID, c.Title, c.Goal, c.AcceptanceCriteria); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET pull_request_choice = $2 WHERE id = $1`, taskID, c.PullRequestChoice); err != nil {
				return err
			}
			for _, repo := range c.Repositories {
				access := repo.Access
				if access == "" {
					access = "write"
				}
				if _, err := tx.Exec(ctx, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
					SELECT $1, $2, r.id, $4::repository_access FROM repositories r WHERE r.id = $3 AND r.project_id = $5`,
					org, taskID, repo.ID, access, projectID); err != nil {
					return err
				}
			}
		}
		if err := delivery.SetSourceTx(ctx, tx, org, taskID, src); err != nil {
			return err
		}
		newKey, err := delivery.TaskKeyOf(ctx, tx, taskID)
		if err != nil {
			return err
		}
		// Stopped as a person's Stop stops it; one not started with no
		// live delivery is only marked so, having nothing running. A
		// queued task can have a live delivery (parked on a blocker added
		// after its start): that one is stopped. A started one whose
		// delivery is not live may still have a Run: stopTaskTx refuses it.
		why := "superseded by " + newKey
		var holderProject, holderStatus string
		if err := tx.QueryRow(ctx, `SELECT project_id, status FROM tasks WHERE id = $1`, holder.ID).Scan(&holderProject, &holderStatus); err != nil {
			return err
		}
		d, err := delivery.ReadDelivery(ctx, tx, holder.ID)
		if err != nil {
			return err
		}
		var runIDs []string
		if delivery.NotStarted(holderStatus) && (d == nil || !d.Live()) {
			if _, err := delivery.SetTaskStatusTx(ctx, tx, org, holderProject, holder.ID, "", "aborted", why); err != nil {
				return err
			}
		} else {
			stopped, _, err := stopTaskTx(ctx, tx, org, holder.ID, why, why, "supersede", p, true)
			if err != nil {
				return err
			}
			runIDs = stopped.All()
		}
		if err := humanEvent(ctx, tx, org, "", runInfo{ProjectID: holderProject, TaskID: holder.ID}, "task.stopped", p,
			map[string]any{"by": p.Person, "note": why, "runIds": db.NonNil(runIDs), "supersededBy": newKey}); err != nil {
			return err
		}
		if s.AfterSupersedeStop != nil {
			if err := s.AfterSupersedeStop(); err != nil {
				return err
			}
		}
		if err := delivery.RecordSupersedeTx(ctx, tx, org, taskID, holder.ID, p.Person, p.ActorType, p.Actor); err != nil {
			return err
		}
		out["taskId"], out["key"], out["superseded"] = taskID, newKey, holder
		return nil
	})
	var refusal delivery.SourceRefusal
	if errors.As(err, &refusal) {
		return sourceFail(refusal)
	}
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.code == "not_in_progress" {
			he.message = strings.Replace(he.message, "only a task in progress can be stopped", "it holds the branch but cannot be stopped now", 1)
		}
		return err
	}
	s.kick()
	write(w, http.StatusCreated, out)
	return nil
}
