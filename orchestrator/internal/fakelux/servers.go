package fakelux

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Servers, as lux keeps them: named ports of a Run, optionally with a
// command lux starts in the container. Records outlive placements; their
// processes do not. The fake runs no process: a started server with a
// command is "starting", then "ready" a moment later (ServerReadyAfter) —
// unless its command says `fakelux-exit=<code>`, and then it exits with that
// code, or `fakelux-never-ready`, and then it stays starting. One with no
// command waits for its port to open (OpenPort), as the
// runner's health check would find someone else serving it.
//
// A placement's end stops every server — "run stopped", "migrated" or
// "host lost" — in the epoch it ran in, and a new placement starts the
// spec's servers again; runtime-added ones stay stopped until started.
// Each change is a server.* lux event on the Run's stream, as lux reports
// it.

type server struct {
	Name         string
	Port         int
	Command      []string
	Workdir      string
	Env          map[string]string
	FromSpec     bool
	State        string
	ExitCode     *int
	Error        string
	Since        time.Time
	ReadySince   *time.Time
	StopReason   string
	StoppedEpoch *int
	Epoch        int
	// When a preview request last reached it (Request).
	LastRequestAt *time.Time
	// The environment its process last started with, beyond the fake's own
	// (processEnv).
	StartEnv map[string]string
	log      []logLine
	// Bumped on every start or stop, so a pending "ready" of an earlier
	// start does nothing.
	gen int
	// The tenant server this is the process of, when attached (tenant.go).
	tenant *tenantServer
}

type logLine struct {
	T      int64  `json:"t"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

var serverNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,29}$`)

// exitRe is how a test makes a server's command exit: `fakelux-exit=3`.
var exitRe = regexp.MustCompile(`fakelux-exit=(\d+)`)

// serverInput is a server as a spec declares it or a request adds it.
type serverInput struct {
	Name    string            `json:"name"`
	Port    int               `json:"port"`
	Command []string          `json:"command"`
	Workdir string            `json:"workdir"`
	Env     map[string]string `json:"env"`
	Start   *bool             `json:"start"`
}

func (in serverInput) check(named bool) string {
	switch {
	case named && (!serverNameRe.MatchString(in.Name) || strings.HasSuffix(in.Name, "-")):
		return "invalid server name"
	case in.Port < 1 || in.Port > 65535:
		return "port must be 1-65535"
	}
	return ""
}

func (sv *server) view(run *Run, domain string) map[string]any {
	var command any
	if sv.Command != nil {
		command = sv.Command
	}
	env := sv.Env
	if env == nil {
		env = map[string]string{}
	}
	out := map[string]any{
		"name": sv.Name, "port": sv.Port, "command": command, "workdir": sv.Workdir, "env": env,
		"fromSpec": sv.FromSpec, "state": sv.State, "since": sv.Since.UTC().Format(time.RFC3339Nano),
		"readySince": nil, "stopReason": nil, "stoppedEpoch": sv.StoppedEpoch, "epoch": sv.Epoch, "url": nil,
		"lastRequestAt": nil,
	}
	if sv.ReadySince != nil {
		out["readySince"] = sv.ReadySince.UTC().Format(time.RFC3339Nano)
	}
	if sv.StopReason != "" {
		out["stopReason"] = sv.StopReason
	}
	if sv.State == "exited" {
		out["exitCode"] = sv.ExitCode
		if sv.Error != "" {
			out["error"] = sv.Error
		}
	}
	if sv.LastRequestAt != nil {
		out["lastRequestAt"] = sv.LastRequestAt.UTC().Format(time.RFC3339Nano)
	}
	if domain != "" {
		out["url"] = fmt.Sprintf("https://%s-%s.%s", sv.Name, strings.TrimPrefix(run.ID, "lrun_"), domain)
	}
	return out
}

// specServers makes the records of a spec's workload.servers. Callers hold
// s.mu.
func (s *Server) specServers(run *Run, spec map[string]any) {
	w, _ := spec["workload"].(map[string]any)
	raw, _ := json.Marshal(w["servers"])
	var ins []serverInput
	_ = json.Unmarshal(raw, &ins)
	for _, in := range ins {
		run.servers = append(run.servers, &server{Name: in.Name, Port: in.Port, Command: in.Command, Workdir: in.Workdir,
			Env: in.Env, FromSpec: true, State: lux.ServerStopped, Since: time.Now(), Epoch: run.Epoch})
	}
}

func (run *Run) server(name string) *server {
	for _, sv := range run.servers {
		if sv.Name == name {
			return sv
		}
	}
	return nil
}

