package fakelux

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
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

// A Run cancelled, or stopped, while lux is still starting it is placed no
// further: no host assigned after it ended, no start stamped after its
// exit, and it never runs.
func TestARunEndedWhileStartingIsPlacedNoFurther(t *testing.T) {
	for _, c := range []struct {
		name string
		// when, in fifths of the start, the Run is cancelled
		at int
	}{
		{"cancelled before a host", 0},
		{"cancelled once placed", 2},
		{"resumed, then cancelled while resuming", -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
			fake.StartAfter = 500 * time.Millisecond
			client, run := submitGeneric(t, fake, "k-"+c.name)
			if c.at < 0 {
				// Run, stopped, resumed; cancelled two fifths into the resume.
				stopAndResume(t, client, run.ID)
				c.at = 2
			}
			time.Sleep(time.Duration(c.at)*fake.StartAfter/5 + fake.StartAfter/10)
			if err := client.Cancel(context.Background(), run.ID); err != nil {
				t.Fatal(err)
			}
			ended, _ := client.Get(context.Background(), run.ID)
			time.Sleep(fake.StartAfter + 100*time.Millisecond)
			got, err := client.Get(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "cancelled" {
				t.Errorf("state %s after the start delay, want cancelled", got.State)
			}
			if len(got.Placements) != len(ended.Placements) {
				t.Fatalf("placements %d after it ended, %d when it did", len(got.Placements), len(ended.Placements))
			}
			for i, p := range got.Placements {
				was := ended.Placements[i]
				if !slices.Equal(stamps(p), stamps(was)) || p.State != was.State {
					t.Errorf("placement %d moved on after the Run ended:\nwhen it ended %+v\nafter        %+v", p.Epoch, was, p)
				}
				if p.Epoch == ended.Epoch && p.WorkloadStartedAt != nil {
					t.Errorf("placement %d started its workload", p.Epoch)
				}
			}
		})
	}
}

// As lux's resumeRun answers with the Run's current epoch, and its
// scheduler moves that epoch only when it assigns the new placement: the
// resume's answer, and a Get before the assign, carry the stopped epoch;
// once assigned, Get carries the new one and its placement. The resume's
// start is held before its assign, so the checks before it are not raced.
func TestAResumesAnswerCarriesTheStoppedEpochUntilThePlacementIsAssigned(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	holds := holdStarts(t, fake, "2/"+holdAssign)
	client, run := submitGeneric(t, fake, "k-epoch")
	resumed := stopAndResume(t, client, run.ID)
	if resumed.State != "resuming" || resumed.Epoch != 1 {
		t.Errorf("resume answered %s at epoch %d, want resuming at the stopped epoch 1", resumed.State, resumed.Epoch)
	}
	holds.reached(2, holdAssign)
	if got, _ := client.Get(context.Background(), run.ID); got.Epoch != 1 || len(got.Placements) != 1 {
		t.Errorf("before the assign: epoch %d, %d placements, want 1 and 1", got.Epoch, len(got.Placements))
	}
	// Answered again while resuming, as lux answers a repeated resume.
	if again, err := client.Resume(context.Background(), run.ID, lux.ResumeInput{}); err != nil || again.Epoch != 1 {
		t.Errorf("a repeated resume answered epoch %d (%v), want 1", again.Epoch, err)
	}
	holds.let(2, holdAssign)
	waitState(t, client, run.ID, "running")
	got, err := client.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Epoch != 2 || len(got.Placements) != 2 || got.Placements[1].Epoch != 2 || got.Placements[1].AssignedAt == nil {
		t.Errorf("once assigned: epoch %d, placements %+v, want epoch 2 with its placement", got.Epoch, got.Placements)
	}
}

// startHolds stops a fake's starts at chosen hold points (Server.onStart):
// reached(epoch, point) closes when a start gets there, and one held there
// waits for release(epoch, point). Everything held is let go at cleanup.
type startHolds struct {
	t       *testing.T
	mu      sync.Mutex
	held    map[string]bool
	reach   map[string]chan struct{}
	release map[string]chan struct{}
}

func holdStarts(t *testing.T, fake *Server, held ...string) *startHolds {
	h := &startHolds{t: t, held: map[string]bool{}, reach: map[string]chan struct{}{}, release: map[string]chan struct{}{}}
	for _, k := range held {
		h.held[k] = true
	}
	fake.onStart = func(epoch int, point string) {
		k := fmt.Sprintf("%d/%s", epoch, point)
		reach, release := h.chans(k)
		h.mu.Lock()
		select {
		case <-reach:
		default:
			close(reach)
		}
		h.mu.Unlock()
		if h.held[k] {
			<-release
		}
	}
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for k := range h.held {
			if _, release := h.chansLocked(k); !closed(release) {
				close(release)
			}
		}
	})
	return h
}

