package lux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The tenant's servers (lux's /v1/servers): named URLs independent of
// Runs, attached to at most one at a time. A server that wakes on request
// tells its owner, on the event feed, when someone opens it with no Run
// serving it; lux never starts a Run for it.

// TenantServer is a server as /v1/servers reports it. Field names are
// lux's (TenantServer in its openapi); only those dude reads.
type TenantServer struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Hostname *string           `json:"hostname"`
	URL      *string           `json:"url"`
	Labels   map[string]string `json:"labels"`
	// ready, waking, asleep, stopped, unreachable, exited or "no answer".
	State string `json:"state"`
	// Its process in its Run: stopped, starting, ready, unreachable, exited.
	Process       string     `json:"process"`
	RunID         *string    `json:"runId"`
	RunState      string     `json:"runState"`
	LastRequestAt *time.Time `json:"lastRequestAt"`
	Wakes         int        `json:"wakes"`
	// When its state last changed.
	Since time.Time `json:"since"`
	// How long without a request before server.idle: a Go duration
	// ("45m0s") or seconds; 0 is never.
	IdleAfter json.RawMessage `json:"idleAfter"`

	Raw json.RawMessage `json:"-"`
}

// IdleAfterDuration is IdleAfter read; 0 when absent or unreadable.
func (s TenantServer) IdleAfterDuration() time.Duration {
	var str string
	if json.Unmarshal(s.IdleAfter, &str) == nil {
		d, _ := time.ParseDuration(str)
		return d
	}
	var secs float64
	if json.Unmarshal(s.IdleAfter, &secs) == nil {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func (s *TenantServer) UnmarshalJSON(b []byte) error {
	type plain TenantServer
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = TenantServer(p)
	s.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// Server states (TenantServer.State) dude decides on.
const (
	SrvReady    = "ready"
	SrvWaking   = "waking"
	SrvAsleep   = "asleep"
	SrvStopped  = "stopped"
	SrvNoAnswer = "no answer"
	SrvExited   = "exited"
)

// CreateServer is POST /v1/servers' body (lux's CreateServerInput).
type CreateServer struct {
	Name        string            `json:"name"`
	Port        int               `json:"port"`
	Command     []string          `json:"command,omitempty"`
	Workdir     string            `json:"workdir,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Hostname    string            `json:"hostname,omitempty"`
	Wake        string            `json:"wake,omitempty"`
	IdleAfter   string            `json:"idleAfter,omitempty"`
	WakeTimeout string            `json:"wakeTimeout,omitempty"`
	Lifetime    string            `json:"lifetime,omitempty"`
	ExpireAfter string            `json:"expireAfter,omitempty"`
	RunID       string            `json:"runId,omitempty"`
}

// SyncRef moves one repository of a Run (its spec's name) to a ref.
type SyncRef struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

// FeedEvent is one event of the tenant's feed, GET /v1/events.
type FeedEvent struct {
	ID       int64          `json:"id"`
	Type     string         `json:"type"`
	ServerID string         `json:"serverId"`
	RunID    *string        `json:"runId"`
	Epoch    *int           `json:"epoch"`
	Time     time.Time      `json:"time"`
	Data     map[string]any `json:"data"`
}

// Servers is what dude asks of lux's server resource and its feed.
type Servers interface {
	CreateServer(ctx context.Context, in CreateServer) (TenantServer, error)
	GetServer(ctx context.Context, id string) (TenantServer, error)
	// ListServers filters by hostname and labels ("k=v").
	ListServers(ctx context.Context, hostname string, labels ...string) ([]TenantServer, error)
	DeleteServer(ctx context.Context, id string) error
	AttachServer(ctx context.Context, id, runID string) (TenantServer, error)
	DetachServer(ctx context.Context, id string) error
	// SyncRun moves a running Run's checkouts.
	SyncRun(ctx context.Context, runID, requestID string, sync []SyncRef) error
	// Feed follows GET /v1/events after an event id (-1: from the latest
	// event lux has), calling fn for each, until ctx ends, the stream does,
	// or fn fails.
	Feed(ctx context.Context, after int64, fn func(FeedEvent) error) error
	PreviewDomain(ctx context.Context) (PreviewConfig, error)
}

func (c *HTTPClient) CreateServer(ctx context.Context, in CreateServer) (TenantServer, error) {
	var s TenantServer
	err := c.do(ctx, "POST", "/v1/servers", in, nil, &s)
	return s, err
}

func (c *HTTPClient) GetServer(ctx context.Context, id string) (TenantServer, error) {
	var s TenantServer
	err := c.do(ctx, "GET", "/v1/servers/"+url.PathEscape(id), nil, nil, &s)
	return s, err
}

func (c *HTTPClient) ListServers(ctx context.Context, hostname string, labels ...string) ([]TenantServer, error) {
	q := url.Values{}
	if hostname != "" {
		q.Set("hostname", hostname)
	}
	for _, l := range labels {
		q.Add("label", l)
	}
	var out struct {
		Servers []TenantServer `json:"servers"`
	}
	err := c.do(ctx, "GET", "/v1/servers?"+q.Encode(), nil, nil, &out)
	return out.Servers, err
}

func (c *HTTPClient) DeleteServer(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/servers/"+url.PathEscape(id), nil, nil, nil)
}

func (c *HTTPClient) AttachServer(ctx context.Context, id, runID string) (TenantServer, error) {
	var s TenantServer
	err := c.do(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/attach", map[string]string{"runId": runID}, nil, &s)
	return s, err
}

func (c *HTTPClient) DetachServer(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/detach", map[string]any{}, nil, nil)
}

func (c *HTTPClient) SyncRun(ctx context.Context, runID, requestID string, sync []SyncRef) error {
	return c.do(ctx, "POST", "/v1/runs/"+url.PathEscape(runID)+"/sync",
		map[string]any{"requestId": requestID, "sync": sync}, nil, nil)
}

type PreviewConfig struct {
	Domain string
	// Nil identifies lux versions that require full hostnames.
	Previews *bool
}

func (c *HTTPClient) PreviewDomain(ctx context.Context) (PreviewConfig, error) {
	var w struct {
		PreviewDomain string `json:"previewDomain"`
		Previews      *bool  `json:"previews"`
	}
	if err := c.do(ctx, "GET", "/v1/whoami", nil, nil, &w); err != nil {
		return PreviewConfig{}, err
	}
	return PreviewConfig{Domain: NormalDomain(w.PreviewDomain), Previews: w.Previews}, nil
}

// NormalDomain is a domain as dude compares and joins it: lowercase, no
// leading or trailing dot.
func NormalDomain(d string) string { return strings.Trim(strings.ToLower(d), ".") }

// Feed reads the SSE stream: `id:`, `event: lux`, `data: <FeedEvent>`;
// Last-Event-ID resumes strictly after the id given. With no id (after < 0)
// it starts at the latest event (?last=1), so the caller has an id to keep.
func (c *HTTPClient) Feed(ctx context.Context, after int64, fn func(FeedEvent) error) error {
	headers := map[string]string{"Accept": "text/event-stream"}
	path := "/v1/events"
	if after >= 0 {
		headers["Last-Event-ID"] = strconv.FormatInt(after, 10)
	} else {
		path += "?last=1"
	}
	res, err := c.send(ctx, c.stream, "GET", path, nil, headers)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	err = ReadSSE(res.Body, func(event string, data []byte) error {
		switch event {
		case "lux":
			var e FeedEvent
			if err := json.Unmarshal(data, &e); err != nil {
				return fmt.Errorf("feed event: %w", err)
			}
			return fn(e)
		case "error":
			return fmt.Errorf("lux's feed failed: %s", data)
		}
		return nil
	})
	if err == nil && ctx.Err() == nil {
		err = errors.New("lux's feed ended")
	}
	return err
}

// ErrNoServers: lux answered, and has no server resource (/v1/servers,
// lux#41): a lux from before it answers 404 or 405.
var ErrNoServers = errors.New("dude needs a lux with wakeable servers (marcioapm/lux#41 or later)")

// RequireServers fails with ErrNoServers unless lux has the server
// resource; any other failure (lux unreachable, 5xx) is returned as it is.
func RequireServers(ctx context.Context, c Servers) error {
	_, err := c.ListServers(ctx, "", "dude.kind=probe")
	if le, ok := AsError(err); ok && (le.Status == http.StatusNotFound || le.Status == http.StatusMethodNotAllowed) {
		return fmt.Errorf("lux has no /v1/servers (%s): %w", le.Message, ErrNoServers)
	}
	return err
}
