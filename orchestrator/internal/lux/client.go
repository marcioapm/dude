// Package lux is dude's client for lux, the runtime that executes phase Runs.
//
// lux owns everything below the agent's task: the container, the checkout,
// the push, the agent process and its session. dude owns everything above
// it. This package is the whole boundary — nothing else speaks lux's wire
// format — so a change in lux's API is a change here and nowhere else.
//
// Authentication is one tenant-scoped API key with the `run` scope; lux has
// no API for keys, an operator makes them with `luxd admin create-key`.
package lux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Run is a lux Run as its API reports it; only the fields dude reads.
type Run struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	StateReason string `json:"stateReason"`
	// busy or idle: whether the agent is working or waiting for input.
	Activity  string `json:"activity"`
	Epoch     int    `json:"epoch"`
	SessionID string `json:"sessionId"`
	// The name of the host of its current placement; "" while it has none.
	Host string `json:"host,omitempty"`
	// Where it ran, one per start; only filled in by Get.
	Placements []Placement `json:"placements,omitempty"`
	// The spec lux stored, without secret values; filled in by Get and by
	// Submit's answer, which for a key lux has seen is the first submit's.
	Spec StoredSpec `json:"spec"`
	// What it used, summed over its placements; nil from a lux that does
	// not say. Only filled in by Get.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage is a Run's resource counters, as lux rolls its placements up.
type Usage struct {
	CPUSeconds float64 `json:"cpuSeconds"`
	NetRxBytes int64   `json:"netRxBytes"`
	NetTxBytes int64   `json:"netTxBytes"`
}

// StoredSpec is the part of a Run's stored spec dude reads back.
type StoredSpec struct {
	Image   Image          `json:"image"`
	Secrets []StoredSecret `json:"secrets"`
}

// StoredSecret is a secret the Run declared, as lux stores it: no value,
// as normalized (an unset one is env, or none for a credential only), and
// runnerOnly for a git or registry credential, never in the container.
type StoredSecret struct {
	Name       string `json:"name"`
	As         string `json:"as"`
	RunnerOnly bool   `json:"runnerOnly"`
}

// Placement is one start of a Run on a host.
type Placement struct {
	Epoch int `json:"epoch"`
	// The host's name, as people know it.
	HostName string `json:"hostName,omitempty"`
	// assigned, starting, running, stopping; then exited, or lost with its
	// host.
	State string `json:"state"`
	// How far its start got, each nil until reached: lux assigned it a
	// host, the image was ready there, its volumes were restored (from the
	// snapshot, for a resume), its container started.
	AssignedAt         *time.Time `json:"assignedAt,omitempty"`
	ImageReadyAt       *time.Time `json:"imageReadyAt,omitempty"`
	VolumesRestoredAt  *time.Time `json:"volumesRestoredAt,omitempty"`
	ContainerStartedAt *time.Time `json:"containerStartedAt,omitempty"`
	// When the agent's process started; nil for a placement that never got
	// that far, and so never published anything.
	WorkloadStartedAt *time.Time `json:"workloadStartedAt,omitempty"`
	// When it was asked to stop; nil for one that was not.
	StopRequestedAt *time.Time `json:"stopRequestedAt,omitempty"`
	// When its container exited; nil while it runs.
	ExitedAt *time.Time `json:"exitedAt,omitempty"`
	// When the host reported what it kept at exit — the snapshot, and the
	// artifacts with it. Nil while it is still running or uploading.
	SnapshotDoneAt *time.Time `json:"snapshotDoneAt,omitempty"`
	// When its snapshot reached object storage, where another host can
	// restore it from, and its size.
	UploadedAt    *time.Time `json:"uploadedAt,omitempty"`
	SnapshotBytes *int64     `json:"snapshotBytes,omitempty"`
	// The memory limit its container was given, in bytes: what the Run
	// asked for less the host's share (a newer lux; nil from one that does
	// not say).
	MemoryLimit *int64 `json:"memoryLimit,omitempty"`
}

// MemoryLimit is the memory limit of the Run's latest placement, when lux
// reports one.
func (r Run) MemoryLimit() *int64 {
	if n := len(r.Placements); n > 0 {
		return r.Placements[n-1].MemoryLimit
	}
	return nil
}

