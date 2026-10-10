package fakelux

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

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

// SpecOf is a Run's stored spec now, a resume's resize included.
func (s *Server) SpecOf(id string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return slices.Clone(run.Spec)
	}
	return nil
}

// ResumeResourcesOf is the resources each accepted resume of a Run carried,
// as received, in order: nil for a resume that carried none.
func (s *Server) ResumeResourcesOf(id string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return slices.Clone(run.ResumeResources)
	}
	return nil
}

// poolOf is the id of the pool run is bound to, as lux's Run answers it:
// its spec's placement.poolId, else the tenant's default pool. Called with
// s.mu held.
func (s *Server) poolOf(run *Run) string {
	var spec lux.Spec
	if json.Unmarshal(run.Spec, &spec) == nil && spec.Placement != nil && spec.Placement.PoolID != "" {
		return spec.Placement.PoolID
	}
	for _, p := range s.pools() {
		if p.IsDefault {
			return p.ID
		}
	}
	return ""
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

// memoryLimit is what a newer lux gives a placement's container: the
// memory its spec asked for when it was placed, less the host's share
// (MemoryShare), rounded down to a MiB. nil when the spec named none, or
// when the fake plays a lux that does not report it (MemoryShare zero).
func (s *Server) memoryLimit(p *placement) *int64 {
	if s.MemoryShare <= 0 || p.memory == 0 {
		return nil
	}
	mib := int64(math.Floor(float64(p.memory) * s.MemoryShare / (1 << 20)))
	limit := mib << 20
	return &limit
}

// specResources is the resources a Run's stored spec holds.
func specResources(rawSpec json.RawMessage) lux.Resources {
	var spec struct {
		Resources *lux.Resources `json:"resources"`
	}
	if json.Unmarshal(rawSpec, &spec) != nil || spec.Resources == nil {
		return lux.Resources{}
	}
	return *spec.Resources
}

// peakDisk is the peak disk use a placement reports at its exit: DiskUse,
// else 512 MiB.
func (s *Server) peakDisk() int64 {
	if s.DiskUse > 0 {
		return s.DiskUse
	}
	return 512 << 20
}

// Disk headroom a shrink must leave above the measured use, as lux's
// diskShrinkFloor: a quarter of it, at least 1 GiB.
func diskShrinkFloor(used int64) int64 {
	return used + max(used/4, gib)
}

// resizeRequest is a resume's resources, as lux's resumeResources: cpus
// and memory absent or set, disk 0 for unchanged. Sizes come as bytes or
// as lux's size strings.
type resizeRequest struct {
	CPUs   *float64  `json:"cpus,omitempty"`
	Memory *luxBytes `json:"memory,omitempty"`
	Disk   luxBytes  `json:"disk,omitempty"`
}

// requested is the resources asked for, the others zero, or lux's 422
// invalid_spec problem.
func (r resizeRequest) requested() (lux.Resources, string) {
	var req lux.Resources
	var problems []string
	if r.CPUs != nil {
		if req.CPUs = *r.CPUs; req.CPUs <= 0 {
			problems = append(problems, "resources.cpus must be greater than 0")
		}
	}
	if r.Memory != nil {
		if req.Memory = int64(*r.Memory); req.Memory <= 0 {
			problems = append(problems, "resources.memory must be greater than 0")
		}
	}
	if req.Disk = int64(r.Disk); req.CPUs < 0 || req.Memory < 0 || req.Disk < 0 {
		problems = append(problems, "resources must not be negative")
	}
	return req, strings.Join(problems, "; ")
}

// resize applies a resume's resources to a stopped Run's stored spec, as
// lux's resizeRun: cpus and memory as asked, a larger disk, and a smaller
// one only down to diskShrinkFloor of the peak disk use of the placement
// whose snapshot it resumes from, reported at that placement's exit. The
// answer's resize says what was asked and what the Run has now, and why a
// disk was kept. Callers hold s.mu.
func (s *Server) resize(run *Run, req resizeRequest, requested lux.Resources) *lux.Resize {
	cur := specResources(run.Spec)
	rz := &lux.Resize{Requested: requested, Applied: cur}
	if req.CPUs != nil {
		rz.Applied.CPUs = *req.CPUs
	}
	if req.Memory != nil {
		rz.Applied.Memory = int64(*req.Memory)
	}
	if req.Disk != 0 {
		rz.Applied.Disk = int64(req.Disk)
	}
	if d := int64(req.Disk); d > 0 && d < cur.Disk {
		// The snapshot it resumes from is its last workload's: lux's
		// runner reports it before the exit status, so a stopped Run has it.
		var from *placement
		for _, p := range run.placements {
			if p.WorkloadStartedAt != nil {
				from = p
			}
		}
		kept := lux.DiskKept{Requested: d, Kept: cur.Disk}
		switch {
		case from != nil && from.peakDisk == nil:
			kept.Reason = "no final measurement: the placement that took the snapshot it resumes from ended without reporting its final disk use, so a smaller disk is not applied"
		case from == nil:
			kept.Reason = "no disk use is recorded for the snapshot it resumes from: a smaller disk is not applied"
		case d < diskShrinkFloor(*from.peakDisk):
			kept.Measured, kept.Needed = from.peakDisk, diskShrinkFloor(*from.peakDisk)
			kept.Reason = fmt.Sprintf("its saved state used up to %s; a smaller disk must be at least %s (that plus max(25%%, 1 GiB))",
				bytesText(*from.peakDisk), bytesText(kept.Needed))
		}
		if kept.Reason != "" {
			rz.Disk, rz.Applied.Disk = &kept, cur.Disk
		}
	}
	if rz.Applied != cur {
		s.setResources(run, rz.Applied)
	}
	return rz
}

// setResources writes res over the resources of run's stored spec, its
// other fields kept. Callers hold s.mu.
func (s *Server) setResources(run *Run, res lux.Resources) {
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	stored, _ := spec["resources"].(map[string]any)
	if stored == nil {
		stored = map[string]any{}
	}
	stored["cpus"], stored["memory"], stored["disk"] = res.CPUs, res.Memory, res.Disk
	spec["resources"] = stored
	run.Spec, _ = json.Marshal(spec)
}

// bytesText is lux's: 1.5 GiB.
func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// luxBytes is lux's spec.Bytes: a number of bytes, or a size string
// ("8Gi", "512Mi", "1G").
type luxBytes int64

var bytesRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([KMGT]i?)?B?$`)

func (b *luxBytes) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*b = luxBytes(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	m := bytesRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return fmt.Errorf("invalid size %q", s)
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"": 1, "K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12,
		"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}[m[2]]
	*b = luxBytes(f * mult)
	return nil
}
