package fakelux

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
		pools[1].HostSizeFrom != "history" || pools[2].HostSize.Disk != 0 || pools[1].ID != "pool_b8r2n5w1c7z3" {
		t.Errorf("pools = %+v", pools)
	}
	fake.mu.Lock()
	fake.Pools = OldPools()
	fake.mu.Unlock()
	if pools, _ = client.Pools(ctx); pools[0].HostSize != nil || pools[0].IsDefault || pools[0].ID != "pool_d4f7k2m9q1x8" {
		t.Errorf("old pools = %+v", pools)
	}

	spec := lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Resources: &lux.Resources{CPUs: 8, Memory: 48 << 30, Disk: 200 << 30}, Placement: &lux.PlacementSpec{PoolID: "pool_b8r2n5w1c7z3"}}
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

// A placement.poolId the tenant has no pool for is refused at submit with
// 422 unknown_pool, as lux refuses it: one never there, and one deleted
// (DELETE /v1/pools/{name}) after a Run was placed in it. Nothing is made.
// A pool added (POST /v1/pools) is placed in by the id it was given.
func TestAnUnknownPoolIDIsRefused(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx := context.Background()
	in := func(poolID string) lux.Spec {
		return lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
			Placement: &lux.PlacementSpec{PoolID: poolID}}
	}
	refused := func(err error) bool {
		le, ok := lux.AsError(err)
		return ok && le.Status == http.StatusUnprocessableEntity && le.Code == "unknown_pool" && !le.Retryable()
	}

	if _, err := client.Submit(ctx, in("pool_nope"), "a"); !refused(err) {
		t.Errorf("a pool id lux never had: err = %v, want 422 unknown_pool", err)
	}
	// By name is not an id: the name of a pool that exists is refused too.
	if _, err := client.Submit(ctx, in("big"), "b"); !refused(err) {
		t.Errorf("a pool's name as its id: err = %v, want 422 unknown_pool", err)
	}
	if _, err := client.Submit(ctx, in("pool_b8r2n5w1c7z3"), "c"); err != nil {
		t.Fatalf("big by its id: %v", err)
	}

	req, _ := http.NewRequest("DELETE", srv.URL+"/v1/pools/big", nil)
	req.Header.Set("Authorization", "Bearer k")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete big: %v %v", res, err)
	}
	pools, _ := client.Pools(ctx)
	if len(pools) != 2 || slices.ContainsFunc(pools, func(p lux.Pool) bool { return p.Name == "big" }) {
		t.Errorf("pools after deleting big = %+v", pools)
	}
	if _, err := client.Submit(ctx, in("pool_b8r2n5w1c7z3"), "d"); !refused(err) {
		t.Errorf("a deleted pool's id: err = %v, want 422 unknown_pool", err)
	}
	if n := len(fake.Runs()); n != 1 {
		t.Errorf("%d Runs made, want only the one placed in big while it was there", n)
	}

	// A pool added gets an id of its own; updating it by name keeps the id.
	put := func(body string) lux.Pool {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/pools", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer k")
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("put pool %s: %v %v", body, res, err)
		}
		defer res.Body.Close()
		var p lux.Pool
		_ = json.NewDecoder(res.Body).Decode(&p)
		return p
	}
	added := put(`{"name":"scratch","provider":"static"}`)
	if !strings.HasPrefix(added.ID, "pool_") || put(`{"name":"scratch","provider":"ec2"}`).ID != added.ID {
		t.Errorf("added %+v: want a pool_ id that an update keeps", added)
	}
	if _, err := client.Submit(ctx, in(added.ID), "e"); err != nil {
		t.Errorf("the added pool by its id: %v", err)
	}
}
