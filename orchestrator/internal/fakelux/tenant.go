package fakelux

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The tenant's servers (lux#41's /v1/servers) and the tenant's event feed
// (GET /v1/events), as lux keeps them: a server is its own resource,
// attached to at most one Run, outliving Runs with lifetime owner. One that
// wakes on request asks its owner for a Run, on the feed, when a signed-in
// request finds nothing serving it (Request); lux never starts a Run.
//
// Shapes are lux's openapi's (TenantServer, FeedEvent): field names copied.
// Idleness: a ready server of a running Run with no request for idleAfter
// emits server.idle once per idle period (checked every IdleCheck), and
// Idle emits it at once for a test.

type tenantServer struct {
	ID, Name, Host    string
	Port              int
	Command           []string
	Workdir           string
	Env, Labels       map[string]string
	Wake, Lifetime    string
	IdleAfter         time.Duration
	WakeTimeout       time.Duration
	ExpireAfter       *time.Duration
	RunID             string
	CreatedAt         time.Time
	LastRequestAt     *time.Time
	WakeRequestedAt   *time.Time
	IdleNotifiedAt    *time.Time
	Wakes             int
	Owner             string
	proc              *server // its process record in its Run, while attached
	lastIdleReadySeen *time.Time
}

type feedEvent struct {
	ID       int64
	Type     string
	ServerID string
	RunID    *string
	Epoch    *int
	Time     time.Time
	Data     map[string]any
}

// feedState is what the fake keeps for the tenant's servers and feed.
type feedState struct {
	tservers  map[string]*tenantServer
	nextSrv   int
	feed      []feedEvent
	feedCond  *sync.Cond
	idleStart sync.Once
	// Bumped by DropFeeds: every feed open before it ends.
	feedGen int
}

var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (s *Server) feedInit() {
	if s.tservers == nil {
		s.tservers = map[string]*tenantServer{}
	}
	if s.feedCond == nil {
		s.feedCond = sync.NewCond(&s.mu)
	}
}

// previewScheme: http for a domain under localhost, as lux's local demo.
func (s *Server) previewScheme() string {
	if strings.HasSuffix(s.PreviewDomain, "localhost") {
		return "http"
	}
	return "https"
}

func (s *Server) hostnameOf(host string) *string {
	if s.PreviewDomain == "" || host == "" {
		return nil
	}
	h := host + "." + lux.NormalDomain(s.PreviewDomain)
	return &h
}

func (s *Server) urlOf(host string) *string {
	h := s.hostnameOf(host)
	if h == nil {
		return nil
	}
	u := s.previewScheme() + "://" + *h
	if s.PreviewPort > 0 {
		u += ":" + strconv.Itoa(s.PreviewPort)
	}
	return &u
}

// emit appends an event to the feed (and, with a Run, to its stream).
// Callers hold s.mu.
func (s *Server) emit(run *Run, serverID, typ string, data map[string]any) {
	s.feedInit()
	s.nextEv++
	e := feedEvent{ID: s.nextEv, Type: typ, ServerID: serverID, Time: time.Now(), Data: data}
	if run != nil {
		id, ep := run.ID, run.Epoch
		e.RunID, e.Epoch = &id, &ep
		run.events = append(run.events, event{ID: e.ID, Epoch: run.Epoch, Type: typ, Data: data, Time: e.Time})
		run.cond.Broadcast()
	}
	s.feed = append(s.feed, e)
	s.feedCond.Broadcast()
}