// setServer moves a server to a state and reports it. Callers hold s.mu.
func (s *Server) setServer(run *Run, sv *server, state string) {
	now := time.Now()
	sv.State, sv.Since, sv.Epoch = state, now, run.Epoch
	if state == lux.ServerReady {
		sv.ReadySince = &now
	} else {
		sv.ReadySince = nil
	}
	data := map[string]any{"name": sv.Name, "state": state, "epoch": run.Epoch}
	if state == "exited" {
		data["exitCode"] = *sv.ExitCode
		if sv.Error != "" {
			data["error"] = sv.Error
		}
	}
	if t := sv.tenant; t != nil {
		// A tenant server's: on the feed with its id; ready resolves its
		// open wake and starts a new idle period.
		if state == lux.ServerReady {
			t.WakeRequestedAt = nil
		}
		if state == lux.ServerStopped && sv.StopReason != "" {
			data["stopReason"] = sv.StopReason
		}
		s.serverEvent(t, "server.state", data)
		return
	}
	s.luxEvent(run, "server.state", data)
}

func (sv *server) logf(stream, format string, a ...any) {
	sv.log = append(sv.log, logLine{T: time.Now().UnixMilli(), Stream: stream, Text: fmt.Sprintf(format, a...)})
}

// startServer starts a server in the Run's current placement: "starting",
// then ready or exited as its command says. Callers hold s.mu.
func (s *Server) startServer(run *Run, sv *server) {
	sv.gen++
	gen, epoch := sv.gen, run.Epoch
	sv.ExitCode, sv.Error, sv.StopReason, sv.StoppedEpoch = nil, "", "", nil
	s.setServer(run, sv, lux.ServerStarting)
	if sv.Command == nil {
		return // someone else serves the port: ready when it opens
	}
	sv.logf("stdout", "$ %s", strings.Join(sv.Command, " "))
	// The environment its process gets, as lux's shim builds it: the Run's
	// (its spec's env and env secrets), then the server's own over it.
	sv.StartEnv = s.processEnv(run, sv)
	if strings.Contains(strings.Join(sv.Command, " "), runMarker) {
		s.runCommand(run, sv)
	}
	after := s.ServerReadyAfter
	if after == 0 {
		after = 30 * time.Millisecond
	}
	go func() {
		time.Sleep(after)
		s.mu.Lock()
		defer s.mu.Unlock()
		if sv.gen != gen || run.Epoch != epoch || run.State != "running" || run.server(sv.Name) != sv {
			return
		}
		if m := exitRe.FindStringSubmatch(strings.Join(sv.Command, " ")); m != nil {
			code, _ := strconv.Atoi(m[1])
			sv.ExitCode = &code
			sv.Error = fmt.Sprintf("listen tcp :%d: bind: address already in use", sv.Port)
			sv.logf("stderr", "%s", sv.Error)
			s.setServer(run, sv, "exited")
			return
		}
		if strings.Contains(strings.Join(sv.Command, " "), "fakelux-never-ready") {
			return
		}
		sv.logf("stdout", "listening on :%d", sv.Port)
		s.setServer(run, sv, lux.ServerReady)
	}()
}

// stopServer stops a server a person stopped. Callers hold s.mu.
func (s *Server) stopServer(run *Run, sv *server, reason string) {
	sv.gen++
	epoch := run.Epoch
	sv.StopReason, sv.StoppedEpoch, sv.ExitCode, sv.Error = reason, &epoch, nil, ""
	s.setServer(run, sv, lux.ServerStopped)
}

// placementEnded stops every server that was running with the placement,
// saying why. Callers hold s.mu.
func (s *Server) placementEnded(run *Run, state, reason string) {
	why := "run stopped"
	switch {
	case state == "lost":
		why = "host lost"
	case lux.Moved(reason):
		why = "migrated"
	}
	for _, sv := range run.servers {
		if sv.State != lux.ServerStopped {
			s.stopServer(run, sv, why)
		}
	}
}

// placementStarted starts the spec's servers and the attached tenant
// servers with a command, as lux does on every start of the Run. Callers
// hold s.mu.
func (s *Server) placementStarted(run *Run) {
	for _, sv := range run.servers {
		if sv.FromSpec || sv.tenant != nil && sv.Command != nil {
			s.startServer(run, sv)
		}
	}
}

// OpenPort is someone starting a server by hand on a server's port (a
// server without a command, or one lux stopped): the runner's health check
// finds it serving, and it is ready whatever it was.
func (s *Server) OpenPort(runID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[runID]; run != nil && run.State == "running" {
		if sv := run.server(name); sv != nil {
			sv.gen++
			sv.StopReason, sv.StoppedEpoch = "", nil
			s.setServer(run, sv, lux.ServerReady)
		}
	}
}

