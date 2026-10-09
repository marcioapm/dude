package phases

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// sweepLux is a lux whose Run is gone (so its exits are settled) and which
// lists, and serves, the artifacts given.
type sweepLux struct {
	lux.Client
	listed []lux.Artifact
	bodies map[string]string
}

func (l *sweepLux) Get(context.Context, string) (lux.Run, error) {
	return lux.Run{}, &lux.Error{Status: http.StatusNotFound, Code: "not_found"}
}
func (l *sweepLux) Artifacts(context.Context, string) ([]lux.Artifact, error) { return l.listed, nil }
func (l *sweepLux) Download(_ context.Context, id string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(l.bodies[id])), nil
}

// The stop-time sweep records every version lux lists, those the stream
// already recorded once only, and of the final diff's patch the latest
// version as the Run's diff, never as a file.
func TestTheSweepRecordsEveryVersionOnceAndTheLatestFinalDiff(t *testing.T) {
	w := newReceiptWorld(t)
	ctx := context.Background()
	if _, err := w.owner.Exec(ctx, `UPDATE runs SET lux_run_id = 'lrun_1', status = 'completed' WHERE id = $1`, w.tr.run.ID); err != nil {
		t.Fatal(err)
	}
	// Recorded from the stream while it ran.
	w.published("art_aaaaaaaaaaaaaaaa", lux.PublishedPrefix+"notes.md", 1)

	notes := lux.PublishedPrefix + "notes.md"
	patch := FinalDiffPrefix + "target.patch"
	fake := &sweepLux{listed: []lux.Artifact{
		{ID: "art_aaaaaaaaaaaaaaaa", Epoch: 2, Path: notes, Version: 1, Description: "Why the export streams rows", Size: 12, SHA256: "ab12", Available: true},
		{ID: "art_bbbbbbbbbbbbbbbb", Epoch: 2, Path: notes, Version: 2, Description: "With the numbers", Size: 20, SHA256: "cd34", Available: true},
		{ID: "art_pppppppppppppp01", Epoch: 2, Path: patch, Version: 1, Available: true},
		{ID: "art_pppppppppppppp02", Epoch: 2, Path: patch, Version: 2, Available: true},
	}, bodies: map[string]string{
		"art_pppppppppppppp01": "# dude-diff target aaaa\ndiff --git a/OLD.md b/OLD.md\nnew file mode 100644\n--- /dev/null\n+++ b/OLD.md\n@@ -0,0 +1 @@\n+old\n",
		"art_pppppppppppppp02": "# dude-diff target aaaa\ndiff --git a/NEW.md b/NEW.md\nnew file mode 100644\n--- /dev/null\n+++ b/NEW.md\n@@ -0,0 +1 @@\n+new\n",
	}}
	a := &Artifacts{DB: w.s.DB, Lux: fake}
	for range 2 {
		if _, err := w.owner.Exec(ctx, `UPDATE runs SET artifacts_due_at = now(), artifacts_next_at = now() WHERE id = $1`, w.tr.run.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := a.Sweep(ctx); err != nil || n != 1 {
			t.Fatalf("swept %d, %v", n, err)
		}
	}

	got := w.artifacts()
	if len(got) != 2 || got[0].StorageKey != "art_aaaaaaaaaaaaaaaa" || got[1].StorageKey != "art_bbbbbbbbbbbbbbbb" ||
		got[1].Description != "With the numbers" || got[1].Name != "notes.md" {
		t.Errorf("recorded %+v; want both versions of notes.md, nothing of the diff", got)
	}
	if n := w.events(ArtifactEventType); n != 2 {
		t.Errorf("%d artifact.created events, want 2", n)
	}
	var files string
	if err := w.owner.QueryRow(ctx, `SELECT files::text FROM run_diffs WHERE run_id = $1 AND final`, w.tr.run.ID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, "NEW.md") || strings.Contains(files, "OLD.md") {
		t.Errorf("the final diff is %s; want the patch's latest version", files)
	}
}
