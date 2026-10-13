package delivery

import (
	"net/http"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Each lux refusal of a move, as the agent is told it and as a person reads
// it on the Run (machine.refused): sizes and pools by name, lux's words
// with their pool ids never shown. apps/web/test/conversation.test.ts
// LUX_REFUSALS folds the same strings.
func TestEachLuxRefusalIsSaidInDudesWords(t *testing.T) {
	high := "pool_high"
	xl := Machine{SizeID: "ms_xl", Name: "XL", CPUs: 8, MemoryMiB: 32768, DiskGiB: 80, PoolID: &high}
	no := false
	pools := []lux.Pool{{ID: "pool_default", Name: "general", IsDefault: true},
		{ID: high, Name: "highmem", Platforms: []string{"linux/arm64"}, Capabilities: &lux.PoolCapabilities{NestedContainers: &no}}}
	run := lux.Run{PoolID: "pool_default", Placements: []lux.Placement{{ImagePlatforms: []string{"linux/amd64"}}}}
	nested := run
	nested.Spec.Sandbox = &lux.Sandbox{NestedContainers: true}
	for _, tc := range []struct {
		name          string
		le            lux.Error
		run           lux.Run
		agent, person string
	}{
		{"platform_mismatch", lux.Error{Status: 422, Code: "platform_mismatch", Message: "pool pool_high runs linux/arm64; this Run's image offers only linux/amd64"}, run,
			"XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only. Ask for another size, or ask a person to add an arm64 image.",
			"XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only"},
		{"pool_unfit nested", lux.Error{Status: 422, Code: "pool_unfit", Message: "pool pool_high cannot take this Run: none of its hosts offers nested containers"}, nested,
			"XL is in highmem, which can't run nested containers, and this agent's image needs them. Ask for another size.",
			"XL is in highmem, which can't run nested containers, and this agent's image needs them"},
		{"pool_unfit size", lux.Error{Status: 422, Code: "pool_unfit", Message: "pool pool_high cannot take this Run: it needs 8 cpus; its largest host has 4"}, run,
			"XL (8 CPUs · 32 GiB) is larger than any machine in highmem. Ask for another size.",
			"XL (8 CPUs · 32 GiB) is larger than any machine in highmem"},
		{"unknown_pool", lux.Error{Status: 422, Code: "unknown_pool", Message: "no pool with id pool_high"}, run,
			"XL's pool has no host it fits; ask for another size", "XL's pool has no host it fits"},
		{"invalid_spec", lux.Error{Status: 422, Code: "invalid_spec", Message: "resources.disk must not be negative"}, run,
			"lux cannot give this agent XL as the size is set up. Ask for another size, or ask a person to check XL in Machines.",
			"lux cannot give this agent XL as the size is set up"},
		{"not_resumable", lux.Error{Status: http.StatusConflict, Code: "not_resumable", Message: "run is resuming already into another pool"}, run,
			"lux was already resuming this Run, so it could not move it to XL. Ask again once you are running.",
			"lux was already resuming this Run, so it could not move it to XL"},
		{"older lux, another pool", lux.Error{Status: http.StatusBadRequest, Code: "bad_request", Message: `invalid JSON body: json: unknown field "placement"`}, run,
			"this lux cannot move a Run between pools yet. Ask for a size in your own pool.", "this lux cannot move a Run between pools yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := RefusalOf(&tc.le, xl, pools, tc.run)
			if got := r.ForAgent(); got != tc.agent {
				t.Errorf("agent:\n got %q\nwant %q", got, tc.agent)
			}
			if got := r.ForPerson(); got != tc.person {
				t.Errorf("person:\n got %q\nwant %q", got, tc.person)
			}
		})
	}
	// An older lux's 400 in the Run's own pool is to resources.
	large := Machine{Name: "Large", CPUs: 4, MemoryMiB: 16384}
	r := RefusalOf(&lux.Error{Status: 400, Code: "bad_request", Message: `unknown field "cpus"`}, large, pools, run)
	if got := r.ForPerson(); got != "this lux cannot change a stopped Run's CPUs or memory" {
		t.Errorf("older lux, same pool: %q", got)
	}
}
