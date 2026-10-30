package branchdiff_test

import (
	"context"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/branchdiff"
)

// The pruner takes the snapshot a reset head already has (its candidate
// lock) before the capture reads it: the capture's insert still conflicts
// with it, its existing_branch_snapshot waits on the lock, and once the
// prune commits finds none. The capture then records the head as a new
// snapshot, in the same transaction, rather than counting a failure.
func TestACaptureWhoseSnapshotThePrunerDeletesUnderItCapturesItAnew(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	first := w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "first\n"})
	w.capture()
	w.fake.CommitFiles(w.branch, "second", map[string]string{"app.ts": "second\n"})
	w.capture()
	got := w.snapshots()
	if len(got) != 2 {
		t.Fatalf("snapshots %+v, want 2", got)
	}
	old := got[0].ID
	// The first is prunable: two days old, superseded a day and a half ago,
	// and the request names the second.
	w.exec(`UPDATE task_branch_snapshots SET captured_at = now() - interval '48 hours' WHERE organization_id = $1 AND id = $2`, old)
	w.exec(`UPDATE task_branch_snapshots SET captured_at = now() - interval '36 hours' WHERE organization_id = $1 AND id = $2`, got[1].ID)
	git(t, w.origin, "update-ref", "refs/heads/"+w.branch, first)
	w.exec(`SELECT want_branch_capture($1, 'wi_b', 1, 'repo_api', '')`)

	// Session A: the prune's transaction, its candidate lock on the first taken.
	prune, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer prune.Rollback(ctx)
	if _, err := prune.Exec(ctx, `SELECT 1 FROM task_branch_snapshots WHERE id = $1 FOR UPDATE`, old); err != nil {
		t.Fatal(err)
	}
	// Session B: the capture, until it waits on that lock.
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := w.cap.Sweep(ctx)
		done <- result{n, err}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		// Statistics are read once per transaction unless cleared.
		if _, err := prune.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		if err := prune.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE '%existing_branch_snapshot%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("the capture ended (%d, %v) before waiting on the pruner's lock", r.n, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the capture never waited on the pruner's lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var pruned int
	if err := prune.QueryRow(ctx, `SELECT prune_branch_snapshots(10)`).Scan(&pruned); err != nil || pruned != 1 {
		t.Fatalf("pruned %d (%v), want the first", pruned, err)
	}
	if err := prune.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.n != 1 {
		t.Fatalf("the capture: %d succeeded (%v), want 1", r.n, r.err)
	}

	after := w.snapshots()
	if len(after) != 2 || after[0].ID != got[1].ID || after[1].ID == old || after[1].Head != first || after[1].Previous != got[1].ID {
		t.Fatalf("snapshots after %+v, want the second and a new one at the first's head", after)
	}
	var id, status string
	var failures, events int
	if err := w.owner.QueryRow(ctx, `SELECT snapshot_id, status, failures FROM task_branch_capture_requests WHERE task_id = 'wi_b'`).
		Scan(&id, &status, &failures); err != nil || id != after[1].ID || status != "done" || failures != 0 {
		t.Fatalf("the request: %s %s %d failures (%v), want %s done 0", id, status, failures, err, after[1].ID)
	}
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE task_id = 'wi_b' AND event_type = $1`, branchdiff.EvSnapshotCaptured).
		Scan(&events); err != nil || events != 3 {
		t.Errorf("%d captured events (%v), want 3: the new one says so", events, err)
	}
}
