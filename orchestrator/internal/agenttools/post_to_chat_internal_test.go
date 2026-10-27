package agenttools

import (
	"errors"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
)

// Slack's code is read from the error's type, however the error is worded
// or wrapped.
func TestAPICodeIsSlacksCodeWhateverTheErrorSays(t *testing.T) {
	api := &slack.APIError{Method: "chat.postMessage", Code: "not_in_channel"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{api, "not_in_channel"},
		{fmt.Errorf("%w (after 2 tries)", api), "not_in_channel"},
		{chat.Transient(&slack.APIError{Method: "chat.postMessage", Code: "ratelimited"}, 0), "ratelimited"},
		{errors.New("slack chat.postMessage: not_in_channel"), ""},
	} {
		if got := apiCode(tc.err); got != tc.want {
			t.Errorf("apiCode(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
