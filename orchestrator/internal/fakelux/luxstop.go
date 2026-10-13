package fakelux

import (
	"encoding/json"
	"time"
)

// ProcessExited is how lux's ACP adapter fails the turn in progress when
// the agent's process goes away under it (lux internal/adapter/rpc.go):
// the container stopped, whatever stopped it.
const ProcessExited = "session/prompt: process exited (0)"

// AgentExits is the agent's harness dying with its container: its turn in
// progress, if any, fails with ProcessExited and it goes idle, in the
// record stream. Callers say separately why lux stopped it, before or
// after (ExceedDisk, EndStopping, Move): lux's events and the agent's
// records arrive in either order.
func (s *Server) AgentExits(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		s.agentExits(run)
	}
}

func (s *Server) agentExits(run *Run) {
	if !run.busy {
		return
	}
	run.busy = false
	s.turnEnd(run, map[string]any{"stopReason": "", "error": ProcessExited}, true)
}

// ExceedDisk is lux finding a running Run over its disk limit: the
// runner's disk.exceeded with its numbers, in bytes, then luxd stopping
// the placement with reason disk (lux internal/server/runner.go). The Run
// stays stopping until EndStopping.
func (s *Server) ExceedDisk(id string, used, limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil || run.State != "running" {
		return
	}
	s.luxEvent(run, "disk.exceeded", map[string]any{"usedBytes": used, "limitBytes": limit, "graceSeconds": 300.0, "ceiling": false})
	s.setStateWith(run, "stopping", "disk")
}

// EndStopping ends a stopping Run's placement as lux reports it: state with
// reason (failed with "disk limit exceeded" for a disk stop).
func (s *Server) EndStopping(id, state, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		s.dropQueued(run)
		s.setStateWith(run, state, reason)
	}
}

// Move is lux moving a running Run to another host for reason (preempt: a
// spot host reclaimed; drain; migrate): it stops, its agent's turn
// breaking with its container (AgentExits), and lux resumes it elsewhere
// at once, idle, with its conversation, waiting for input. A BeforeStart
// hook holds the new placement, as Migrate's does.
func (s *Server) Move(id, reason string) {
	s.mu.Lock()
	run := s.runs[id]
	if run == nil || run.State != "running" {
		s.mu.Unlock()
		return
	}
	run.Calls = append(run.Calls, reason)
	s.setStateWith(run, "stopping", reason)
	s.agentExits(run)
	s.dropQueued(run)
	s.setStateWith(run, "stopped", reason)
	run.Epoch++
	run.starts++
	run.moveNext = true
	run.pendingResize = nil
	accepted := time.Now()
	run.acceptedAt = &accepted
	s.setStateWith(run, "resuming", "auto-resume after "+reason)
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	epoch, start := run.Epoch, run.starts
	s.mu.Unlock()
	go s.play(run, epoch, start, spec, true)
}