// Request is someone opening a server's preview URL: lux records when.
func (s *Server) Request(runID, name string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[runID]; run != nil {
		if sv := run.server(name); sv != nil {
			sv.LastRequestAt = &at
		}
	}
}

// Migrate moves a running Run to another host, as an operator's migrate
// or a drain does: it stops (its servers "migrated"), and lux resumes it
// elsewhere at once, starting its spec's servers there.
func (s *Server) Migrate(id string) {
	s.mu.Lock()
	run := s.runs[id]
	if run == nil || run.State != "running" {
		s.mu.Unlock()
		return
	}
	run.Calls = append(run.Calls, "migrate")
	run.busy = false
	s.setState(run, "stopping")
	s.setStateWith(run, "stopped", "migrate")
	run.Epoch++
	run.starts++
	run.moveNext = true
	accepted := time.Now()
	run.acceptedAt = &accepted
	s.setStateWith(run, "resuming", "auto-resume after migrate")
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	epoch, start := run.Epoch, run.starts
	s.mu.Unlock()
	go s.play(run, epoch, start, spec, true)
}

// ServerStates is each of a Run's servers' state, by name.
func (s *Server) ServerStates(id string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	if run := s.runs[id]; run != nil {
		for _, sv := range run.servers {
			out[sv.Name] = sv.State
		}
	}
	return out
}

// ServerEnv is the environment a Run's server's process last started with,
// beyond the fake's own: nil if it never started.
func (s *Server) ServerEnv(id, name string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		if sv := run.server(name); sv != nil {
			return maps.Clone(sv.StartEnv)
		}
	}
	return nil
}

// runMarker in a server's command makes the fake run it, through sh, as
// lux would (a server command is otherwise only played): a test's server
// can write what it sees where the test reads it.
const runMarker = "fakelux-run"

// processEnv is what lux's shim gives a server's process: the spec's env,
// each env secret's value lux holds for the placement (a credential or a
// registry login is the runner's alone), then the server's own env.
// Callers hold s.mu.
func (s *Server) processEnv(run *Run, sv *server) map[string]string {
	var spec lux.Spec
	_ = json.Unmarshal(run.Spec, &spec)
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	runnerOnly := runnerOnlySecrets(run.Spec)
	for _, sec := range spec.Secrets {
		if (sec.As == "env" || sec.As == "") && !runnerOnly[sec.Name] && !strings.HasPrefix(sec.Name, "LUX_") {
			env[sec.Name] = run.secretValues[sec.Name]
		}
	}
	maps.Copy(env, sv.Env)
	return env
}

// runnerOnlySecrets are a spec's git and registry credentials: lux marks
// them runnerOnly, whatever their as, and never puts them in the container.
func runnerOnlySecrets(rawSpec json.RawMessage) map[string]bool {
	var spec lux.Spec
	_ = json.Unmarshal(rawSpec, &spec)
	out := map[string]bool{}
	if spec.Git != nil {
		for _, r := range spec.Git.Repositories {
			if r.Credential != "" {
				out[r.Credential] = true
			}
		}
	}
	for _, a := range spec.Image.RegistryAuth {
		out[a.Secret] = true
	}
	return out
}

// runCommand runs a server's command through sh -c, once, in the Run's
// checkout when it has one, with the environment processEnv gave it. What
// it writes reaches the server's log on its own stream, and its exit's
// error on stderr, each redacted against the placement's secret values,
// as lux's shim writes them. Callers hold s.mu.
func (s *Server) runCommand(run *Run, sv *server) {
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	if _, err := s.checkout(run, spec); err != nil {
		sv.logf("stderr", "could not check out: %v", err)
	}
	cmd := exec.Command(sv.Command[0], s.inWorkspace(run, sv.Command[1:])...)
	cmd.Dir = run.workspace
	cmd.Env = os.Environ()
	for k, v := range sv.StartEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	values := maps.Clone(run.secretValues)
	go func() {
		err := cmd.Run()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, out := range []struct{ stream, text string }{{"stdout", stdout.String()}, {"stderr", stderr.String()}} {
			if out.text != "" {
				sv.logf(out.stream, "%s", redact(values, out.text))
			}
		}
		if err != nil {
			sv.logf("stderr", "%s", redact(values, err.Error()))
		}
	}()
}

