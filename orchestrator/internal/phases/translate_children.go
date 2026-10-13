package phases

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Child sessions: a subagent the Run's agent started from one of its tool
// calls (docs/design/agent-tools.md, "Child sessions"). Each harness says
// so its own way (translate_claude.go, translate_codex.go,
// translate_opencode.go); what is recorded is the same: agent.child.*
// events nested under the parent's call, and runs.children for the stall
// detector.

// childState is one child as the translator knows it, kept in
// harness_state between batches.
type childState struct {
	ID            string `json:"id"`
	CallID        string `json:"callId,omitempty"`
	ParentChildID string `json:"parentChildId,omitempty"`
	Kind          string `json:"kind"`
	Title         string `json:"title,omitempty"`
	AgentType     string `json:"agentType,omitempty"`
	Model         string `json:"model,omitempty"`
	Started       bool   `json:"started,omitempty"`
	Ended         bool   `json:"ended,omitempty"`
	// When agent.child.started was written, for durationSecs when the
	// harness gives none.
	StartedAt time.Time `json:"startedAt,omitzero"`
	// Its tool calls started and not ended, by id, with the tool they were
	// recorded under.
	Calls map[string]string `json:"calls,omitempty"`
	// What it reported spending, summed over its messages or requests.
	CostUSD float64     `json:"costUsd,omitempty"`
	Tokens  codexTokens `json:"tokens,omitzero"`
	// Its last message, the summary of an end that carries none.
	LastText string `json:"lastText,omitempty"`
}

// Child kinds: a task tool's subagent (OpenCode, Claude Code) or a spawned
// agent thread (Codex).
const (
	childTask  = "task"
	childSpawn = "spawn"
)

// childTextLimit bounds a child's message and summary (2 KB).
const childTextLimit = 2048

// maxChildren bounds the children kept in harness_state: past it, ended
// ones are dropped, oldest first.
const maxChildren = 64

