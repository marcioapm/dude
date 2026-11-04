package phases

import (
	"strings"
	"testing"
	"time"
)

// The temporary model failures each harness reports, as it reports them,
// are retried — with the wait an error asks for — and nothing else is.
func TestATransientTurnErrorIsToldFromAnyOther(t *testing.T) {
	for _, c := range []struct {
		err   string
		after time.Duration
	}{
		// Claude Code's result line (translate_claude.go, line.failure).
		{`API Error: Request rejected (429) · {"error":"All provider keys are rate limited","retry_after":143}`, 143 * time.Second},
		{`API Error: Request rejected (429) · {\"error\":\"All provider keys are rate limited\",\"retry_after\":143}`, 143 * time.Second},
		{"API Error: Connection closed mid-response", 0},
		{"API Error: 529 overloaded", 0},
		{"API Error: 502 Bad Gateway", 0},
		{"API Error: 503 Service Unavailable", 0},
		{"API Error: 504 Gateway Timeout", 0},
		// Codex's error notification after its own retries.
		{"unexpected status 503 Service Unavailable", 0},
		{"exceeded retry limit, last status: 429 Too Many Requests", 0},
		{"stream disconnected before completion: error sending request", 0},
		// OpenCode, through lux's ACP adapter.
		{"session/prompt: rate limited (429)", 0},
		{"session/prompt: Internal error: read ECONNRESET (-32603)", 0},
		{"session/prompt: Internal error: Overloaded (-32603)", 0},
		{"429 Too Many Requests; Retry-After: 30", 30 * time.Second},
	} {
		transient, after := transientTurnError(c.err)
		if !transient || after != c.after {
			t.Errorf("%q: transient %v after %v, want transient after %v", c.err, transient, after, c.after)
		}
	}
	for _, err := range []string{
		"unexpected status 401 Unauthorized",
		"API Error: 400 {\"type\":\"invalid_request_error\"}",
		"API Error: 500",
		"session/prompt: Internal error: Cannot connect to API: Unable to connect. Is the computer able to access the url? (-32603)",
		"session/prompt: Internal error: prompt is too long: 212000 tokens > 200000 maximum (-32603)",
		"session/prompt: process exited (137)",
		"There's an issue with the selected model (claude-nope-9).",
		"Claude Code ended the turn as failed (error_during_execution)",
		"Codex ended the turn as failed",
		"read 5290 tokens (-32603)",
	} {
		if transient, _ := transientTurnError(err); transient {
			t.Errorf("%q: retried, want not", err)
		}
	}
}

// Without a wait asked for, the back-off doubles from 15 s up to 5 min,
// jittered; with one, it is never sooner than asked.
func TestATurnRetryBacksOffExponentiallyOrAsAsked(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 15 * time.Second, 2: 30 * time.Second, 3: time.Minute, 4: 2 * time.Minute,
		6: 5 * time.Minute, 9: 5 * time.Minute} {
		if got := turnRetryWait(attempt, 0, 0.5); got != want {
			t.Errorf("attempt %d: %v, want %v", attempt, got, want)
		}
		lo, hi := turnRetryWait(attempt, 0, 0), turnRetryWait(attempt, 0, 0.999)
		if lo >= want || hi <= want && want < turnRetryMax || hi > turnRetryMax {
			t.Errorf("attempt %d: jitter spans %v..%v around %v", attempt, lo, hi, want)
		}
	}
	if got := turnRetryWait(1, 143*time.Second, 0); got != 143*time.Second {
		t.Errorf("retry_after 143: %v", got)
	}
	if got := turnRetryWait(1, 143*time.Second, 0.999); got < 143*time.Second || got > 158*time.Second {
		t.Errorf("retry_after 143, jittered: %v", got)
	}
	if !strings.Contains(retriedReason("the agent's turn failed: x", 4), "retried a temporary model error 4 times") {
		t.Errorf("reason: %q", retriedReason("the agent's turn failed: x", 4))
	}
	if retriedReason("r", 0) != "r" {
		t.Errorf("no retries changes the reason")
	}
}
