// Package fakelux is a stand-in for lux, for dude's tests.
//
// It implements the endpoints dude calls, records every RunSpec it is sent,
// and plays each Run from a script instead of starting a container: the
// agent takes its task, "writes" the reply the test chose, optionally
// "commits", and goes idle. A push lands a real commit in a real git
// repository, so the forge side of a test — fast-forward, compare, pull
// request — runs against real history.
//
// It exists so dude tests what it sends lux and what it does with lux's
// answers, without testing lux itself. The contract between the two is
// pinned separately, against a real lux.
package fakelux

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Behaviour is what a Run's agent does. Chosen per Run by the test, from the
// spec it was submitted with.
type Behaviour struct {
	// The agent's reply, streamed as message chunks.
	Reply string
	// Files to commit, path → content; none means the agent changes nothing.
	Commit map[string]string
	// The commit's message.
	Message string
	// Tool calls to report before replying.
	Tools []string
	// Never finish the turn: for steering, pausing and aborting a live agent.
	Hang bool
	// Exit instead of going idle, as a crashed agent does.
	Crash bool
}

type Run struct {
	ID          string
	Spec        json.RawMessage
	State       string
	Epoch       int
	SessionID   string
	Inputs      []string
	Pushed      string
	Cancelled   bool
	Stopped     int
	Resumed     int
	Interrupted int
	// Forgotten: lux lost it. Open streams drop, as lux's connection would.
	Forgotten bool

	repo     string
	busy     bool
	woken    bool
	queued   []queuedInput
	records  []record
	events   []event
	behavior Behaviour
	cond     *sync.Cond
}

type queuedInput struct{ text, requestID string }

// deliverQueued hands queued input to an idle agent, which takes it as a
// new turn. Callers hold s.mu.
func (s *Server) deliverQueued(run *Run) {
	if len(run.queued) == 0 || run.State != "running" {
		return
	}
	for _, in := range run.queued {
		run.Inputs = append(run.Inputs, in.text)
		s.recordEvent(run, "lux.input", map[string]any{"requestId": in.requestID})
	}
	run.queued = nil
	// Input after a resume is what a paused agent was waiting for: it
	// finishes its work this time.
	run.woken = run.Resumed > 0
	s.turn(run)
}

type record struct {
	Seq   int64
	Epoch int
	Event map[string]any
}

type event struct {
	ID    int64
	Epoch int
	Type  string
	Data  map[string]any
}

type Server struct {
	mu     sync.Mutex
	runs   map[string]*Run
	byKey  map[string]string
	next   int
	nextEv int64
	// Decide chooses each Run's behaviour from its spec.
	Decide func(spec map[string]any) Behaviour
	// Repo is a bare git repository pushes land in, as `git push` would.
	Repo string
	// RepoFor, when set, finds the repository from a Run's spec instead: one
	// fake lux serving tests that each have their own repository.
	RepoFor func(url string) string
	// Key is the API key the fake accepts.
	Key string
}

// New serves a fake lux. With decide nil, every Run plays dude's scripted
// agent (fakeagent), as a real lux running lux-fake would.
func New(repo, key string, decide func(map[string]any) Behaviour) *Server {
	s := &Server{runs: map[string]*Run{}, byKey: map[string]string{}, Decide: decide, Repo: repo, Key: key}
	if decide == nil {
		s.Decide = s.scripted
	}
	return s
}

// scripted plays fakeagent's step for the Run's phase, deciding the
// reviewer's verdict from the tree it checks out.
func (s *Server) scripted(spec map[string]any) Behaviour {
	labels, _ := spec["labels"].(map[string]any)
	str := func(k string) string { v, _ := labels[k].(string); return v }
	repo := s.Repo
	if s.RepoFor != nil {
		repo = s.RepoFor(SpecField(spec, "url"))
	}
	fixed := exec.Command("git", "-C", repo, "cat-file", "-e", SpecField(spec, "ref")+":"+fakeagent.FixedFile).Run() == nil
	step := fakeagent.For(str("dude.phase"), str("dude.model"), str("dude.run"), fixed)
	files := map[string]string{}
	for path, line := range step.Commit {
		files[path] = line + "\n"
	}
	// Every phase plans and looks around first, as an agent does.
	return Behaviour{Reply: step.Reply, Commit: files, Message: step.Message, Hang: step.Hang, Tools: []string{"todowrite", "read"}}
}