// childFact is a child's line in runs.children: what the stall detector
// reads. LastEventAt is stamped by the database when saved.
type childFact struct {
	ChildID   string         `json:"childId"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	LastEvent string         `json:"lastEvent"`
	WaitingOn map[string]any `json:"waitingOn,omitempty"`
}

// childFacts is what save writes into runs.children: entries touched in
// this batch (stamped now()), and calls whose entry goes.
type childFacts struct {
	touched map[string]childFact
	dropped map[string]bool
	clear   bool
}

func (f *childFacts) touch(callID string, fact childFact) {
	if f.touched == nil {
		f.touched = map[string]childFact{}
	}
	delete(f.dropped, callID)
	f.touched[callID] = fact
}

func (f *childFacts) drop(callID string) {
	if f.dropped == nil {
		f.dropped = map[string]bool{}
	}
	delete(f.touched, callID)
	f.dropped[callID] = true
}

// args are save's parameters for runs.children: the entries touched, as
// JSON, the calls whose entry goes, and whether all go (a turn's end).
func (f childFacts) args() (touched []byte, dropped []string, all bool) {
	touched, _ = json.Marshal(f.touched)
	if f.touched == nil {
		touched = []byte("{}")
	}
	return touched, db.NonNil(slices.Sorted(maps.Keys(f.dropped))), f.clear
}

// child is the child with this id, nil for none.
func (t *translator) child(id string) *childState {
	if id == "" {
		return nil
	}
	return t.Children[id]
}

// childOfCall is the child the parent's call started, nil for none.
func (t *translator) childOfCall(callID string) *childState {
	for _, c := range t.Children {
		if c.CallID == callID && callID != "" {
			return c
		}
	}
	return nil
}

// addChild keeps a new child, dropping the oldest ended ones past
// maxChildren.
func (t *translator) addChild(c *childState) *childState {
	if t.Children == nil {
		t.Children = map[string]*childState{}
	}
	t.Children[c.ID] = c
	for len(t.Children) > maxChildren {
		var oldest *childState
		for _, o := range t.Children {
			if o.Ended && (oldest == nil || o.StartedAt.Before(oldest.StartedAt)) {
				oldest = o
			}
		}
		if oldest == nil {
			break
		}
		delete(t.Children, oldest.ID)
	}
	return c
}

// touchChild is a child's activity: the agent is working, and the Run's
// open calls it keeps busy say what it did last (runs.children). Any
// event ends a wait.
func (t *translator) touchChild(c *childState, last string, waitingOn map[string]any) {
	t.active = true
	fact := childFact{ChildID: c.ID, Kind: c.Kind, Title: c.Title, LastEvent: cut(oneLine(last), inputChars), WaitingOn: waitingOn}
	for _, call := range t.busyCalls(c) {
		t.facts.touch(call, fact)
	}
}

// busyCalls are the Run's own open calls a child's activity keeps busy:
// the call that started it, or its ancestor's; and for a Codex agent,
// whose spawn call ends at once, the Run's agent's open wait calls that
// wait on it (waitsOn).
func (t *translator) busyCalls(c *childState) []string {
	root := c
	for seen := 0; root.ParentChildID != "" && seen < maxChildren; seen++ {
		p := t.child(root.ParentChildID)
		if p == nil {
			break
		}
		root = p
	}
	var calls []string
	if t.openCalls[root.CallID] {
		calls = append(calls, root.CallID)
	}
	if root.Kind == childSpawn {
		for _, id := range slices.Sorted(maps.Keys(t.waitsOn)) {
			on := t.waitsOn[id]
			if t.openCalls[id] && (len(on) == 0 || slices.Contains(on, root.ID)) {
				calls = append(calls, id)
			}
		}
	}
	return calls
}

// childPayload is a child event's payload with the child's ids.
func childPayload(c *childState, p map[string]any) map[string]any {
	p["childId"] = c.ID
	if _, ok := p["callId"]; !ok {
		p["callId"] = c.CallID
	}
	if c.ParentChildID != "" {
		p["parentChildId"] = c.ParentChildID
	}
	return p
}

// startChild records agent.child.started once.
func (t *translator) startChild(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState) error {
	if c.Started {
		return nil
	}
	c.Started, c.StartedAt = true, time.Now()
	p := map[string]any{"kind": c.Kind, "title": c.Title}
	if c.AgentType != "" {
		p["agentType"] = c.AgentType
	}
	if c.Model != "" {
		p["model"] = c.Model
	}
	t.touchChild(c, "started: "+c.Title, nil)
	return s.event(ctx, tx, t.run, evChildStarted, ledger.ActorAgent, childPayload(c, p))
}

// callStarted records a tool call once: the Run's own (c nil) or a
// child's. payload is agent.tool.called's, {tool, callId, input, title}.
func (t *translator) callStarted(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, payload map[string]any) error {
	id, _ := payload["callId"].(string)
	name, _ := payload["tool"].(string)
	if c == nil {
		if _, seen := t.seenCalls[id]; seen {
			return nil
		}
		t.seenCalls[id] = name
		t.openCalls[id] = true
		return s.event(ctx, tx, t.run, evToolCalled, ledger.ActorAgent, payload)
	}
	if err := t.startChild(ctx, tx, s, c); err != nil {
		return err
	}
	if _, seen := c.Calls[id]; seen {
		return nil
	}
	if c.Calls == nil {
		c.Calls = map[string]string{}
	}
	c.Calls[id] = name
	raw, _ := json.Marshal(payload["input"])
	t.touchChild(c, strings.TrimSpace(name+" "+callInput(raw)), nil)
	payload["childId"], payload["parentCallId"] = c.ID, c.CallID
	return s.event(ctx, tx, t.run, evChildToolCalled, ledger.ActorAgent, payload)
}

// callEnded records a tool call's end: the Run's own (c nil) or a
// child's. payload is agent.tool.completed's, {tool, callId, status, …};
// its tool is the one the call was recorded under, when it was.
func (t *translator) callEnded(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, payload map[string]any) error {
	id, _ := payload["callId"].(string)
	name, _ := payload["tool"].(string)
	if c == nil {
		if seen, ok := t.seenCalls[id]; ok {
			name = seen
		}
		delete(t.openCalls, id)
		delete(t.waitsOn, id)
		payload["tool"] = name
		if isEdit(name) {
			s.pokeDiff(t.run.ID)
		}
		if err := t.parentCallEnded(ctx, tx, s, id, payload); err != nil {
			return err
		}
		return s.event(ctx, tx, t.run, evToolCompleted, ledger.ActorAgent, payload)
	}
	if err := t.startChild(ctx, tx, s, c); err != nil {
		return err
	}
	if seen, ok := c.Calls[id]; ok {
		name = seen
	}
	delete(c.Calls, id)
	payload["tool"] = name
	if isEdit(name) {
		s.pokeDiff(t.run.ID)
	}
	st, _ := payload["status"].(string)
	t.touchChild(c, name+" "+st, nil)
	payload["childId"], payload["parentCallId"] = c.ID, c.CallID
	return s.event(ctx, tx, t.run, evChildToolCompleted, ledger.ActorAgent, payload)
}

// parentCallEnded: the Run's own call ended, and its line in
// runs.children goes. A task's child that never said it ended ends with
// it; a Codex agent outlives the call that spawned it.
func (t *translator) parentCallEnded(ctx context.Context, tx pgx.Tx, s *Syncer, callID string, payload map[string]any) error {
	t.facts.drop(callID)
	c := t.childOfCall(callID)
	if c == nil || c.Kind != childTask || c.Ended || !c.Started {
		return nil
	}
	status := "completed"
	if payload["status"] == "error" {
		status = "failed"
	}
	return t.endChild(ctx, tx, s, c, status, "", nil, 0)
}

// childMessage records a child's completed message; empty text is none.
func (t *translator) childMessage(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, text string, cost float64,
	tokens map[string]any, truncated bool) error {
	if err := t.startChild(ctx, tx, s, c); err != nil {
		return err
	}
	c.CostUSD += cost
	text = strings.TrimSpace(text)
	if text == "" {
		t.touchChild(c, "step", nil)
		return nil
	}
	c.LastText = cutBytes(text, childTextLimit)
	t.touchChild(c, "said: "+text, nil)
	p := map[string]any{"text": c.LastText}
	if cost > 0 {
		p["costUsd"] = cost
	}
	if tokens != nil {
		p["tokens"] = tokens
	}
	if truncated {
		p["truncated"] = true
	}
	return s.event(ctx, tx, t.run, evChildMessage, ledger.ActorAgent, childPayload(c, p))
}

// childWaiting records a permission the harness asked for a child, on its
// call callID. It holds the child only while nothing answered it (lux's
// answered "allow" did) and the call is still open: lux relays the ask
// with a lux.warning, after child records that may follow it, so an ask
// for a call already over (or a child already ended) is said and holds
// nothing. Any later event of the child ends the wait.
func (t *translator) childWaiting(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, callID, permission string,
	patterns []string, answered string) error {
	if err := t.startChild(ctx, tx, s, c); err != nil {
		return err
	}
	if patterns == nil {
		patterns = []string{}
	}
	_, open := c.Calls[callID]
	held := answered != "allow" && !c.Ended && (callID == "" || open)
	p := map[string]any{"on": "permission", "permission": permission, "patterns": patterns, "answered": answered, "held": held}
	if callID != "" {
		p["childCallId"] = callID
	}
	if held {
		t.touchChild(c, "asked permission "+permission+" for "+strings.Join(patterns, ", "),
			map[string]any{"permission": permission, "patterns": patterns})
	}
	return s.event(ctx, tx, t.run, evChildWaiting, ledger.ActorAgent, childPayload(c, p))
}

// endChild records agent.child.ended once. durationSecs 0 is measured from
// its start as recorded.
func (t *translator) endChild(ctx context.Context, tx pgx.Tx, s *Syncer, c *childState, status, summary string,
	tokens map[string]any, durationSecs float64) error {
	if c.Ended {
		return nil
	}
	if err := t.startChild(ctx, tx, s, c); err != nil {
		return err
	}
	c.Ended = true
	clear(c.Calls)
	if durationSecs == 0 && !c.StartedAt.IsZero() {
		durationSecs = time.Since(c.StartedAt).Seconds()
	}
	if strings.TrimSpace(summary) == "" {
		summary = c.LastText
	}
	p := map[string]any{"status": status, "summary": cutBytes(strings.TrimSpace(summary), childTextLimit),
		"durationSecs": int64(durationSecs)}
	if c.CostUSD > 0 {
		p["costUsd"] = c.CostUSD
	}
	if tokens == nil && c.Tokens != (codexTokens{}) {
		tokens = map[string]any{"input": c.Tokens.Input - c.Tokens.Cached, "output": c.Tokens.Output, "cacheRead": c.Tokens.Cached}
	}
	if tokens != nil {
		p["tokens"] = tokens
	}
	t.touchChild(c, "ended "+status, nil)
	return s.event(ctx, tx, t.run, evChildEnded, ledger.ActorAgent, childPayload(c, p))
}

// cutBytes is s cut to at most n bytes on a character boundary, saying so.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutRunes(s[:n-len("…")], false) + "…"
}