// serverEvent is a tenant server's event, with what lux puts in every
// one's data. Callers hold s.mu.
func (s *Server) serverEvent(ts *tenantServer, typ string, extra map[string]any) {
	var runID any
	var run *Run
	if ts.RunID != "" {
		runID, run = ts.RunID, s.runs[ts.RunID]
	}
	d := map[string]any{"serverId": ts.ID, "name": ts.Name, "host": ts.Host, "labels": nonNil(ts.Labels), "runId": runID}
	if h := s.hostnameOf(ts.Host); h != nil {
		d["hostname"], d["url"] = *h, *s.urlOf(ts.Host)
	}
	for k, v := range extra {
		d[k] = v
	}
	s.emit(run, ts.ID, typ, d)
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func rfc(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (ts *tenantServer) process() string {
	if ts.proc == nil {
		return lux.ServerStopped
	}
	return ts.proc.State
}

// state derives a server's state as lux's serverRow.derive does.
func (s *Server) state(ts *tenantServer, now time.Time) string {
	var runState string
	if run := s.runs[ts.RunID]; run != nil {
		runState = run.State
	}
	running := runState == "running"
	proc := ts.process()
	switch {
	case running && proc == lux.ServerReady:
		return lux.SrvReady
	case running && proc == "unreachable":
		return "unreachable"
	case running && proc == "exited":
		return lux.SrvExited
	case running && proc == lux.ServerStarting:
		return lux.SrvWaking
	case ts.RunID != "" && slices.Contains([]string{"submitted", "scheduled", "starting", "resuming"}, runState):
		return lux.SrvWaking
	case running:
		return lux.SrvStopped
	case ts.Wake == "request" && ts.WakeRequestedAt != nil && now.Before(ts.WakeRequestedAt.Add(ts.WakeTimeout)):
		return lux.SrvWaking
	case ts.Wake == "request" && ts.WakeRequestedAt != nil:
		return lux.SrvNoAnswer
	case ts.Wake == "request":
		return lux.SrvAsleep
	}
	return lux.SrvStopped
}

func dur(d time.Duration) string { return d.String() }

// tview is a server as /v1/servers shows it (lux's TenantServer).
func (s *Server) tview(ts *tenantServer) map[string]any {
	var command any
	if ts.Command != nil {
		command = ts.Command
	}
	var runID, expire any
	out := map[string]any{"id": ts.ID, "name": ts.Name, "hostname": s.hostnameOf(ts.Host), "url": s.urlOf(ts.Host),
		"state": s.state(ts, time.Now()), "process": ts.process(), "desired": "up", "port": ts.Port, "command": command,
		"workdir": ts.Workdir, "env": nonNil(ts.Env), "afterSync": nil, "labels": nonNil(ts.Labels), "wake": ts.Wake,
		"idleAfter": dur(ts.IdleAfter), "wakeTimeout": dur(ts.WakeTimeout), "lifetime": ts.Lifetime, "owner": ts.Owner,
		"fromSpec": false, "since": ts.CreatedAt.UTC().Format(time.RFC3339Nano), "readySince": nil, "stopReason": nil,
		"epoch": nil, "lastRequestAt": rfc(ts.LastRequestAt), "wakes": ts.Wakes,
		"createdAt": ts.CreatedAt.UTC().Format(time.RFC3339Nano), "updatedAt": ts.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if ts.ExpireAfter != nil {
		expire = dur(*ts.ExpireAfter)
	}
	out["expireAfter"] = expire
	if ts.RunID != "" {
		runID = ts.RunID
		if run := s.runs[ts.RunID]; run != nil {
			out["runState"] = run.State
		}
	}
	out["runId"] = runID
	if ts.WakeRequestedAt != nil {
		out["wakeRequestedAt"] = rfc(ts.WakeRequestedAt)
	}
	if p := ts.proc; p != nil {
		out["since"] = p.Since.UTC().Format(time.RFC3339Nano)
		if p.ReadySince != nil {
			out["readySince"] = rfc(p.ReadySince)
		}
		if p.StopReason != "" {
			out["stopReason"] = p.StopReason
		}
		out["epoch"] = p.Epoch
	}
	return out
}

func parseDur(raw json.RawMessage, def time.Duration) (time.Duration, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return def, nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return time.ParseDuration(str)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return time.Duration(n * float64(time.Second)), nil
}

func (s *Server) createTenantServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string            `json:"name"`
		Port        int               `json:"port"`
		Command     []string          `json:"command"`
		Workdir     string            `json:"workdir"`
		Env         map[string]string `json:"env"`
		Labels      map[string]string `json:"labels"`
		Hostname    string            `json:"hostname"`
		Wake        string            `json:"wake"`
		IdleAfter   json.RawMessage   `json:"idleAfter"`
		WakeTimeout json.RawMessage   `json:"wakeTimeout"`
		Lifetime    string            `json:"lifetime"`
		ExpireAfter json.RawMessage   `json:"expireAfter"`
		RunID       string            `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if msg := (serverInput{Name: in.Name, Port: in.Port}).check(true); msg != "" {
		writeErr(w, 422, "invalid_server", msg)
		return
	}
	wake := in.Wake
	if wake == "" {
		wake = "never"
	}
	lifetime := in.Lifetime
	if lifetime == "" {
		lifetime = "owner"
		if in.RunID != "" && wake == "never" {
			lifetime = "run"
		}
	}
	if wake == "request" && lifetime != "owner" {
		writeErr(w, 422, "invalid_server", "a server that wakes on request has lifetime owner: it outlives the Runs that serve it")
		return
	}
	idle, err1 := parseDur(in.IdleAfter, 10*time.Minute)
	timeout, err2 := parseDur(in.WakeTimeout, 5*time.Minute)
	expire, err3 := parseDur(in.ExpireAfter, 30*24*time.Hour)
	if err1 != nil || err2 != nil || err3 != nil {
		writeErr(w, 422, "invalid_server", "a duration does not parse")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedInit()
	host := ""
	if in.Hostname != "" {
		domain := lux.NormalDomain(s.PreviewDomain)
		if domain == "" {
			writeErr(w, 422, "invalid_server", "hostname: previews are not configured on this lux (preview.domain)")
			return
		}
		h := strings.ToLower(strings.TrimSuffix(in.Hostname, "."))
		rel, full := strings.CutSuffix(h, "."+domain)
		relative := s.Previews != nil && *s.Previews && !strings.Contains(in.Hostname, ".")
		if (!full && !relative) || rel == "" || len(rel)+1+len(domain) > 253 {
			writeErr(w, 422, "invalid_server", fmt.Sprintf("hostname: %q is not under the preview domain %s", in.Hostname, domain))
			return
		}
		for _, l := range strings.Split(rel, ".") {
			if !hostLabelRe.MatchString(l) {
				writeErr(w, 422, "invalid_server", fmt.Sprintf("hostname: %q: %q is not a DNS label (a-z, 0-9 and -, 1-63, not starting or ending in -)", in.Hostname, l))
				return
			}
		}
		host = rel
	}
	for _, other := range s.tservers {
		if host != "" && other.Host == host {
			writeErr(w, 409, "hostname_taken", "that hostname is taken")
			return
		}
	}
	s.nextSrv++
	t := &tenantServer{ID: fmt.Sprintf("srv_%d", s.nextSrv), Name: in.Name, Port: in.Port, Command: in.Command,
		Workdir: in.Workdir, Env: in.Env, Labels: in.Labels, Wake: wake, Lifetime: lifetime, IdleAfter: idle,
		WakeTimeout: timeout, CreatedAt: time.Now(), Owner: "key"}
	if host == "" {
		host = fmt.Sprintf("%s-%08d", in.Name, s.nextSrv)
	}
	t.Host = host
	if lifetime == "owner" && expire > 0 {
		t.ExpireAfter = &expire
	}
	s.tservers[t.ID] = t
	s.serverEvent(t, "server.created", map[string]any{"by": "key", "wake": wake, "lifetime": lifetime})
	if in.RunID != "" {
		if status, code, msg := s.attach(t, in.RunID); status != 0 {
			delete(s.tservers, t.ID)
			writeErr(w, status, code, msg)
			return
		}
	}
	s.idleStart.Do(func() { go s.idleLoop() })
	writeJSON(w, 201, s.tview(t))
}

// attach puts a server on a Run: started now if the Run runs, else at its
// next placement. A refusal is lux's: its status, code and message.
// Callers hold s.mu.
func (s *Server) attach(t *tenantServer, runID string) (status int, code, msg string) {
	run := s.runs[runID]
	switch {
	case run == nil:
		return 404, "not_found", "not found"
	case !runLive(run.State):
		return 409, "finished", "run is " + run.State
	case t.RunID == runID:
		return 0, "", ""
	case t.RunID != "":
		return 409, "attached", fmt.Sprintf("server %s is attached to %s: detach it there first", t.ID, t.RunID)
	case run.server(t.Name) != nil:
		return 409, "name_taken", "the Run already has a server of that name"
	}
	t.RunID = runID
	t.proc = &server{Name: t.Name, Port: t.Port, Command: t.Command, Workdir: t.Workdir, Env: t.Env,
		State: lux.ServerStopped, Since: time.Now(), Epoch: run.Epoch, tenant: t}
	run.servers = append(run.servers, t.proc)
	run.Calls = append(run.Calls, "server.attach "+t.ID)
	s.serverEvent(t, "server.attached", map[string]any{"by": "key", "runState": run.State})
	if run.State == "running" && t.Command != nil {
		s.startServer(run, t.proc)
	}
	return 0, "", ""
}

// detach takes a server off its Run (its process stops). Callers hold s.mu.
func (s *Server) detach(t *tenantServer, why string) {
	run := s.runs[t.RunID]
	if run == nil {
		t.RunID, t.proc = "", nil
		return
	}
	if t.proc != nil && t.proc.State != lux.ServerStopped {
		s.stopServer(run, t.proc, "detached")
	}
	run.servers = slices.DeleteFunc(run.servers, func(x *server) bool { return x == t.proc })
	from := t.RunID
	t.RunID, t.proc, t.WakeRequestedAt = "", nil, nil
	s.serverEvent(t, "server.detached", map[string]any{"from": from, "reason": why, "by": "key"})
}

func (s *Server) tfind(w http.ResponseWriter, r *http.Request) *tenantServer {
	s.feedInit()
	t := s.tservers[r.PathValue("sid")]
	if t == nil {
		writeErr(w, 404, "not_found", "no server "+r.PathValue("sid"))
	}
	return t
}

func (s *Server) getTenantServer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tfind(w, r); t != nil {
		writeJSON(w, 200, s.tview(t))
	}
}

func (s *Server) listTenantServers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedInit()
	var list []*tenantServer
	for _, t := range s.tservers {
		list = append(list, t)
	}
	slices.SortFunc(list, func(a, b *tenantServer) int { return b.CreatedAt.Compare(a.CreatedAt) })
	out := []any{}
	counts := map[string]int{}
	host := strings.TrimSuffix(strings.ToLower(q.Get("hostname")), ".")
	for _, t := range list {
		if host != "" {
			if h := s.hostnameOf(t.Host); h == nil || (*h != host && t.Host != host) {
				continue
			}
		}
		if run := q.Get("run"); run != "" && t.RunID != run {
			continue
		}
		if wake := q.Get("wake"); wake != "" && t.Wake != wake {
			continue
		}
		ok := true
		for _, l := range q["label"] {
			k, v, _ := strings.Cut(l, "=")
			if t.Labels[k] != v {
				ok = false
			}
		}
		if !ok {
			continue
		}
		st := s.state(t, time.Now())
		counts[st]++
		if states := q.Get("state"); states != "" && !slices.Contains(strings.Split(states, ","), st) {
			continue
		}
		out = append(out, s.tview(t))
	}
	writeJSON(w, 200, map[string]any{"servers": out, "counts": counts})
}

func (s *Server) deleteTenantServer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tfind(w, r)
	if t == nil {
		return
	}
	s.removeTenantServer(t, "server.deleted", "deleted")
	w.WriteHeader(204)
}

// removeTenantServer detaches and forgets a server. Callers hold s.mu.
func (s *Server) removeTenantServer(t *tenantServer, typ, why string) {
	if run := s.runs[t.RunID]; run != nil {
		run.Calls = append(run.Calls, "server.delete "+t.ID)
	}
	s.detach(t, why)
	delete(s.tservers, t.ID)
	s.DeletedServers = append(s.DeletedServers, t.ID)
	s.serverEvent(t, typ, map[string]any{"reason": why})
}

func (s *Server) attachTenantServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID string `json:"runId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tfind(w, r)
	if t == nil {
		return
	}
	if in.RunID == "" {
		writeErr(w, 422, "invalid_request", "runId is required")
		return
	}
	if status, code, msg := s.attach(t, in.RunID); status != 0 {
		writeErr(w, status, code, msg)
		return
	}
	writeJSON(w, 200, s.tview(t))
}

func (s *Server) detachTenantServer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tfind(w, r)
	if t == nil {
		return
	}
	s.detach(t, "detached")
	writeJSON(w, 200, s.tview(t))
}

// ownerRunEnded detaches the owner servers of a Run that finished for
// good, as lux does. Callers hold s.mu.
func (s *Server) ownerRunEnded(run *Run) {
	for _, t := range s.tservers {
		if t.RunID == run.ID {
			s.detach(t, "run finished")
		}
	}
}

// Request is a signed-in browser request to a server's hostname: served
// (lastRequestAt) when its Run runs and it is ready; otherwise the waking
// page, and, for a server that wakes on request with no wake open, one
// server.wake_requested. Returns whether it was served.
func (s *Server) RequestServer(id, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedInit()
	t := s.tservers[id]
	if t == nil {
		return false
	}
	now := time.Now()
	if s.state(t, now) == lux.SrvReady {
		t.LastRequestAt = &now
		return true
	}
	if t.Wake == "request" && (t.WakeRequestedAt == nil || !now.Before(t.WakeRequestedAt.Add(t.WakeTimeout))) {
		t.WakeRequestedAt = &now
		t.Wakes++
		s.serverEvent(t, "server.wake_requested", map[string]any{"by": "someone@example.com", "path": path, "wake": t.Wakes})
	}
	return false
}

// Idle emits server.idle for a ready server now, as lux does after its
// idleAfter without a request.
func (s *Server) Idle(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedInit()
	t := s.tservers[id]
	if t == nil || s.state(t, time.Now()) != lux.SrvReady {
		return false
	}
	s.idle(t)
	return true
}

// Callers hold s.mu.
func (s *Server) idle(t *tenantServer) {
	now := time.Now()
	t.IdleNotifiedAt = &now
	s.serverEvent(t, "server.idle", map[string]any{"idleAfter": dur(t.IdleAfter), "lastRequestAt": rfc(t.LastRequestAt)})
}

// Expire deletes a server as lux does after its expireAfter without a
// request: server.expired.
func (s *Server) Expire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedInit()
	if t := s.tservers[id]; t != nil {
		s.removeTenantServer(t, "server.expired", "expired")
	}
}

