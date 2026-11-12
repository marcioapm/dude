package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Dispatch is one workflow_dispatch asked of the fake.
type Dispatch struct {
	Workflow, Ref string
	Inputs        map[string]any
}

// dispatch answers POST actions/workflows/{workflow}/dispatches as GitHub
// does. Asked with return_run_details, it creates the run, queued, and
// answers 200 with its id and URLs (workflow-dispatch-response); otherwise,
// or with DispatchLegacy set (an older GitHub or GHES), 204 and no body,
// and a run is created only with DispatchRunID set, as GitHub does a moment
// later.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref        string         `json:"ref"`
		Inputs     map[string]any `json:"inputs"`
		RunDetails bool           `json:"return_run_details"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Ref == "" {
		fail(w, 422, "Invalid request: ref is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DispatchStatus != 0 {
		fail(w, s.DispatchStatus, "Workflow does not have 'workflow_dispatch' trigger")
		return
	}
	workflow := r.PathValue("workflow")
	s.Dispatches = append(s.Dispatches, Dispatch{Workflow: workflow, Ref: body.Ref, Inputs: body.Inputs})
	details := body.RunDetails && !s.DispatchLegacy
	id := s.DispatchRunID
	if id == 0 && details {
		id = 70000 + int64(len(s.Dispatches))
	}
	if id != 0 {
		if s.WorkflowRuns == nil {
			s.WorkflowRuns = map[int64]WorkflowRun{}
		}
		s.WorkflowRuns[id] = WorkflowRun{Name: workflow, Branch: body.Ref, Event: "workflow_dispatch",
			Status: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		if s.DispatchRunID != 0 {
			s.DispatchRunID++
		}
	}
	if !details {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	write(w, 200, map[string]any{"workflow_run_id": id,
		"run_url":  fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d", s.Slug, id),
		"html_url": fmt.Sprintf("https://github.com/%s/actions/runs/%d", s.Slug, id)})
}

// UpdateWorkflowRun changes run id in place (its status, conclusion), as
// GitHub would, sending no webhook. Its attempt changes only by Rerun.
func (s *Server) UpdateWorkflowRun(id int64, f func(*WorkflowRun)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.WorkflowRuns[id]
	attempt := run.Attempt
	f(&run)
	if run.Attempt != attempt {
		panic("fakegithub: a run's attempt changes only by Rerun, of a completed run")
	}
	s.WorkflowRuns[id] = run
}

// Rerun re-runs run id as GitHub does: only a completed run (else 403, as
// GitHub's "This workflow is already running"), the same id, attempt n+1,
// queued with no conclusion.
func (s *Server) Rerun(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.WorkflowRuns[id]
	if !ok {
		return fmt.Errorf("no run %d", id)
	}
	if run.Status != "completed" {
		return fmt.Errorf("run %d is %s: GitHub re-runs only a completed run", id, run.Status)
	}
	run.Attempt = max(run.Attempt, 1) + 1
	run.Status, run.Conclusion = "queued", ""
	s.WorkflowRuns[id] = run
	return nil
}
