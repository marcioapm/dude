package lux_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// DefaultPool picks the pool lux's resolvePool puts a Run that names no
// pool in: a mark before a name, the tenant's before the platform's.
func TestDefaultPoolResolvesAsLuxDoes(t *testing.T) {
	tenantMark := lux.Pool{ID: "pool_tm", Name: "amd64", IsDefault: true}
	platformMark := lux.Pool{ID: "pool_pm", Name: "shared", Platform: true, IsDefault: true}
	tenantNamed := lux.Pool{ID: "pool_tn", Name: "default"}
	platformNamed := lux.Pool{ID: "pool_pn", Name: "default", Platform: true}
	other := lux.Pool{ID: "pool_o", Name: "ondemand-arm64"}
	for _, tc := range []struct {
		name  string
		pools []lux.Pool
		want  string
	}{
		{"a tenant mark beats a platform mark and a pool named default", []lux.Pool{platformNamed, tenantNamed, platformMark, tenantMark}, "pool_tm"},
		{"a platform mark beats a pool named default", []lux.Pool{tenantNamed, platformNamed, platformMark, other}, "pool_pm"},
		{"with no marks the tenant's default beats the platform's", []lux.Pool{platformNamed, other, tenantNamed}, "pool_tn"},
		{"the platform's default when the tenant has none", []lux.Pool{other, platformNamed}, "pool_pn"},
		{"nothing marked or named default", []lux.Pool{other, {ID: "pool_x", Name: "amd64"}}, ""},
		{"no pools", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lux.DefaultPool(tc.pools)
			if ok != (tc.want != "") || got.ID != tc.want {
				t.Fatalf("DefaultPool = %q, %v; want %q", got.ID, ok, tc.want)
			}
		})
	}
}
