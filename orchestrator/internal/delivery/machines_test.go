package delivery

import (
	"encoding/json"
	"testing"
)

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
		got, ok := sizes.ForRole(tc.role, json.RawMessage(tc.project), org)
		if !ok || got.SizeID != tc.want || got.From != tc.from {
			t.Errorf("%s over %s = %+v %v, want %s from %s", tc.role, tc.project, got, ok, tc.want, tc.from)
		}
	}
	if got, _ := sizes.ForPreview("large"); got.Name != "Large" || got.From != "project" {
		t.Errorf("preview = %+v", got)
	}
	if got, _ := sizes.ForPreview(""); got.Name != "Standard" || got.From != "default" {
		t.Errorf("preview with none = %+v", got)
	}
	if _, ok := (Sizes{}).ForRole("implementer", nil, nil); ok {
		t.Error("an organization with no sizes has a size")
	}
}