// Runs returns every Run submitted, in order.
func (s *Server) Runs() []*Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Run, 0, len(s.runs))
	for i := 1; i <= s.next; i++ {
		out = append(out, s.runs[fmt.Sprintf("lrun_%d", i)])
	}
	return out
}

// Forget drops every Run, as a lux that lost its data would.
func (s *Server) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		run.Forgotten = true
		run.cond.Broadcast()
	}
	s.runs = map[string]*Run{}
	s.byKey = map[string]string{}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs", s.submit)
	mux.HandleFunc("GET /v1/runs/{id}", s.get)
	mux.HandleFunc("GET /v1/runs/{id}/output", s.output)
	mux.HandleFunc("POST /v1/runs/{id}/input", s.input)
	mux.HandleFunc("POST /v1/runs/{id}/push", s.push)
	mux.HandleFunc("POST /v1/runs/{id}/stop", s.stop)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/runs/{id}/resume", s.resume)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.Key {
			writeErr(w, 401, "unauthorized", "invalid API key")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	var spec map[string]any
	_ = json.Unmarshal(raw, &spec)
	s.mu.Lock()
	if id, ok := s.byKey[r.Header.Get("Idempotency-Key")]; ok && id != "" {
		run := s.runs[id]
		s.mu.Unlock()
		writeJSON(w, 200, s.view(run))
		return
	}
	s.next++
	run := &Run{ID: fmt.Sprintf("lrun_%d", s.next), Spec: raw, State: "submitted", Epoch: 1}
	run.cond = sync.NewCond(&s.mu)
	run.behavior = s.Decide(spec)
	run.repo = s.Repo
	if s.RepoFor != nil {
		run.repo = s.RepoFor(SpecField(spec, "url"))
	}
	s.runs[run.ID] = run
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		s.byKey[k] = run.ID
	}
	s.mu.Unlock()
	go s.play(run, spec, false)
	writeJSON(w, 201, s.view(run))
}

// play is the agent's life: start, check out, take the task, work, go idle.
func (s *Server) play(run *Run, spec map[string]any, resumed bool) {
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setState(run, "running")
	if !resumed {
		base := head(run.repo, SpecField(spec, "ref"))
		s.luxEvent(run, "git.checkout", map[string]any{"repo": "target", "ref": SpecField(spec, "ref"), "base": base})
		run.SessionID = fmt.Sprintf("ses_%s", run.ID)
	}
	// As lux's shim does: in the record stream, in order with the agent's
	// own messages.
	s.recordEvent(run, "lux.session", map[string]any{"sessionId": run.SessionID})
	s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
	if resumed {
		// A resumed agent has its conversation back and waits for input, as
		// lux resumes one; what it is given next is a new turn.
		s.deliverQueued(run)
		return
	}
	s.turn(run)
}

