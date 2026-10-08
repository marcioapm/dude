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
	"path/filepath"
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
	// A task's conductor: each turn's reply quotes its briefing and the
	// input it answers (fakeagent.ConductorReply), in place of Reply.
	Conductor bool
	// Tool calls to open before a Hang, each as OpenCode reports it:
	// [title, kind, raw input as JSON]. They stay open, as a call that
	// never settles does.
	OpenCalls [][3]string
	// Names the agent looks up in its first turn, before its tools: each
	// distinct one is a dns event, as lux's resolver records it.
	Lookups []Lookup
}

// Lookup is a name the agent looks up, and whether lux lets it resolve.
type Lookup struct {
	Name    string
	Allowed bool
}

// Exec answers for `ps` in a Run's container: the fake has no processes,
// so a test says what lux's exec would print (PS), or that exec fails
// (ExecFails, with lux's refusal). Usage is what GET /v1/runs/{id} reports
// as the Run's usage, by lux Run id; none reports no usage.
type execPlay struct {
	PS        func(runID string) string
	ExecFails string
	Usage     map[string]lux.Usage
}

type Run struct {
	ID        string
	Spec      json.RawMessage
	State     string
	Epoch     int
	SessionID string
	Inputs    []string
	// Names its agent looked up: lux records each once (lookUp).
	looked map[string]bool
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
	// What each accepted input carried as images, by request id.
	attachments map[string][]attachmentMeta
	// Request ids of input taken.
	taken map[string]bool
	// The secrets each accepted resume carried, in order, decoded and as
	// sent (every field of each descriptor).
	ResumeSecrets    [][]lux.Secret
	ResumeSecretsRaw []json.RawMessage
	// The input text each accepted resume carried, in order: "" for none.
	ResumeInputs []string
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
	// The values of its secrets lux holds for its next placement: the
	// submit's, then each resume's for the names the Run declared, as lux
	// keeps them in memory (secretValues).
	secretValues map[string]string
	// What GET /cost answers; nil is lux's answer before any plugin priced
	// anything: pending, no amounts.
	cost *lux.RunCost

	// Starts of the Run, as lux lists them; the last is the current one.
	placements []*placement
	// When lux accepted the submit or resume its next placement is for.
	acceptedAt *time.Time
	// The epoch a resume's placement is for, until lux assigns it a host:
	// Epoch stays the stopped one till then, as lux's runs.current_epoch
	// does. 0 when none is pending.
	assigning int
	// The start lux accepted last (submit, resume or migrate), counted up:
	// a start under way goes on only while it is still the latest one.
	starts int
	// Its next placement goes to another host (a migrate).
	moveNext  bool
	artifacts []*artifact
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
	attachments     []attachmentMeta
}

// accept acknowledges input the harness took, as lux's shim does, once: in
// the legacy contract there is no accepted receipt, only the delivery.
// Callers hold s.mu.
func (s *Server) accept(run *Run, in *queuedInput) {
	if s.LegacyInput || in.accepted {
		return
	}
	in.accepted = true
	s.recordAccepted(run, in.requestID, in.text, in.attachments)
}

// recordAccepted writes the harness's accepted answer, with a read receipt
// to follow, and the metadata of any images it carried. Callers hold s.mu.
func (s *Server) recordAccepted(run *Run, requestID, text string, attachments []attachmentMeta) {
	lands := "next_step"
	if s.NextTurnInput {
		lands = "next_turn"
	}
	record := map[string]any{"requestId": requestID, "phase": lux.InputAccepted, "receipt": true,
		"lands": lands, "text": text}
	if len(attachments) > 0 {
		record["attachments"] = records(attachments)
	}
	s.recordEvent(run, lux.RecordInput, record)
}