// TenantServers is every server's id, by hostname.
func (s *Server) TenantServers() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, t := range s.tservers {
		if h := s.hostnameOf(t.Host); h != nil {
			out[*h] = t.ID
		} else {
			out[t.Host] = t.ID
		}
	}
	return out
}

// TenantServer is one server as GET /v1/servers/{id} answers, decoded.
func (s *Server) TenantServer(id string) (lux.TenantServer, bool) {
	s.mu.Lock()
	t := s.tservers[id]
	if t == nil {
		s.mu.Unlock()
		return lux.TenantServer{}, false
	}
	b, _ := json.Marshal(s.tview(t))
	s.mu.Unlock()
	var out lux.TenantServer
	_ = json.Unmarshal(b, &out)
	return out, true
}

// idleLoop emits server.idle for ready servers past their idleAfter, once
// per idle period.
func (s *Server) idleLoop() {
	every := s.IdleCheck
	if every <= 0 {
		every = 100 * time.Millisecond
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-tick.C:
		}
		s.mu.Lock()
		now := time.Now()
		for _, t := range s.tservers {
			if t.IdleAfter <= 0 || t.proc == nil || t.proc.ReadySince == nil || s.state(t, now) != lux.SrvReady {
				continue
			}
			base := *t.proc.ReadySince
			if t.LastRequestAt != nil && t.LastRequestAt.After(base) {
				base = *t.LastRequestAt
			}
			if now.Before(base.Add(t.IdleAfter)) || (t.IdleNotifiedAt != nil && !t.IdleNotifiedAt.Before(base)) {
				continue
			}
			s.idle(t)
		}
		s.mu.Unlock()
	}
}