// Artifact is a file a Run produced, kept by lux after the Run ends.
type Artifact struct {
	ID    string `json:"id"`
	Epoch int    `json:"epoch"`
	// Where it was in the container: /.lux/artifacts/<name> for what the
	// agent published into $LUX_ARTIFACTS.
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	// Uploaded from its host, so it can be downloaded.
	Available bool `json:"available"`
}

// PublishedDir is $LUX_ARTIFACTS inside the container, and PublishedPrefix
// where what an agent put there is listed.
const (
	PublishedDir    = "/.lux/run/artifacts"
	PublishedPrefix = "/.lux/artifacts/"
)

// Every Run and server dude submits carries AppLabel=App: lux names the tool
// behind a cost by it.
const (
	AppLabel = "app"
	App      = "dude"
)

// Terminal says whether lux will report nothing more without a resume.
func Terminal(state string) bool {
	switch state {
	case "stopped", "succeeded", "failed", "cancelled", "terminated", "lost":
		return true
	}
	return false
}

// Terminated says lux has ended the Run for good: nothing of it is left to
// resume. lux before the rename reports "cancelled", lux after "terminated".
func Terminated(state string) bool {
	return state == "cancelled" || state == "terminated"
}

// Moved says a Run lux stopped for this reason is moving host, not
// stopping: lux resumes it elsewhere on its own (a drain, a preemption, an
// operator's migrate), and reports "stopped" on the way.
func Moved(reason string) bool {
	switch reason {
	case "drain", "preempt", "migrate":
		return true
	}
	return false
}

// Recorded is the state to keep for a Run lux reports in state for
// reason: one stopped to move is as good as resuming — lux resumes it at
// once — and must not read as over (Terminal) to anything that decides on
// it meanwhile (an abort, a push, a pause).
func Recorded(state, reason string) string {
	if state == "stopped" && Moved(reason) {
		return "resuming"
	}
	return state
}

type Secret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	As    string `json:"as,omitempty"`
	Path  string `json:"path,omitempty"`
}

// Spec is the RunSpec, as far as dude fills it in. lux fills in the rest and
// rejects what it cannot run, reporting every problem at once.
type Spec struct {
	Name     string            `json:"name,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Image    Image             `json:"image"`
	Workload Workload          `json:"workload"`
	Env      map[string]string `json:"env,omitempty"`
	Secrets  []Secret          `json:"secrets,omitempty"`
	Git      *Git              `json:"git,omitempty"`
	Volumes  []Volume          `json:"volumes,omitempty"`
	Timeout  string            `json:"timeout,omitempty"`
	Network  *Network          `json:"network,omitempty"`
	Sandbox  *Sandbox          `json:"sandbox,omitempty"`
	// What the Run gets and its host reserves; nil is lux's default size.
	Resources *Resources `json:"resources,omitempty"`
	// Where it may run; nil is the tenant's default pool.
	Placement *PlacementSpec `json:"placement,omitempty"`
}

// Resources is a Run's size. Memory and disk are bytes, which lux takes as
// well as its size strings ("8Gi").
type Resources struct {
	CPUs   float64 `json:"cpus,omitempty"`
	Memory int64   `json:"memory,omitempty"`
	Disk   int64   `json:"disk,omitempty"`
}

// PlacementSpec names the pool a Run is placed in. PoolID is lux's id for
// it, which a rename leaves alone: lux refuses an id it does not have for
// the tenant (422 unknown_pool) at submit.
type PlacementSpec struct {
	PoolID string `json:"poolId,omitempty"`
}

// CodeUnknownPool is lux refusing a placement.poolId it has no pool for.
const CodeUnknownPool = "unknown_pool"

// Pool is one of lux's pools dude's key can use (GET /v1/pools). HostSize,
// HostSizeFrom and InstanceType are from a newer lux: nil or "" from one
// that does not say.
type Pool struct {
	// lux's id (pool_…), unchanged by a rename.
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Platform bool   `json:"platform,omitempty"`
	Shared   bool   `json:"shared,omitempty"`
	// Where lux puts a Run that names no pool.
	IsDefault bool `json:"isDefault,omitempty"`
	// One host's size: what the biggest Run it can hold may ask for.
	HostSize *HostSize `json:"hostSize,omitempty"`
	// "running": from hosts up now; "history": from its last hosts.
	HostSizeFrom string `json:"hostSizeFrom,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	HostsRunning *int   `json:"hostsRunning,omitempty"`
}

