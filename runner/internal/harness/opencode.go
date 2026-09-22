// Package harness drives an agent harness inside a Run container.
//
// The runner does not interpret what the agent does — it starts the harness,
// relays the task, and reports what happened. Judgment belongs to the model;
// policy belongs to the control plane.
//
// OpenCode is invoked through its CLI rather than its HTTP server: the server
// would need a port published out of an otherwise network-isolated container,
// and `opencode run` already gives structured JSON events on stdout.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marciomartins/dude/runner/internal/protocol"
)

// Execer runs a command inside the Run container.
//
// An interface rather than a concrete Docker manager so the harness can be
// tested without a daemon, and so a future non-Docker runtime fits unchanged.
type Execer interface {
	ExecStream(ctx context.Context, containerID string, cmd []string, env map[string]string,
		onLine func(string)) (int, error)
}

// Spec is one agent invocation.
type Spec struct {
	ContainerID string
	// Directory inside the container to work in, e.g. /workspace/repos/dude.
	WorkDir string
	Model   string
	// Agent role; maps to an OpenCode agent definition of the same name.
	Agent  string
	Prompt string
	// Provider credentials and endpoints, injected per Session.
	Env map[string]string
}

// Event is a normalized occurrence from the harness.
//
// The runner forwards these to the control plane, which persists them into the
// durable ledger. Types match packages/domain/src/events/types.ts.
type Event struct {
	Type    string
	Payload map[string]any
}

// Result summarizes a finished agent invocation.
type Result struct {
	ExitCode int
	// Raw harness output, kept for debugging a session that behaved oddly.
	Output string
	Events []Event
}

// OpenCode drives the `opencode` CLI.
type OpenCode struct {
	exec Execer
}

func NewOpenCode(exec Execer) *OpenCode { return &OpenCode{exec: exec} }

// Run executes one agent turn and returns the normalized events it produced.
//
// `onEvent` is called as events arrive so the caller can stream them to the
// control plane rather than waiting for the turn to finish — a long agent turn
// should still be observable while it runs.
func (o *OpenCode) Run(ctx context.Context, spec Spec, onEvent func(Event)) (*Result, error) {
	// `--format json` puts one event object per line on stdout. `--print-logs`
	// is deliberately omitted: it writes key=value diagnostics that would
	// interleave with the event stream without adding anything the ledger uses.
	args := []string{"opencode", "run", "--format", "json"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Agent != "" {
		args = append(args, "--agent", spec.Agent)
	}
	args = append(args, spec.Prompt)

	// `cd` into the repository: opencode operates on its working directory,
	// and the workspace root holds several repositories.
	//
	// `sh -c`, not `sh -lc`: a login shell re-reads the system profile and
	// drops the image's PATH, which hides the harness binary.
	shell := fmt.Sprintf("cd %q && %s", spec.WorkDir, shellJoin(args))

	var output strings.Builder
	result := &Result{}

	exitCode, err := o.exec.ExecStream(ctx, spec.ContainerID,
		[]string{"sh", "-c", shell}, spec.Env,
		func(line string) {
			output.WriteString(line)
			output.WriteByte('\n')

			if ev, ok := normalizeLine(line); ok {
				result.Events = append(result.Events, ev)
				if onEvent != nil {
					onEvent(ev)
				}
			}
		})

	result.ExitCode = exitCode
	result.Output = output.String()
	if err != nil {
		return result, fmt.Errorf("opencode run: %w", err)
	}
	return result, nil
}

/*
normalizeLine translates one line of OpenCode's `--format json` output into a
factory event.

The shape is `{"type": ..., "sessionID": ..., "part": {...}}`, one JSON object
per line on stdout. Non-JSON lines and events with no factory meaning are
skipped, so the ledger records semantic milestones rather than every internal
tick (plan §106).
*/
func normalizeLine(line string) (Event, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return Event{}, false
	}

	var raw struct {
		Type      string         `json:"type"`
		SessionID string         `json:"sessionID"`
		Part      map[string]any `json:"part"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return Event{}, false
	}

	part := raw.Part
	if part == nil {
		part = map[string]any{}
	}

	switch raw.Type {
	case "tool_use":
		/*
		 * A tool call carries its own lifecycle in `state.status`, so one wire
		 * type becomes either a call or a completion.
		 *
		 * In practice `opencode run` emits each tool once, already completed —
		 * it buffers until the tool returns. The running case is still handled
		 * because the streaming path does emit it, and a long-running tool that
		 * only appeared after finishing would make the UI look stalled.
		 */
		state, _ := part["state"].(map[string]any)
		status, _ := state["status"].(string)

		payload := map[string]any{
			"tool":      part["tool"],
			"callId":    part["callID"],
			"sessionId": raw.SessionID,
		}
		if state != nil {
			payload["input"] = state["input"]
			if title, ok := state["title"]; ok {
				payload["title"] = title
			}
		}

		if status == "completed" || status == "error" {
			payload["status"] = status
			return Event{Type: protocol.EventAgentToolCompleted, Payload: payload}, true
		}
		return Event{Type: protocol.EventAgentToolCalled, Payload: payload}, true

	case "text":
		text, _ := part["text"].(string)
		if strings.TrimSpace(text) == "" {
			return Event{}, false
		}
		return Event{Type: protocol.EventAgentMessage, Payload: map[string]any{
			"text":      text,
			"sessionId": raw.SessionID,
		}}, true

	case "step_finish":
		// Carries token usage and cost for this step — the input to cost
		// accounting (plan §19).
		payload := map[string]any{"sessionId": raw.SessionID}
		if tokens, ok := part["tokens"]; ok {
			payload["tokens"] = tokens
		}
		if cost, ok := part["cost"]; ok {
			payload["costUsd"] = cost
		}
		if reason, ok := part["reason"]; ok {
			payload["reason"] = reason
		}
		return Event{Type: protocol.EventAgentModelRequestCompleted, Payload: payload}, true

	case "error":
		return Event{Type: protocol.EventAgentSessionStopped, Payload: map[string]any{
			"reason":    "error",
			"error":     part,
			"sessionId": raw.SessionID,
		}}, true

	default:
		// step_start and anything new: not a milestone on its own.
		return Event{}, false
	}
}

// shellJoin quotes arguments for `sh -c`.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// DefaultTimeout bounds one agent turn.
const DefaultTimeout = 20 * time.Minute