// secretValues are the values lux holds for a Run's next placement: each
// secret its spec declares, from given when given has it, else from the
// spec as submitted. lux keeps no value past a placement, so a resume
// brings them again; a name the Run did not declare is ignored, as lux
// v0.1.11 does.
func secretValues(rawSpec json.RawMessage, given []lux.Secret) map[string]string {
	var spec lux.Spec
	_ = json.Unmarshal(rawSpec, &spec)
	out := map[string]string{}
	for _, sec := range spec.Secrets {
		out[sec.Name] = sec.Value
		if i := slices.IndexFunc(given, func(g lux.Secret) bool { return g.Name == sec.Name }); i >= 0 {
			out[sec.Name] = given[i].Value
		}
	}
	return out
}

// runLive: lux edits a Run's servers in any state but a terminal one.
func runLive(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled":
		return false
	}
	return true
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"servers": s.serverViews(run)})
}

// serverViews: callers hold s.mu.
func (s *Server) serverViews(run *Run) []any {
	out := []any{}
	for _, sv := range run.servers {
		out = append(out, sv.view(run, s.PreviewDomain))
	}
	return out
}

func (s *Server) addServer(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var in serverInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if msg := in.check(true); msg != "" {
		writeErr(w, 422, "invalid_server", msg)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run.Calls = append(run.Calls, "server.add "+in.Name)
	switch {
	case !runLive(run.State):
		writeErr(w, 409, "run_ended", "run is "+run.State)
		return
	case run.server(in.Name) != nil:
		writeErr(w, 409, "name_taken", "a server named "+in.Name+" exists")
		return
	}
	sv := &server{Name: in.Name, Port: in.Port, Command: in.Command, Workdir: in.Workdir, Env: in.Env,
		State: lux.ServerStopped, Since: time.Now(), Epoch: run.Epoch}
	run.servers = append(run.servers, sv)
	s.luxEvent(run, "server.added", map[string]any{"name": sv.Name})
	start := in.Command != nil
	if in.Start != nil {
		start = *in.Start
	}
	if start && run.State == "running" {
		s.startServer(run, sv)
	}
	writeJSON(w, 201, sv.view(run, s.PreviewDomain))
}

func (s *Server) putServer(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var in serverInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if msg := in.check(false); msg != "" {
		writeErr(w, 422, "invalid_server", msg)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sv := run.server(r.PathValue("name"))
	if sv == nil {
		writeErr(w, 404, "not_found", "no such server")
		return
	}
	// Changes apply on its next start.
	sv.Port, sv.Command, sv.Workdir, sv.Env = in.Port, in.Command, in.Workdir, in.Env
	writeJSON(w, 200, sv.view(run, s.PreviewDomain))
}

func (s *Server) serverAction(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	action := r.PathValue("action")
	s.mu.Lock()
	defer s.mu.Unlock()
	sv := run.server(r.PathValue("name"))
	if sv == nil {
		writeErr(w, 404, "not_found", "no such server")
		return
	}
	run.Calls = append(run.Calls, "server."+action+" "+sv.Name)
	if run.State != "running" {
		writeErr(w, 409, "not_running", "run is "+run.State)
		return
	}
	switch action {
	case "start", "restart":
		if sv.Command == nil && action == "restart" {
			writeErr(w, 409, "no_command", "the server has no command")
			return
		}
		if action == "start" && (sv.State == lux.ServerStarting || sv.State == lux.ServerReady) {
			break // already
		}
		s.startServer(run, sv)
	case "stop":
		if sv.State != lux.ServerStopped {
			s.stopServer(run, sv, "stopped")
		}
	default:
		writeErr(w, 404, "not_found", "no such action")
		return
	}
	writeJSON(w, 200, sv.view(run, s.PreviewDomain))
}

func (s *Server) removeServer(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.PathValue("name")
	sv := run.server(name)
	if sv == nil {
		writeErr(w, 404, "not_found", "no such server")
		return
	}
	if !runLive(run.State) {
		writeErr(w, 409, "run_ended", "run is "+run.State)
		return
	}
	run.Calls = append(run.Calls, "server.remove "+name)
	if sv.State != lux.ServerStopped {
		s.stopServer(run, sv, "stopped")
	}
	for i, x := range run.servers {
		if x == sv {
			run.servers = append(run.servers[:i], run.servers[i+1:]...)
			break
		}
	}
	s.luxEvent(run, "server.removed", map[string]any{"name": name})
	w.WriteHeader(204)
}

func (s *Server) serverLog(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	tail := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && v > 0 {
		tail = v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sv := run.server(r.PathValue("name"))
	if sv == nil {
		writeErr(w, 404, "not_found", "no such server")
		return
	}
	lines := sv.log
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	if lines == nil {
		lines = []logLine{}
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}