// HostSize is one host's resources: memory and disk in bytes, disk 0 when
// the host reserves none.
type HostSize struct {
	CPUs   float64 `json:"cpus"`
	Memory int64   `json:"memory"`
	Disk   int64   `json:"disk"`
}

// Sandbox relaxes a Run's container for what its workload needs.
type Sandbox struct {
	// Requires a host offering nested containers and an image with the engine.
	NestedContainers bool `json:"nestedContainers,omitempty"`
}

type Image struct {
	Ref string `json:"ref"`
	// Logins the runner uses to pull Ref. The secret each names is
	// runner-only in lux: it never enters the container.
	RegistryAuth []RegistryAuth `json:"registryAuth,omitempty"`
}

type RegistryAuth struct {
	// A host with an optional port, lowercase, no scheme or path.
	Registry string `json:"registry"`
	// A secret holding user:password, or a bare token.
	Secret string `json:"secret"`
}

type Workload struct {
	Adapter string   `json:"adapter"`
	Command []string `json:"command,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Workdir string   `json:"workdir,omitempty"`
	// MCP servers the agent is given (streamable HTTP), each header's value
	// from a named secret.
	MCPServers []Service `json:"mcpServers,omitempty"`
	// Outside services lux serves inside the container on a local socket
	// ($LUX_SERVICE_<NAME>), adding each header on the way out, so the
	// workload can call them without holding the credential.
	Services []Service `json:"services,omitempty"`
	// Run in the container on every stop, before the workload is
	// signalled: dude's stop, cancel and pause, and lux's own (a timeout, a
	// drain). Not after a crash or a lost host.
	BeforeStop *BeforeStop `json:"beforeStop,omitempty"`
	// Servers lux starts on every start of the Run (a branch preview's).
	Servers []ServerInput `json:"servers,omitempty"`
	// Images given with the first prompt (request id "prompt"), as an
	// input's are.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is an image given to the agent with an input or the prompt
// (lux feat/input-attachments): its bytes, standard base64 without a data:
// prefix, on the wire. lux checks the bytes are ContentType.
type Attachment struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Data        []byte `json:"data"`
}

// What lux takes in one input's attachments.
const (
	MaxAttachments     = 10
	MaxAttachmentBytes = 5 << 20
)

// lux's refusals of an input's attachments: one it cannot take
// (invalid_attachment), or a Run that has nowhere to put an image (a
// generic adapter).
const (
	CodeInvalidAttachment      = "invalid_attachment"
	CodeAttachmentsUnsupported = "attachments_unsupported"
)

// InputRequest is one input to a running agent.
type InputRequest struct {
	Text, RequestID string
	Interrupt       bool
	Attachments     []Attachment
}

// BeforeStop is a command lux runs inside the container, as the workload's
// user with its environment and working directory, when it stops the Run —
// bounded by Timeout, and never longer than the stop's grace. What it
// writes into $LUX_ARTIFACTS is collected like any artifact.
type BeforeStop struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout,omitempty"`
}

// Service is an outside HTTP service a workload may reach as its Run: an
// MCP server for the agent, or one lux serves on a local socket. Header
// values come from named secrets, filled in by lux, never seen by the
// workload.
type Service struct {
	Name    string   `json:"name,omitempty"`
	URL     string   `json:"url,omitempty"`
	Headers []Header `json:"headers,omitempty"`
	// services: also served on 127.0.0.1 inside the Run
	// ($LUX_SERVICE_<NAME>_URL), for clients that take a URL.
	Loopback bool `json:"loopback,omitempty"`
	// mcpServers: reach the MCP server through this service's loopback
	// address instead of a url; lux adds the headers, so none reaches the
	// agent's harness.
	Service string `json:"service,omitempty"`
}

type Header struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

type Git struct {
	Repositories []Repository `json:"repositories"`
	Push         *Push        `json:"push,omitempty"`
}

