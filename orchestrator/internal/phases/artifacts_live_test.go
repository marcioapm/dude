package phases

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// published feeds the translator lux's artifact.published for one
// artifact, in a transaction of its own as a batch would be.
func (w *receiptWorld) published(id, path string, version int) {
	w.t.Helper()
	data, _ := json.Marshal(map[string]any{"artifactId": id, "path": path, "name": path[len(lux.PublishedPrefix):],
		"version": version, "description": "Why the export streams rows", "size": 12, "sha256": "ab12",
		"contentType": "text/markdown"})
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.tr.luxEvent(context.Background(), tx, w.s, lux.Frame{Kind: "lux", Epoch: 2, EventType: "artifact.published", EventData: data})
	}); err != nil {
		w.t.Fatal(err)
	}
}

type recordedArtifact struct {
	Name, StorageKey, ContentType, SHA256 string
	Size                                  int64
	Epoch                                 int
}

func (w *receiptWorld) artifacts() []recordedArtifact {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT name, storage_key, content_type, sha256, size_bytes, epoch
		FROM artifacts WHERE run_id = $1 ORDER BY storage_key`, w.tr.run.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[recordedArtifact])
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

// A file the agent publishes is recorded the moment lux says it can be
// downloaded, with artifact.created, while the Run is still running; a
// replay of the same event (a restarted follower reads the stream again)
// records nothing more.
func TestAPublishedArtifactIsRecordedOnceFromTheStream(t *testing.T) {
	w := newReceiptWorld(t)
	if _, err := w.owner.Exec(context.Background(), `UPDATE runs SET status = 'running' WHERE id = $1`, w.tr.run.ID); err != nil {
		t.Fatal(err)
	}
	w.published("art_aaaaaaaaaaaaaaaa", lux.PublishedPrefix+"design/notes.md", 1)
	w.published("art_aaaaaaaaaaaaaaaa", lux.PublishedPrefix+"design/notes.md", 1)

	got := w.artifacts()
	want := recordedArtifact{Name: "design/notes.md", StorageKey: "art_aaaaaaaaaaaaaaaa", ContentType: "text/markdown",
		SHA256: "ab12", Size: 12, Epoch: 2}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("recorded %+v, want only %+v", got, want)
	}
	if n := w.events(ArtifactEventType); n != 1 {
		t.Errorf("%d artifact.created events, want 1", n)
	}
	var status string
	var due bool
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, artifacts_due_at IS NOT NULL FROM runs WHERE id = $1`,
		w.tr.run.ID).Scan(&status, &due)
	if status != statusRunning || due {
		t.Errorf("the Run is %s, due for the sweep %v; recorded live, it is neither stopped nor swept", status, due)
	}

	// Published again: lux's next version, a new artifact id, a new row.
	w.published("art_bbbbbbbbbbbbbbbb", lux.PublishedPrefix+"design/notes.md", 2)
	if got := w.artifacts(); len(got) != 2 || got[1].Name != "design/notes.md" {
		t.Errorf("after a second version, recorded %+v", got)
	}
	if n := w.events(ArtifactEventType); n != 2 {
		t.Errorf("%d artifact.created events, want 2", n)
	}
}

// The final diff the beforeStop hook publishes is dude's own, left to the
// stop-time sweep, which records it as the Run's diff; nor is anything
// outside the published files (lux's artifacts.paths) for people.
func TestTheFinalDiffAndCollectedFilesAreNotRecordedLive(t *testing.T) {
	w := newReceiptWorld(t)
	w.published("art_cccccccccccccccc", FinalDiffPrefix+"api.patch", 1)
	data, _ := json.Marshal(map[string]any{"artifactId": "art_dddddddddddddddd", "path": "/workspace/out/report.xml",
		"name": "/workspace/out/report.xml", "version": 1, "size": 3, "sha256": "cd"})
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.tr.luxEvent(context.Background(), tx, w.s, lux.Frame{Kind: "lux", EventType: "artifact.published", EventData: data})
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.artifacts(); len(got) != 0 {
		t.Errorf("recorded %+v; want nothing", got)
	}
	if n := w.events(ArtifactEventType); n != 0 {
		t.Errorf("%d artifact.created events, want none", n)
	}
}
