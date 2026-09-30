package fakelux

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Machines: the pools a tenant's key can use, and the memory a Run's
// container is given.

const gib = int64(1) << 30

// DefaultPools are three pools with their hosts' sizes, as a lux that
// reports them (hostSize, hostSizeFrom, instanceType) answers: the
// tenant's default, a bigger one whose hosts are all gone, and a static
// platform pool that reserves no disk.
func DefaultPools() []lux.Pool {
	three, none, two := 3, 0, 2
	return []lux.Pool{
		{Name: "default", Provider: "ec2", IsDefault: true, InstanceType: "c7a.4xlarge", HostSizeFrom: "running", HostsRunning: &three,
			HostSize: &lux.HostSize{CPUs: 16, Memory: 32 * gib, Disk: 180 * gib}},
		{Name: "big", Provider: "ec2", InstanceType: "c7a.8xlarge", HostSizeFrom: "history", HostsRunning: &none,
			HostSize: &lux.HostSize{CPUs: 32, Memory: 64 * gib, Disk: 380 * gib}},
		{Name: "shared", Provider: "static", Platform: true, Shared: true, HostSizeFrom: "running", HostsRunning: &two,
			HostSize: &lux.HostSize{CPUs: 8, Memory: 16 * gib, Disk: 0}},
	}
}

// OldPools are the same pools as a lux from before host sizes lists them:
// name, provider, platform, and nothing about their hosts.
func OldPools() []lux.Pool {
	out := DefaultPools()
	for i := range out {
		out[i] = lux.Pool{Name: out[i].Name, Provider: out[i].Provider, Platform: out[i].Platform, Shared: out[i].Shared}
	}
	return out
}

func (s *Server) listPools(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	pools := s.Pools
	s.mu.Unlock()
	if pools == nil {
		pools = DefaultPools()
	}
	writeJSON(w, 200, map[string]any{"pools": pools})
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
