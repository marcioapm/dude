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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// Behaviour is what a Run's agent does. Chosen per Run by the test, from the
// spec it was submitted with.
type Behaviour struct {
	// The agent's reply, streamed as message chunks.
	Reply string
	// Files to commit, path → content; none means the agent changes nothing.
	// "<repo>:<path>" commits in that repository, "*:<path>" in every one
	// the Run may push, and a bare path in the first of those.
	Commit map[string]string
	// The commit's message.
	Message string
	// Tool calls to report before replying.
	Tools []string
	// What the agent thinks before it acts, streamed as thought chunks.
	Thought string
	// Tool output, per tool name, reported on completion as OpenCode's bash
	// tool does: both streams merged, and the exit code in metadata.
	ToolOutput map[string]string
	// Never finish the turn: for steering, pausing and aborting a live agent.
	Hang bool
	// Exit instead of going idle, as a crashed agent does.
	Crash bool
	// Exit right after finishing the turn, as a container stopped from
	// outside would: done, but no longer running when dude looks.
	ExitAfterTurn bool
	// Ask a person with dude's ask_person tool (these are its JSON
	// arguments) and end the first turn there; do the rest (Reply, Commit)
	// in the turn the answer starts.
	Ask string
	// Tools start and do not finish: a long command.
	KeepToolsOpen bool
	// Files the agent writes into $LUX_ARTIFACTS, name → content: listed as
	// the Run's artifacts once its container exits, as lux collects them.
	Publish map[string]string
	// Fail before the agent starts (a bad image, a failed clone): lux
	// reports such an exit without a snapshot, ever.
	FailToStart bool
	// dude tools to call, in order, before replying: tool name → JSON
	// arguments. Called as lux's service proxy would, with the header the
	// spec's services name, so dude sees an agent in its container.
	CallTools [][2]string
	// Files the agent writes into its checkout, path → content, each with
	// an edit tool call, in its first turn: uncommitted work, which exec
	// sees and the live diff shows. Paths are in the first repository.
	Edits map[string]string
	// Files written into $LUX_ARTIFACTS in its first turn, as it works —
	// before a Hang — rather than as it finishes (Publish).
	PublishNow map[string]string
	// Files written into its checkout in the turn that finishes, after a
	// Hang is woken.
	FinishEdits map[string]string
	// Every turn fails at once with this error, as lux's ACP adapter reports
	// a session/prompt the agent answered with a JSON-RPC error (a model it
	// cannot reach): turn_end carries it, formatted "session/prompt:
	// <message> (<code>)", and the agent goes idle.
	TurnError string
	// A Hang is woken by any input, not only one after a resume.
	WakeOnInput bool
}

type Run struct {
	ID        string
	Spec      json.RawMessage
	State     string
	Epoch     int
	SessionID string
	Inputs    []string
	// Some repository got a commit from a push.
	Pushed bool
	// dude tools the agent called: "tool status".
	ToolCalls   []string
	Cancelled   bool
	Stopped     int
	Resumed     int
	Interrupted int
	// Each input request's body as received (refused ones too), by request
	// id, in order: a retry of one request adds another.
	InputBodies map[string][]string
	// The secrets each accepted resume carried, in order, decoded and as
	// sent (every field of each descriptor).
	ResumeSecrets    [][]lux.Secret
	ResumeSecretsRaw []json.RawMessage
	// Forgotten: lux lost it. Open streams drop, as lux's connection would.
	Forgotten bool
	// What was asked of it, in order: "exec", "stop", "cancel".
	Calls []string
	// Each resume's sync and each POST /sync's, as received.
	ResumeSyncs [][]lux.SyncRef
	Syncs       [][]lux.SyncRef
	// Where each checkout is, by repository: its clone, then its syncs.
	at map[string]string
	// A resume's sync, applied when its placement starts.
	pendingSync []lux.SyncRef

	// Its servers (servers.go).
	servers []*server
	// What GET /cost answers; nil is lux's answer before any plugin priced
	// anything: pending, no amounts.
	cost *lux.RunCost

	// Starts of the Run, as lux lists them; the last is the current one.
	placements []*placement
	artifacts  []*artifact
	// Written into $LUX_ARTIFACTS by the agent's turns and not yet collected.
	published map[string]string

	// Its checkout, a real git clone made when first needed: the
	// container's /workspace.
	workspace string
	// Edit tool calls made, so each has an id of its own.
	edits int

	busy  bool
	woken bool
	// Turns the agent finished (went idle after), for TurnsEnded.
	turnsEnded int
	// Tool calls started and not finished (KeepToolsOpen), until FinishTools.
	openTools []string
	queued    []queuedInput
	records   []record
	events    []event
	behavior  Behaviour
	cond      *sync.Cond
}

// queuedInput is input the agent has not read yet. accepted: its accepted
// receipt is on the stream already.
type queuedInput struct {
	text, requestID string
	accepted        bool
}

// accept acknowledges input the harness took, as lux's shim does, once: in
// the legacy contract there is no accepted receipt, only the delivery.
// Callers hold s.mu.
func (s *Server) accept(run *Run, in *queuedInput) {
	if s.LegacyInput || in.accepted {
		return
	}
	in.accepted = true
	s.recordAccepted(run, in.requestID, in.text)
}

// recordAccepted writes the harness's accepted answer, with a read receipt
// to follow. Callers hold s.mu.
func (s *Server) recordAccepted(run *Run, requestID, text string) {
	lands := "next_step"
	if s.NextTurnInput {
		lands = "next_turn"
	}
	s.recordEvent(run, lux.RecordInput, map[string]any{"requestId": requestID, "phase": lux.InputAccepted, "receipt": true,
		"lands": lands, "text": text})
}

