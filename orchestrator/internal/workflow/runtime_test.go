package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type harness struct {
	t     *testing.T
	rt    *Runtime
	owner *pgx.Conn
	org   string

	mu    sync.Mutex
	trace []string
}

func newHarness(t *testing.T) *harness {
	app, owner := dbtest.Open(t)
	h := &harness{t: t, owner: owner, org: dbtest.Org(t, owner)}
	h.rt = New(app, "test-poller", quiet)
	return h
}

func (h *harness) record(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trace = append(h.trace, s)
}

func (h *harness) start(typ string) string {
	h.t.Helper()
	id, _, err := h.rt.Start(context.Background(), StartOptions{
		Type: typ, OrganizationID: h.org, IdempotencyKey: time.Now().Format(time.RFC3339Nano), Input: map[string]any{},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) tick() int {
	h.t.Helper()
	n, err := h.rt.Tick(context.Background(), 10)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

// drain ticks until nothing is runnable. Each test has its own organization
// but the same database, so tick may also advance another test's leftovers:
// the budget is generous and assertions check this test's own run.
func (h *harness) drain() {
	for range 100 {
		if h.tick() == 0 {
			return
		}
	}
}

func (h *harness) get(id string) *Run {
	h.t.Helper()
	run, err := h.rt.Get(context.Background(), h.org, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return run
}

// wakeNow clears a backoff timer rather than sleeping through it.
func (h *harness) wakeNow(id string) {
	h.t.Helper()
	if _, err := h.owner.Exec(context.Background(), `UPDATE workflow_runs SET wake_at = now() WHERE id = $1`, id); err != nil {
		h.t.Fatal(err)
	}
}

func linear(h *harness) *Definition {
	return &Definition{Type: "test.linear", InitialStep: "a", Steps: map[string]Step{
		"a": func(context.Context, StepContext) (Result, error) {
			h.record("a")
			return Result{Next: "b", State: map[string]any{"after": "a"}}, nil
		},
		"b": func(context.Context, StepContext) (Result, error) {
			h.record("b")
			return Result{}, nil
		},
	}}
}

func waiter(h *harness) *Definition {
	return &Definition{Type: "test.waiter", InitialStep: "ask", Steps: map[string]Step{
		"ask": func(context.Context, StepContext) (Result, error) {
			return Result{Next: "receive", AwaitSignals: []string{"approved"}}, nil
		},
		"receive": func(_ context.Context, sc StepContext) (Result, error) {
			h.record("received:" + string(sc.Signals[0].Payload))
			return Result{}, nil
		},
	}}
}

func TestRunsALinearWorkflowToCompletion(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(linear(h))
	id := h.start("test.linear")
	h.drain()

	run := h.get(id)
	if run.Status != "completed" {
		t.Fatalf("status = %s, want completed", run.Status)
	}
	var state map[string]string
	_ = json.Unmarshal(run.State, &state)
	if state["after"] != "a" {
		t.Errorf("state = %s, want the last step's state kept", run.State)
	}
}

func TestStartIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(linear(h))
	ctx := context.Background()
	opts := StartOptions{Type: "test.linear", OrganizationID: h.org, IdempotencyKey: "same", Input: map[string]any{}}
	first, dup, err := h.rt.Start(ctx, opts)
	if err != nil || dup {
		t.Fatalf("first start: %v, dup=%v", err, dup)
	}
	second, dup, err := h.rt.Start(ctx, opts)
	if err != nil || !dup || second != first {
		t.Fatalf("second start = %s dup=%v err=%v, want %s deduplicated", second, dup, err, first)
	}
}

func TestParksUntilSignalledAndThenResumes(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(waiter(h))
	id := h.start("test.waiter")
	h.drain()

	if run := h.get(id); run.Status != "waiting" {
		t.Fatalf("status = %s, want waiting", run.Status)
	}
	// Waiting is free: a parked run is not claimed by a tick.
	h.drain()

	if err := h.rt.Signal(context.Background(), h.org, id, "approved", map[string]any{"by": "human"}, ""); err != nil {
		t.Fatal(err)
	}
	h.drain()
	if run := h.get(id); run.Status != "completed" {
		t.Fatalf("status = %s, want completed after the signal", run.Status)
	}
	if len(h.trace) != 1 || h.trace[0] != `received:{"by": "human"}` {
		t.Errorf("trace = %v", h.trace)
	}
}

func TestASignalThatArrivesEarlyIsNotLost(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(waiter(h))
	id := h.start("test.waiter")
	// Before the workflow has run its first step and parked.
	if err := h.rt.Signal(context.Background(), h.org, id, "approved", map[string]any{"early": true}, ""); err != nil {
		t.Fatal(err)
	}
	h.drain()
	if run := h.get(id); run.Status != "completed" {
		t.Fatalf("status = %s, want completed", run.Status)
	}
}

func TestSignalsAreDeduplicatedByKey(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(waiter(h))
	id := h.start("test.waiter")
	ctx := context.Background()
	for range 3 {
		if err := h.rt.Signal(ctx, h.org, id, "approved", map[string]any{}, "once"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = h.owner.QueryRow(ctx, `SELECT count(*) FROM workflow_signals WHERE workflow_run_id = $1`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("signals = %d, want 1", n)
	}
}

func TestAnotherOrganizationCannotSignal(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(waiter(h))
	id := h.start("test.waiter")
	other := dbtest.Org(t, h.owner)
	err := h.rt.Signal(context.Background(), other, id, "approved", map[string]any{}, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRetriesAFailingStepThenSucceeds(t *testing.T) {
	h := newHarness(t)
	failures := 2
	h.rt.Register(&Definition{Type: "test.flaky", InitialStep: "try", MaxAttempts: 3, Steps: map[string]Step{
		"try": func(context.Context, StepContext) (Result, error) {
			if failures > 0 {
				failures--
				return Result{}, errors.New("transient failure")
			}
			return Result{}, nil
		},
	}})
	id := h.start("test.flaky")

	h.tick()
	run := h.get(id)
	if run.Attempt != 1 || run.LastError != "transient failure" {
		t.Fatalf("after one failure: attempt=%d error=%q", run.Attempt, run.LastError)
	}
	for range 2 {
		h.wakeNow(id)
		h.tick()
	}
	run = h.get(id)
	if run.Status != "completed" || run.Attempt != 0 {
		t.Fatalf("status=%s attempt=%d, want completed with the counter reset", run.Status, run.Attempt)
	}
}

func TestDeadLettersAfterExhaustingAttempts(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(&Definition{Type: "test.doomed", InitialStep: "fail", MaxAttempts: 2, Steps: map[string]Step{
		"fail": func(context.Context, StepContext) (Result, error) { return Result{}, errors.New("permanent failure") },
	}})
	id := h.start("test.doomed")
	for range 3 {
		h.tick()
		h.wakeNow(id)
	}
	run := h.get(id)
	if run.Status != "dead_lettered" || run.LastError != "permanent failure" {
		t.Fatalf("status=%s error=%q, want dead_lettered and inspectable", run.Status, run.LastError)
	}
}

func TestAPanickingStepFailsItsRunNotThePoller(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(&Definition{Type: "test.panics", InitialStep: "boom", MaxAttempts: 1, Steps: map[string]Step{
		"boom": func(context.Context, StepContext) (Result, error) { panic("nil map") },
	}})
	id := h.start("test.panics")
	h.tick()
	if run := h.get(id); run.Status != "dead_lettered" {
		t.Fatalf("status = %s, want dead_lettered", run.Status)
	}
}

func TestBackoffIsExponentialWithACeiling(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 99: time.Minute} {
		if got := Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestTwoPollersNeverAdvanceTheSameRun(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(linear(h))
	other := New(h.rt.db, "test-poller-2", quiet)
	other.Register(linear(h))
	h.drain()
	h.trace = nil

	id := h.start("test.linear")
	var wg sync.WaitGroup
	for _, rt := range []*Runtime{h.rt, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = rt.Tick(context.Background(), 10)
		}()
	}
	wg.Wait()

	a := 0
	for _, s := range h.trace {
		if s == "a" {
			a++
		}
	}
	if a != 1 {
		t.Fatalf("step a ran %d times, want once", a)
	}
	if run := h.get(id); run.Step != "b" {
		t.Errorf("step = %s, want b", run.Step)
	}
}

func TestAnAbortedRunIsNotResumedByALateSignal(t *testing.T) {
	h := newHarness(t)
	h.rt.Register(waiter(h))
	id := h.start("test.waiter")
	h.drain()
	ctx := context.Background()
	if err := h.rt.Abort(ctx, h.org, id, "requirement changed"); err != nil {
		t.Fatal(err)
	}
	_ = h.rt.Signal(ctx, h.org, id, "approved", map[string]any{}, "")
	h.drain()
	run := h.get(id)
	if run.Status != "aborted" || run.LastError != "requirement changed" {
		t.Fatalf("status=%s error=%q", run.Status, run.LastError)
	}
	if len(h.trace) != 0 {
		t.Errorf("an aborted run executed a step: %v", h.trace)
	}
}

// Consuming signals before the step committed meant a transient failure
// discarded a person's approval, and the retry saw an empty signal list.
func TestASignalSurvivesAStepThatFails(t *testing.T) {
	h := newHarness(t)
	attempts := 0
	var seen [][]string
	h.rt.Register(&Definition{Type: "test.signal_retry", InitialStep: "ask", MaxAttempts: 3, Steps: map[string]Step{
		"ask": func(context.Context, StepContext) (Result, error) {
			return Result{Next: "receive", AwaitSignals: []string{"approved"}}, nil
		},
		"receive": func(_ context.Context, sc StepContext) (Result, error) {
			names := []string{}
			for _, s := range sc.Signals {
				names = append(names, s.Name)
			}
			seen = append(seen, names)
			attempts++
			if attempts == 1 {
				return Result{}, errors.New("transient failure")
			}
			return Result{}, nil
		},
	}})
	id := h.start("test.signal_retry")
	h.tick()
	if err := h.rt.Signal(context.Background(), h.org, id, "approved", map[string]any{}, ""); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.wakeNow(id)
	h.tick()

	if run := h.get(id); run.Status != "completed" {
		t.Fatalf("status = %s", run.Status)
	}
	if len(seen) != 2 || len(seen[1]) != 1 || seen[1][0] != "approved" {
		t.Fatalf("signals seen per attempt = %v, want the retry to see the approval", seen)
	}
}
