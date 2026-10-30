package servers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunningRun is one live Run of a project that serves something: a task's
// (its agent at work or its preview) or a branch preview's.
type RunningRun struct {
	ID string `json:"id"`
	// "task" for a task's Run, "preview" for a branch preview (no task).
	Kind      string          `json:"kind"`
	Task      *RunningTask    `json:"task"`
	Branch    *string         `json:"branch"`
	State     string          `json:"state"`
	StartedAt *time.Time      `json:"startedAt"`
	StartedBy *PersonRef      `json:"startedBy"`
	Servers   []RunningServer `json:"servers"`
}

type RunningTask struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

// RunningServer is one server as the project's Servers page lists it: a
// projection of lux's object, never the whole of it (no env, no command).
type RunningServer struct {
	Name        string     `json:"name"`
	Port        int        `json:"port"`
	URL         *string    `json:"url"`
	State       string     `json:"state"`
	ServerState *string    `json:"serverState,omitempty"`
	Since       *time.Time `json:"since"`
	ReadySince  *time.Time `json:"readySince"`
	Commenting  any        `json:"commenting,omitempty"`
}

// runningLookups bounds the lux reads one listing makes at once.
const runningLookups = 8

// Running lists every live Run of a project with at least one server, every
// person's: task Runs (the agent at work, a task's preview) and branch
// previews, newest first. A project the organisation does not have is a 404.
func (s *Service) Running(ctx context.Context, org, projectID string) ([]RunningRun, error) {
	var rows []runRow
	tasks := map[string]RunningTask{}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, projectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return refuse(http.StatusNotFound, "not_found", "project %s not found", projectID)
		}
		list, err := tx.Query(ctx, runSelect+` WHERE r.project_id = $1 AND r.session_id IS NULL
			AND ((r.task_id IS NOT NULL AND `+liveAgent+`) OR ((r.task_id IS NOT NULL OR r.preview_branch IS NOT NULL) AND `+livePreview+`))
			ORDER BY r.created_at DESC, r.id`, projectID)
		if err != nil {
			return err
		}
		if rows, err = pgx.CollectRows(list, func(row pgx.CollectableRow) (runRow, error) { return scanRun(row) }); err != nil {
			return err
		}
		named, err := tx.Query(ctx, `SELECT t.id, p.key_prefix || '-' || t.number, t.title FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.project_id = $1 AND t.id IN (SELECT r.task_id FROM runs r WHERE r.project_id = $1 AND r.task_id IS NOT NULL
				AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused'))`, projectID)
		if err != nil {
			return err
		}
		defer named.Close()
		for named.Next() {
			var t RunningTask
			if err := named.Scan(&t.ID, &t.Key, &t.Title); err != nil {
				return err
			}
			tasks[t.ID] = t
		}
		return named.Err()
	})
	if err != nil {
		return nil, err
	}
	views := make([]RunningRun, len(rows))
	var wg sync.WaitGroup
	slots := make(chan struct{}, runningLookups)
	for i := range rows {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int) {
			defer func() { <-slots; wg.Done() }()
			views[i] = s.runningView(ctx, &rows[i], tasks)
		}(i)
	}
	wg.Wait()
	out := []RunningRun{}
	for _, v := range views {
		if len(v.Servers) > 0 {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *Service) runningView(ctx context.Context, r *runRow, tasks map[string]RunningTask) RunningRun {
	view := s.view(ctx, r, nil)
	out := RunningRun{ID: r.ID, Kind: "preview", State: r.Status, StartedAt: r.StartedAt, StartedBy: r.StartedBy, Servers: []RunningServer{}}
	if r.TaskID != "" {
		out.Kind = "task"
		if t, ok := tasks[r.TaskID]; ok {
			out.Task = &t
		} else {
			out.Task = &RunningTask{ID: r.TaskID}
		}
		out.Branch = nonEmpty(r.Branch)
	} else {
		out.Branch = nonEmpty(r.PreviewBranch)
	}
	if view.Run != nil {
		out.StartedAt = view.Run.StartedAt
	}
	for _, sv := range view.Servers {
		var raw struct {
			URL         *string `json:"url"`
			ServerState *string `json:"serverState"`
			Commenting  any     `json:"commenting"`
		}
		_ = json.Unmarshal(sv.Raw, &raw)
		out.Servers = append(out.Servers, RunningServer{Name: sv.Name, Port: sv.Port, URL: raw.URL, State: sv.State,
			ServerState: raw.ServerState, Since: utc(sv.Since), ReadySince: utc(sv.ReadySince), Commenting: raw.Commenting})
	}
	return out
}