type Repository struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Ref        string `json:"ref,omitempty"`
	Credential string `json:"credential,omitempty"`
	Path       string `json:"path,omitempty"`
	// false: cloned, never pushed (lux reports it "skipped"). Nil is lux's
	// default, true.
	Push *bool `json:"push,omitempty"`
}

type Push struct {
	Branch string `json:"branch"`
}

type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type Network struct {
	Egress       []EgressRule `json:"egress,omitempty"`
	Unrestricted bool         `json:"unrestricted,omitempty"`
}

type EgressRule struct {
	Host string `json:"host,omitempty"`
	CIDR string `json:"cidr,omitempty"`
}

// Frame is one message of a Run's output stream.
type Frame struct {
	// record: something the workload wrote, or a structured event from it.
	// lux: a lifecycle event lux recorded (state, push, checkout).
	// gap: output that is gone, e.g. with a host that died.
	// end: no more output until the Run is resumed.
	Kind string

	// record
	Cursor  string
	Epoch   int
	Channel string
	Data    string
	Event   *RecordEvent

	// lux
	EventID   int64
	EventType string
	EventData json.RawMessage

	// gap
	Reason string

	// end
	State      string
	AfterEvent int64
}

type RecordEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// What lux's shim records of an input, by request id ("prompt" for the
// task). RecordInput is its one first answer: {requestId, phase: accepted,
// lands, receipt, text?}, or {requestId, phase: failed, error} when it was
// never accepted. An older lux writes it once with no phase, on handoff
// ({requestId, text}), or {requestId, error}. After an accepted answer at
// most one of RecordInputConsumed {requestId} (the agent's step has it;
// only with receipt) or RecordInputFailed {requestId, error} follows.
const (
	RecordInput         = "lux.input"
	RecordInputConsumed = "lux.input.consumed"
	RecordInputFailed   = "lux.input.failed"

	InputAccepted = "accepted"
	InputFailed   = "failed"
)

// Error is lux refusing a request, with its stable error code.
type Error struct {
	Status  int
	Code    string
	Message string
	Details json.RawMessage
}

func (e *Error) Error() string { return fmt.Sprintf("lux %d %s: %s", e.Status, e.Code, e.Message) }

// Retryable: trying again later could succeed. A quota or lux being down
// passes; an invalid spec does not.
func (e *Error) Retryable() bool { return e.Status == 0 || e.Status == 429 || e.Status >= 500 }

// IsNotFound says lux has no such Run or artifact: it will not come back.
func IsNotFound(err error) bool {
	e, ok := AsError(err)
	return ok && e.Status == http.StatusNotFound
}

