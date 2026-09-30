package delivery

import (
	"encoding/json"
	"testing"
)

func TestARolesMachineSizeResolvesLikeItsOtherFields(t *testing.T) {
	org := json.RawMessage(`{"implementer":{"model":"org/impl","machineSize":"msz_large"},"reviewer":{"machineSize":"msz_small"}}`)
	project := json.RawMessage(`{"reviewer":{"machineSize":"msz_xl","effort":"low"}}`)
	if got := ResolveRole("reviewer", project, org); got.MachineSize != "msz_xl" || got.Effort != "low" {
		t.Errorf("reviewer = %+v, want the project's size", got)
	}
	if got := ResolveRole("implementer", project, org); got.MachineSize != "msz_large" || got.Model != "org/impl" {
		t.Errorf("implementer = %+v, want the organization's size", got)
	}
	// The fixer follows the implementer, a field at a time.
	if got := ResolveRole("fixer", project, org); got.MachineSize != "msz_large" {
		t.Errorf("fixer = %+v, want the implementer's size", got)
	}
	if got := ResolveRole("fixer", json.RawMessage(`{"fixer":{"machineSize":"msz_small"}}`), org); got.MachineSize != "msz_small" {
		t.Errorf("fixer with its own = %+v", got)
	}
	// Unset at every layer: none named, so the default size.
	if got := ResolveRole("simplifier", project, org); got.MachineSize != "" {
		t.Errorf("simplifier = %+v", got)
	}
}

func TestASizeIsTheOneNamedElseTheDefault(t *testing.T) {
	big := "big"
	sizes := Sizes{Default: "std", ByID: map[string]Machine{
		"std":   {SizeID: "std", Name: "Standard", CPUs: 2, MemoryMiB: 8192, DiskGiB: 20},
		"large": {SizeID: "large", Name: "Large", CPUs: 8, MemoryMiB: 16384, DiskGiB: 80, Pool: &big},
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
