package phases

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// costLux is lux as far as costs go: an answer or an error per lux Run,
// and a count of what was asked. Anything else is a test failure (the
// embedded nil Client panics).
type costLux struct {
	lux.Client
	mu      sync.Mutex
	answers map[string]lux.RunCost
	errs    map[string]error
	asked   map[string]int
}

func (f *costLux) Cost(_ context.Context, id string) (lux.RunCost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked[id]++
	if err := f.errs[id]; err != nil {
		return lux.RunCost{}, err
	}
	return f.answers[id], nil
}

func (f *costLux) set(id string, c lux.RunCost) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[id] = c
}

func usd(family string, amount lux.Decimal) lux.FamilyCost {
	return lux.FamilyCost{Family: family, Currency: "USD", Amount: amount}
}

type costFixture struct {
	t     *testing.T
	ctx   context.Context
	owner *pgx.Conn
	org   string
	lux   *costLux
	costs *Costs
}

func newCostFixture(t *testing.T) *costFixture {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	fake := &costLux{answers: map[string]lux.RunCost{}, errs: map[string]error{}, asked: map[string]int{}}
	return &costFixture{t: t, ctx: ctx, owner: owner, org: org, lux: fake,
		costs: &Costs{DB: app, Lux: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
}

// run adds an agent's Run on lux as lux_<id>, ended endedAgo ago ("" for
// one still going).
func (f *costFixture) run(id, endedAgo string) {
	f.t.Helper()
	f.runOf("agent", id, endedAgo)
}

// runOf inserts the Run without a lux Run and then gives it one, the way
// Syncer.submit does, so the work list is joined through the UPDATE path.
func (f *costFixture) runOf(kind, id, endedAgo string) {
	f.t.Helper()
	f.insertRun(kind, id, endedAgo, false)
	f.setLuxRun(id)
}

func (f *costFixture) insertRun(kind, id, endedAgo string, withLux bool) {
	f.t.Helper()
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, lux_run_id, started_at, ended_at)
		VALUES ($1, $2, 'prj_'||$2, 'wi_'||$2, 1, $4, CASE WHEN $5 THEN 'lux_'||$1 END, now() - interval '9 days',
		        CASE WHEN $3 = '' THEN NULL ELSE now() - $3::interval END)`, id, f.org, endedAgo, kind, withLux); err != nil {
		f.t.Fatal(err)
	}
}

func (f *costFixture) setLuxRun(id string) {
	f.t.Helper()
	if _, err := f.owner.Exec(f.ctx, `UPDATE runs SET lux_run_id = 'lux_'||$1 WHERE id = $1`, id); err != nil {
		f.t.Fatal(err)
	}
}

// nextIn is how long until the Run's cost is read again; ok is false when
// no read is due at all (the Run left the work list).
func (f *costFixture) nextIn(id string) (d time.Duration, ok bool) {
	f.t.Helper()
	var s *float64
	if err := f.owner.QueryRow(f.ctx, `SELECT EXTRACT(EPOCH FROM lux_cost_next_at - now())::float8 FROM runs WHERE id = $1`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	if s == nil {
		return 0, false
	}
	return time.Duration(*s * float64(time.Second)), true
}

// sweep reads every Run due, then makes them all due again, as the next
// tick after the cadence would find them.
func (f *costFixture) sweep() {
	f.t.Helper()
	if _, err := f.costs.Sweep(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.owner.Exec(f.ctx, `UPDATE runs SET lux_cost_next_at = now() - interval '1 second' WHERE lux_cost_next_at IS NOT NULL`); err != nil {
		f.t.Fatal(err)
	}
}

type storedCost struct {
	AI, Compute, Status *string
	Read                bool
}

func (f *costFixture) stored(id string) storedCost {
	f.t.Helper()
	var s storedCost
	if err := f.owner.QueryRow(f.ctx, `SELECT lux_ai_usd::text, lux_compute_usd::text, lux_cost_status, lux_cost_read_at IS NOT NULL
		FROM runs WHERE id = $1`, id).Scan(&s.AI, &s.Compute, &s.Status, &s.Read); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *costFixture) reported(id string) []map[string]any {
	f.t.Helper()
	rows, err := f.owner.Query(f.ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, id, EvCostReported)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func str(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

// The poller keeps lux's AI and compute amounts and its status exactly as
// lux wrote them, records a change once, and stops asking once final.
func TestCostsAreStoredAsLuxReportsThemUntilFinal(t *testing.T) {
	f := newCostFixture(t)
	f.run("run_a", "")
	f.lux.set("lux_run_a", lux.RunCost{Status: lux.CostIncomplete,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.5")}})
	f.sweep()
	if s := f.stored("run_a"); str(s.AI) != "0.5" || s.Compute != nil || str(s.Status) != "incomplete" || !s.Read {
		t.Fatalf("after the first read: ai %s compute %s status %s", str(s.AI), str(s.Compute), str(s.Status))
	}
	// The same answer again: stored, but nothing new for the ledger.
	f.sweep()
	if n := len(f.reported("run_a")); n != 1 {
		t.Errorf("%d cost events after an unchanged read, want 1", n)
	}

	// The amount rises at the same status: this is what moves the run header
	// while lux is still settling.
	f.lux.set("lux_run_a", lux.RunCost{Status: lux.CostIncomplete,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.9")}})
	f.sweep()
	if n := len(f.reported("run_a")); n != 2 {
		t.Errorf("%d cost events after the AI amount rose at the same status, want 2", n)
	}
	// Only compute appears, AI and status unchanged.
	f.lux.set("lux_run_a", lux.RunCost{Status: lux.CostIncomplete,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.9"), usd(lux.FamilyCompute, "0.004")}})
	f.sweep()
	if n := len(f.reported("run_a")); n != 3 {
		t.Errorf("%d cost events after only compute changed, want 3", n)
	}

	f.lux.set("lux_run_a", lux.RunCost{Status: lux.CostFinal, Final: true,
		ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "1.810247"), usd(lux.FamilyCompute, "0.007659225")}})
	f.sweep()
	s := f.stored("run_a")
	if str(s.AI) != "1.810247" || str(s.Compute) != "0.007659225" || str(s.Status) != "final" {
		t.Fatalf("after final: ai %s compute %s status %s", str(s.AI), str(s.Compute), str(s.Status))
	}
	events := f.reported("run_a")
	if len(events) != 4 {
		t.Fatalf("%d cost events, want 4", len(events))
	}
	last := events[3]
	if last["aiUsd"] != 1.810247 || last["computeUsd"] != 0.007659225 || last["status"] != "final" {
		t.Errorf("last event %v", last)
	}
	if events[0]["computeUsd"] != nil {
		t.Errorf("a family lux had not reported is %v in the event, want null", events[0]["computeUsd"])
	}

	asked := f.lux.asked["lux_run_a"]
	f.sweep()
	f.sweep()
	if f.lux.asked["lux_run_a"] != asked {
		t.Errorf("a final cost was asked for again")
	}
}

// Amounts in another currency are not USD; the families lux has not
// priced stay unreported (NULL), not zero.
func TestOnlyUSDAmountsAreKept(t *testing.T) {
	f := newCostFixture(t)
	f.run("run_eur", "")
	f.lux.set("lux_run_eur", lux.RunCost{Status: lux.CostComplete,
		ByFamily: []lux.FamilyCost{{Family: lux.FamilyAI, Currency: "EUR", Amount: "2"}, usd(lux.FamilyCompute, "0.01")}})
	f.sweep()
	if s := f.stored("run_eur"); s.AI != nil || str(s.Compute) != "0.01" {
		t.Errorf("ai %s compute %s", str(s.AI), str(s.Compute))
	}
}

// A Run that ended more than eight days ago is past lux's settling and is
// not asked about; one lux fails on does not stop the others, and is
// asked again on a later tick.
func TestOneFailingRunDoesNotStopTheOthersAndOldRunsAreLeftAlone(t *testing.T) {
	f := newCostFixture(t)
	f.run("run_old", "9 days")
	f.run("run_recent", "7 days")
	f.run("run_down", "")
	f.run("run_gone", "")
	f.run("run_ok", "")
	for _, id := range []string{"lux_run_old", "lux_run_recent", "lux_run_ok"} {
		f.lux.set(id, lux.RunCost{Status: lux.CostComplete, ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.25")}})
	}
	f.lux.errs["lux_run_down"] = &lux.Error{Status: 503, Code: "unavailable", Message: "down"}
	f.lux.errs["lux_run_gone"] = &lux.Error{Status: 404, Code: "not_found", Message: "no such run"}
	f.sweep()

	if f.lux.asked["lux_run_old"] != 0 {
		t.Error("a Run that ended nine days ago was asked about")
	}
	for _, id := range []string{"run_recent", "run_ok"} {
		if s := f.stored(id); str(s.AI) != "0.25" {
			t.Errorf("%s: ai %s after another Run failed", id, str(s.AI))
		}
	}
	if s := f.stored("run_down"); s.Read || s.AI != nil {
		t.Errorf("a failed read stored something: %+v", s)
	}

	delete(f.lux.errs, "lux_run_down")
	f.lux.set("lux_run_down", lux.RunCost{Status: lux.CostFinal, ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.75")}})
	f.sweep()
	if s := f.stored("run_down"); str(s.AI) != "0.75" {
		t.Errorf("the Run lux was down for was not read again: ai %s", str(s.AI))
	}
}

// When lux is next asked, measured without making anything due again: a
// read waits one cadence, a failed read too, a Run lux does not know waits
// an hour, and a Run with nothing left to settle leaves the work list. A
// preview's lux Run is never asked about.
func TestAReadWaitsItsTurnAndALostRunWaitsAnHour(t *testing.T) {
	f := newCostFixture(t)
	f.costs.Every = time.Minute
	f.run("run_ok", "")
	f.run("run_down", "")
	f.run("run_gone", "")
	f.run("run_final", "")
	f.run("run_aging", "7 days")
	f.run("run_aging_down", "7 days")
	f.runOf("preview", "run_prev", "")
	// A Run inserted with its lux Run already set is due from the start.
	f.insertRun("agent", "run_direct", "", true)
	// Both ended within costPatience when they got their lux Run, and have
	// aged past it since.
	if _, err := f.owner.Exec(f.ctx, `UPDATE runs SET ended_at = now() - interval '9 days' WHERE id IN ('run_aging', 'run_aging_down')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"lux_run_ok", "lux_run_aging", "lux_run_prev", "lux_run_direct"} {
		f.lux.set(id, lux.RunCost{Status: lux.CostIncomplete, ByFamily: []lux.FamilyCost{usd(lux.FamilyAI, "0.1")}})
	}
	f.lux.set("lux_run_final", lux.RunCost{Status: lux.CostFinal, Final: true})
	f.lux.errs["lux_run_down"] = &lux.Error{Status: 503, Code: "unavailable"}
	f.lux.errs["lux_run_aging_down"] = &lux.Error{Status: 503, Code: "unavailable"}
	f.lux.errs["lux_run_gone"] = &lux.Error{Status: 404, Code: "not_found"}

	if n, err := f.costs.Sweep(f.ctx); err != nil || n != 7 {
		t.Fatalf("first sweep read %d (%v), want the 7 agent Runs", n, err)
	}
	if f.lux.asked["lux_run_direct"] != 1 {
		t.Error("a Run inserted with its lux Run was not read")
	}
	if n, err := f.costs.Sweep(f.ctx); err != nil || n != 0 {
		t.Fatalf("second sweep at once read %d (%v), want none due", n, err)
	}
	if f.lux.asked["lux_run_prev"] != 0 {
		t.Error("a preview's lux Run was asked for its cost")
	}
	if _, ok := f.nextIn("run_prev"); ok {
		t.Error("a preview is on the cost work list")
	}

	for _, id := range []string{"run_ok", "run_down"} {
		if w, ok := f.nextIn(id); !ok || w < 50*time.Second || w > time.Minute {
			t.Errorf("%s: next read in %v (due %v), want ~1m", id, w, ok)
		}
	}
	if w, ok := f.nextIn("run_gone"); !ok || w < 59*time.Minute || w > time.Hour {
		t.Errorf("after a 404: next read in %v (due %v), want ~1h", w, ok)
	}
	for _, id := range []string{"run_final", "run_aging", "run_aging_down"} {
		if w, ok := f.nextIn(id); ok {
			t.Errorf("%s: still due in %v after its last read, want off the work list", id, w)
		}
	}
	// Writing the same lux Run again is not a new lux Run.
	f.setLuxRun("run_final")
	if w, ok := f.nextIn("run_final"); ok {
		t.Errorf("re-setting the same lux Run put a final Run back, due in %v", w)
	}
}

// A sweep reads no more than its batch; the rest wait for the next tick.
func TestASweepReadsABoundedBatch(t *testing.T) {
	f := newCostFixture(t)
	f.costs.Batch = 2
	for _, id := range []string{"run_1", "run_2", "run_3"} {
		f.run(id, "")
		f.lux.set("lux_"+id, lux.RunCost{Status: lux.CostPending})
	}
	n, err := f.costs.Sweep(f.ctx)
	if err != nil || n != 2 {
		t.Fatalf("read %d (%v), want 2", n, err)
	}
	n, err = f.costs.Sweep(f.ctx)
	if err != nil || n != 1 {
		t.Fatalf("second sweep read %d (%v), want the 1 left", n, err)
	}
}
