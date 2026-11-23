package servers

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// AgentPreviews is the Service as the agent tools start previews with it
// (agenttools.Previews): start_preview's task preview and preview_branch's
// branch preview, answered as the preview itself, its servers' names, URLs
// and states alone. agenttools cannot import this package (servers →
// phases → agenttools), so cmd/dude-orchestrator wires this in.
type AgentPreviews struct{ Service *Service }

var _ agenttools.Previews = AgentPreviews{}

// StartPreview is Service.StartPreview, answering the task's preview: the
// Run ForTask shows when no agent is at work, else the preview behind it.
func (a AgentPreviews) StartPreview(ctx context.Context, org, taskID, actor string, identity ...string) (agenttools.Preview, error) {
	view, err := a.Service.StartPreview(ctx, org, taskID, actor, identity...)
	if err != nil {
		return agenttools.Preview{}, agentError(err)
	}
	if view.Run == nil || view.Run.Kind != KindPreview {
		if view.Preview == nil {
			return agenttools.Preview{}, errors.New("a started preview is not the task's live preview")
		}
		if view, err = a.Service.ForRun(ctx, org, view.Preview.ID); err != nil {
			return agenttools.Preview{}, agentError(err)
		}
	}
	out := agenttools.Preview{ID: view.Run.ID, State: view.Run.State, Branch: deref(view.Run.Branch), Servers: []agenttools.PreviewServer{}}
	for _, sv := range view.Servers {
		var raw struct {
			URL *string `json:"url"`
		}
		_ = json.Unmarshal(sv.Raw, &raw)
		out.Servers = append(out.Servers, agenttools.PreviewServer{Name: sv.Name, URL: deref(raw.URL), State: sv.State})
	}
	return out, nil
}

// StartBranchPreview is Service.StartBranchPreview, answered as the preview.
func (a AgentPreviews) StartBranchPreview(ctx context.Context, gh *forge.GitHub, org, projectID, branch, actor string, identity ...string) (agenttools.Preview, error) {
	b, err := a.Service.StartBranchPreview(ctx, gh, org, projectID, branch, actor, identity...)
	if err != nil {
		return agenttools.Preview{}, agentError(err)
	}
	out := agenttools.Preview{ID: b.ID, State: b.Status, Branch: b.Branch, Servers: []agenttools.PreviewServer{}}
	for _, sv := range b.Servers {
		out.Servers = append(out.Servers, agenttools.PreviewServer{Name: sv.Name, URL: deref(sv.URL)})
	}
	return out, nil
}

// agentError is a refusal of the Service's said to the agent; any other
// error stays the server's.
func agentError(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return &agenttools.PreviewRefusal{Code: e.Code, Message: e.Message}
	}
	return err
}