// turn is the agent working on what it was given, and going idle.
// Callers hold s.mu.
func (s *Server) turn(run *Run) {
	s.recordEvent(run, "lux.activity", map[string]any{"activity": "busy"})
	run.busy = true
	b := run.behavior
	for i, tool := range b.Tools {
		id := fmt.Sprintf("call_%d", i)
		input := map[string]any{"cmd": tool}
		if tool == "todowrite" {
			input = map[string]any{"todos": []any{map[string]any{"content": "do it", "status": "in_progress"}}}
		}
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": tool, "kind": "execute", "status": "in_progress", "rawInput": input})
		// As OpenCode reports it: the completion names neither the tool nor its kind.
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"})
	}
	if b.Hang && !run.woken {
		return
	}
	if b.Crash {
		s.setState(run, "failed")
		return
	}
	for _, chunk := range chunks(b.Reply, 7) {
		s.agent(run, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": chunk}})
	}
	s.agent(run, map[string]any{"sessionUpdate": "usage_update", "cost": map[string]any{"amount": 0.01, "currency": "USD"}, "used": 1000})
	s.recordEvent(run, "acp.turn_end", map[string]any{"stopReason": "end_turn"})
	s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
	run.busy = false
	s.deliverQueued(run)
}

// SpecField reads a field of the spec's first repository ("url", "ref").
func SpecField(spec map[string]any, name string) string {
	git, _ := spec["git"].(map[string]any)
	repos, _ := git["repositories"].([]any)
	if len(repos) == 0 {
		return ""
	}
	r, _ := repos[0].(map[string]any)
	v, _ := r[name].(string)
	return v
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// Callers hold s.mu.
func (s *Server) setState(run *Run, state string) {
	run.State = state
	s.luxEvent(run, "state", map[string]any{"state": state})
}

func (s *Server) luxEvent(run *Run, typ string, data map[string]any) {
	s.nextEv++
	run.events = append(run.events, event{ID: s.nextEv, Epoch: run.Epoch, Type: typ, Data: data})
	run.cond.Broadcast()
}

func (s *Server) agent(run *Run, update map[string]any) {
	s.recordEvent(run, "acp."+update["sessionUpdate"].(string), update)
}

func (s *Server) recordEvent(run *Run, typ string, data map[string]any) {
	run.records = append(run.records, record{Seq: int64(len(run.records) + 1), Epoch: run.Epoch, Event: map[string]any{"type": typ, "data": data}})
	run.cond.Broadcast()
}

// head resolves ref (a branch or sha; "" is the default branch) in repo.
func head(repo, ref string) string {
	if ref == "" {
		ref = "HEAD"
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Server) view(run *Run) map[string]any {
	return map[string]any{"id": run.ID, "state": run.State, "epoch": run.Epoch, "sessionId": run.SessionID}
}

func (s *Server) find(w http.ResponseWriter, r *http.Request) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[r.PathValue("id")]
	if run == nil {
		writeErr(w, 404, "not_found", "not found")
	}
	return run
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if run := s.find(w, r); run != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, 200, s.view(run))
	}
}

func (s *Server) input(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var in struct {
		Text      string `json:"text"`
		RequestID string `json:"requestId"`
		Interrupt bool   `json:"interrupt"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	// lux takes input for a Run that is live or about to be, and delivers
	// it once the agent can take it.
	switch run.State {
	case "running", "resuming", "submitted", "scheduled", "starting":
	default:
		writeErr(w, 409, "not_running", "run is "+run.State)
		return
	}
	// As lux's ACP adapter does: ACP has no way to add to a turn in
	// progress, so input to a busy agent waits for the turn to end, and is
	// acknowledged when it is actually delivered.
	run.queued = append(run.queued, queuedInput{in.Text, in.RequestID})
	if in.Interrupt && run.busy && run.State == "running" {
		// The turn is cancelled, and the agent is free to hear it.
		run.Interrupted++
		s.recordEvent(run, "acp.turn_end", map[string]any{"stopReason": "cancelled"})
		run.busy = false
	}
	if !run.busy {
		s.deliverQueued(run)
	}
	writeJSON(w, 202, map[string]any{"requestId": in.RequestID})
}

// push commits the Run's files to its push branch in the repository, from
// the commit it checked out.
func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var in struct {
		RequestID string `json:"requestId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	var spec struct {
		Git struct {
			Repositories []struct {
				Ref string `json:"ref"`
			} `json:"repositories"`
			Push *struct {
				Branch string `json:"branch"`
			} `json:"push"`
		} `json:"git"`
	}
	_ = json.Unmarshal(run.Spec, &spec)
	if spec.Git.Push == nil {
		writeErr(w, 409, "no_push", "the run's spec has no git.push branch")
		return
	}
	writeJSON(w, 202, map[string]any{"requestId": in.RequestID})
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		base := ""
		if len(spec.Git.Repositories) > 0 {
			base = head(run.repo, spec.Git.Repositories[0].Ref)
		}
		result := map[string]any{"repo": "target", "branch": spec.Git.Push.Branch}
		sha, err := commit(run.repo, base, spec.Git.Push.Branch, run.behavior.Commit, run.behavior.Message)
		switch {
		case err != nil:
			result["status"], result["error"] = "failed", err.Error()
		case sha == base:
			result["status"], result["commit"] = "up-to-date", sha
		default:
			result["status"], result["commit"] = "pushed", sha
			run.Pushed = sha
		}
		s.luxEvent(run, "git.push", map[string]any{"requestId": in.RequestID, "results": []any{result}})
	}()
}

