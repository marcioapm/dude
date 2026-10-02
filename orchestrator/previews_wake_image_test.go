package orchestrator_test

// A wakeable preview whose project names a library image: the wake waits
// for the image without holding its claim or logging a failure, then runs
// on the final; an image it cannot have fails the preview before lux.

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/images"
)

// lockedBuffer is a log sink the sweep's goroutines write to at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAWokenPreviewWaitsForItsLibraryImageThenRunsOnTheFinal(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	logs := &lockedBuffer{}
	w.previews.Log = slog.New(slog.NewTextHandler(logs, nil))
	w.useLayer(nextLayer)
	version := w.libraryImage("img_web", "web", true)
	mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_web' WHERE id = $1`, w.project)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")

	w.lux.RequestServer(web, "/")
	w.until("the woken preview to wait on a finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND image_build_id IS NOT NULL AND lux_run_id IS NULL
			AND wake_wanted_at IS NOT NULL AND wake_claimed_at IS NULL`, runID) == 1
	})
	if got := w.finishJobs(); len(got) != 1 || got[0] != "queued "+nextLayer {
		t.Fatalf("finish jobs = %v", got)
	}
	// Looked at again a few times while it waits: one preparing_image, no
	// warning, no lux Run.
	for range 3 {
		mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
		w.pump()
	}
	if n := len(w.luxRuns()); n != 0 {
		t.Fatalf("%d lux Runs before the image", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed' AND payload->>'change' = 'preparing_image'`, runID); n != 1 {
		t.Errorf("%d preparing_image changes, want 1", n)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("waiting logged a warning:\n%s", logs.String())
	}
	// What the Run page reads: the preview is preparing its image.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND image_waiting_since IS NOT NULL`, runID); n != 1 {
		t.Errorf("not waiting as paused with its wait recorded")
	}

	// The builder finishes it: the next look submits on the final.
	mustExec(t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, $2, $3, $4)`,
		w.org, version, nextLayer, finalRef)
	mustExec(t, w.owner, `UPDATE image_builds SET state = 'succeeded' WHERE organization_id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the preview to reach lux", func() bool { return len(w.luxRuns()) == 1 })
	if got := submitted(t, w.luxRuns()[0]).Image.Ref; got != finalRef {
		t.Errorf("woke on %s", got)
	}
	w.open(web)
	if got := w.str(`SELECT image->>'name' || ' v' || (image->>'version') FROM runs WHERE id = $1`, runID); got != "web v1" {
		t.Errorf("runs.image = %s", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND image_waiting_since IS NULL`, runID); n != 1 {
		t.Error("still marked as waiting for its image")
	}
}

func TestAWokenPreviewWhoseImageIsRefusedFails(t *testing.T) {
	for _, c := range []struct {
		name  string
		layer string
		want  string
	}{
		{"no dude layer", "", "cannot start: " + images.ErrNotConfigured},
		{"a failed finish", nextLayer, "cannot start: its image web v1 could not get the dude layer: the image needs git: agents commit with it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.useLayer(c.layer)
			w.libraryImage("img_web", "web", true)
			mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_web' WHERE id = $1`, w.project)
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			if c.layer != "" {
				// The wake waits on a finish, which then fails.
				w.lux.RequestServer(w.serverID(runID, "web"), "/")
				w.until("the wake to wait", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND image_build_id IS NOT NULL`, runID) == 1
				})
				mustExec(t, w.owner, `UPDATE image_builds SET state = 'failed', error = 'the image needs git: agents commit with it'
					WHERE organization_id = $1 AND kind = 'finish'`, w.org)
				mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
			} else {
				w.lux.RequestServer(w.serverID(runID, "web"), "/")
			}
			w.until("the preview to fail", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1
			})
			if got := w.str(`SELECT error FROM runs WHERE id = $1`, runID); got != c.want {
				t.Errorf("error = %q, want %q", got, c.want)
			}
			if n := len(w.luxRuns()); n != 0 {
				t.Errorf("%d lux Runs", n)
			}
			if n := w.count(`SELECT count(*) FROM image_builds WHERE organization_id = $1 AND kind = 'finish'`, w.org); c.layer != "" && n != 1 {
				t.Errorf("%d finish jobs: a failed one was queued again", n)
			}
		})
	}
}