// feedHandler is GET /v1/events: every event from now, from after
// Last-Event-ID (or ?after=), or the last N, following.
func (s *Server) feedHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.feedInit()
	low := s.nextEv
	for _, v := range []string{r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after")} {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			low = n
			break
		}
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("last")); err == nil && n > 0 && r.Header.Get("Last-Event-ID") == "" && r.URL.Query().Get("after") == "" {
		if n > len(s.feed) {
			n = len(s.feed)
		}
		low = 0
		if n < len(s.feed) {
			low = s.feed[len(s.feed)-n-1].ID
		}
	}
	follow := r.URL.Query().Get("follow") != "false"
	gen := s.feedGen
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
		case <-s.closed:
		}
		s.mu.Lock()
		close(done)
		s.feedCond.Broadcast()
		s.mu.Unlock()
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		select {
		case <-done:
			return
		default:
		}
		for _, e := range s.feed {
			if e.ID <= low {
				continue
			}
			low = e.ID
			b, _ := json.Marshal(map[string]any{"id": e.ID, "type": e.Type, "serverId": e.ServerID, "runId": e.RunID,
				"epoch": e.Epoch, "time": e.Time.UTC().Format(time.RFC3339Nano), "data": e.Data, "tenant": "default"})
			if _, err := fmt.Fprintf(w, "id: %d\nevent: lux\ndata: %s\n\n", e.ID, b); err != nil {
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !follow || s.feedGen != gen {
			return
		}
		s.feedCond.Wait()
		if s.feedGen != gen {
			return
		}
	}
}

