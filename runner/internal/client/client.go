// Package client talks to the control plane.
//
// The runner is deliberately dumb about policy: it asks what to do, reports
// what happened, and never decides whether something is allowed. Every call
// here maps to one endpoint under /v1/runner.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a control-plane API client authenticated with a runner key.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Repository is one repo to materialize into the Session Workspace.
type Repository struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	DefaultBranch string `json:"defaultBranch"`
	Trust         string `json:"trust"`
}

// Run is a leased unit of work.
//
// The control plane sends everything the runner needs to execute it, so the
// runner never has to ask a second question — and never has to decide what
// the task is or which model should do it.
type Run struct {
	ID           string       `json:"id"`
	WorkItemID   string       `json:"workItemId"`
	ProjectID    string       `json:"projectId"`
	Attempt      int          `json:"attempt"`
	Repositories []Repository `json:"repositories"`
	RuntimeImage string       `json:"runtimeImage"`

	// The task, resolved by the control plane from the Work Item.
	Prompt string `json:"prompt"`
	// Agent role, and the model resolved for it (project -> org -> default).
	Role  string `json:"role"`
	Model string `json:"model"`
}

// Worker is this node's registration with the control plane.
type Worker struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Pool         string `json:"pool"`
	Status       string `json:"status"`
	MaxRuns      int    `json:"maxRuns"`
	ActiveRuns   int    `json:"activeRuns"`
	LeaseSeconds int    `json:"leaseSeconds"`
}

// Event is an occurrence inside the execution plane, normalized for the
// control plane's durable ledger.
type Event struct {
	EventType     string         `json:"eventType"`
	OccurredAt    string         `json:"occurredAt,omitempty"`
	RunID         string         `json:"runId,omitempty"`
	SessionID     string         `json:"sessionId,omitempty"`
	ProjectID     string         `json:"projectId,omitempty"`
	WorkItemID    string         `json:"workItemId,omitempty"`
	ActorType     string         `json:"actorType,omitempty"`
	ActorID       string         `json:"actorId,omitempty"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// APIError is a non-2xx response from the control plane.
type APIError struct {
	Status int
	Body   string
	Path   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("control plane %s returned %d: %s", e.Path, e.Status, e.Body)
}

// Retryable reports whether the call is worth repeating. A 4xx means the
// runner asked for something wrong and retrying will not change that; a 5xx
// or transport failure may be transient.
func (e *APIError) Retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+c.apiKey)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()

	// Bounded read: a runaway response must not exhaust the runner's memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(raw), Path: path}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// RegisterRequest describes this node's identity and capacity.
type RegisterRequest struct {
	Name               string   `json:"name"`
	Pool               string   `json:"pool"`
	CPUMillis          int      `json:"cpuMillis"`
	MemoryMB           int      `json:"memoryMb"`
	MaxRuns            int      `json:"maxRuns"`
	CachedImages       []string `json:"cachedImages"`
	CachedRepositories []string `json:"cachedRepositories"`
}

// Register announces this worker. Re-registering after a restart reclaims the
// same worker identity, preserving repository-cache affinity.
func (c *Client) Register(ctx context.Context, req RegisterRequest) (*Worker, error) {
	var worker Worker
	if err := c.post(ctx, "/v1/runner/workers", req, &worker); err != nil {
		return nil, err
	}
	return &worker, nil
}

// HeartbeatRequest reports liveness, load and cache contents.
type HeartbeatRequest struct {
	ActiveRuns         int      `json:"activeRuns"`
	Status             string   `json:"status"`
	CachedImages       []string `json:"cachedImages,omitempty"`
	CachedRepositories []string `json:"cachedRepositories,omitempty"`
}

func (c *Client) Heartbeat(ctx context.Context, workerID string, req HeartbeatRequest) error {
	return c.post(ctx, "/v1/runner/workers/"+workerID+"/heartbeat", req, nil)
}

// ClaimRuns takes up to limit pending Runs. Returns an empty slice when there
// is no work, which is the common case and not an error.
func (c *Client) ClaimRuns(ctx context.Context, workerID string, limit int) ([]Run, error) {
	var out struct {
		Runs []Run `json:"runs"`
	}
	body := map[string]int{"limit": limit}
	if err := c.post(ctx, "/v1/runner/workers/"+workerID+"/claim", body, &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

// Directive is a steering instruction from a human (plan §24).
type Directive struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Scope     string `json:"scope"`
	CreatedAt string `json:"createdAt"`
}

// LeaseResponse carries the control channel back with the lease renewal.
//
// Renewal already happens on a timer, so pending interventions ride along
// rather than needing a second poll — and a runner that has stopped renewing
// would not have heard a separate poll either.
type LeaseResponse struct {
	OK            bool        `json:"ok"`
	LeaseSeconds  int         `json:"leaseSeconds"`
	Control       string      `json:"control"`
	ControlReason string      `json:"controlReason"`
	Directives    []Directive `json:"directives"`
}

// ErrRunNotLeasable means the control plane no longer considers this Run
// ours to execute — it was aborted, reclaimed, or has otherwise finished.
//
// Distinguished from a transient failure because the responses are opposite:
// a transient error means try again, this means stop immediately.
var ErrRunNotLeasable = errors.New("run is no longer leasable")

// RenewLease extends this worker's claim on a Run still in progress and
// returns anything a human has asked of it since the last renewal.
func (c *Client) RenewLease(ctx context.Context, runID string) (*LeaseResponse, error) {
	var out LeaseResponse
	if err := c.post(ctx, "/v1/runner/runs/"+runID+"/lease", nil, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, ErrRunNotLeasable
		}
		return nil, err
	}
	return &out, nil
}

// UpdateRun reports Run progress or terminal state.
func (c *Client) UpdateRun(ctx context.Context, runID, status, errMsg, workspacePath string) error {
	body := map[string]any{"status": status}
	if errMsg != "" {
		body["error"] = errMsg
	}
	if workspacePath != "" {
		body["workspacePath"] = workspacePath
	}
	return c.post(ctx, "/v1/runner/runs/"+runID+"/status", body, nil)
}

// ReportRuntime records container lifecycle for a Run.
func (c *Client) ReportRuntime(ctx context.Context, runID, containerID, imageDigest, status string) error {
	body := map[string]any{"status": status}
	if containerID != "" {
		body["containerId"] = containerID
	}
	if imageDigest != "" {
		body["imageDigest"] = imageDigest
	}
	return c.post(ctx, "/v1/runner/runs/"+runID+"/runtime", body, nil)
}

// SendEvents appends execution-plane events to the durable ledger.
func (c *Client) SendEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	return c.post(ctx, "/v1/runner/events", map[string]any{"events": events}, nil)
}
