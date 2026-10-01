package phases

import (
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A Run's machine size is on its spec as lux reads it: resources at the
// top level with memory and disk in bytes, and placement.poolId — lux's id,
// never the pool's name — only when the size names a pool. The scripted
// agent is placed like the agent it stands in for.
func TestTheMachineSizeIsTheSpecsResourcesAndPool(t *testing.T) {
	big, name := "pool_b8r2n5w1c7z3", "big"
	for _, tc := range []struct {
		name      string
		machine   *delivery.Machine
		resources string
		placement string
	}{
		{"none: lux's default size", nil, "", ""},
		{"default pool", &delivery.Machine{Name: "Standard", CPUs: 2, MemoryMiB: 8192, DiskGiB: 20},
			`{"cpus":2,"memory":8589934592,"disk":21474836480}`, ""},
		{"half steps in a named pool", &delivery.Machine{Name: "Large", CPUs: 6.5, MemoryMiB: 23040, DiskGiB: 120, PoolID: &big, Pool: &name},
			`{"cpus":6.5,"memory":24159191040,"disk":128849018880}`, `{"poolId":"pool_b8r2n5w1c7z3"}`},
	} {
		for _, model := range []string{"llm/impl", "fake/scripted"} {
			t.Run(tc.name+" "+model, func(t *testing.T) {
				c, in := goldenInput(model)
				in.Machine = tc.machine
				b, err := json.Marshal(buildSpec(c, in))
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(b, &wire); err != nil {
					t.Fatal(err)
				}
				if got := string(wire["resources"]); got != tc.resources {
					t.Errorf("resources = %s, want %s", got, tc.resources)
				}
				if got := string(wire["placement"]); got != tc.placement {
					t.Errorf("placement = %s, want %s", got, tc.placement)
				}
			})
		}
	}
}
