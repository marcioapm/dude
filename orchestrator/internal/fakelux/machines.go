package fakelux

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Machines: the pools a tenant's key can use, and the memory a Run's
// container is given.

const gib = int64(1) << 30

// DefaultPools are three pools with their hosts' sizes, as a lux that
// reports them (hostSize, hostSizeFrom, instanceType) answers: the
// tenant's default, a bigger one whose hosts are all gone, and a static
// platform pool that reserves no disk. Their ids are unlike their names,
// so code that confuses the two fails here.
func DefaultPools() []lux.Pool {
	three, none, two := 3, 0, 2
	return []lux.Pool{
		{ID: "pool_d4f7k2m9q1x8", Name: "default", Provider: "ec2", IsDefault: true, InstanceType: "c7a.4xlarge", HostSizeFrom: "running", HostsRunning: &three,
			HostSize: &lux.HostSize{CPUs: 16, Memory: 32 * gib, Disk: 180 * gib}},
		{ID: "pool_b8r2n5w1c7z3", Name: "big", Provider: "ec2", InstanceType: "c7a.8xlarge", HostSizeFrom: "history", HostsRunning: &none,
			HostSize: &lux.HostSize{CPUs: 32, Memory: 64 * gib, Disk: 380 * gib}},
		{ID: "pool_s3v6p8j4t0h5", Name: "shared", Provider: "static", Platform: true, Shared: true, HostSizeFrom: "running", HostsRunning: &two,
			HostSize: &lux.HostSize{CPUs: 8, Memory: 16 * gib, Disk: 0}},
	}
}

// OldPools are the same pools as a lux from before host sizes lists them:
// id, name, provider, platform, and nothing about their hosts.
func OldPools() []lux.Pool {
	out := DefaultPools()
	for i := range out {
		out[i] = lux.Pool{ID: out[i].ID, Name: out[i].Name, Provider: out[i].Provider, Platform: out[i].Platform, Shared: out[i].Shared}
	}
	return out
}

// pools is what GET /v1/pools lists now. Called with s.mu held.
func (s *Server) pools() []lux.Pool {
	if s.Pools == nil {
		s.Pools = DefaultPools()
	}
	return s.Pools
}

func (s *Server) listPools(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	pools := slices.Clone(s.pools())
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"pools": pools})
}

// putPool is POST /v1/pools, as lux takes it: the pool of that name
// updated, its id kept, or a new one with an id of its own.
func (s *Server) putPool(w http.ResponseWriter, r *http.Request) {
	var in lux.Pool
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		writeErr(w, 422, "invalid_request", "a pool needs a name")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pools := s.pools()
	if i := slices.IndexFunc(pools, func(p lux.Pool) bool { return p.Name == in.Name }); i >= 0 {
		in.ID = pools[i].ID
		pools[i] = in
	} else {
		s.nextPool++
		in.ID = fmt.Sprintf("pool_fake%d", s.nextPool)
		s.Pools = append(pools, in)
	}
	writeJSON(w, 200, in)
}

// deletePool is DELETE /v1/pools/{name}, as lux removes one.
func (s *Server) deletePool(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pools := s.pools()
	i := slices.IndexFunc(pools, func(p lux.Pool) bool { return p.Name == r.PathValue("name") })
	if i < 0 {
		writeErr(w, 404, "not_found", "no pool named "+r.PathValue("name"))
		return
	}
	s.Pools = slices.Delete(pools, i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

// placementProblem refuses, as lux's submit does, a placement.poolId the
// tenant has no pool for. Called with s.mu held.
func (s *Server) placementProblem(rawSpec json.RawMessage) string {
	var spec lux.Spec
	if json.Unmarshal(rawSpec, &spec) != nil || spec.Placement == nil || spec.Placement.PoolID == "" {
		return ""
	}
	if slices.ContainsFunc(s.pools(), func(p lux.Pool) bool { return p.ID == spec.Placement.PoolID }) {
		return ""
	}
	return "no pool with id " + spec.Placement.PoolID
}

// memoryLimit is what a newer lux gives a Run's container: the memory its
// spec asks for, less the host's share (MemoryShare), rounded down to a
// MiB. nil when the spec names none, or when the fake plays a lux that
// does not report it (MemoryShare zero).
func (s *Server) memoryLimit(run *Run) *int64 {
	if s.MemoryShare <= 0 {
		return nil
	}
	var spec struct {
		Resources *lux.Resources `json:"resources"`
	}
	if json.Unmarshal(run.Spec, &spec) != nil || spec.Resources == nil || spec.Resources.Memory == 0 {
		return nil
	}
	mib := int64(math.Floor(float64(spec.Resources.Memory) * s.MemoryShare / (1 << 20)))
	limit := mib << 20
	return &limit
}