// recordFailed writes the failure of input the agent never read, in the
// shape this lux writes it. Callers hold s.mu.
func (s *Server) recordFailed(run *Run, in queuedInput, reason string) {
	switch {
	case s.LegacyInput:
		s.recordEvent(run, lux.RecordInput, map[string]any{"requestId": in.requestID, "error": reason})
	case in.accepted:
		s.recordEvent(run, lux.RecordInputFailed, map[string]any{"requestId": in.requestID, "error": reason})
	default:
		s.recordEvent(run, lux.RecordInput, map[string]any{"requestId": in.requestID, "phase": lux.InputFailed, "error": reason})
	}
}

// completeOpenTools finishes the tool calls KeepToolsOpen left running.
// Callers hold s.mu.
func (s *Server) completeOpenTools(run *Run) {
	for _, call := range run.openTools {
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": call, "status": "completed"})
	}
	run.openTools = nil
}

// consume is the agent's step reading everything queued: its read receipt
// (or, from a legacy lux, its one acknowledgement). Callers hold s.mu.
func (s *Server) consume(run *Run) {
	for _, in := range run.queued {
		run.Inputs = append(run.Inputs, in.text)
		if s.LegacyInput {
			s.recordEvent(run, lux.RecordInput, map[string]any{"requestId": in.requestID, "text": in.text})
		} else {
			s.recordEvent(run, lux.RecordInputConsumed, map[string]any{"requestId": in.requestID})
		}
	}
	run.queued = nil
}

// FinishTools completes the Run's open tool calls (KeepToolsOpen), as a
// long command finishing: the agent's next step starts, and reads what it
// was steered with while the tool ran, in the same turn — unless the
// harness reads input only between turns, or the lux is a legacy one that
// holds it until then. A Hang agent carries on hanging.
func (s *Server) FinishTools(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil || run.State != "running" {
		return
	}
	s.completeOpenTools(run)
	if run.busy && !s.NextTurnInput && !s.LegacyInput {
		s.consume(run)
	}
}

// FailInput is the harness failing input the agent has not read (an agent
// error), as lux reports it: lux.input.failed after its accepted answer,
// else a failed first answer (an older lux: lux.input with the error).
func (s *Server) FailInput(id, requestID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil {
		return
	}
	for i, q := range run.queued {
		if q.requestID == requestID {
			run.queued = slices.Delete(run.queued, i, i+1)
			s.recordFailed(run, q, reason)
			return
		}
	}
}

// EndTurn ends a busy agent's turn as the agent would on its own (a Hang
// agent finishing, not interrupted): what it was steered with and has not
// read is read now, as the input of its next turn.
func (s *Server) EndTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil || run.State != "running" || !run.busy {
		return
	}
	s.completeOpenTools(run)
	s.recordEvent(run, "acp.turn_end", map[string]any{"stopReason": "end_turn"})
	s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
	run.busy = false
	run.turnsEnded++
	s.deliverQueued(run)
}

type placement struct {
	Epoch int
	// The host it ran on (host-<epoch>: each start is on another).
	HostName string
	// running, then exited.
	State                                       string
	WorkloadStartedAt, ExitedAt, SnapshotDoneAt *time.Time
}

type artifact struct {
	ID, Path, ContentType, SHA256 string
	Epoch                         int
	Size                          int64
	Content                       string
}

// deliverQueued hands queued input to an idle agent, which takes it as a
// new turn. Callers hold s.mu.
func (s *Server) deliverQueued(run *Run) {
	if len(run.queued) == 0 || run.State != "running" {
		return
	}
	for i := range run.queued {
		s.accept(run, &run.queued[i])
	}
	s.consume(run)
	// Input after a resume is what a paused agent was waiting for: it
	// finishes its work this time. Without input it waits, as a real one
	// does. WakeOnInput has any input wake it, a nudge included.
	run.woken = run.Resumed > 0 || run.behavior.WakeOnInput
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
	mu      sync.Mutex
	runs    map[string]*Run
	byKey   map[string]string
	next    int
	nextEv  int64
	nextArt int
	// Decide chooses each Run's behaviour from its spec.
	Decide func(spec map[string]any) Behaviour
	// Repo is a bare git repository pushes land in, as `git push` would.
	Repo string
	// RepoFor, when set, finds the repository from a Run's spec instead: one
	// fake lux serving tests that each have their own repository.
	RepoFor func(url string) string
	// Key is the API key the fake accepts.
	Key string
	// Workspaces is where each Run's checkout is made; "" is the system's
	// temporary directory.
	Workspaces string
	// How long a started server with a command takes to be ready; zero is
	// 30ms.
	ServerReadyAfter time.Duration
	// How long a submitted or resumed Run takes to start; zero is 20ms.
	StartAfter time.Duration
	// The preview domain servers' URLs are under; "" gives them none, as a
	// lux without previews configured.
	PreviewDomain string
	// The port in preview URLs, when not the scheme's (a local demo).
	PreviewPort int
	// How often idle servers are looked for; zero is 100ms.
	IdleCheck time.Duration
	// Tenant servers deleted or expired, in order.
	DeletedServers []string
	// The tenant's servers and its event feed (tenant.go).
	feedState
	// How lux acknowledges input. By default as lux does now: "accepted"
	// when the harness takes it (at once, even mid-turn), "consumed" when
	// the agent's next step reads it — after the tool it was running
	// finishes, or at the turn's end. A Hang agent mid-step with no tool
	// open reaches no boundary until its turn ends.
	//
	// LegacyInput is a lux from before the two receipts: input to a busy
	// agent waits for its turn to end, and is acknowledged once, with no
	// phase, when handed over. NextTurnInput is a harness that reads input
	// only between turns: accepted at once with lands "next_turn", read
	// when the turn ends.
	LegacyInput, NextTurnInput bool
	// FailUnreadOnInterrupt is a lux from before interrupts carried unread
	// input over: an interrupt fails what the harness took and the agent had
	// not read ("the turn was cancelled before the agent read it"), rather
	// than starting the next turn with it. With LegacyInput, which is what
	// production lux is today, that is everything queued, failed with a
	// phase-less lux.input {requestId, error}.
	FailUnreadOnInterrupt bool
	// BeforeInput, when set, runs as each input request arrives, before the
	// fake acts on it; false refuses the request (503), as a lux that is
	// briefly unavailable. Called without the fake's lock.
	BeforeInput func(runID, requestID string) bool
	// InputGate, when set, holds what input starts in an idle agent (its
	// receipts, and the turn it takes) until the channel is closed. The
	// request is answered first, as lux answers the POST before the
	// agent's records arrive.
	InputGate chan struct{}

	// Pools is what GET /v1/pools lists; nil is DefaultPools. POST
	// /v1/pools adds one, or updates the one of its name; DELETE
	// /v1/pools/{name} removes one. A submit naming a placement.poolId not
	// in it is refused (422 unknown_pool), as lux does.
	Pools    []lux.Pool
	nextPool int
	// MemoryShare is the part of what a Run asks for that its container is
	// given, reported as each placement's memoryLimit (a newer lux); zero
	// reports none, as today's lux.
	MemoryShare float64
	// Starts still to fail, by spec label "key=value" (FailStarts).
	failStarts map[string]int
	// closed ends everything the fake waits on in the background (Close).
	closed    chan struct{}
	closeOnce sync.Once
}

