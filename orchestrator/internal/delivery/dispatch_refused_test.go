package delivery

import (
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// L2: only the refusals GitHub gives before it creates a run mean nothing
// ran. Any other status, a 408 or 499 a proxy may answer after forwarding
// the POST among them, may or may not have started one.
func TestOnlyGitHubsOwnRefusalsMeanNothingRan(t *testing.T) {
	for status, refused := range map[int]bool{
		400: true, 401: true, 403: true, 404: true, 409: true, 422: true, 429: true,
		402: false, 405: false, 408: false, 410: false, 413: false, 499: false, 500: false, 502: false, 503: false,
	} {
		err := fmt.Errorf("dispatch: %w", &forge.Error{Status: status, Message: "x"})
		if got := postRefused(err); got != refused {
			t.Errorf("%d: refused %v, want %v", status, got, refused)
		}
	}
	if postRefused(fmt.Errorf("cut off")) {
		t.Error("an error with no answer is a refusal")
	}
}
