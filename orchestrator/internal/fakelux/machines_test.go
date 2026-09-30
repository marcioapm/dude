package fakelux

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The fake lists pools with their host sizes by default, the old shape on
// request, and reports a container's memory limit only when told to.
func TestPoolsAndTheMemoryLimit(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx := context.Background()

	pools, err := client.Pools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 3 || !pools[0].IsDefault || pools[0].HostSize == nil || pools[0].HostSize.Memory != 32<<30 ||
		pools[1].HostSizeFrom != "history" || pools[2].HostSize.Disk != 0 {
		t.Errorf("pools = %+v", pools)
	}
	fake.mu.Lock()
	fake.Pools = OldPools()
	fake.mu.Unlock()
	if pools, _ = client.Pools(ctx); pools[0].HostSize != nil || pools[0].IsDefault {
		t.Errorf("old pools = %+v", pools)
	}

	spec := lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Resources: &lux.Resources{CPUs: 8, Memory: 48 << 30, Disk: 200 << 30}, Placement: &lux.PlacementSpec{Pool: "big"}}
	run, err := client.Submit(ctx, spec, "m")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "the Run never ran", func(r *Run) bool { return r.State == "running" })
	got, _ := client.Get(ctx, run.ID)
	if len(got.Placements) == 0 || got.Placements[0].MemoryLimit != nil {
		t.Errorf("a lux that does not report the limit reported %+v", got.Placements)
	}
	fake.mu.Lock()
	fake.MemoryShare = 0.95
	fake.mu.Unlock()
	got, _ = client.Get(ctx, run.ID)
	if l := got.Placements[0].MemoryLimit; l == nil || *l != 46694<<20 {
		t.Errorf("memory limit = %v, want 95%% of 48 GiB, to the MiB", l)
	}
}
