package phases

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

// The sweep's lock on the Run's row admits the KEY SHARE an unrelated child
// insert's foreign-key check holds on it (a directive, an event, a peer
// wait): the sweep records its artifact while that transaction is open.
func TestTheSweepRecordsWhileAnotherTransactionHoldsTheRunsKeyShare(t *testing.T) {
	w := newReceiptWorld(t)
	ctx := context.Background()
	if _, err := w.owner.Exec(ctx, `UPDATE runs SET lux_run_id = 'lrun_1', status = 'completed',
		artifacts_due_at = now(), artifacts_next_at = now() WHERE id = $1`, w.tr.run.ID); err != nil {
		t.Fatal(err)
	}
	const id = "art_aaaaaaaaaaaaaaaa"
	a := &Artifacts{DB: w.s.DB, Lux: &sweepLux{listed: []lux.Artifact{
		{ID: id, Epoch: 2, Path: lux.PublishedPrefix + "notes.md", Version: 1, Description: "Why the export streams rows", Size: 12, SHA256: "ab12", Available: true},
	}}}

	holder, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR KEY SHARE`, w.tr.run.ID); err != nil {
		t.Fatal(err)
	}

	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if n, err := a.Sweep(bounded); err != nil || n != 1 {
		t.Fatalf("with the Run's KEY SHARE held elsewhere the sweep returned %d, %v; want it to record without waiting", n, err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if got := w.artifacts(); len(got) != 1 || got[0].StorageKey != id {
		t.Errorf("recorded %+v; want %s once", got, id)
	}
	if n := w.events(ArtifactEventType); n != 1 {
		t.Errorf("%d artifact.created events, want 1", n)
	}
	var due bool
	if err := w.owner.QueryRow(ctx, `SELECT artifacts_due_at IS NOT NULL FROM runs WHERE id = $1`, w.tr.run.ID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due {
		t.Error("the Run is still due; the sweep should have marked it collected")
	}
}

// The sweep and the stream's follower recording the same artifact at once:
// the follower's batch holds the Run's row (followOutput) when the sweep
// reaches its insert, and then records the artifact itself. Each waits for
// the other to finish, in turn, and the artifact is recorded once.
func TestTheSweepAndTheFollowerRecordingOneArtifactDoNotDeadlock(t *testing.T) {
	w := newReceiptWorld(t)
	// Registered after newReceiptWorld's cleanups, so it runs before them:
	// both workers have returned, their transactions ended, before the
	// pool and database are closed.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		workers.Wait()
	})
	// Every return, Fatal included, lets the follower go on.
	record := make(chan struct{})
	release := sync.OnceFunc(func() { close(record) })
	defer release()
	await := func(ch <-chan error) error {
		select {
		case err := <-ch:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if _, err := w.owner.Exec(ctx, `UPDATE runs SET lux_run_id = 'lrun_1', status = 'completed',
		artifacts_due_at = now(), artifacts_next_at = now() WHERE id = $1`, w.tr.run.ID); err != nil {
		t.Fatal(err)
	}
	const id = "art_aaaaaaaaaaaaaaaa"
	path := lux.PublishedPrefix + "notes.md"
	a := &Artifacts{DB: w.s.DB, Lux: &sweepLux{listed: []lux.Artifact{
		{ID: id, Epoch: 2, Path: path, Version: 1, Description: "Why the export streams rows", Size: 12, SHA256: "ab12", Available: true},
	}}}

	// The follower's batch, as followOutput runs it: the Run's row first,
	// then the frame. It records once the sweep is waiting on it.
	locked := make(chan int32)
	followed := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		followed <- w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			var pid int32
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM runs WHERE id = $1 FOR UPDATE`, w.tr.run.ID).Scan(&pid); err != nil {
				close(locked)
				return err
			}
			select {
			case locked <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-record:
			case <-ctx.Done():
				return ctx.Err()
			}
			data, _ := json.Marshal(map[string]any{"artifactId": id, "path": path, "name": "notes.md", "version": 1,
				"description": "Why the export streams rows", "size": 12, "sha256": "ab12", "contentType": "text/markdown"})
			return w.tr.luxEvent(ctx, tx, w.s, lux.Frame{Kind: "lux", Epoch: 2, EventType: "artifact.published", EventData: data})
		})
	}()
	var follower int32
	select {
	case pid, ok := <-locked:
		if !ok {
			t.Fatal(await(followed))
		}
		follower = pid
	case <-ctx.Done():
		t.Fatalf("the follower never locked the Run: %v", ctx.Err())
	}
	swept := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := a.Sweep(ctx)
		swept <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; {
		var waiting bool
		if err := w.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`,
			follower).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			release()
			t.Fatalf("the sweep never waited on the follower: %v", await(swept))
		}
		time.Sleep(5 * time.Millisecond)
	}
	release()
	if err := await(followed); err != nil {
		t.Errorf("follower: %v", err)
	}
	if err := await(swept); err != nil {
		t.Errorf("sweep: %v", err)
	}
	if got := w.artifacts(); len(got) != 1 || got[0].StorageKey != id {
		t.Errorf("recorded %+v; want %s once", got, id)
	}
	if n := w.events(ArtifactEventType); n != 1 {
		t.Errorf("%d artifact.created events, want 1", n)
	}
}
