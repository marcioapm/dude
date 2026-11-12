package fakegithub

import (
	"encoding/json"
	"net/http"
	"time"
)

// Dispatch is one workflow_dispatch asked of the fake.
type Dispatch struct {
	Workflow, Ref string
	Inputs        map[string]any
}

// dispatch answers POST actions/workflows/{workflow}/dispatches as GitHub
// does: 204 and no body, no run id. With DispatchRunID set it creates a
// queued run, as GitHub does a moment later.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref    string         `json:"ref"`
		Inputs map[string]any `json:"inputs"`
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
	if s.DispatchRunID != 0 {
		if s.WorkflowRuns == nil {
			s.WorkflowRuns = map[int64]WorkflowRun{}
		}
		s.WorkflowRuns[s.DispatchRunID] = WorkflowRun{Name: workflow, Workflow: workflow, Branch: body.Ref, Event: "workflow_dispatch",
			Status: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		s.DispatchRunID++
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateWorkflowRun changes run id in place (a re-run's attempt, its
// status), as GitHub would, sending no webhook.
func (s *Server) UpdateWorkflowRun(id int64, f func(*WorkflowRun)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.WorkflowRuns[id]
	f(&run)
	s.WorkflowRuns[id] = run
}