// Close is the fake lux shutting down: work held on InputGate is dropped,
// so no goroutine outlives the test that served it.
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.closed) })
}

// New serves a fake lux. With decide nil, every Run plays dude's scripted
// agent (fakeagent), as a real lux running lux-fake would.
func New(repo, key string, decide func(map[string]any) Behaviour) *Server {
	s := &Server{runs: map[string]*Run{}, byKey: map[string]string{}, Decide: decide, Repo: repo, Key: key,
		closed: make(chan struct{})}
	s.feedInit()
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
	// The fix is in if any repository the reviewer checked out holds it.
	fixed := slices.ContainsFunc(specRepos(spec), func(r specRepo) bool {
		return exec.Command("git", "-C", s.repoPath(r.URL), "cat-file", "-e", r.Ref+":"+fakeagent.FixedFile).Run() == nil
	})
	step := fakeagent.For(str("dude.phase"), str("dude.model"), str("dude.run"), fixed)
	if str("dude.phase") == "review" && len(specRepos(spec)) == 0 {
		// No checkout: the reviewer reads what was published, and the
		// scripted one is content with it.
		step.Reply = fakeagent.NothingToReview
	}
	files := map[string]string{}
	for path, line := range step.Commit {
		files[path] = line + "\n"
	}
	published := map[string]string{}
	for name, text := range step.Publish {
		published[name] = text + "\n"
	}
	// Every phase plans and looks around first, as an agent does.
	return Behaviour{Reply: step.Reply, Commit: files, Message: step.Message, Hang: step.Hang, Ask: step.Ask,
		Publish: published, Tools: []string{"todowrite", "read"}, CallTools: step.Tools, Edits: step.Edits, PublishNow: step.PublishNow,
		FinishEdits: step.FinishEdits}
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

// CallsOf is what was asked of a Run, in order: "exec", "stop", "cancel".
func (s *Server) CallsOf(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return slices.Clone(run.Calls)
	}
	return nil
}

// TurnsEnded is closed once the Run's agent has finished n turns and gone
// idle: a lifecycle boundary a test waits on instead of a sleep.
func (s *Server) TurnsEnded(id string, n int) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for {
			run := s.runs[id]
			if run != nil && run.turnsEnded >= n {
				close(done)
				return
			}
			if run == nil || run.Forgotten || lux.Terminal(run.State) {
				return
			}
			run.cond.Wait()
		}
	}()
	return done
}

// FailStarts makes the next n starts (a submit's or a resume's placement)
// of Runs whose spec has the label "key=value" fail before the workload
// runs, as lux reports a container that would not start; n <= 0 clears it.
func (s *Server) FailStarts(label string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStarts == nil {
		s.failStarts = map[string]int{}
	}
	if n <= 0 {
		delete(s.failStarts, label)
		return
	}
	s.failStarts[label] = n
}

// takeFailStart uses up one of FailStarts' for a Run of this spec.
// Callers hold s.mu.
func (s *Server) takeFailStart(spec map[string]any) bool {
	labels, _ := spec["labels"].(map[string]any)
	for k, v := range labels {
		key := fmt.Sprintf("%s=%v", k, v)
		if s.failStarts[key] > 0 {
			s.failStarts[key]--
			return true
		}
	}
	return false
}

// Crash ends a Run's agent as a dead container would.
func (s *Server) Crash(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		s.setState(run, "failed")
	}
}

// SetCost sets what lux's cost API answers for a Run, as its cost plugins
// would have priced it.
func (s *Server) SetCost(id string, cost lux.RunCost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		cost.RunID = id
		// lux always sends every amount; a test that set none means zero.
		for i := range cost.ByFamily {
			f := &cost.ByFamily[i]
			zeroIfUnset(&f.Amount)
			zeroIfUnset(&f.Final, &f.Estimate)
		}
		for i := range cost.Totals {
			t := &cost.Totals[i]
			zeroIfUnset(&t.Amount, &t.Final, &t.Estimate)
		}
		for i := range cost.Lines {
			zeroIfUnset(&cost.Lines[i].Amount)
		}
		run.cost = &cost
	}
}

