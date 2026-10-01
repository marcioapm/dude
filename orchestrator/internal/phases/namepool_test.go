package phases

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// stubPools is a lux.Client that answers only Pools; any other call panics
// on the nil embedded interface.
type stubPools struct {
	lux.Client
	pools []lux.Pool
	err   error
	calls int
}

func (s *stubPools) Pools(context.Context) ([]lux.Pool, error) {
	s.calls++
	return s.pools, s.err
}

// NamePool is best effort: lux not answering, or a list without the size's
// pool, leaves runs.machine's pool unnamed and the machine otherwise as it
// was, so the submit that follows still goes ahead.
func TestNamePoolLeavesThePoolUnnamedWhenLuxCannotName(t *testing.T) {
	big := "pool_b8r2n5w1c7z3"
	listed := []lux.Pool{{ID: "pool_d3f4u1t9k2m7", Name: "general"}, {ID: "pool_s1x2y3z4w5v6", Name: "small"}}
	for _, tc := range []struct {
		name string
		stub *stubPools
	}{
		{"lux unreachable", &stubPools{err: &lux.Error{Status: 0, Code: "unreachable", Message: "dial tcp: connection refused"}}},
		{"lux answers 503", &stubPools{err: &lux.Error{Status: 503, Code: "unavailable", Message: "down"}}},
		{"id missing from the list", &stubPools{pools: listed}},
		{"empty list", &stubPools{pools: []lux.Pool{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := big
			m := &delivery.Machine{Name: "Large", CPUs: 8, MemoryMiB: 16384, DiskGiB: 80, PoolID: &id}
			NamePool(context.Background(), tc.stub, m)
			if tc.stub.calls != 1 {
				t.Errorf("Pools called %d times, want 1", tc.stub.calls)
			}
			if m.Pool != nil {
				t.Errorf("Pool = %q, want nil", *m.Pool)
			}
			if m.PoolID == nil || *m.PoolID != big {
				t.Errorf("PoolID = %v, want %s", m.PoolID, big)
			}
		})
	}
}

func TestNamePoolNamesThePoolLuxListsByItsID(t *testing.T) {
	big := "pool_b8r2n5w1c7z3"
	stub := &stubPools{pools: []lux.Pool{{ID: "pool_d3f4u1t9k2m7", Name: "general"}, {ID: big, Name: "huge"}}}
	m := &delivery.Machine{Name: "Large", PoolID: &big}
	NamePool(context.Background(), stub, m)
	if m.Pool == nil || *m.Pool != "huge" {
		t.Errorf("Pool = %v, want huge", m.Pool)
	}
}

// With no machine, or a machine in the default pool, there is nothing to
// name and lux is not asked.
func TestNamePoolDoesNotAskLuxWithoutAPool(t *testing.T) {
	stub := &stubPools{err: &lux.Error{Status: 503, Code: "unavailable"}}
	NamePool(context.Background(), stub, nil)
	m := &delivery.Machine{Name: "Standard"}
	NamePool(context.Background(), stub, m)
	if stub.calls != 0 || m.Pool != nil {
		t.Errorf("calls = %d, Pool = %v; want 0, nil", stub.calls, m.Pool)
	}
}
