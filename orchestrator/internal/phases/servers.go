package phases

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// EvServersChanged tells a watching browser that a task's servers, or its
// branch preview, changed: read them again (GET /v1/tasks/{id}/servers).
// Payload {taskId, runId, change}, and for one server's change its name and
// state.
const EvServersChanged = "servers.changed"

// ServersChanged records servers.changed for a Run, in tx.
func ServersChanged(ctx context.Context, tx pgx.Tx, org, projectID, taskID, runID string, extra map[string]any) error {
	payload := map[string]any{"taskId": taskID, "runId": runID}
	maps.Copy(payload, extra)
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvServersChanged, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, RunID: runID, ActorType: ledger.ActorSystem, ActorID: runID, Source: ledger.SourceRunner,
		CorrelationID: taskID, Payload: payload})
	return err
}

// ServerEvent turns one of lux's server.* events on a Run's stream
// (server.state {name, state, exitCode?, error?, epoch}, server.added
// {name}, server.removed {name}) into servers.changed. The state itself is
// not kept: lux has it, and the browser reads it from there.
func ServerEvent(ctx context.Context, tx pgx.Tx, org, projectID, taskID, runID, luxEvent string, data json.RawMessage) error {
	var d struct {
		Name     string `json:"name"`
		State    string `json:"state"`
		ExitCode *int   `json:"exitCode"`
	}
	_ = json.Unmarshal(data, &d)
	extra := map[string]any{"change": strings.TrimPrefix(luxEvent, "server."), "server": d.Name}
	if d.State != "" {
		extra["state"] = d.State
	}
	if d.ExitCode != nil {
		extra["exitCode"] = *d.ExitCode
	}
	return ServersChanged(ctx, tx, org, projectID, taskID, runID, extra)
}
