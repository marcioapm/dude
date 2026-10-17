package phases

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// hangLux holds the first cancel of each lux Run in hang until release
// (its own gate if it has one, else open), then refuses it as lux
// unavailable; every other cancel succeeds. It counts cancels per lux Run
// and how many of one were in flight at once.
type hangLux struct {
	lux.Client
	hang    map[string]bool
	open    chan struct{}
	gates   map[string]chan struct{}
	mu      sync.Mutex
	calls   map[string]int
	running map[string]int
	most    map[string]int
}

func newHangLux(hung ...string) *hangLux {
	h := &hangLux{hang: map[string]bool{}, open: make(chan struct{}), gates: map[string]chan struct{}{},
		calls: map[string]int{}, running: map[string]int{}, most: map[string]int{}}
	for _, id := range hung {
		h.hang[id] = true
	}
	return h
}

// gate makes id's hung cancel wait for the returned channel instead of open.
func (h *hangLux) gate(id string) chan struct{} {
	g := make(chan struct{})
	h.hang[id], h.gates[id] = true, g
	return g
}

func (h *hangLux) Cancel(ctx context.Context, id string) error {
	h.mu.Lock()
	h.calls[id]++
	first := h.calls[id] == 1
	h.running[id]++
	h.most[id] = max(h.most[id], h.running[id])
	release := h.open
	if g, ok := h.gates[id]; ok {
		release = g
	}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.running[id]--; h.mu.Unlock() }()
	if !first || !h.hang[id] {
		return nil
	}
	select {
	case <-release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return &lux.Error{Status: 503, Code: "unavailable"}
}

