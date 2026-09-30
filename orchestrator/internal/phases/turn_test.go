package phases

import (
	"strings"
	"testing"
)

func TestATurnFailureNamesTheModelOnlyWhenTheAgentNeverStarted(t *testing.T) {
	const apiErr = "session/prompt: Internal error: Cannot connect to API (-32603)"
	if got := turnFailure(apiErr, "claude-sonnet-5-5", false); !strings.Contains(got, `model "claude-sonnet-5-5"`) ||
		!strings.HasSuffix(got, apiErr) {
		t.Errorf("nothing produced: %q", got)
	}
	// The agent had been working: the model reached it, so the error is
	// the agent's alone.
	if got := turnFailure(apiErr, "llm-anthropic/claude-sonnet-5", true); got != "the agent's turn failed: "+apiErr {
		t.Errorf("after work: %q", got)
	}
	if got := turnFailure("session/prompt: rate limited (429)", "m", false); got != "the agent's turn failed: session/prompt: rate limited (429)" {
		t.Errorf("another error: %q", got)
	}
}
