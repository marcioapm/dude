package delivery

import (
	"encoding/json"
	"testing"
)

func TestTaskRaiseReachesFixerUnlessItNamesItsOwnSize(t *testing.T) {
	sizes := Sizes{Default: "std", ByID: map[string]Machine{
		"std":   {SizeID: "std", Name: "Standard"},
		"large": {SizeID: "large", Name: "Large"},
	}}
	raised := Raises{"implementer": {SizeID: "large", MoveID: "move", AskedBy: "run", Reason: "OOM"}}
	for _, role := range []string{"implementer", "fixer"} {
		m, ok := sizes.ForRole(role, raised, nil, nil)
		if !ok || m.SizeID != "large" || m.From != "task" || m.Raised == nil || m.Raised.MoveID != "move" {
			t.Fatalf("%s: %+v", role, m)
		}
	}
	m, _ := sizes.ForRole("fixer", raised, nil, json.RawMessage(`{"fixer":{"machineSize":"std"}}`))
	if m.SizeID != "std" || m.Raised != nil {
		t.Fatalf("fixer's own organization size: %+v", m)
	}
	raised["fixer"] = Raise{SizeID: "std", MoveID: "fixer-move"}
	m, _ = sizes.ForRole("fixer", raised, json.RawMessage(`{"fixer":{"machineSize":"large"}}`), nil)
	if m.SizeID != "std" || m.Raised == nil || m.Raised.MoveID != "fixer-move" {
		t.Fatalf("fixer's own task layer: %+v", m)
	}
}

func TestASizeIsTheOneNamedElseTheDefault(t *testing.T) {
	big := "pool_b8r2n5w1c7z3"
	sizes := Sizes{Default: "std", ByID: map[string]Machine{
		"std":   {SizeID: "std", Name: "Standard", CPUs: 2, MemoryMiB: 8192, DiskGiB: 20},
		"large": {SizeID: "large", Name: "Large", CPUs: 8, MemoryMiB: 16384, DiskGiB: 80, PoolID: &big},
	}}
	org := json.RawMessage(`{"implementer":{"machineSize":"large"},"reviewer":{"machineSize":"gone"}}`)
	for _, tc := range []struct {
		role, project, want, from string
	}{
		{"implementer", `{}`, "large", "organization"},
		{"implementer", `{"implementer":{"machineSize":"std"}}`, "std", "project"},
		{"fixer", `{}`, "large", "implementer"},
		// A size that is gone is passed over: the default.
		{"reviewer", `{}`, "std", "default"},
		{"investigator", `{}`, "std", "default"},
	} {
		got, ok := sizes.ForRole(tc.role, nil, json.RawMessage(tc.project), org)
		if !ok || got.SizeID != tc.want || got.From != tc.from {
			t.Errorf("%s over %s = %+v %v, want %s from %s", tc.role, tc.project, got, ok, tc.want, tc.from)
		}
	}
	if got, _ := sizes.ForPreview("large"); got.Name != "Large" || got.From != "project" || got.Missing != "" {
		t.Errorf("preview = %+v", got)
	}
	if got, _ := sizes.ForPreview(""); got.Name != "Standard" || got.From != "default" || got.Missing != "" {
		t.Errorf("preview with none = %+v", got)
	}
	// A size that is gone: the default, flagged so a resume keeps the
	// Run's size (phases.PlanResize).
	if got, _ := sizes.ForPreview("gone"); got.Name != "Standard" || got.From != "default" || got.Missing != "gone" {
		t.Errorf("preview naming a size that is gone = %+v", got)
	}
	if got, _ := sizes.ForRole("reviewer", nil, nil, org); got.Missing != "gone" {
		t.Errorf("reviewer naming a size that is gone = %+v", got)
	}
	if got, _ := sizes.ForRole("investigator", nil, nil, org); got.Missing != "" {
		t.Errorf("investigator naming none = %+v", got)
	}
	if _, ok := (Sizes{}).ForRole("implementer", nil, nil, nil); ok {
		t.Error("an organization with no sizes has a size")
	}
}
