package phases

import (
	"strings"
	"testing"
)

func TestATurnFailureNamesTheTierAndModelOnlyWhenTheAgentNeverStarted(t *testing.T) {
	const apiErr = "session/prompt: Internal error: Cannot connect to API (-32603)"
	if got := turnFailure(apiErr, "Coder", "claude-sonnet-5-5", false); !strings.Contains(got, `on Coder, which requested "claude-sonnet-5-5" from the LLM proxy`) ||
		!strings.HasSuffix(got, apiErr) {
		t.Errorf("nothing produced: %q", got)
	}
	// A Run from before tiers has its model alone.
	if got := turnFailure(apiErr, "", "llm-anthropic/claude-sonnet-5", false); !strings.Contains(got, `on model "llm-anthropic/claude-sonnet-5"`) {
		t.Errorf("no tier: %q", got)
	}
	// The agent had been working: the model reached it, so the error is
	// the agent's alone.
	if got := turnFailure(apiErr, "Coder", "claude-sonnet-5", true); got != "the agent's turn failed: "+apiErr {
		t.Errorf("after work: %q", got)
	}
	if got := turnFailure("session/prompt: rate limited (429)", "Coder", "m", false); got != "the agent's turn failed: session/prompt: rate limited (429)" {
		t.Errorf("another error: %q", got)
	}
}