// recordFailed writes the failure of input the agent never read, in the
// shape this lux writes it. Callers hold s.mu.
func (s *Server) recordFailed(run *Run, in queuedInput, reason string) {
	record := map[string]any{"requestId": in.requestID, "error": reason}
	if len(in.attachments) > 0 && !s.LegacyInput {
		record["attachments"] = records(in.attachments)
	}
	switch {
	case s.LegacyInput:
		s.recordEvent(run, lux.RecordInput, record)
	case in.accepted:
		s.recordEvent(run, lux.RecordInputFailed, record)
	default:
		record["phase"] = lux.InputFailed
		s.recordEvent(run, lux.RecordInput, record)
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
	// The host it ran on: host-1 first; a resume stays on its host, as
	// lux's scheduler prefers, unless the Run is migrated (or
	// MoveOnResume), when it goes to host-<epoch>.
	HostName string
	// assigned, starting, running, then exited.
	State string
	// How far its start got, as lux reports each, in order.
	AcceptedAt, AssignedAt, ImageReadyAt, VolumesRestoredAt, ContainerStartedAt, WorkloadStartedAt *time.Time
	// How it ended, in order: asked to stop (by a stop or a migrate), its
	// container gone, its snapshot taken, then uploaded.
	StopRequestedAt, ExitedAt, SnapshotDoneAt, UploadedAt *time.Time
	SnapshotBytes                                         int64
}

// newPlacement is lux assigning the Run's current epoch a host, now: the
// one it was on unless it moves. Callers hold s.mu.
func (s *Server) newPlacement(run *Run) *placement {
	host := fmt.Sprintf("host-%d", run.Epoch)
	if n := len(run.placements); n > 0 && !run.moveNext && !s.MoveOnResume {
		host = run.placements[n-1].HostName
	}
	run.moveNext = false
	now := time.Now()
	accepted := run.acceptedAt
	if accepted == nil {
		accepted = &now
	}
	p := &placement{Epoch: run.Epoch, HostName: host, State: "assigned", AcceptedAt: accepted, AssignedAt: &now}
	run.placements = append(run.placements, p)
	return p
}

// currentPlacement is the placement of the Run's current epoch; nil before
// lux assigned it. Callers hold s.mu.
func (run *Run) currentPlacement() *placement {
	if n := len(run.placements); n > 0 && run.placements[n-1].Epoch == run.Epoch {
		return run.placements[n-1]
	}
	return nil
}

// placing is lux starting the Run's placement of epoch, over after: a host
// assigned, its image ready, its volumes restored, its container started,
// each a moment after the last, as a runner reports them. The workload
// starts when the Run is running (setStateWith). It stops, and says so
// (false), once that start is over: the Run ended (cancelled, stopped)
// or moved on to another epoch or start meanwhile.
func (s *Server) placing(run *Run, epoch, start int, after time.Duration) bool {
	step := after / 5
	time.Sleep(step)
	s.hold(epoch, holdAssign)
	s.mu.Lock()
	if !run.starting(epoch, start) {
		s.mu.Unlock()
		return false
	}
	if run.assigning == epoch {
		// The scheduler's assign: the Run's epoch moves to the placement's.
		run.Epoch, run.assigning = epoch, 0
	}
	p := run.currentPlacement()
	if p == nil {
		p = s.newPlacement(run)
	}
	s.mu.Unlock()
	for _, stamp := range []**time.Time{&p.ImageReadyAt, &p.VolumesRestoredAt, &p.ContainerStartedAt} {
		time.Sleep(step)
		s.mu.Lock()
		if !run.starting(epoch, start) {
			s.mu.Unlock()
			return false
		}
		now := time.Now()
		*stamp, p.State = &now, "starting"
		s.mu.Unlock()
	}
	time.Sleep(step)
	return true
}

// starting says the Run's start into epoch, accepted as start, is still
// under way, assigned or not: a later accepted start ends it even before
// its own epoch is assigned. Callers hold s.mu.
func (run *Run) starting(epoch, start int) bool {
	return run.starts == start && (run.Epoch == epoch || run.assigning == epoch) && !lux.Terminal(run.State)
}

// Where a start can be held by a test (Server.onStart).
const (
	holdAssign   = "assign"   // before its host is assigned
	holdWorkload = "workload" // placed, before its workload starts
	holdOver     = "over"     // the start has gone as far as it will
)

// hold lets a test stop a start at point, without the fake's lock.
func (s *Server) hold(epoch int, point string) {
	if s.onStart != nil {
		s.onStart(epoch, point)
	}
}

// stopRequested stamps the current placement asked to stop. Callers hold
// s.mu.
func (run *Run) stopRequested() {
	if p := run.currentPlacement(); p != nil && p.StopRequestedAt == nil {
		now := time.Now()
		p.StopRequestedAt = &now
	}
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
	Time  time.Time
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
	// Every resume is placed on another host, as when its host was drained
	// or is full; by default a resume stays on its host, as lux prefers.
	MoveOnResume bool
	// The preview domain servers' URLs are under; "" gives them none, as a
	// lux without previews configured.
	PreviewDomain string
	// Previews optionally advertises the relative-naming capability in whoami.
	Previews *bool
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
	// NoSyncModes is a lux from before sync modes: a sync or resume naming
	// fast-forward or fetch is refused with a 409.
	NoSyncModes bool
	// CancelledState is a lux from before terminate: a Run ended for good
	// is "cancelled", and a succeeded Run is final too. By default it is
	// "terminated", and succeeded resumes as stopped does.
	CancelledState bool
	// HoldPushes is a lux that accepts a push and never reports it: no
	// git.push follows, until ReleasePushes.
	HoldPushes bool
	heldPushes []func()
	// HoldSyncs is a lux that accepts a running Run's sync and never
	// reports it: no git.sync or sync.done follows.
	HoldSyncs bool
	// BeforeInput, when set, runs as each input request arrives, before the
	// fake acts on it; false refuses the request (503), as a lux that is
	// briefly unavailable. Called without the fake's lock.
	BeforeInput func(runID, requestID string) bool
	// InputGate, when set, holds what input starts in an idle agent (its
	// receipts, and the turn it takes) until the channel is closed. The
	// request is answered first, as lux answers the POST before the
	// agent's records arrive.
	InputGate chan struct{}
	// onStart, set by a test before any Run, is called by each start at
	// its hold points (hold), and may block to order it against others.
	onStart func(epoch int, point string)

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
	// Starts still to fail, by spec label "key=value" (FailStarts), and
	// the state each ends in.
	failStarts map[string]int
	failAs     map[string]string
	// Test hooks for stream delivery, placement starts, and event pages.
	beforeEvent func(runID string, eventID int64, typ string)
	beforeStart func(runID string)
	eventPages  func(runID string, after int64, ids []int64) int
	// closed ends everything the fake waits on in the background (Close).
	closed    chan struct{}
	closeOnce sync.Once
	// What exec and GET say of a Run's processes and usage (execPlay).
	execPlay
	// fakeagent.StallModel's and SilentModel's reviewers so far, by task.
	stallReviews map[string]int
}

// SetUsage is what lux reports as the Run's usage from now on.
func (s *Server) SetUsage(luxRunID string, u lux.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Usage == nil {
		s.Usage = map[string]lux.Usage{}
	}
	s.Usage[luxRunID] = u
}

// SetPS is what `ps` prints in a Run's container ("" fails exec with
// reason ExecFails when that is set).
func (s *Server) SetPS(ps func(luxRunID string) string, execFails string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PS, s.ExecFails = ps, execFails
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
	tools := []string{"todowrite", "read"}
	if step.LongCommand {
		tools = []string{"bash"}
	}
	var open [][3]string
	if str("dude.model") == fakeagent.StallModel && str("dude.phase") == fakeagent.Brainstorm {
		step.Hang = true
		open = [][3]string{fakeagent.StallCall}
	}
	if m := str("dude.model"); (m == fakeagent.StallModel || m == fakeagent.SilentModel) && str("dude.phase") == "review" {
		// The task's first reviewer hangs, in its open call or silent;
		// later ones review. Decide is called with s.mu held.
		if s.stallReviews == nil {
			s.stallReviews = map[string]int{}
		}
		s.stallReviews[str("dude.task")]++
		if s.stallReviews[str("dude.task")] == 1 {
			step.Hang = true
			if m == fakeagent.StallModel {
				open = [][3]string{fakeagent.StallCall}
			}
		}
	}
	// Its lookups are decided by the Run's own network, as lux's resolver
	// decides them.
	var network *lux.Network
	if raw, err := json.Marshal(spec["network"]); err == nil {
		_ = json.Unmarshal(raw, &network)
	}
	var lookups []Lookup
	for _, name := range step.Lookups {
		lookups = append(lookups, Lookup{Name: name, Allowed: network.Allows(name)})
	}
	// Every phase plans and looks around first, as an agent does.
	return Behaviour{Lookups: lookups, Reply: step.Reply, Commit: files, Message: step.Message, Hang: step.Hang, Ask: step.Ask,
		Publish: published, Tools: tools, KeepToolsOpen: step.LongCommand, CallTools: step.Tools, Edits: step.Edits, PublishNow: step.PublishNow,
		FinishEdits: step.FinishEdits, Conductor: str("dude.phase") == fakeagent.Conductor || str("dude.phase") == fakeagent.Brainstorm, OpenCalls: open}
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

// Records is what lux recorded of a Run, in order: each {type, data}.
func (s *Server) Records(id string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil {
		return nil
	}
	out := make([]map[string]any, len(run.records))
	for i, rec := range run.records {
		out[i] = rec.Event
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

// ResumeInputs is the input text each accepted resume of a Run carried.
func (s *Server) ResumeInputs(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return slices.Clone(run.ResumeInputs)
	}
	return nil
}

// InputBodies is each body POSTed to a Run's /input with requestID.
func (s *Server) InputBodies(id, requestID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return slices.Clone(run.InputBodies[requestID])
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

// ended is the state of a Run ended for good, by this lux's name for it.
func (s *Server) ended() string {
	if s.CancelledState {
		return "cancelled"
	}
	return "terminated"
}

// CancelInLux cancels a Run as an operator would in lux itself: nothing of
// it is left to resume.
func (s *Server) CancelInLux(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		run.Cancelled = true
		s.setState(run, s.ended())
	}
}

// FailStarts makes the next n starts (a submit's or a resume's placement)
// of Runs whose spec has the label "key=value" fail before the workload
// runs, as lux reports a container that would not start; n <= 0 clears it.
func (s *Server) FailStarts(label string, n int) { s.failStartsAs(label, n, "failed") }

// LoseStarts is FailStarts with the host lost mid-start: the Run is lost.
func (s *Server) LoseStarts(label string, n int) { s.failStartsAs(label, n, "lost") }

func (s *Server) failStartsAs(label string, n int, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStarts == nil {
		s.failStarts, s.failAs = map[string]int{}, map[string]string{}
	}
	if n <= 0 {
		delete(s.failStarts, label)
		return
	}
	s.failStarts[label], s.failAs[label] = n, state
}

// takeFailStart uses up one of FailStarts' for a Run of this spec: the
// state the start ends in, "" for none. Callers hold s.mu.
func (s *Server) takeFailStart(spec map[string]any) string {
	labels, _ := spec["labels"].(map[string]any)
	for k, v := range labels {
		key := fmt.Sprintf("%s=%v", k, v)
		if s.failStarts[key] > 0 {
			s.failStarts[key]--
			return s.failAs[key]
		}
	}
	return ""
}

// BeforeEvent has every Run's output stream call fn before it sends each
// lifecycle event, without the fake's lock: a test holds a follower there
// while the Run moves on. nil clears it.
func (s *Server) BeforeEvent(fn func(runID string, eventID int64, typ string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforeEvent = fn
}

// BeforeStart has every placement (a submit's, a resume's) call fn as it
// starts, before it runs or fails, without the fake's lock. nil clears it.
func (s *Server) BeforeStart(fn func(runID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforeStart = fn
}

// PageEvents has every GET /v1/runs/{id}/events ask fn, without the fake's
// lock, how many of the events past after (ids, at most lux's page of
// eventsPage) its page carries: fewer is a short page, one that blocks is a
// slow request, a negative answer fails it (503). An empty page while there
// are events past after is lux recording them after the page's query. nil
// clears it.
func (s *Server) PageEvents(fn func(runID string, after int64, ids []int64) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventPages = fn
}

// EventState is the state a Run's state event reported; "" for another.
func (s *Server) EventState(runID string, eventID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[runID]; run != nil {
		for _, e := range run.events {
			if e.ID == eventID {
				st, _ := e.Data["state"].(string)
				return st
			}
		}
	}
	return ""
}

// State is the Run's state now.
func (s *Server) State(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return run.State
	}
	return ""
}

// Lose ends a Run as lux does when its host stops answering: lost,
// resumable from its last snapshot.
func (s *Server) Lose(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		s.setStateWith(run, "lost", "host lost")
	}
}

// Crash ends a Run's agent as a dead container would.
func (s *Server) Crash(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		s.setState(run, "failed")
	}
}

// Succeed ends a Run as lux does when its workload exits 0 on its own.
func (s *Server) Succeed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		run.busy = false
		s.setState(run, "succeeded")
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
	mux.HandleFunc("GET /v1/runs/{id}/events", s.events)
	mux.HandleFunc("POST /v1/runs/{id}/input", s.input)
	mux.HandleFunc("POST /v1/runs/{id}/push", s.push)
	mux.HandleFunc("POST /v1/runs/{id}/stop", s.stop)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/runs/{id}/terminate", s.terminate)
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
	// What each input of a Run carried as images, by request id: lux's
	// record of them (name, type, size, sha256), never the bytes.
	mux.HandleFunc("GET /fake/runs/{id}/attachments", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		run := s.runs[r.PathValue("id")]
		if run == nil {
			writeErr(w, 404, "not_found", "no such run")
			return
		}
		out := map[string]any{}
		for id, ms := range run.attachments {
			out[id] = records(ms)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /fake/fail-starts", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		s.FailStarts(r.URL.Query().Get("label"), n)
		writeJSON(w, 200, map[string]any{"label": r.URL.Query().Get("label"), "n": n})
	})
	// A Run's long command finishing (FinishTools): its next step reads
	// what it was steered with meanwhile.
	mux.HandleFunc("POST /fake/runs/{id}/finish-tools", func(w http.ResponseWriter, r *http.Request) {
		s.FinishTools(r.PathValue("id"))
		writeJSON(w, 200, map[string]any{"finished": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.Key {
			writeErr(w, 401, "unauthorized", "invalid API key")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// MaxBody is luxd's limit on a request body: an input or a submit larger
// is refused before it is read as JSON.
const MaxBody = 8 << 20

// readBody reads a request body as luxd does, answering 400 for one over
// MaxBody. On an error the answer is written.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		writeErr(w, 400, "bad_request", "request body too large or unreadable: "+err.Error())
	}
	return body, err
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r)
	if err != nil {
		return
	}
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	var spec map[string]any
	_ = json.Unmarshal(raw, &spec)
	if msg := registryAuthProblem(raw); msg != "" {
		writeErr(w, 422, "invalid_spec", msg)
		return
	}
	if msg := specNameProblems(raw); msg != "" {
		writeErr(w, 422, "invalid_spec", msg)
		return
	}
	// The prompt's images, checked at submit as lux does.
	var withImages struct {
		Workload struct {
			Attachments json.RawMessage `json:"attachments"`
		} `json:"workload"`
	}
	_ = json.Unmarshal(raw, &withImages)
	promptImages, problem := checkAttachments(withImages.Workload.Attachments)
	if problem != "" {
		writeErr(w, 400, lux.CodeInvalidAttachment, "workload."+problem)
		return
	}
	if len(promptImages) > 0 && generic(spec) {
		writeErr(w, 400, lux.CodeAttachmentsUnsupported, "a generic workload cannot take images")
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
	run := &Run{ID: fmt.Sprintf("lrun_%d", s.next), Spec: raw, State: "submitted", Epoch: 1, starts: 1,
		secretValues: secretValues(raw, nil)}
	if len(promptImages) > 0 {
		run.attachments = map[string][]attachmentMeta{"prompt": promptImages}
	}
	start := run.starts
	accepted := time.Now()
	run.acceptedAt = &accepted
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
	go s.play(run, 1, start, spec, false)
	writeJSON(w, 201, s.view(run))
}

// play is the agent's life: start, check out, take the task, work, go idle.
// start is the accepted start it plays (Run.starts).
func (s *Server) play(run *Run, epoch, start int, spec map[string]any, resumed bool) {
	defer s.hold(epoch, holdOver)
	after := s.StartAfter
	if after <= 0 {
		after = 20 * time.Millisecond
	}
	if !s.placing(run, epoch, start, after) {
		return // cancelled, stopped or moved on before it started
	}
	s.hold(epoch, holdWorkload)
	s.mu.Lock()
	hook := s.beforeStart
	s.mu.Unlock()
	if hook != nil {
		hook(run.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !run.starting(epoch, start) {
		return
	}
	if run.behavior.FailToStart {
		s.setState(run, "failed")
		return
	}
	switch s.takeFailStart(spec) {
	case "failed":
		// As lux's runner reports a container that would not start (exit
		// 125): the placement never ran its workload, and the Run is failed.
		s.setStateWith(run, "failed", "start-failed")
		return
	case "lost":
		s.setStateWith(run, "lost", "host lost")
		return
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
			s.recordAccepted(run, "prompt", prompt, run.attachments["prompt"])
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
	if len(run.Inputs) == 0 {
		s.lookUp(run, b.Lookups)
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
	if b.Conductor {
		// The tools the input asks the scripted conductor for: its first
		// prompt's (its script), then each later input's.
		input := run.Prompt()
		if !first {
			input = run.Inputs[len(run.Inputs)-1]
		}
		for _, c := range fakeagent.ConductorCalls(conductorAsked(input, first)) {
			s.callTool(run, c[0], c[1])
		}
	}
	if b.Hang && !run.woken && (first || !b.Conductor || len(b.OpenCalls) == 0) {
		if first {
			for i, c := range b.OpenCalls {
				var input any
				_ = json.Unmarshal([]byte(c[2]), &input)
				id := fmt.Sprintf("open_%d", i)
				s.agent(run, map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": c[0], "kind": c[1],
					"status": "pending"})
				s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": c[0], "kind": c[1],
					"status": "in_progress", "rawInput": input})
				run.openTools = append(run.openTools, id)
			}
		}
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
	if b.Conductor {
		reply = run.Prompt()[strings.LastIndex(run.Prompt(), "echo ")+len("echo "):]
		if !first {
			reply = fakeagent.ConductorTurn(run.Prompt(), run.Inputs[len(run.Inputs)-1])
		}
	}
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

// lookUp records each distinct name as lux's resolver does (lux
// internal/runner/network.go): a dns event, allowed or not, with the
// addresses it answered. Callers hold s.mu.
func (s *Server) lookUp(run *Run, lookups []Lookup) {
	for _, l := range lookups {
		if run.looked == nil {
			run.looked = map[string]bool{}
		}
		if run.looked[l.Name] {
			continue
		}
		run.looked[l.Name] = true
		answers := []string{}
		if l.Allowed {
			answers = []string{"192.0.2.10"}
		}
		s.luxEvent(run, "dns", map[string]any{"name": l.Name, "allowed": l.Allowed, "answers": answers})
	}
}

// conductorAsked is what a scripted conductor's input asks of it, as
// fakeagent.ConductorCalls reads it: a later input as it came; the first,
// its script's tool calls (lux-fake's "http dude POST /tools/NAME ARGS"
// lines) put back as "tool: NAME ARGS".
func conductorAsked(input string, first bool) string {
	if !first {
		return input
	}
	var lines []string
	for _, line := range strings.Split(input, "\n") {
		if rest, ok := strings.CutPrefix(line, "http dude POST /tools/"); ok {
			lines = append(lines, fakeagent.ConductorCallPrefix+rest)
		}
	}
	return strings.Join(lines, "\n")
}

// callTool calls one of dude's tools as the agent's container would through
// lux's service proxy: the spec's "dude" service, its header filled in from
// the spec's secret. (The CLI's own path through a socket is tested in
// agenttools.) Callers hold s.mu; the call is made without it, since dude
// may be slow. A failure is said in the agent's reply.
func (s *Server) callTool(run *Run, tool, args string) {
	if fakeagent.Local(tool) {
		s.localCall(run, tool, args)
		return
	}
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
	if state == "running" {
		p := run.currentPlacement()
		if p == nil {
			p = s.newPlacement(run)
		}
		if p.WorkloadStartedAt == nil {
			now := time.Now()
			p.WorkloadStartedAt, p.State = &now, "running"
		}
	}
	if state == "stopping" {
		run.stopRequested()
	}
	if lux.Terminal(state) {
		s.exited(run)
		s.placementEnded(run, state, reason)
	}
	if lux.Terminated(state) || (state == "succeeded" && s.CancelledState) {
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
		p.SnapshotBytes = int64(4<<20 + 4096*len(run.records))
		// Uploaded a moment after it was taken, as a runner's upload trails
		// its snapshot.
		go func() {
			time.Sleep(5 * time.Millisecond)
			s.mu.Lock()
			defer s.mu.Unlock()
			up := time.Now()
			p.UploadedAt = &up
		}()
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
			"acceptedAt": p.AcceptedAt, "assignedAt": p.AssignedAt, "imageReadyAt": p.ImageReadyAt,
			"volumesRestoredAt": p.VolumesRestoredAt, "containerStartedAt": p.ContainerStartedAt,
			"workloadStartedAt": p.WorkloadStartedAt, "stopRequestedAt": p.StopRequestedAt,
			"exitedAt": p.ExitedAt, "snapshotDoneAt": p.SnapshotDoneAt, "uploadedAt": p.UploadedAt}
		// Absent until reached, as lux leaves them out.
		for k, v := range view {
			if t, ok := v.(*time.Time); ok && t == nil {
				delete(view, k)
			}
		}
		if p.SnapshotDoneAt != nil {
			view["snapshotBytes"] = p.SnapshotBytes
		}
		if limit != nil {
			view["memoryLimit"] = *limit
		}
		placements = append(placements, view)
		if p.Epoch == run.Epoch && p.ExitedAt == nil {
			host = p.HostName
		}
	}
	// The stored spec, as lux returns it: every secret's value dropped, its
	// as normalized and a credential marked runnerOnly (lux's Normalize).
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	runnerOnly := runnerOnlySecrets(run.Spec)
	if secrets, ok := spec["secrets"].([]any); ok {
		for _, sec := range secrets {
			if m, ok := sec.(map[string]any); ok {
				delete(m, "value")
				name, _ := m["name"].(string)
				if as, _ := m["as"].(string); as == "" {
					m["as"] = "env"
					if runnerOnly[name] {
						m["as"] = "none"
					}
				}
				if runnerOnly[name] {
					m["runnerOnly"] = true
				}
			}
		}
	}
	out := map[string]any{"id": run.ID, "state": run.State, "epoch": run.Epoch, "sessionId": run.SessionID,
		"host": host, "placements": placements, "servers": s.serverViews(run), "spec": spec}
	if u, ok := s.Usage[run.ID]; ok {
		out["usage"] = u
	}
	return out
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
		Text        string          `json:"text"`
		RequestID   string          `json:"requestId"`
		Interrupt   bool            `json:"interrupt"`
		Attachments json.RawMessage `json:"attachments"`
	}
	body, err := readBody(w, r)
	if err != nil {
		return
	}
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
	images, problem := checkAttachments(in.Attachments)
	if problem != "" {
		writeErr(w, 400, lux.CodeInvalidAttachment, problem)
		return
	}
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	if len(images) > 0 && generic(spec) {
		writeErr(w, 400, lux.CodeAttachmentsUnsupported, "a generic workload cannot take images")
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
	// One request id is one input, as lux keys input by it: a retry of one
	// it already took is answered, not queued again.
	if run.taken[in.RequestID] && in.RequestID != "" {
		writeJSON(w, 202, map[string]any{"requestId": in.RequestID})
		return
	}
	if run.taken == nil {
		run.taken = map[string]bool{}
	}
	run.taken[in.RequestID] = true
	run.Calls = append(run.Calls, "input")
	if len(images) > 0 {
		if run.attachments == nil {
			run.attachments = map[string][]attachmentMeta{}
		}
		run.attachments[in.RequestID] = images
	}
	// A busy agent: lux (the harness) takes input at once and the agent
	// reads it at its next step (FinishTools, or the turn's end). A legacy
	// lux, as its ACP adapter did, holds it until the turn ends and
	// acknowledges it only then. An interrupt with no text only stops the
	// turn, as lux's interrupt message does: nothing to deliver.
	if in.Text != "" || len(images) > 0 {
		run.queued = append(run.queued, queuedInput{text: in.Text, requestID: in.RequestID, attachments: images})
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
		if s.HoldPushes {
			s.heldPushes = append(s.heldPushes, func() { s.pushNow(run, spec, branch, in.RequestID) })
			return
		}
		s.pushNow(run, spec, branch, in.RequestID)
	}()
}

// ReleasePushes makes the pushes HoldPushes held, now, each reported with
// its git.push as lux would have.
func (s *Server) ReleasePushes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.heldPushes
	s.heldPushes = nil
	for _, push := range held {
		push()
	}
}

// AddEvents records n lifecycle events of typ on a Run, as a long-lived
// Run's history.
func (s *Server) AddEvents(id, typ string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		for range n {
			s.luxEvent(run, typ, map[string]any{})
		}
	}
}

// pushNow pushes the Run's checkouts to branch and reports it. Callers
// hold s.mu.
func (s *Server) pushNow(run *Run, spec map[string]any, branch, requestID string) {
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
		var sha string
		var err error
		work, op := "", ""
		if run.workspace != "" {
			work = filepath.Join(run.workspace, "repos", repo.Name)
			if op, err = operation(work); err != nil {
				result["status"], result["error"] = "failed", operationUnknown(err)
				results = append(results, result)
				continue
			}
		}
		if op != "" {
			// As lux: nothing of a checkout mid-operation is bundled.
			result["status"], result["operation"] = lux.PushRefused, op
			result["error"] = fmt.Sprintf("a %s is in progress in the checkout: finish or abort it, then push", op)
			results = append(results, result)
			continue
		}
		if len(files) == 0 && work != "" {
			// What the agent committed in its checkout itself, as lux
			// pushes a checkout's HEAD.
			sha, err = pushWorkspace(work, path, branch, base)
		} else {
			sha, err = commit(path, base, branch, files, run.behavior.Message)
		}
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
	s.luxEvent(run, "git.push", map[string]any{"requestId": requestID, "results": results})
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

// cancel is the deprecated alias of terminate, and a CancelledState lux's
// only one.
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.end(w, run, "cancel")
}

func (s *Server) terminate(w http.ResponseWriter, r *http.Request) {
	if s.CancelledState {
		http.NotFound(w, r)
		return
	}
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.end(w, run, "terminate")
}

// end ends a Run for good, asked as call.
func (s *Server) end(w http.ResponseWriter, run *Run, call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.Calls = append(run.Calls, call)
	run.Cancelled = true
	s.beforeStop(run)
	if !lux.Terminated(run.State) {
		s.setState(run, s.ended())
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
	resumable := run.State == "stopped" || run.State == "failed" || run.State == "lost" ||
		run.State == "succeeded" && !s.CancelledState
	switch {
	case resumable:
	case run.State == "resuming":
		view := s.view(run)
		s.mu.Unlock()
		writeJSON(w, 202, view)
		return
	case lux.Terminal(run.State):
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
	if msg := missingSecrets(run.Spec, in.Secrets); len(msg) > 0 {
		s.mu.Unlock()
		writeErr(w, 422, "secrets_required", "secret values required: "+strings.Join(msg, ", "))
		return
	}
	if msg := s.syncModeProblem(in.Sync); msg != "" {
		s.mu.Unlock()
		writeErr(w, 409, "sync_mode_unsupported", msg)
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
		msg := workloadCredential(run.Spec, added)
		if msg == "" {
			msg = addedRepoProblems(run.Spec, added)
		}
		if msg != "" {
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
	run.secretValues = secretValues(run.Spec, in.Secrets)
	run.ResumeSecrets = append(run.ResumeSecrets, in.Secrets)
	run.ResumeSecretsRaw = append(run.ResumeSecretsRaw, raw.Secrets)
	run.ResumeSyncs = append(run.ResumeSyncs, in.Sync)
	run.pendingSync = in.Sync
	run.Calls = append(run.Calls, "resume")
	// As lux's resumeRun: the answer carries the Run's current epoch, the
	// stopped one; the new one is set when the placement is assigned
	// (placing).
	run.assigning = run.Epoch + 1
	run.starts++
	accepted := time.Now()
	run.acceptedAt = &accepted
	resumeInput := ""
	if in.Input != nil {
		resumeInput = in.Input.Text
	}
	run.ResumeInputs = append(run.ResumeInputs, resumeInput)
	if in.Input != nil {
		// Delivered once the agent is back, as lux does: it is the input the
		// resumed agent was waiting for.
		run.queued = append(run.queued, queuedInput{text: in.Input.Text, requestID: "resume"})
	}
	s.setState(run, "resuming")
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	epoch, start := run.assigning, run.starts
	view := s.view(run)
	s.mu.Unlock()
	go s.play(run, epoch, start, spec, true)
	writeJSON(w, 202, view)
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
		for i := 0; i < len(run.events); i++ {
			e := run.events[i]
			if e.ID <= afterEvent {
				continue
			}
			afterEvent = e.ID
			if hook := s.beforeEvent; hook != nil {
				s.mu.Unlock()
				hook(run.ID, e.ID, e.Type)
				s.mu.Lock()
			}
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

// eventsPage is how many events lux's GET /v1/runs/{id}/events returns at
// most.
const eventsPage = 1000

// events is GET /v1/runs/{id}/events: the Run's lifecycle events after an
// id, in id order, a page at a time, as lux lists them.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	var after int64
	fmt.Sscan(r.URL.Query().Get("after"), &after)
	s.mu.Lock()
	var out []event
	for _, e := range run.events {
		if e.ID > after && len(out) < eventsPage {
			out = append(out, e)
		}
	}
	pages := s.eventPages
	s.mu.Unlock()
	if pages != nil {
		ids := make([]int64, len(out))
		for i, e := range out {
			ids[i] = e.ID
		}
		n := pages(run.ID, after, ids)
		if n < 0 {
			writeErr(w, 503, "unavailable", "events unavailable")
			return
		}
		out = out[:min(n, len(out))]
	}
	list := make([]map[string]any, 0, len(out))
	for _, e := range out {
		list = append(list, map[string]any{"id": e.ID, "epoch": e.Epoch, "type": e.Type, "data": e.Data, "time": e.Time})
	}
	writeJSON(w, 200, map[string]any{"events": list})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