// commit writes files on top of base and points branch at the result.
func commit(repo, base, branch string, files map[string]string, message string) (string, error) {
	if message == "" {
		message = "agent work"
	}
	if len(files) == 0 {
		return base, nil
	}
	env := []string{"GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=a@x",
		"GIT_INDEX_FILE=" + repo + "/fakelux.index"}
	git := func(args ...string) (string, error) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(c.Environ(), env...)
		out, err := c.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("read-tree", base); err != nil {
		return "", err
	}
	for path, content := range files {
		blob := exec.Command("git", "-C", repo, "hash-object", "-w", "--stdin")
		blob.Stdin = strings.NewReader(content)
		out, err := blob.Output()
		if err != nil {
			return "", err
		}
		if _, err := git("update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(out))+","+path); err != nil {
			return "", err
		}
	}
	tree, err := git("write-tree")
	if err != nil {
		return "", err
	}
	sha, err := git("commit-tree", tree, "-p", base, "-m", message)
	if err != nil {
		return "", err
	}
	_, err = git("update-ref", "refs/heads/"+branch, sha)
	return sha, err
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run.Stopped++
	run.busy = false
	if run.State == "running" {
		s.setState(run, "stopped")
	}
	writeJSON(w, 202, s.view(run))
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run.Cancelled = true
	if run.State != "cancelled" {
		s.setState(run, "cancelled")
	}
	writeJSON(w, 202, s.view(run))
}

// resume starts a new placement; the agent continues its session.
func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var in struct {
		Secrets []struct {
			Name, Value string
		} `json:"secrets"`
		Input *struct {
			Text string `json:"text"`
		} `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	if run.State != "stopped" && run.State != "failed" && run.State != "lost" {
		s.mu.Unlock()
		writeErr(w, 409, "not_resumable", "run is "+run.State)
		return
	}
	run.Resumed++
	run.Epoch++
	if in.Input != nil {
		run.Inputs = append(run.Inputs, in.Input.Text)
	}
	s.setState(run, "resuming")
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	s.mu.Unlock()
	go s.play(run, spec, true)
	writeJSON(w, 202, s.view(run))
}

// output streams a Run's records and events as lux does: from a cursor,
// following until the Run stops.
func (s *Server) output(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var sinceEpoch int
	var sinceSeq int64
	if c := r.URL.Query().Get("since"); c != "" {
		fmt.Sscanf(c, "%d.%d", &sinceEpoch, &sinceSeq)
	}
	var afterEvent int64
	fmt.Sscan(r.URL.Query().Get("afterEvent"), &afterEvent)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	send := func(kind string, v any) bool {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	done := make(chan struct{})
	go func() {
		<-r.Context().Done()
		s.mu.Lock()
		close(done)
		run.cond.Broadcast()
		s.mu.Unlock()
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	sentRec := 0
	for {
		select {
		case <-done:
			return
		default:
		}
		if run.Forgotten {
			return
		}
		// Records first, then lifecycle events, as lux relays them: its
		// lifecycle events are flushed on a timer and trail the agent's own
		// records. A client that assumes the other order is wrong on lux.
		for ; sentRec < len(run.records); sentRec++ {
			rec := run.records[sentRec]
			if rec.Epoch < sinceEpoch || rec.Epoch == sinceEpoch && rec.Seq <= sinceSeq {
				continue
			}
			cursor := fmt.Sprintf("%d.%d", rec.Epoch, rec.Seq)
			if !send("record", map[string]any{"cursor": cursor, "epoch": rec.Epoch, "seq": rec.Seq, "ch": "event", "event": rec.Event}) {
				return
			}
		}
		for _, e := range run.events {
			if e.ID <= afterEvent {
				continue
			}
			afterEvent = e.ID
			if !send("lux", map[string]any{"id": e.ID, "epoch": e.Epoch, "type": e.Type, "data": e.Data}) {
				return
			}
		}
		if lux.Terminal(run.State) {
			send("end", map[string]any{"cursor": "", "state": run.State, "afterEvent": afterEvent})
			return
		}
		run.cond.Wait()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
