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
	"net/http"
	"net/url"
	"strings"
	"time"
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
}

// Terminal says whether lux will report nothing more without a resume.
func Terminal(state string) bool {
	switch state {
	case "stopped", "succeeded", "failed", "cancelled", "lost":
		return true
	}
	return false
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
}

type Image struct {
	Ref string `json:"ref"`
}

type Workload struct {
	Adapter string   `json:"adapter"`
	Command []string `json:"command,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Workdir string   `json:"workdir,omitempty"`
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

// AsError unwraps a lux error.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Client is what dude calls on lux. An interface so tests can stand in.
type Client interface {
	Submit(ctx context.Context, spec Spec, idempotencyKey string) (Run, error)
	Input(ctx context.Context, runID, text, requestID string, interrupt bool) error
	Push(ctx context.Context, runID, requestID string) error
	Stop(ctx context.Context, runID string) error
	Cancel(ctx context.Context, runID string) error
	Resume(ctx context.Context, runID string, secrets []Secret, input string) (Run, error)
	// Output follows a Run's output from a position until lux says there is
	// no more, calling fn for each frame. Returning an error from fn stops.
	Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(Frame) error) error
}

type HTTPClient struct {
	url  string
	key  string
	http *http.Client
	// No timeout: the output stream stays open for as long as a Run runs.
	stream *http.Client
}

func New(baseURL, apiKey string) *HTTPClient {
	return &HTTPClient{
		url:    strings.TrimRight(baseURL, "/"),
		key:    apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
		stream: &http.Client{},
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
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		// Unreachable is a status like any other to the caller: retry later.
		return &Error{Status: 0, Code: "unreachable", Message: err.Error()}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return errorFrom(res.StatusCode, data, method+" "+path)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
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

func (c *HTTPClient) Input(ctx context.Context, runID, text, requestID string, interrupt bool) error {
	body := map[string]any{"requestId": requestID}
	if text != "" {
		body["text"] = text
	}
	if interrupt {
		body["interrupt"] = true
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
	return c.do(ctx, "POST", "/v1/runs/"+runID+"/cancel", nil, nil, nil)
}

func (c *HTTPClient) Resume(ctx context.Context, runID string, secrets []Secret, input string) (Run, error) {
	body := map[string]any{"secrets": secrets}
	if input != "" {
		body["input"] = map[string]string{"text": input}
	}
	var r Run
	err := c.do(ctx, "POST", "/v1/runs/"+runID+"/resume", body, nil, &r)
	return r, err
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
	req, err := http.NewRequestWithContext(ctx, "GET", c.url+"/v1/runs/"+runID+"/output?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Status: 0, Code: "unreachable", Message: err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(res.Body)
		return errorFrom(res.StatusCode, data, "GET output")
	}
	return ReadSSE(res.Body, func(event string, data []byte) error {
		f, ok := ParseFrame(event, data)
		if !ok {
			return nil
		}
		return fn(f)
	})
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