// AsError unwraps a lux error.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// ServerInput is a server as a spec declares it or a person adds it: a
// named port of the Run, and optionally the command lux starts to serve it.
type ServerInput struct {
	Name    string            `json:"name"`
	Port    int               `json:"port"`
	Command []string          `json:"command,omitempty"`
	Workdir string            `json:"workdir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Added at runtime: start it now (lux's default when it has a command).
	Start *bool `json:"start,omitempty"`
}

// Server is one of a Run's servers as lux reports it. dude reads a few of
// its fields and passes the object on as lux sent it (Raw), so what a
// person sees is lux's word, whatever lux adds to it later.
type Server struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	// nil for a server that is only a port someone else serves on.
	Command []string          `json:"command"`
	Workdir string            `json:"workdir"`
	Env     map[string]string `json:"env"`
	// Declared in the Run's spec: lux starts it on every start of the Run.
	FromSpec bool `json:"fromSpec"`
	// stopped, starting, ready, unreachable or exited.
	State string `json:"state"`
	// When it last became ready; nil unless it is.
	ReadySince *time.Time `json:"readySince"`
	Since      *time.Time `json:"since"`
	// Why it is stopped: "stopped", "run stopped", "migrated", "host lost".
	StopReason *string `json:"stopReason"`
	// The placement it stopped in, and the one its state is from.
	StoppedEpoch *int `json:"stoppedEpoch"`
	Epoch        int  `json:"epoch"`
	// When a preview request last reached it (lux flushes it every 30s).
	LastRequestAt *time.Time `json:"lastRequestAt"`

	Raw json.RawMessage `json:"-"`
}

func (s *Server) UnmarshalJSON(b []byte) error {
	type plain Server
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = Server(p)
	s.Raw = append(json.RawMessage(nil), b...)
	return nil
}

func (s Server) MarshalJSON() ([]byte, error) {
	if s.Raw != nil {
		return s.Raw, nil
	}
	type plain Server
	return json.Marshal(plain(s))
}

// Server states and stop reasons dude decides on.
const (
	ServerStopped  = "stopped"
	ServerStarting = "starting"
	ServerReady    = "ready"
	StopMigrated   = "migrated"
)

// Client is what dude calls on lux. An interface so tests can stand in.
type Client interface {
	Submit(ctx context.Context, spec Spec, idempotencyKey string) (Run, error)
	// Input sends a Run's agent words, images or an interrupt.
	Input(ctx context.Context, runID string, in InputRequest) error
	Push(ctx context.Context, runID, requestID string) error
	Stop(ctx context.Context, runID string) error
	Cancel(ctx context.Context, runID string) error
	Resume(ctx context.Context, runID string, in ResumeInput) (Run, error)
	// Output follows a Run's output from a position until lux says there is
	// no more, calling fn for each frame. Returning an error from fn stops.
	Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(Frame) error) error
	// Events is one page of a Run's lifecycle events after an event id,
	// without its output.
	Events(ctx context.Context, runID string, after int64) ([]Frame, error)
	Get(ctx context.Context, runID string) (Run, error)
	Artifacts(ctx context.Context, runID string) ([]Artifact, error)
	// Download streams an artifact as the Run wrote it. The caller closes it.
	Download(ctx context.Context, artifactID string) (io.ReadCloser, error)
	// Exec runs a command in a running Run's container, as its workload
	// user with its environment, and returns what it printed.
	Exec(ctx context.Context, runID string, command []string) (ExecResult, error)

	// A Run's servers: listed, added, started, stopped, restarted, removed,
	// and each one's log ({lines: [{t, stream, text}]}, as lux answers).
	Servers(ctx context.Context, runID string) ([]Server, error)
	AddServer(ctx context.Context, runID string, in ServerInput) (Server, error)
	// ServerAction is start, stop or restart.
	ServerAction(ctx context.Context, runID, name, action string) (Server, error)
	RemoveServer(ctx context.Context, runID, name string) error
	ServerLog(ctx context.Context, runID, name string, tail int) (json.RawMessage, error)

	// Cost is what lux's cost plugins have priced for a Run so far.
	Cost(ctx context.Context, runID string) (RunCost, error)

	// Pools are the pools dude's key can place Runs in.
	Pools(ctx context.Context) ([]Pool, error)

	// The tenant's servers and its event feed (servers.go).
	Servers
}

type HTTPClient struct {
	url  string
	key  string
	http *http.Client
	// No timeout: the output stream stays open for as long as a Run runs.
	stream *http.Client
}

// idleConnsPerHost bounds the connections to lux kept open between
// requests. http.DefaultTransport keeps 2, below the preview and phase
// sweeps' 8 at a time plus each preview's output stream, so most
// connections were closed after one request and dialled again.
const idleConnsPerHost = 32

func New(baseURL, apiKey string) *HTTPClient {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns, t.MaxIdleConnsPerHost = 2*idleConnsPerHost, idleConnsPerHost
	return &HTTPClient{
		url:    strings.TrimRight(baseURL, "/"),
		key:    apiKey,
		http:   &http.Client{Timeout: 30 * time.Second, Transport: t},
		stream: &http.Client{Transport: t},
	}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
		headers = maps.Clone(headers)
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = "application/json"
	}
	res, err := c.send(ctx, c.http, method, path, reader, headers)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// send makes an authenticated request and turns a refusal or an unreachable
// lux into an *Error. On success the caller owns the body.
func (c *HTTPClient) send(ctx context.Context, client *http.Client, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Unreachable is a status like any other to the caller: retry later.
		return nil, &Error{Status: 0, Code: "unreachable", Message: err.Error()}
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return nil, errorFrom(res.StatusCode, data, method+" "+path)
	}
	return res, nil
}

// lux's error body is {"error": {"code", "message", "details"?}}.
func errorFrom(status int, body []byte, what string) *Error {
	var e struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	out := &Error{Status: status, Code: e.Error.Code, Message: e.Error.Message, Details: e.Error.Details}
	if out.Code == "" {
		out.Code = "error"
	}
	if out.Message == "" {
		out.Message = fmt.Sprintf("%s → %d", what, status)
	}
	return out
}

func (c *HTTPClient) Submit(ctx context.Context, spec Spec, key string) (Run, error) {
	var r Run
	err := c.do(ctx, "POST", "/v1/runs", spec, map[string]string{"Idempotency-Key": key}, &r)
	return r, err
}

func (c *HTTPClient) Input(ctx context.Context, runID string, in InputRequest) error {
	body := map[string]any{"requestId": in.RequestID}
	if in.Text != "" {
		body["text"] = in.Text
	}
	if in.Interrupt {
		body["interrupt"] = true
	}
	if len(in.Attachments) > 0 {
		body["attachments"] = in.Attachments
	}
	return c.do(ctx, "POST", "/v1/runs/"+runID+"/input", body, nil, nil)
}

func (c *HTTPClient) Push(ctx context.Context, runID, requestID string) error {
	return c.do(ctx, "POST", "/v1/runs/"+runID+"/push", map[string]string{"requestId": requestID}, nil, nil)
}

func (c *HTTPClient) Stop(ctx context.Context, runID string) error {
	return c.do(ctx, "POST", "/v1/runs/"+runID+"/stop", nil, nil, nil)
}

func (c *HTTPClient) Cancel(ctx context.Context, runID string) error {
	// /cancel, not /terminate: every lux takes it, a newer one as terminate's alias.
	return c.do(ctx, "POST", "/v1/runs/"+runID+"/cancel", nil, nil, nil)
}

func (c *HTTPClient) Resume(ctx context.Context, runID string, in ResumeInput) (Run, error) {
	body := map[string]any{"secrets": in.Secrets}
	if in.Input != "" {
		body["input"] = map[string]string{"text": in.Input}
	}
	if in.RequestID != "" {
		body["requestId"] = in.RequestID
	}
	if len(in.AddRepositories) > 0 {
		body["git"] = map[string]any{"repositories": in.AddRepositories}
	}
	if len(in.Sync) > 0 {
		body["sync"] = in.Sync
	}
	var r Run
	err := c.do(ctx, "POST", "/v1/runs/"+runID+"/resume", body, nil, &r)
	return r, err
}

// ResumeInput is what a resume carries: the secrets again (lux never keeps
// them), input for the agent, repositories to add to the Run — cloned
// before it starts, each reported as a git.clone event with the request id
// — and checkouts to move to new commits before init (Sync).
type ResumeInput struct {
	Secrets         []Secret
	Input           string
	RequestID       string
	AddRepositories []Repository
	Sync            []SyncRef
}

func (c *HTTPClient) Get(ctx context.Context, runID string) (Run, error) {
	var r Run
	err := c.do(ctx, "GET", "/v1/runs/"+runID, nil, nil, &r)
	return r, err
}

func (c *HTTPClient) Artifacts(ctx context.Context, runID string) ([]Artifact, error) {
	var out struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	err := c.do(ctx, "GET", "/v1/runs/"+runID+"/artifacts", nil, nil, &out)
	return out.Artifacts, err
}

func (c *HTTPClient) Download(ctx context.Context, artifactID string) (io.ReadCloser, error) {
	res, err := c.send(ctx, c.stream, "GET", "/v1/artifacts/"+artifactID, nil, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func serverPath(runID, name string) string {
	return "/v1/runs/" + url.PathEscape(runID) + "/servers/" + url.PathEscape(name)
}

func (c *HTTPClient) Servers(ctx context.Context, runID string) ([]Server, error) {
	var out struct {
		Servers []Server `json:"servers"`
	}
	err := c.do(ctx, "GET", "/v1/runs/"+url.PathEscape(runID)+"/servers", nil, nil, &out)
	return out.Servers, err
}

func (c *HTTPClient) AddServer(ctx context.Context, runID string, in ServerInput) (Server, error) {
	var s Server
	err := c.do(ctx, "POST", "/v1/runs/"+url.PathEscape(runID)+"/servers", in, nil, &s)
	return s, err
}

func (c *HTTPClient) ServerAction(ctx context.Context, runID, name, action string) (Server, error) {
	switch action {
	case "start", "stop", "restart":
	default:
		return Server{}, fmt.Errorf("no server action %q", action)
	}
	var s Server
	err := c.do(ctx, "POST", serverPath(runID, name)+"/"+action, nil, nil, &s)
	return s, err
}

func (c *HTTPClient) RemoveServer(ctx context.Context, runID, name string) error {
	return c.do(ctx, "DELETE", serverPath(runID, name), nil, nil, nil)
}

func (c *HTTPClient) ServerLog(ctx context.Context, runID, name string, tail int) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, "GET", serverPath(runID, name)+"/log?tail="+fmt.Sprint(tail), nil, nil, &out)
	return out, err
}

func (c *HTTPClient) Cost(ctx context.Context, runID string) (RunCost, error) {
	var out RunCost
	err := c.do(ctx, "GET", "/v1/runs/"+url.PathEscape(runID)+"/cost", nil, nil, &out)
	return out, err
}

// Pools reads GET /v1/pools. The key's `run` scope includes `read`.
func (c *HTTPClient) Pools(ctx context.Context) ([]Pool, error) {
	var out struct {
		Pools []Pool `json:"pools"`
	}
	err := c.do(ctx, "GET", "/v1/pools", nil, nil, &out)
	return out.Pools, err
}

// Output follows the SSE stream. Two positions, because lux keeps two
// streams: the workload's records by cursor, and its own lifecycle events by
// id. Both come back in the frames, so a caller that stores them resumes
// exactly where it left off.
func (c *HTTPClient) Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(Frame) error) error {
	q := url.Values{"follow": {"true"}, "events": {"true"}, "afterEvent": {fmt.Sprint(afterEvent)}}
	if cursor != "" {
		q.Set("since", cursor)
	}
	res, err := c.send(ctx, c.stream, "GET", "/v1/runs/"+runID+"/output?"+q.Encode(), nil,
		map[string]string{"Accept": "text/event-stream"})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return ReadSSE(res.Body, func(event string, data []byte) error {
		f, ok := ParseFrame(event, data)
		if !ok {
			return nil
		}
		return fn(f)
	})
}

// Events reads one page of a Run's lifecycle events after an event id, in id
// order: up to 1000 of them (lux's page), none when lux had nothing past after at
// its query. Each is the Frame the output stream sends for it (Kind "lux").
func (c *HTTPClient) Events(ctx context.Context, runID string, after int64) ([]Frame, error) {
	var out struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := c.do(ctx, "GET", "/v1/runs/"+url.PathEscape(runID)+"/events?after="+fmt.Sprint(after), nil, nil, &out); err != nil {
		return nil, err
	}
	frames := make([]Frame, 0, len(out.Events))
	for _, e := range out.Events {
		f, ok := ParseFrame("lux", e)
		if !ok {
			return nil, fmt.Errorf("lux run %s: an event dude cannot read: %s", runID, e)
		}
		frames = append(frames, f)
	}
	return frames, nil
}

// ReadSSE calls fn for each server-sent event: `event:` and `data:` lines,
// separated by a blank line.
func ReadSSE(r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	event := "message"
	var data [][]byte
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if len(data) > 0 {
				if err := fn(event, bytes.Join(data, []byte("\n"))); err != nil {
					return err
				}
			}
			event, data = "message", nil
		case bytes.HasPrefix(line, []byte("event:")):
			event = strings.TrimSpace(string(line[6:]))
		case bytes.HasPrefix(line, []byte("data:")):
			d := bytes.TrimPrefix(line[5:], []byte(" "))
			data = append(data, append([]byte(nil), d...))
		}
	}
	return sc.Err()
}

// ParseFrame turns one SSE message into a Frame; false for a kind dude
// does not use.
func ParseFrame(event string, data []byte) (Frame, bool) {
	// An agent's output is whatever its tools printed, binary included, and
	// Postgres takes no NUL in text or jsonb: one would fail every write of
	// the frame, and the Run's stream could never get past it. Replaced
	// here, where lux's output enters dude, so nothing downstream sees one.
	data = bytes.ReplaceAll(data, []byte(`\u0000`), []byte(`\ufffd`))
	switch event {
	case "record":
		var r struct {
			Cursor string       `json:"cursor"`
			Epoch  int          `json:"epoch"`
			Ch     string       `json:"ch"`
			Data   string       `json:"data"`
			Event  *RecordEvent `json:"event"`
		}
		if json.Unmarshal(data, &r) != nil {
			return Frame{}, false
		}
		return Frame{Kind: "record", Cursor: r.Cursor, Epoch: r.Epoch, Channel: r.Ch, Data: r.Data, Event: r.Event}, true
	case "lux":
		var e struct {
			ID    int64           `json:"id"`
			Epoch *int            `json:"epoch"`
			Type  string          `json:"type"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &e) != nil {
			return Frame{}, false
		}
		f := Frame{Kind: "lux", EventID: e.ID, EventType: e.Type, EventData: e.Data}
		if e.Epoch != nil {
			f.Epoch = *e.Epoch
		}
		return f, true
	case "gap":
		var g struct {
			Epoch  int    `json:"epoch"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(data, &g) != nil {
			return Frame{}, false
		}
		return Frame{Kind: "gap", Epoch: g.Epoch, Reason: g.Reason}, true
	case "end":
		var e struct {
			Cursor     string `json:"cursor"`
			State      string `json:"state"`
			AfterEvent int64  `json:"afterEvent"`
		}
		if json.Unmarshal(data, &e) != nil {
			return Frame{}, false
		}
		return Frame{Kind: "end", Cursor: e.Cursor, State: e.State, AfterEvent: e.AfterEvent}, true
	}
	return Frame{}, false
}

// ExecResult is what a command run with Exec printed, and how it exited.
type ExecResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// ExecLimit caps what Exec keeps of a command's output. Past it the
// command's output is dropped rather than held: the caller asked a question
// whose answer was too big to use.
const ExecLimit = 8 << 20

// streamData is one message of lux's interactive stream (lux's
// proto.StreamData): bytes as base64, the channel they came on, and on the
// last one the exit code or why the stream ended.
type streamData struct {
	Data     []byte `json:"data,omitempty"`
	Channel  string `json:"ch,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	EOF      bool   `json:"eof,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Exec speaks lux's exec stream: a WebSocket to /v1/runs/{id}/exec whose
// first message is the command, then the command's output as it comes, and
// last its exit code. No terminal, and no input: stdin is closed at once.
//
// Refusals come before the upgrade, as HTTP errors (409 not_running, 503
// host_unreachable), and are returned as an *Error like any other call's.
func (c *HTTPClient) Exec(ctx context.Context, runID string, command []string) (ExecResult, error) {
	var out ExecResult
	u := "ws" + strings.TrimPrefix(c.url, "http") + "/v1/runs/" + runID + "/exec"
	ws, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.key}},
		HTTPClient: c.stream,
	})
	if err != nil {
		if res != nil && res.StatusCode >= 300 {
			var body []byte
			if res.Body != nil {
				body, _ = io.ReadAll(res.Body)
			}
			return out, errorFrom(res.StatusCode, body, "exec "+runID)
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, &Error{Status: 0, Code: "unreachable", Message: err.Error()}
	}
	defer ws.CloseNow()
	ws.SetReadLimit(4 << 20)
	if err := wsjson.Write(ctx, ws, map[string]any{"command": command}); err != nil {
		return out, err
	}
	if err := wsjson.Write(ctx, ws, streamData{EOF: true}); err != nil {
		return out, err
	}
	for {
		var d streamData
		if err := wsjson.Read(ctx, ws, &d); err != nil {
			// Closed without an exit code: the stream ended early (a host
			// that went away, a stream that fell behind).
			return out, &Error{Status: 0, Code: "stream_ended", Message: "exec ended before the command did: " + err.Error()}
		}
		switch {
		case d.Error != "":
			return out, &Error{Status: http.StatusConflict, Code: "exec_failed", Message: d.Error}
		case d.ExitCode != nil:
			out.ExitCode = *d.ExitCode
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return out, nil
		case d.Channel == "stderr":
			if len(out.Stderr) < 64<<10 {
				out.Stderr = append(out.Stderr, d.Data...)
			}
		default:
			if len(out.Stdout)+len(d.Data) > ExecLimit {
				continue // past the limit: dropped
			}
			out.Stdout = append(out.Stdout, d.Data...)
		}
	}
}