func closed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func (h *startHolds) chans(k string) (chan struct{}, chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.chansLocked(k)
}

func (h *startHolds) chansLocked(k string) (chan struct{}, chan struct{}) {
	if h.reach[k] == nil {
		h.reach[k], h.release[k] = make(chan struct{}), make(chan struct{})
	}
	return h.reach[k], h.release[k]
}

// reached waits, up to 5 s, for a start to get to point.
func (h *startHolds) reached(epoch int, point string) {
	h.t.Helper()
	reach, _ := h.chans(fmt.Sprintf("%d/%s", epoch, point))
	select {
	case <-reach:
	case <-time.After(5 * time.Second):
		h.t.Fatalf("the start of epoch %d never got to %s", epoch, point)
	}
}

func (h *startHolds) let(epoch int, point string) {
	_, release := h.chans(fmt.Sprintf("%d/%s", epoch, point))
	close(release)
}

// A start lux has given up on goes no further: the Run's epoch-1 start
// is placed, the Run fails (Crash) before its workload starts, and lux
// accepts a resume while that start is still going. The resume's start
// is held before it is assigned, so the Run's epoch is still 1 when the
// old start carries on. Only epoch 2 runs, on the one placement it added.
func TestAStartTheRunHasGivenUpOnGoesNoFurther(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	holds := holdStarts(t, fake, "1/"+holdWorkload, "2/"+holdAssign)
	client, run := submitRun(t, fake)
	ctx := context.Background()

	holds.reached(1, holdWorkload)
	fake.Crash(run.ID)
	resumed, err := client.Resume(ctx, run.ID, lux.ResumeInput{})
	if err != nil || resumed.State != "resuming" || resumed.Epoch != 1 {
		t.Fatalf("resume answered %s at epoch %d (%v), want resuming at 1", resumed.State, resumed.Epoch, err)
	}
	holds.reached(2, holdAssign)
	// The old start goes on, and is over, before the new one is assigned.
	holds.let(1, holdWorkload)
	holds.reached(1, holdOver)
	holds.let(2, holdAssign)
	holds.reached(2, holdOver)

	fake.mu.Lock()
	var running []int
	for _, e := range fake.runs[run.ID].events {
		if e.Type == "state" && e.Data["state"] == "running" {
			running = append(running, e.Epoch)
		}
	}
	fake.mu.Unlock()
	if !slices.Equal(running, []int{2}) {
		t.Errorf("running in epochs %v, want only 2", running)
	}
	got, err := client.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "running" || got.Epoch != 2 || len(got.Placements) != 2 {
		t.Fatalf("got %s at epoch %d with %d placements, want running at 2 with 2", got.State, got.Epoch, len(got.Placements))
	}
	if old := got.Placements[0]; old.Epoch != 1 || old.WorkloadStartedAt != nil {
		t.Errorf("the crashed placement %d started its workload at %v", old.Epoch, old.WorkloadStartedAt)
	}
	if p := got.Placements[1]; p.Epoch != 2 || p.WorkloadStartedAt == nil {
		t.Errorf("the resume's placement is epoch %d, workload started %v; want epoch 2, started", p.Epoch, p.WorkloadStartedAt)
	}
}

// submitGeneric serves fake and submits one generic Run to it under key.
func submitGeneric(t *testing.T, fake *Server, key string) (*lux.HTTPClient, lux.Run) {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return client, run
}

// stopAndResume waits for the Run to run, stops it, and resumes it once
// stopped; it returns lux's answer to the resume.
func stopAndResume(t *testing.T, c *lux.HTTPClient, id string) lux.Run {
	t.Helper()
	waitState(t, c, id, "running")
	if err := c.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, id, "stopped")
	resumed, err := c.Resume(context.Background(), id, lux.ResumeInput{})
	if err != nil {
		t.Fatal(err)
	}
	return resumed
}

func waitState(t *testing.T, c *lux.HTTPClient, id, state string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if r, err := c.Get(context.Background(), id); err == nil && r.State == state {
			return
		}
	}
	t.Fatalf("the Run never got %s", state)
}

func stamps(p lux.Placement) []string {
	var out []string
	for _, t := range []*time.Time{p.AssignedAt, p.ImageReadyAt, p.VolumesRestoredAt, p.ContainerStartedAt, p.WorkloadStartedAt, p.ExitedAt} {
		if t == nil {
			out = append(out, "")
		} else {
			out = append(out, t.Format(time.RFC3339Nano))
		}
	}
	return out
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