// DropFeeds closes every open feed, as a luxd restart would.
func (s *Server) DropFeeds() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedInit()
	s.feedGen++
	s.feedCond.Broadcast()
}

func (s *Server) whoami(w http.ResponseWriter, _ *http.Request) {
	var domain any
	if s.PreviewDomain != "" {
		domain = s.PreviewDomain
	}
	out := map[string]any{"name": "dude", "keyId": "key_fake", "operator": false, "previewDomain": domain,
		"previewScheme": s.previewScheme(), "scopes": []string{"read", "run"}}
	if s.Previews != nil {
		out["previews"] = *s.Previews
	}
	if s.PreviewPort > 0 {
		out["previewPort"] = s.PreviewPort
	}
	writeJSON(w, 200, out)
}

// syncRun is POST /v1/runs/{id}/sync: each repository moves now (git.sync),
// then sync.done. 409 not_running unless the Run runs.
func (s *Server) syncRun(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var in struct {
		RequestID string        `json:"requestId"`
		Sync      []lux.SyncRef `json:"sync"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run.Calls = append(run.Calls, "sync")
	// As lux's checkSync, before the Run's state: every repo the spec's.
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	for _, sr := range in.Sync {
		if !slices.ContainsFunc(specRepos(spec), func(x specRepo) bool { return x.Name == sr.Repo }) {
			writeErr(w, 422, "invalid_request", fmt.Sprintf("sync: the run has no repository %q", sr.Repo))
			return
		}
	}
	if msg := s.syncModeProblem(in.Sync); msg != "" {
		writeErr(w, 409, "sync_mode_unsupported", msg)
		return
	}
	if run.State != "running" {
		writeErr(w, 409, "not_running", "run is "+run.State+": sync a running Run, or resume it with sync")
		return
	}
	run.Syncs = append(run.Syncs, in.Sync)
	if s.HoldSyncs {
		writeJSON(w, 202, map[string]any{"requestId": in.RequestID})
		return
	}
	changed := s.applySync(run, in.Sync, in.RequestID)
	s.luxEvent(run, "sync.done", map[string]any{"requestId": in.RequestID, "changed": changed})
	writeJSON(w, 202, map[string]any{"requestId": in.RequestID})
}

// applySync moves each named checkout to its ref's commit, as git.sync
// events say. Callers hold s.mu.
func (s *Server) applySync(run *Run, sync []lux.SyncRef, requestID string) bool {
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	changed := false
	for _, sr := range sync {
		ev := map[string]any{"repo": sr.Repo, "ref": sr.Ref}
		if requestID != "" {
			ev["requestId"] = requestID
		}
		i := slices.IndexFunc(specRepos(spec), func(x specRepo) bool { return x.Name == sr.Repo })
		if i < 0 {
			ev["status"], ev["error"] = "failed", "no repository "+sr.Repo
			s.luxEvent(run, "git.sync", ev)
			continue
		}
		to := head(s.repoPath(specRepos(spec)[i].URL), sr.Ref)
		if sr.Mode != "" && sr.Mode != lux.SyncMove {
			s.safeSync(run, spec, specRepos(spec)[i], sr, to, ev)
			if ev["status"] == "fast-forward" {
				changed = true
			}
			s.luxEvent(run, "git.sync", ev)
			continue
		}
		if run.at == nil {
			run.at = map[string]string{}
		}
		from := run.at[sr.Repo]
		ev["from"], ev["to"] = from, to
		switch {
		case to == "":
			ev["status"], ev["error"] = "failed", "ref not found"
		case to == from:
			ev["status"] = "up-to-date"
		default:
			ev["status"] = "fast-forward"
			run.at[sr.Repo] = to
			changed = true
		}
		s.luxEvent(run, "git.sync", ev)
	}
	return changed
}