func (h *hangLux) count(id string) (calls, most int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[id], h.most[id]
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// sweepWorld is failed Runs whose lux Runs the sweep cancels.
type sweepWorld struct {
	t   *testing.T
	ctx context.Context
	s   *Syncer
	org string
	run func(sql string, args ...any)
}

func newSweepWorld(t *testing.T, fake lux.Client) *sweepWorld {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx, cancel := context.WithCancel(context.Background())
	// Stragglers still held when the test ends are let go.
	t.Cleanup(cancel)
	w := &sweepWorld{t: t, ctx: ctx, org: org,
		s: &Syncer{DB: app, Lux: fake, Log: discard(), SweepWait: 300 * time.Millisecond, RetryAhead: time.Hour}}
	w.run = func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	w.run(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	w.run(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`, org)
	return w
}

// failed adds a failed Run not kept, whose lux Run is l<id>; older ones
// are swept first.
func (w *sweepWorld) failed(ids ...string) {
	w.t.Helper()
	for _, id := range ids {
		w.run(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state,
				ended_at, created_at)
			VALUES ($2, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'failed', 'l'||$2, 'running', now(), clock_timestamp())`, w.org, id)
	}
}

func (w *sweepWorld) sweep(want int) time.Duration {
	w.t.Helper()
	start := time.Now()
	n, err := w.s.Sweep(w.ctx)
	took := time.Since(start)
	if err != nil {
		w.t.Fatal(err)
	}
	if n != want {
		w.t.Fatalf("the sweep advanced %d Runs, want %d", n, want)
	}
	return took
}

// idle waits until no slot of the sweep is held.
func (w *sweepWorld) idle(slots *pool) {
	w.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); slots.free() != cap(slots.slots); {
		if time.Now().After(deadline) {
			w.t.Fatalf("%d slots still held", cap(slots.slots)-slots.free())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A Run whose lux call hangs holds one slot past the sweep's wait, not the
// sweep: the next pass advances another Run, never the hung one a second
// time while it is held, and once it is let go it is taken up as before.
func TestAHungRunDoesNotHoldTheNextSweep(t *testing.T) {
	fake := newHangLux("lhung")
	w := newSweepWorld(t, fake)
	w.failed("hung", "first")
	// Room enough for the Runs that do finish to be counted.
	w.s.SweepWait = time.Second

	if took := w.sweep(1); took > 5*time.Second {
		t.Errorf("the sweep took %v with a SweepWait of %v", took, w.s.SweepWait)
	}
	// The next pass cancels a newer Run; then, with it cancelled and the
	// hung one still held, there is nothing to advance.
	w.failed("second")
	w.sweep(1)
	w.sweep(0)
	w.sweep(0)
	if calls, _ := fake.count("lsecond"); calls != 1 {
		t.Errorf("the second Run was cancelled %d times, want once", calls)
	}
	if calls, most := fake.count("lhung"); calls != 1 || most != 1 {
		t.Errorf("the hung Run was asked %d times, %d at once, while held; want once", calls, most)
	}

	// Let go, it fails as lux unavailable and is retried by a later pass,
	// which, with nothing hung, waits for all it started.
	close(fake.open)
	slots, _ := w.s.pools()
	w.idle(slots)
	w.s.SweepWait = 0
	w.sweep(1)
	if calls, most := fake.count("lhung"); calls != 2 || most != 1 {
		t.Errorf("the hung Run was asked %d times, %d at once; want twice, one at a time", calls, most)
	}
}

// With every slot held by hung Runs, a pass advances nothing and returns 0
// once its wait is over, without asking lux about the Run behind them.
func TestASweepWithEverySlotHeldReturnsAfterItsWait(t *testing.T) {
	var hung []string
	for i := range sweepSlots {
		hung = append(hung, fmt.Sprintf("hung%d", i))
	}
	luxIDs := make([]string, len(hung))
	for i, id := range hung {
		luxIDs[i] = "l" + id
	}
	fake := newHangLux(luxIDs...)
	w := newSweepWorld(t, fake)
	w.failed(hung...)
	w.failed("behind")

	for pass := range 3 {
		if took := w.sweep(0); took > w.s.SweepWait+3*time.Second {
			t.Errorf("pass %d took %v with a SweepWait of %v", pass, took, w.s.SweepWait)
		}
	}
	if calls, _ := fake.count("lbehind"); calls != 0 {
		t.Errorf("the Run behind the hung ones was asked %d times", calls)
	}
	for _, id := range luxIDs {
		if calls, most := fake.count(id); calls != 1 || most != 1 {
			t.Errorf("%s was asked %d times, %d at once; want once", id, calls, most)
		}
	}
	close(fake.open)
	slots, _ := w.s.pools()
	w.idle(slots)
	w.s.SweepWait = 0
	w.sweep(sweepSlots + 1)
	if calls, _ := fake.count("lbehind"); calls != 1 {
		t.Errorf("the Run behind the hung ones was asked %d times once they were let go, want once", calls)
	}
}

// A Run whose earlier advance ends during a pass that read it while it was
// still busy is not advanced again from that row: once per pass at most,
// and not before the back-off its advance wrote is over.
func TestARunFreedMidPassIsNotAdvancedFromItsStaleRow(t *testing.T) {
	var hung []string
	for i := range sweepSlots - 1 {
		hung = append(hung, fmt.Sprintf("hung%d", i))
	}
	luxIDs := make([]string, len(hung))
	for i, id := range hung {
		luxIDs[i] = "l" + id
	}
	fake := newHangLux(luxIDs...)
	gx := fake.gate("lx")
	w := newSweepWorld(t, fake)
	w.s.RetryAhead = 0
	w.failed(hung...)
	w.failed("x")
	slots, _ := w.s.pools()

	// The hung Runs and x fill every slot.
	w.sweep(0)
	// A Run sorted before x, so the next pass waits for a slot on it
	// before it reaches x.
	w.failed("behind")
	w.run(`UPDATE runs SET created_at = (SELECT created_at FROM runs WHERE id = 'x') - interval '1 millisecond' WHERE id = 'behind'`)

	w.s.SweepWait = 5 * time.Second
	done := make(chan int, 1)
	go func() {
		n, err := w.s.Sweep(w.ctx)
		if err != nil {
			t.Error(err)
		}
		done <- n
	}()
	for deadline := time.Now().Add(10 * time.Second); !slots.busyIDs()["behind"]; {
		if time.Now().After(deadline) {
			t.Fatal("the pass never reached the Run before x")
		}
		time.Sleep(time.Millisecond)
	}
	// x fails as lux unavailable and is backed off; its slot goes to the
	// Run before it, whose cancel succeeds and frees the slot again.
	close(gx)
	if n := <-done; n != 1 {
		t.Errorf("the pass advanced %d Runs, want 1 (the one before x)", n)
	}
	if calls, most := fake.count("lx"); calls != 1 || most != 1 {
		t.Fatalf("x was asked %d times, %d at once, in the pass that read it busy; want once", calls, most)
	}
	if calls, _ := fake.count("lbehind"); calls != 1 {
		t.Errorf("the Run before x was asked %d times, want once", calls)
	}

	// Its back-off holds: x is not due for 5 s.
	w.s.SweepWait = 0
	w.sweep(0)
	if calls, _ := fake.count("lx"); calls != 1 {
		t.Errorf("x was asked %d times before its back-off was over, want once", calls)
	}
	w.run(`UPDATE runs SET next_attempt_at = now() WHERE id = 'x'`)
	w.sweep(1)
	if calls, most := fake.count("lx"); calls != 2 || most != 1 {
		t.Errorf("x was asked %d times, %d at once, once due; want twice, one at a time", calls, most)
	}
}

// hangForges holds For for the hung organization until released, and
// fails it for every other, which backs their publishes off.
type hangForges struct {
	hung  string
	open  chan struct{}
	mu    sync.Mutex
	calls map[string]int
}

func (f *hangForges) For(ctx context.Context, org string) (*forge.GitHub, error) {
	f.mu.Lock()
	f.calls[org]++
	f.mu.Unlock()
	if org == f.hung {
		select {
		case <-f.open:
		case <-ctx.Done():
		}
	}
	return nil, errors.New("no forge")
}

func (f *hangForges) count(org string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[org]
}

// A hung publish keeps its slot past the pass's wait, and the next pass
// claims only the slots left free; one is never claimed twice.
func TestAHungPublishHoldsOneSlotAndTheNextPassClaimsTheRest(t *testing.T) {
	app, owner := dbtest.Open(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A moving publish per organization, so the fake forge tells them
	// apart; the first is the oldest.
	var orgs []string
	for i := range 6 {
		org := dbtest.Org(t, owner)
		orgs = append(orgs, org)
		exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
		exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`, org)
		exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'conductor', 'agent', 'running')`, org)
		exec(`INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, status, created_at)
			VALUES ('pub_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'req_'||$1, 'moving', now() + make_interval(secs => $2))`, org, i)
	}
	forges := &hangForges{hung: orgs[0], open: make(chan struct{}), calls: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Syncer{DB: app, Forges: forges, Log: discard(), SweepWait: 300 * time.Millisecond}
	_, slots := s.pools()
	settle := func(want int) {
		t.Helper()
		start := time.Now()
		n, err := s.SettlePublishes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("claimed %d publishes, want %d", n, want)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("the pass took %v with a SweepWait of %v", took, s.SweepWait)
		}
	}
	held := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); len(slots.slots) != want; {
			if time.Now().After(deadline) {
				t.Fatalf("%d slots held, want %d", len(slots.slots), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	settle(publishSlots)
	held(1)
	// Two slots free: two more, though three are due.
	settle(publishSlots - 1)
	held(1)
	if n := forges.count(orgs[0]); n != 1 {
		t.Errorf("the hung publish was carried on %d times, want once", n)
	}
	for _, org := range orgs[1:] {
		if n := forges.count(org); n > 1 {
			t.Errorf("%s's publish was carried on %d times", org, n)
		}
	}
	close(forges.open)
	held(0)
}

// Sweep with SweepWait zero waits for all it started, as the tests' pump
// expects: a slow Run is advanced and counted before Sweep returns.
func TestASweepWithoutAWaitWaitsForEveryRun(t *testing.T) {
	fake := newHangLux("lslow")
	w := newSweepWorld(t, fake)
	w.s.SweepWait = 0
	w.failed("slow")
	time.AfterFunc(300*time.Millisecond, func() { close(fake.open) })
	if took := w.sweep(1); took < 300*time.Millisecond {
		t.Errorf("the sweep returned after %v, before its Run was let go", took)
	}
	slots, _ := w.s.pools()
	if n := cap(slots.slots) - slots.free(); n != 0 {
		t.Errorf("%d slots still held after a sweep that waited", n)
	}
}

// A pass whose wait is over counts every result already sent, and does not
// wait for those that are not.
func TestAnExpiredPassCountsTheResultsAlreadySent(t *testing.T) {
	expired := make(chan struct{})
	close(expired)
	w := passWait{expired: expired, stop: func() bool { return false }}
	// Many times: a select between the results and the expiry picks at random.
	for range 200 {
		results := make(chan bool, sweepSlots+2)
		for i := range sweepSlots {
			results <- i != 0
		}
		if got := w.collect(results, sweepSlots+2); got != sweepSlots-1 {
			t.Fatalf("counted %d of %d results that acted", got, sweepSlots-1)
		}
	}
}
