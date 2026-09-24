package agenttools_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Claude Code asks server/discover before initialize. Any JSON-RPC error
// (at HTTP 200) lets it go on to initialize; an HTTP failure would not.
func TestAnUnknownMethodBeforeInitializeIsAJSONRPCError(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_disc", "implementer", "running")
	req, _ := http.NewRequest("POST", f.url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	t.Logf("%d %s", res.StatusCode, body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"error"`) {
		t.Errorf("server/discover got %d %s, want a JSON-RPC error at 200", res.StatusCode, body)
	}
}
