package fakelux

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// As lux's shim: init runs before the workload on every placement, so a
// resumed Run runs it again, in the same /workspace, from the same spec.
func TestInitRunsOnEveryPlacementFromTheSameSpec(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx := context.Background()
	// The here-document's /workspace is data: only the commands' is the
	// Run's directory.
	script := "set -eu\necho placed >> /workspace/placements\ncat > /workspace/f.tmp <<'X'\nkept /workspace $HOME `x`\nX\nmv /workspace/f.tmp /workspace/f\n"
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Init:     &lux.Init{Script: script}}, "init")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	dir := fake.Workspace(run.ID)
	first, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(first) != "kept /workspace $HOME `x`\n" {
		t.Fatalf("first placement wrote %q: %v", first, err)
	}
	if err := client.Stop(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "stopped")
	if _, err := client.Resume(ctx, run.ID, lux.ResumeInput{}); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	second, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(second) != string(first) {
		t.Fatalf("second placement wrote %q, first %q: %v", second, first, err)
	}
	placements, _ := os.ReadFile(filepath.Join(dir, "placements"))
	if string(placements) != "placed\nplaced\n" || fake.Workspace(run.ID) != dir {
		t.Errorf("init ran %q in %s, first in %s", placements, fake.Workspace(run.ID), dir)
	}
	fake.mu.Lock()
	inits := fake.runs[run.ID].inits
	fake.mu.Unlock()
	if inits != 2 {
		t.Errorf("init ran %d times over two placements", inits)
	}
}

// An init that exits non-zero fails the placement before the workload
// starts, with lux's reason.
func TestAFailingInitFailsThePlacement(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Init:     &lux.Init{Script: "exit 3"}}, "init-fails")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "failed")
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got, _ := client.Get(context.Background(), run.ID); got.StateReason == "init-failed" {
			return
		}
	}
	got, _ := client.Get(context.Background(), run.ID)
	t.Errorf("failed with reason %q", got.StateReason)
}
