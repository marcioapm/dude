package fakelux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

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
		// Otherwise valid for real lux (its Normalize requires a generic
		// workload's command), so an accept is about the registry alone.
		spec := lux.Spec{
			Image:    lux.Image{Ref: "agent:1", RegistryAuth: []lux.RegistryAuth{{Registry: registry, Secret: "LOGIN"}}},
			Workload: lux.Workload{Adapter: "generic", Command: []string{"sh", "-c", "true"}},
			Secrets:  []lux.Secret{{Name: "LOGIN", Value: "u:p"}},
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

// inputWorkers counts goroutines holding gated input (InputGate).
func inputWorkers() int {
	buf := make([]byte, 1<<22)
	return strings.Count(string(buf[:runtime.Stack(buf, true)]), "fakelux.(*Server).input.func")
}

// Input held on a gate that is never opened is dropped when the fake
// closes: no worker outlives it.
func TestClosingTheFakeReleasesGatedInput(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Reply: "Done."} })
	fake.InputGate = make(chan struct{})
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	c := lux.New(srv.URL, "k")
	run, err := c.Submit(context.Background(), lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		fake.mu.Lock()
		ended := fake.runs[run.ID].turnsEnded
		fake.mu.Unlock()
		if ended == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first turn never ended")
		}
	}
	if err := c.Input(context.Background(), run.ID, "more", "req_1", false); err != nil {
		t.Fatal(err)
	}
	if n := inputWorkers(); n != 1 {
		t.Fatalf("%d gated workers, want 1", n)
	}
	fake.Close()
	for deadline := time.Now().Add(2 * time.Second); inputWorkers() > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d gated workers after Close", inputWorkers())
		}
	}
}