func zeroIfUnset[T ~string](amounts ...*T) {
	for _, a := range amounts {
		if *a == "" {
			*a = "0"
		}
	}
}

func (s *Server) getCost(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := lux.RunCost{RunID: run.ID, Status: lux.CostPending, Basis: "list",
		Totals: []lux.CostAmount{}, ByFamily: []lux.FamilyCost{}, Lines: []lux.CostLine{}, Sources: []lux.CostSource{}}
	if run.cost != nil {
		out = *run.cost
	}
	writeJSON(w, 200, out)
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
	mux.HandleFunc("GET /v1/runs/{id}/artifacts", s.listArtifacts)
	mux.HandleFunc("GET /v1/artifacts/{aid}", s.downloadArtifact)
	mux.HandleFunc("GET /v1/runs/{id}/exec", s.exec)
	mux.HandleFunc("GET /v1/runs/{id}/servers", s.listServers)
	mux.HandleFunc("POST /v1/runs/{id}/servers", s.addServer)
	mux.HandleFunc("PUT /v1/runs/{id}/servers/{name}", s.putServer)
	mux.HandleFunc("POST /v1/runs/{id}/servers/{name}/{action}", s.serverAction)
	mux.HandleFunc("DELETE /v1/runs/{id}/servers/{name}", s.removeServer)
	mux.HandleFunc("GET /v1/runs/{id}/servers/{name}/log", s.serverLog)
	mux.HandleFunc("GET /v1/runs/{id}/cost", s.getCost)
	mux.HandleFunc("GET /v1/pools", s.listPools)
	mux.HandleFunc("POST /v1/pools", s.putPool)
	mux.HandleFunc("DELETE /v1/pools/{name}", s.deletePool)
	mux.HandleFunc("POST /v1/runs/{id}/sync", s.syncRun)
	mux.HandleFunc("GET /v1/servers", s.listTenantServers)
	mux.HandleFunc("POST /v1/servers", s.createTenantServer)
	mux.HandleFunc("GET /v1/servers/{sid}", s.getTenantServer)
	mux.HandleFunc("DELETE /v1/servers/{sid}", s.deleteTenantServer)
	mux.HandleFunc("POST /v1/servers/{sid}/attach", s.attachTenantServer)
	mux.HandleFunc("POST /v1/servers/{sid}/detach", s.detachTenantServer)
	mux.HandleFunc("GET /v1/events", s.feedHandler)
	mux.HandleFunc("GET /v1/whoami", s.whoami)
	// Test hooks, not lux's: a signed-in browser request to a server's
	// hostname, and lux finding it idle.
	mux.HandleFunc("POST /fake/servers/{sid}/request", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"served": s.RequestServer(r.PathValue("sid"), r.URL.Query().Get("path"))})
	})
	mux.HandleFunc("POST /fake/servers/{sid}/idle", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"idle": s.Idle(r.PathValue("sid"))})
	})
	mux.HandleFunc("POST /fake/fail-starts", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		s.FailStarts(r.URL.Query().Get("label"), n)
		writeJSON(w, 200, map[string]any{"label": r.URL.Query().Get("label"), "n": n})
	})
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
	if msg := registryAuthProblem(raw); msg != "" {
		writeErr(w, 422, "invalid_spec", msg)
		return
	}
	s.mu.Lock()
	if id, ok := s.byKey[r.Header.Get("Idempotency-Key")]; ok && id != "" {
		run := s.runs[id]
		s.mu.Unlock()
		writeJSON(w, 200, s.view(run))
		return
	}
	if msg := s.placementProblem(raw); msg != "" {
		s.mu.Unlock()
		writeErr(w, 422, lux.CodeUnknownPool, msg)
		return
	}
	s.next++
	run := &Run{ID: fmt.Sprintf("lrun_%d", s.next), Spec: raw, State: "submitted", Epoch: 1}
	run.cond = sync.NewCond(&s.mu)
	if generic(spec) {
		run.behavior = Behaviour{}
	} else {
		run.behavior = s.Decide(spec)
	}
	s.specServers(run, spec)
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
	after := s.StartAfter
	if after <= 0 {
		after = 20 * time.Millisecond
	}
	time.Sleep(after)
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.behavior.FailToStart {
		run.placements = append(run.placements, &placement{Epoch: run.Epoch, State: "starting"})
		s.setState(run, "failed")
		return
	}
	if run.State != "cancelled" && s.takeFailStart(spec) {
		// As lux's runner reports a container that would not start (exit
		// 125): the placement never ran, and the Run is failed.
		run.placements = append(run.placements, &placement{Epoch: run.Epoch, State: "starting"})
		s.setStateWith(run, "failed", "start-failed")
		return
	}
	if run.State == "cancelled" {
		return // cancelled before it started
	}
	s.setState(run, "running")
	if !resumed {
		for _, repo := range specRepos(spec) {
			base := head(s.repoPath(repo.URL), repo.Ref)
			if base == "" {
				s.luxEvent(run, "git.clone", map[string]any{"repo": repo.Name, "ref": repo.Ref, "status": "failed", "error": "ref not found"})
				s.setState(run, "failed")
				return
			}
			s.luxEvent(run, "git.clone", map[string]any{"repo": repo.Name, "ref": repo.Ref, "status": "cloned", "commit": base})
			s.luxEvent(run, "git.checkout", map[string]any{"repo": repo.Name, "ref": repo.Ref, "base": base})
			if run.at == nil {
				run.at = map[string]string{}
			}
			run.at[repo.Name] = base
		}
		run.SessionID = fmt.Sprintf("ses_%s", run.ID)
	}
	if resumed && run.pendingSync != nil {
		// Restored checkouts move before init, as a resume's sync does.
		s.applySync(run, run.pendingSync, "")
		run.pendingSync = nil
	}
	// Every start of the Run starts its spec's servers.
	s.placementStarted(run)
	if generic(spec) {
		// No agent: the workload (sleep infinity) runs until stopped.
		return
	}
	// As lux's shim does: in the record stream, in order with the agent's
	// own messages.
	s.recordEvent(run, "lux.session", map[string]any{"sessionId": run.SessionID})
	s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
	if !resumed {
		// The task, acknowledged when the agent takes it, with what it got.
		prompt, _ := spec["workload"].(map[string]any)["prompt"].(string)
		if s.LegacyInput {
			s.recordEvent(run, lux.RecordInput, map[string]any{"requestId": "prompt", "text": prompt})
		} else {
			s.recordAccepted(run, "prompt", prompt)
			s.recordEvent(run, lux.RecordInputConsumed, map[string]any{"requestId": "prompt"})
		}
	}
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
	run.openTools = nil
	b := run.behavior
	if b.TurnError != "" {
		s.recordEvent(run, "acp.turn_end", map[string]any{"stopReason": "", "error": b.TurnError})
		s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
		run.busy = false
		return
	}
	for _, chunk := range chunks(b.Thought, 7) {
		s.agent(run, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": chunk}})
	}
	for i, tool := range b.Tools {
		id := fmt.Sprintf("call_%d", i)
		input := map[string]any{"cmd": tool}
		if tool == "todowrite" {
			input = map[string]any{"todos": []any{map[string]any{"content": "do it", "status": "in_progress"}}}
		}
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": tool, "kind": "execute", "status": "in_progress", "rawInput": input})
		if b.KeepToolsOpen {
			run.openTools = append(run.openTools, id)
			continue
		}
		// As OpenCode reports it: the completion names neither the tool nor
		// its kind, and a command's result is its merged output and exit code.
		done := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"}
		if out, ok := b.ToolOutput[tool]; ok {
			done["content"] = []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": out}}}
			done["rawOutput"] = map[string]any{"output": out, "metadata": map[string]any{"output": out, "exit": 3, "truncated": false}}
		}
		s.agent(run, done)
	}
	// Its tools are part of its first turn's work, not repeated on every
	// turn after; asking a person is one of them, and ends that turn.
	first := len(run.Inputs) == 0
	asking := b.Ask != "" && first
	if first {
		for _, c := range b.CallTools {
			s.callTool(run, c[0], c[1])
		}
		s.edit(run, b.Edits)
		for name, content := range b.PublishNow {
			if run.published == nil {
				run.published = map[string]string{}
			}
			run.published[name] = content
		}
	}
	if asking {
		s.callTool(run, "ask_person", b.Ask)
	}
	if b.Hang && !run.woken {
		return
	}
	if b.Crash {
		s.setState(run, "failed")
		return
	}
	if run.woken {
		s.edit(run, b.FinishEdits)
	}
	reply := b.Reply
	if asking {
		reply = "I asked; waiting for the answer."
	}
	for _, chunk := range chunks(reply, 7) {
		s.agent(run, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": chunk}})
	}
	if !asking && len(b.Publish) > 0 {
		if run.published == nil {
			run.published = map[string]string{}
		}
		for name, content := range b.Publish {
			run.published[name] = content
		}
	}
	s.agent(run, map[string]any{"sessionUpdate": "usage_update", "cost": map[string]any{"amount": 0.01, "currency": "USD"}, "used": 1000, "size": 200000})
	// The prompt's response, as the ACP adapter relays it: with the turn's
	// token usage.
	s.recordEvent(run, "acp.turn_end", map[string]any{"stopReason": "end_turn", "usage": map[string]any{
		"inputTokens": 12, "outputTokens": 34, "totalTokens": 1046, "cachedReadTokens": 900, "cachedWriteTokens": 100}})
	s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
	run.busy = false
	run.turnsEnded++
	if b.ExitAfterTurn {
		s.setState(run, "failed")
		return
	}
	s.deliverQueued(run)
}

// callTool calls one of dude's tools as the agent's container would through
// lux's service proxy: the spec's "dude" service, its header filled in from
// the spec's secret. (The CLI's own path through a socket is tested in
// agenttools.) Callers hold s.mu; the call is made without it, since dude
// may be slow. A failure is said in the agent's reply.
func (s *Server) callTool(run *Run, tool, args string) {
	var spec struct {
		Secrets  []lux.Secret `json:"secrets"`
		Workload struct {
			Services []lux.Service `json:"services"`
		} `json:"workload"`
	}
	_ = json.Unmarshal(run.Spec, &spec)
	for _, svc := range spec.Workload.Services {
		if svc.Name != "dude" {
			continue
		}
		req, err := http.NewRequest("POST", strings.TrimRight(svc.URL, "/")+"/tools/"+tool, strings.NewReader(args))
		if err != nil {
			return
		}
		for _, h := range svc.Headers {
			for _, sec := range spec.Secrets {
				if sec.Name == h.Secret {
					req.Header.Set(h.Name, sec.Value)
				}
			}
		}
		s.mu.Unlock()
		res, err := http.DefaultClient.Do(req)
		s.mu.Lock()
		// Said in the agent's reply, as an agent would report a tool it ran.
		switch {
		case err != nil:
			s.agent(run, map[string]any{"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{"type": "text", "text": "dude " + tool + " failed: " + err.Error() + "\n"}})
		case res.StatusCode >= 300:
			res.Body.Close()
			s.agent(run, map[string]any{"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{"type": "text", "text": fmt.Sprintf("dude %s answered %d\n", tool, res.StatusCode)}})
		default:
			res.Body.Close()
		}
	}
}

// Prompt is what the Run's agent was told.
func (r *Run) Prompt() string {
	var spec struct {
		Workload struct {
			Prompt string `json:"prompt"`
		} `json:"workload"`
	}
	_ = json.Unmarshal(r.Spec, &spec)
	return spec.Workload.Prompt
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
func (s *Server) setState(run *Run, state string) { s.setStateWith(run, state, "") }

// setStateWith records a state with lux's reason for it. Callers hold s.mu.
func (s *Server) setStateWith(run *Run, state, reason string) {
	run.State = state
	if state == "running" && (len(run.placements) == 0 || run.placements[len(run.placements)-1].Epoch != run.Epoch) {
		now := time.Now()
		run.placements = append(run.placements, &placement{Epoch: run.Epoch, HostName: fmt.Sprintf("host-%d", run.Epoch),
			State: "running", WorkloadStartedAt: &now})
	}
	if lux.Terminal(state) {
		s.exited(run)
		s.placementEnded(run, state, reason)
	}
	if state == "succeeded" || state == "cancelled" {
		// Never runs again: its owner servers are detached, as lux does.
		defer s.ownerRunEnded(run)
	}
	data := map[string]any{"state": state}
	if reason != "" {
		data["reason"] = reason
	}
	s.luxEvent(run, "state", data)
}

// exited is the container going away: lux collects what the agent put in
// $LUX_ARTIFACTS and, a moment later, the host reports it. The report trails
// the exit, as it does in lux, so dude must wait for it. Callers hold s.mu.
func (s *Server) exited(run *Run) {
	if len(run.placements) == 0 {
		return
	}
	p := run.placements[len(run.placements)-1]
	if p.ExitedAt != nil {
		return
	}
	now := time.Now()
	p.ExitedAt, p.State = &now, "exited"
	published := run.published
	run.published = nil
	if p.WorkloadStartedAt == nil {
		return // never started: lux sends no snapshot for it
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		s.mu.Lock()
		defer s.mu.Unlock()
		for name, content := range published {
			sum := sha256.Sum256([]byte(content))
			s.nextArt++
			run.artifacts = append(run.artifacts, &artifact{ID: fmt.Sprintf("art_%d", s.nextArt),
				Path: lux.PublishedPrefix + name, ContentType: mimeFor(name), SHA256: hex.EncodeToString(sum[:]),
				Epoch: p.Epoch, Size: int64(len(content)), Content: content})
		}
		done := time.Now()
		p.SnapshotDoneAt = &done
	}()
}

// mimeFor guesses a type from a name, as lux does from the file. Go reads
// its extension table from the host (/etc/mime.types), and macOS has no
// entry for Markdown, so the type an agent's notes need is fixed here, as
// lux's Linux hosts give it.
func mimeFor(name string) string {
	if ext := path.Ext(name); ext == ".md" || ext == ".markdown" {
		return "text/markdown; charset=utf-8"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// luxEvent is one of a Run's events: on its stream and on the tenant's
// feed. Callers hold s.mu.
func (s *Server) luxEvent(run *Run, typ string, data map[string]any) {
	s.emit(run, "", typ, data)
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
	placements := []any{}
	host := ""
	limit := s.memoryLimit(run)
	for _, p := range run.placements {
		view := map[string]any{"epoch": p.Epoch, "hostName": p.HostName, "state": p.State,
			"workloadStartedAt": p.WorkloadStartedAt, "exitedAt": p.ExitedAt, "snapshotDoneAt": p.SnapshotDoneAt}
		if limit != nil {
			view["memoryLimit"] = *limit
		}
		placements = append(placements, view)
		if p.Epoch == run.Epoch && p.ExitedAt == nil {
			host = p.HostName
		}
	}
	// The stored spec, as lux returns it: every secret's value dropped.
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	if secrets, ok := spec["secrets"].([]any); ok {
		for _, sec := range secrets {
			if m, ok := sec.(map[string]any); ok {
				delete(m, "value")
			}
		}
	}
	return map[string]any{"id": run.ID, "state": run.State, "epoch": run.Epoch, "sessionId": run.SessionID,
		"host": host, "placements": placements, "servers": s.serverViews(run), "spec": spec}
}

// generic: the spec's workload is a plain command, no agent (a branch
// preview's sleep infinity).
func generic(spec map[string]any) bool {
	w, _ := spec["workload"].(map[string]any)
	return w["adapter"] == "generic"
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, a := range run.artifacts {
		out = append(out, map[string]any{"id": a.ID, "epoch": a.Epoch, "path": a.Path, "contentType": a.ContentType,
			"size": a.Size, "sha256": a.SHA256, "available": true})
	}
	writeJSON(w, 200, map[string]any{"artifacts": out})
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		for _, a := range run.artifacts {
			if a.ID == r.PathValue("aid") {
				w.Header().Set("Content-Type", a.ContentType)
				w.Header().Set("X-Lux-SHA256", a.SHA256)
				_, _ = w.Write([]byte(a.Content))
				return
			}
		}
	}
	writeErr(w, 404, "not_found", "no such artifact")
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
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	if run.InputBodies == nil {
		run.InputBodies = map[string][]string{}
	}
	run.InputBodies[in.RequestID] = append(run.InputBodies[in.RequestID], string(body))
	s.mu.Unlock()
	if s.BeforeInput != nil && !s.BeforeInput(run.ID, in.RequestID) {
		writeErr(w, 503, "unavailable", "try again")
		return
	}
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
	// A busy agent: lux (the harness) takes input at once and the agent
	// reads it at its next step (FinishTools, or the turn's end). A legacy
	// lux, as its ACP adapter did, holds it until the turn ends and
	// acknowledges it only then. An interrupt with no text only stops the
	// turn, as lux's interrupt message does: nothing to deliver.
	if in.Text != "" {
		run.queued = append(run.queued, queuedInput{text: in.Text, requestID: in.RequestID})
		if run.busy && run.State == "running" {
			s.accept(run, &run.queued[len(run.queued)-1])
		}
	}
	if in.Interrupt && run.busy && run.State == "running" {
		// The turn is cancelled, and the agent is free to hear it. What the
		// harness took and the agent had not read starts the next turn,
		// under the same request ids; FailUnreadOnInterrupt fails it instead.
		run.Interrupted++
		s.recordEvent(run, "acp.turn_end", map[string]any{"stopReason": "cancelled"})
		run.busy = false
		if s.FailUnreadOnInterrupt {
			// A legacy lux holds input it never acknowledges; it fails it
			// with a phase-less lux.input carrying the error.
			kept := run.queued[:0]
			for _, q := range run.queued {
				if q.requestID == in.RequestID || !(q.accepted || s.LegacyInput) {
					kept = append(kept, q)
					continue
				}
				s.recordFailed(run, q, "the turn was cancelled before the agent read it")
			}
			run.queued = kept
		}
		// Nothing left to hear: the agent goes idle, as lux's interrupt
		// alone starts no turn.
		if len(run.queued) == 0 {
			s.recordEvent(run, "lux.activity", map[string]any{"activity": "idle"})
			run.turnsEnded++
		}
	}
	if !run.busy {
		if gate := s.InputGate; gate != nil {
			go func() {
				select {
				case <-gate:
				case <-s.closed:
					return
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if !run.busy {
					s.deliverQueued(run)
				}
			}()
		} else {
			s.deliverQueued(run)
		}
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
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	git, _ := spec["git"].(map[string]any)
	push, _ := git["push"].(map[string]any)
	branch, _ := push["branch"].(string)
	if branch == "" {
		writeErr(w, 409, "no_push", "the run's spec has no git.push branch")
		return
	}
	writeJSON(w, 202, map[string]any{"requestId": in.RequestID})
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// Each repository, in the spec's order, as lux pushes them: its own
		// result, "skipped" for one that is never pushed.
		repos := specRepos(spec)
		first := ""
		for _, repo := range repos {
			if repo.Push && first == "" {
				first = repo.Name
			}
		}
		var results []any
		for _, repo := range repos {
			result := map[string]any{"repo": repo.Name}
			if !repo.Push {
				result["status"] = "skipped"
				results = append(results, result)
				continue
			}
			result["branch"] = branch
			files := map[string]string{}
			for path, content := range run.behavior.Commit {
				if name, rest, ok := strings.Cut(path, ":"); ok && (name == repo.Name || name == "*") {
					files[rest] = content
				} else if !ok && repo.Name == first {
					files[path] = content
				}
			}
			path := s.repoPath(repo.URL)
			base := head(path, repo.Ref)
			sha, err := commit(path, base, branch, files, run.behavior.Message)
			switch {
			case err != nil:
				result["status"], result["error"] = "failed", err.Error()
			case sha == base:
				result["status"], result["commit"] = "up-to-date", sha
			default:
				result["status"], result["commit"] = "pushed", sha
				run.Pushed = true
			}
			results = append(results, result)
		}
		s.luxEvent(run, "git.push", map[string]any{"requestId": in.RequestID, "results": results})
	}()
}

type specRepo struct {
	Name, URL, Ref string
	Push           bool
}

// workloadCredential refuses, as lux does, a repository credential that is
// a secret the workload sees: a secret the spec declares that none of its
// own repositories uses as a credential. added are the repositories a
// resume brings.
func workloadCredential(rawSpec json.RawMessage, added []lux.Repository) string {
	var spec lux.Spec
	_ = json.Unmarshal(rawSpec, &spec)
	credentials := map[string]bool{}
	if spec.Git != nil {
		for _, r := range spec.Git.Repositories {
			credentials[r.Credential] = true
		}
	}
	declared := map[string]bool{}
	for _, sec := range spec.Secrets {
		declared[sec.Name] = true
	}
	for _, r := range added {
		if c := r.Credential; c != "" && declared[c] && !credentials[c] {
			return fmt.Sprintf("invalid spec: credential %q is a secret the workload sees: use a secret of its own", c)
		}
	}
	return ""
}

// registryAuthProblem refuses, as lux's spec validation does, a registry
// lux's validRegistry would not take, a login naming a secret the spec
// does not declare, or a registry twice.
func registryAuthProblem(rawSpec json.RawMessage) string {
	var spec lux.Spec
	_ = json.Unmarshal(rawSpec, &spec)
	seen := map[string]bool{}
	for i, a := range spec.Image.RegistryAuth {
		if !registry.ValidRegistry(a.Registry) {
			return fmt.Sprintf("image.registryAuth[%d].registry: %q is not a registry host", i, a.Registry)
		}
		if seen[a.Registry] {
			return fmt.Sprintf("image.registryAuth: duplicate registry %q", a.Registry)
		}
		seen[a.Registry] = true
		if !slices.ContainsFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == a.Secret }) {
			return fmt.Sprintf("image.registryAuth[%d].secret: no secret named %q", i, a.Secret)
		}
	}
	return ""
}

// missingSecrets are the Run's secrets a resume brings no value for. A
// repository a resume adds with a credential the spec lacks is left out,
// as it is here.
func missingSecrets(rawSpec json.RawMessage, given []lux.Secret) []string {
	var spec lux.Spec
	_ = json.Unmarshal(rawSpec, &spec)
	var missing []string
	for _, sec := range spec.Secrets {
		if !slices.ContainsFunc(given, func(g lux.Secret) bool { return g.Name == sec.Name && g.Value != "" }) {
			missing = append(missing, sec.Name)
		}
	}
	return missing
}

// specRepos are a spec's repositories as lux reads them.
func specRepos(spec map[string]any) []specRepo {
	git, _ := spec["git"].(map[string]any)
	list, _ := git["repositories"].([]any)
	var out []specRepo
	for _, item := range list {
		r, _ := item.(map[string]any)
		name, _ := r["name"].(string)
		url, _ := r["url"].(string)
		ref, _ := r["ref"].(string)
		push, set := r["push"].(bool)
		out = append(out, specRepo{Name: name, URL: url, Ref: ref, Push: push || !set})
	}
	return out
}

// repoPath is where a spec's repository lives for this fake.
func (s *Server) repoPath(url string) string {
	if s.RepoFor != nil {
		return s.RepoFor(url)
	}
	return s.Repo
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
	run.Calls = append(run.Calls, "stop")
	run.Stopped++
	run.busy = false
	if run.State == "running" {
		s.beforeStop(run)
		// As lux does: stopping at once, stopped once the container has
		// gone — a moment later, on the stream.
		s.setState(run, "stopping")
		go func() {
			time.Sleep(30 * time.Millisecond)
			s.mu.Lock()
			defer s.mu.Unlock()
			if run.State == "stopping" {
				s.setState(run, "stopped")
			}
		}()
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
	run.Calls = append(run.Calls, "cancel")
	run.Cancelled = true
	s.beforeStop(run)
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
		Secrets []lux.Secret `json:"secrets"`
		Input   *struct {
			Text string `json:"text"`
		} `json:"input"`
		RequestID string `json:"requestId"`
		Git       *struct {
			Repositories []map[string]any `json:"repositories"`
		} `json:"git"`
		Sync []lux.SyncRef `json:"sync"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &in)
	var raw struct {
		Secrets json.RawMessage `json:"secrets"`
	}
	_ = json.Unmarshal(body, &raw)
	s.mu.Lock()
	// As lux's resumeRun: a Run resuming already answers as the first
	// resume did; every other 409 means it was not resumed.
	switch run.State {
	case "stopped", "failed", "lost":
	case "resuming":
		view := s.view(run)
		s.mu.Unlock()
		writeJSON(w, 202, view)
		return
	case "cancelled", "succeeded":
		s.mu.Unlock()
		writeErr(w, 409, "not_resumable", "run is "+run.State)
		return
	default:
		s.mu.Unlock()
		writeErr(w, 409, "not_resumable", "run is "+run.State+": stop it first")
		return
	}
	// As lux does (resumeRun, requireSecrets): it kept no value, so every
	// secret the Run has must come again, non-empty.
	if missing := missingSecrets(run.Spec, in.Secrets); len(missing) > 0 {
		s.mu.Unlock()
		writeErr(w, 422, "secrets_required", "secret values required: "+strings.Join(missing, ", "))
		return
	}
	if in.Git != nil && len(in.Git.Repositories) > 0 {
		// Added to the Run's spec, as lux does, and cloned before it starts:
		// each reported with the resume's request id.
		var spec map[string]any
		_ = json.Unmarshal(run.Spec, &spec)
		var added []lux.Repository
		raw, _ := json.Marshal(in.Git.Repositories)
		_ = json.Unmarshal(raw, &added)
		if msg := workloadCredential(run.Spec, added); msg != "" {
			s.mu.Unlock()
			writeErr(w, 422, "invalid_spec", msg)
			return
		}
		git, _ := spec["git"].(map[string]any)
		if git == nil {
			git = map[string]any{}
			spec["git"] = git
		}
		repos, _ := git["repositories"].([]any)
		for _, repo := range in.Git.Repositories {
			repos = append(repos, repo)
			name, _ := repo["name"].(string)
			ref, _ := repo["ref"].(string)
			url, _ := repo["url"].(string)
			base := head(s.repoPath(url), ref)
			event := map[string]any{"requestId": in.RequestID, "repo": name, "status": "cloned", "commit": base}
			if base == "" {
				event = map[string]any{"requestId": in.RequestID, "repo": name, "status": "failed", "error": "ref not found"}
			} else {
				s.luxEvent(run, "git.checkout", map[string]any{"repo": name, "ref": ref, "base": base})
			}
			s.luxEvent(run, "git.clone", event)
		}
		git["repositories"] = repos
		run.Spec, _ = json.Marshal(spec)
	}
	run.Resumed++
	run.ResumeSecrets = append(run.ResumeSecrets, in.Secrets)
	run.ResumeSecretsRaw = append(run.ResumeSecretsRaw, raw.Secrets)
	run.ResumeSyncs = append(run.ResumeSyncs, in.Sync)
	run.pendingSync = in.Sync
	run.Calls = append(run.Calls, "resume")
	run.Epoch++
	if in.Input != nil {
		// Delivered once the agent is back, as lux does: it is the input the
		// resumed agent was waiting for.
		run.queued = append(run.queued, queuedInput{text: in.Input.Text, requestID: "resume"})
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
