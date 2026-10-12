package orchestrator_test

// Try again on a session whose agent did not start (delivery.RetryBrainstorm):
// a fresh agent, briefed with the message the failed one was started for,
// which it reads once; nothing to retry otherwise.

import (
	"strings"
	"testing"
)

func TestTryAgainOnASessionWhoseAgentDidNotStartStartsAFreshOneWithTheMessage(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	// Nothing failed yet: nothing to retry.
	if status, _ := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil); status != 409 {
		t.Fatalf("retry with no failed agent: %d, want 409", status)
	}
	s.lux.FailStarts("dude.session="+id, 1)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where would metering live?"})
	var failed string
	s.until("the session's first agent to fail to start", func() bool {
		var status string
		failed, status = s.brainstorm(id)
		return status == "failed"
	})
	if s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed' AND payload ? 'stage'`, failed) != 1 {
		t.Errorf("the failed start's run.failed names no stage")
	}
	// Someone not in the session may not.
	if status, _ := s.as(s.outsider, "POST", "/internal/sessions/"+id+"/retry", nil); status != 404 {
		t.Errorf("an outsider's retry: %d, want 404", status)
	}
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil)
	next, _ := out["runId"].(string)
	if next == "" || next == failed {
		t.Fatalf("retry answered %v", out)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, failed); n != 1 {
		t.Errorf("the new Run's run.created names the one it retries %d times, want 1", n)
	}
	started := s.started(id)
	if started != next {
		t.Fatalf("the agent that answered is %s, want the retry %s", started, next)
	}
	v := s.luxRun(next)
	if v == nil || !strings.Contains(v.raw, "where would metering live?") {
		t.Fatalf("the fresh agent's spec does not carry the message")
	}
	if n := strings.Count(v.raw, "where would metering live?"); n != 1 {
		t.Errorf("the message is in the fresh agent's briefing %d times, want once", n)
	}
	// The member's message is shown once in the session's Chat, on the first Run.
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message'`, id); n != 1 {
		t.Errorf("%d chat.message in the session, want the one", n)
	}
	// Now it ran: nothing to retry.
	if status, _ := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil); status != 409 {
		t.Errorf("retry after a start that worked: %d, want 409", status)
	}
}
