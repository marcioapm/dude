package orchestrator_test

// Serving a published file of a Run given project secrets
// (/internal/artifacts/:id/content): text is read whole and redacted, at
// most redact.MaxText of it; binary is served as stored. The checksum lux
// gave is kept whenever the bytes served are the bytes stored.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

type served struct {
	status int
	body   []byte
	sha    string
	length int64
	code   string
}

// publishedBy publishes name with content from the Run's agent and waits
// for dude to record it; its artifact id.
func (w *world) publishedBy(runID, luxID, name, content string) string {
	w.t.Helper()
	w.lux.Publish(luxID, name, content, "")
	var id string
	w.until(name+" recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM artifacts WHERE run_id = $1 AND name = $2`, runID, name).Scan(&id)
		return id != ""
	})
	return id
}

func (w *world) serveArtifact(id string) served {
	w.t.Helper()
	req, _ := http.NewRequest("GET", w.api+"/internal/artifacts/"+id+"/content", nil)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	out := served{status: res.StatusCode, body: body, sha: res.Header.Get("X-Content-SHA256"), length: res.ContentLength}
	if res.StatusCode >= 300 {
		out.code = string(body)
	}
	return out
}

func sha(b string) string {
	sum := sha256.Sum256([]byte(b))
	return hex.EncodeToString(sum[:])
}

func TestAnArtifactOfARunGivenSecretsIsServedAsDocumented(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	_, runID, luxID := w.liveImplementer()

	t.Run("binary is served byte-identical with its checksum", func(t *testing.T) {
		png := "\x89PNG\r\n\x1a\n\x00\x00token=" + implToken + "\x00\xff"
		got := w.serveArtifact(w.publishedBy(runID, luxID, "shot.png", png))
		if got.status != 200 || string(got.body) != png {
			t.Errorf("served %d, %d bytes equal to the stored: %v", got.status, len(got.body), string(got.body) == png)
		}
		if got.sha != sha(png) || got.length != int64(len(png)) {
			t.Errorf("served with sha %q and length %d, want %q and %d", got.sha, got.length, sha(png), len(png))
		}
	})

	t.Run("text holding no secret keeps its checksum", func(t *testing.T) {
		notes := "connected with the token from the environment\n"
		got := w.serveArtifact(w.publishedBy(runID, luxID, "notes.txt", notes))
		if got.status != 200 || string(got.body) != notes || got.sha != sha(notes) || got.length != int64(len(notes)) {
			t.Errorf("served %d %q sha %q length %d", got.status, got.body, got.sha, got.length)
		}
	})

	t.Run("text of the limit is served redacted", func(t *testing.T) {
		tail := "token=" + implToken + "\n"
		text := strings.Repeat("x", redact.MaxText-len(tail)) + tail
		got := w.serveArtifact(w.publishedBy(runID, luxID, "at-limit.txt", text))
		want := text[:len(text)-len(tail)] + "token=[REDACTED:IMPL_TOKEN]\n"
		if got.status != 200 || string(got.body) != want {
			t.Errorf("served %d, %d bytes, redacted as expected: %v", got.status, len(got.body), string(got.body) == want)
		}
		if got.sha != "" || got.length != int64(len(want)) {
			t.Errorf("served with sha %q and length %d, want none and %d", got.sha, got.length, len(want))
		}
	})

	t.Run("text over the limit is refused, not served unredacted", func(t *testing.T) {
		text := strings.Repeat("x", redact.MaxText-len(implToken)) + implToken + "\n"
		got := w.serveArtifact(w.publishedBy(runID, luxID, "over.txt", text))
		if got.status != http.StatusUnprocessableEntity || !strings.Contains(got.code, "too_large_to_redact") {
			t.Errorf("served %d %.200s", got.status, got.code)
		}
		if strings.Contains(string(got.body), implToken) {
			t.Error("the refusal holds the value")
		}
	})
}
