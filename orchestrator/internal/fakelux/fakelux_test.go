package fakelux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A registry login real lux would refuse (lux internal/spec/spec.go
// validRegistry) is refused here too, so a spec cannot pass only the fake.
func TestASubmitWithARegistryLuxWouldRefuseIsRefused(t *testing.T) {
	srv := httptest.NewServer(New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} }).Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	for registry, ok := range map[string]bool{
		"ghcr.io":                true,
		"registry.example:5000":  true,
		"10.0.0.5:5000":          true,
		"registry.example:0":     false,
		"registry.example:65536": false,
		"registry.example:05000": false,
		"127.0.0.1:5000":         false,
		"0.0.0.0":                false,
		"169.254.169.254":        false,
		"localhost:5000":         false,
		"GHCR.io":                false,
		"https://ghcr.io":        false,
	} {
		spec := lux.Spec{
			Image:   lux.Image{Ref: "agent:1", RegistryAuth: []lux.RegistryAuth{{Registry: registry, Secret: "LOGIN"}}},
			Secrets: []lux.Secret{{Name: "LOGIN", Value: "u:p"}},
		}
		_, err := client.Submit(context.Background(), spec, "key-"+registry)
		le, refused := lux.AsError(err)
		switch {
		case ok && err != nil:
			t.Errorf("%q: refused: %v", registry, err)
		case !ok && (!refused || le.Status != http.StatusUnprocessableEntity || le.Code != "invalid_spec"):
			t.Errorf("%q: err = %v, want 422 invalid_spec", registry, err)
		}
	}
}
