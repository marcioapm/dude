package phases

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Evidence past what Exec keeps (lux.ExecLimit) is incomplete: the publish
// counts GitHub's whole compare instead of failing the Run.
func TestEvidencePastExecLimitCountsGitHubsNumbers(t *testing.T) {
	a := conflictMerged(t)
	flood := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		var open map[string]any
		_ = wsjson.Read(r.Context(), ws, &open)
		chunk := []byte("DUDE_ACCOUNTING\t" + strings.Repeat("x", 64<<10))
		for sent := 0; sent <= lux.ExecLimit; sent += len(chunk) {
			if wsjson.Write(r.Context(), ws, map[string]any{"data": chunk, "ch": "stdout"}) != nil {
				return
			}
		}
		_ = wsjson.Write(r.Context(), ws, map[string]any{"exitCode": 0})
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(flood.Close)
	client := lux.New(flood.URL, "k")
	a.evidence = func(ctx context.Context, start, head, base, repo string) (accounting, error) {
		return publicationAccounting(ctx, client, "run", start, head, base, repo)
	}
	acc := a.published()
	if !strings.Contains(acc.Full, "evidence is unavailable") || acc.Lines < 300 {
		t.Fatalf("lines %d full %q, want GitHub's whole compare", acc.Lines, acc.Full)
	}
	samePaths(t, acc, "base.go", "mine.go", "shared.txt")
}
