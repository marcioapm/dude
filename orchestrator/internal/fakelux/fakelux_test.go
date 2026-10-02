package fakelux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Before any plugin priced a Run, lux answers pending with no amounts; a
// priced one comes back as the test set it, through the real client.
func TestCostIsPendingUntilPriced(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}}}, "cost")
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Cost(context.Background(), run.ID)
	if err != nil || c.Status != lux.CostPending {
		t.Fatalf("got %+v %v", c, err)
	}
	if _, ok := c.FamilyUSD(lux.FamilyAI); ok {
		t.Error("an unpriced Run has an AI amount")
	}
	fake.SetCost(run.ID, lux.RunCost{Status: lux.CostFinal, Final: true,
		ByFamily: []lux.FamilyCost{{Family: lux.FamilyAI, Currency: "USD", Amount: "1.810247"}}})
	c, err = client.Cost(context.Background(), run.ID)
	if ai, _ := c.FamilyUSD(lux.FamilyAI); err != nil || c.Status != lux.CostFinal || ai != "1.810247" {
		t.Errorf("got %+v %v", c, err)
	}
	if _, err := client.Cost(context.Background(), "lrun_404"); !lux.IsNotFound(err) {
		t.Errorf("unknown Run: %v", err)
	}
}

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

// submitRun serves fake and submits one opencode Run to it.
func submitRun(t *testing.T, fake *Server) (*lux.HTTPClient, lux.Run) {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	c := lux.New(srv.URL, "k")
	run, err := c.Submit(context.Background(), lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	return c, run
}

// awaitRun polls the Run under the fake's lock for 5 s until cond holds,
// and fails the test with failure if it never does.
func awaitRun(t *testing.T, fake *Server, id, failure string, cond func(*Run) bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		fake.mu.Lock()
		ok := cond(fake.runs[id])
		fake.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(failure)
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
	c, run := submitRun(t, fake)
	awaitRun(t, fake, run.ID, "the first turn never ended", func(r *Run) bool { return r.turnsEnded == 1 })
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

// inputReceipts is each input record of the Run for requestID, in order:
// lux.input's phase, "consumed" (lux.input.consumed) or "failed"
// (lux.input.failed); an older lux's phase-less lux.input is "handoff", or
// "error" when it carries one.
func inputReceipts(s *Server, id, requestID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.runs[id].records {
		data, _ := r.Event["data"].(map[string]any)
		if data["requestId"] != requestID {
			continue
		}
		switch r.Event["type"] {
		case lux.RecordInput:
			phase, _ := data["phase"].(string)
			switch {
			case phase != "":
				out = append(out, phase)
			case data["error"] != nil:
				out = append(out, "error")
			default:
				out = append(out, "handoff")
			}
		case lux.RecordInputConsumed:
			out = append(out, "consumed")
		case lux.RecordInputFailed:
			out = append(out, "failed")
		}
	}
	return out
}

func cancelledTurns(s *Server, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.runs[id].records {
		data, _ := r.Event["data"].(map[string]any)
		if r.Event["type"] == "acp.turn_end" && data["stopReason"] == "cancelled" {
			n++
		}
	}
	return n
}

// A steer the harness took while a tool ran, then an interrupt with no
// text: lux now carries the steer into the next turn, where it is read
// once; an older lux fails it. In both input contracts: a legacy lux hands
// the steer over (or fails it) with one phase-less lux.input.
func TestAnInterruptCarriesAnUnreadSteerIntoTheNextTurn(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, old := range []bool{false, true} {
			name := map[bool]string{false: "receipts", true: "legacy"}[legacy] + "/" + map[bool]string{false: "carries", true: "older lux fails it"}[old]
			t.Run(name, func(t *testing.T) {
				fake := New("", "k", func(map[string]any) Behaviour {
					return Behaviour{Hang: true, Tools: []string{"bash"}, KeepToolsOpen: true}
				})
				fake.LegacyInput = legacy
				fake.FailUnreadOnInterrupt = old
				c, run := submitRun(t, fake)
				awaitRun(t, fake, run.ID, "the tool never started", func(r *Run) bool { return len(r.openTools) == 1 })
				if err := c.Input(context.Background(), run.ID, "check the migration", "dir_a", false); err != nil {
					t.Fatal(err)
				}
				if err := c.Input(context.Background(), run.ID, "", "dir_b", true); err != nil {
					t.Fatal(err)
				}
				got, inputs := inputReceipts(fake, run.ID, "dir_a"), fake.Runs()[0].Inputs
				var want, wantInputs []string
				switch {
				case legacy && old:
					want = []string{"error"}
				case legacy:
					want, wantInputs = []string{"handoff"}, []string{"check the migration"}
				case old:
					want = []string{"accepted", "failed"}
				default:
					want, wantInputs = []string{"accepted", "consumed"}, []string{"check the migration"}
				}
				if !slices.Equal(got, want) || !slices.Equal(inputs, wantInputs) {
					t.Errorf("receipts %v inputs %q, want %v %q", got, inputs, want, wantInputs)
				}
				if r := inputReceipts(fake, run.ID, "dir_b"); len(r) != 0 {
					t.Errorf("an interrupt alone got receipts %v", r)
				}
				if r := fake.Runs()[0]; r.Interrupted != 1 {
					t.Errorf("interrupted=%d, want one", r.Interrupted)
				}
				if n := cancelledTurns(fake, run.ID); n != 1 {
					t.Errorf("%d cancelled turns, want 1", n)
				}
			})
		}
	}
}

// GET /v1/runs/{id}/events is lux's listEvents: the Run's lifecycle events
// after an id, in id order, at most 1000 a page; PageEvents shortens or
// fails a page.
func TestEventsAreListedAfterAnIdAPageAtATime(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx := context.Background()
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}}}, "events")
	if err != nil {
		t.Fatal(err)
	}
	for range eventsPage + 5 {
		fake.Crash(run.ID)
	}
	var all []int64
	for after := int64(0); ; {
		page, err := client.Events(ctx, run.ID, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > eventsPage {
			t.Fatalf("a page of %d events", len(page))
		}
		if len(page) == 0 {
			break
		}
		for _, f := range page {
			if f.Kind != "lux" || f.EventID <= after || f.EventType == "" {
				t.Fatalf("event %+v after %d", f, after)
			}
			after = f.EventID
			all = append(all, f.EventID)
		}
	}
	if len(all) < eventsPage+5 || !slices.IsSorted(all) {
		t.Fatalf("%d events, sorted %v", len(all), slices.IsSorted(all))
	}
	fake.PageEvents(func(_ string, _ int64, ids []int64) int { return 1 })
	if page, err := client.Events(ctx, run.ID, all[2]); err != nil || len(page) != 1 || page[0].EventID != all[3] {
		t.Fatalf("a short page after %d: %+v %v", all[2], page, err)
	}
	fake.PageEvents(func(string, int64, []int64) int { return -1 })
	if _, err := client.Events(ctx, run.ID, 0); err == nil {
		t.Fatal("a failed page answered")
	}
	fake.PageEvents(nil)
	if _, err := client.Events(ctx, "lrun_404", 0); !lux.IsNotFound(err) {
		t.Fatalf("unknown Run: %v", err)
	}
}
