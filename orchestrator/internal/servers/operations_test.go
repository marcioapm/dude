package servers

// A preview's resume or replacement calls lux over HTTP. No database
// transaction, row lock or pool connection is held across those calls: a
// slow lux must not starve unrelated work, nor keep StopPreview or the
// event follower waiting on the preview's row.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// gatedLux is lux over HTTP whose answers to one kind of request wait until
// the gate opens (or the request's context ends).
type gatedLux struct {
	*httptest.Server
	entered chan string
	gate    chan struct{}
	once    sync.Once
}

func newGatedLux(t *testing.T, gated func(*http.Request) bool) *gatedLux {
	g := &gatedLux{entered: make(chan string, 64), gate: make(chan struct{})}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gated(r) {
			g.entered <- r.URL.Path
			select {
			case <-g.gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/servers"):
			fmt.Fprint(w, `{"servers":[]}`)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{}`)
		default:
			fmt.Fprint(w, `{"id":"lux","state":"resuming","spec":{"image":{}}}`)
		}
	}))
	t.Cleanup(func() { g.open(); g.Close() })
	return g
}

func (g *gatedLux) open() { g.once.Do(func() { close(g.gate) }) }

// previewRows makes n previews of one project in a fresh organization:
// wakeable ones with a wake this orchestrator has claimed, or eager ones
// parked; each on lux Run lux_<i>, stopped.
func previewRows(t *testing.T, owner *pgx.Conn, n int, wakeable bool) (org string, runs []wakeRun) {
	t.Helper()
	ctx := context.Background()
	org = dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	var claimed time.Time
	if err := owner.QueryRow(ctx, `SELECT date_trunc('milliseconds', now())`).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		r := wakeRun{previewRun: previewRun{ID: fmt.Sprintf("run_%s_%d", org, i), Org: org, ProjectID: "prj_" + org,
			TaskID: fmt.Sprintf("wi_%s_%d", org, i), Status: "paused", LuxRunID: fmt.Sprintf("lux_%d", i), LuxState: "stopped"}}
		exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ($1, $2, $3, $4, 'T', 'G')`,
			r.TaskID, org, r.ProjectID, i+1)
		exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status, lux_run_id, lux_state, wakeable,
				lux_stop_reason, dude_pause, pending_starts, wake_wanted_at, wake_claimed_at)
			VALUES ($1, $2, $3, $4, 1, 'preview', 'paused', $5, 'stopped', $6, 'pause', 'unused', ARRAY['web'],
				CASE WHEN $6 THEN $7::timestamptz END, CASE WHEN $6 THEN $7::timestamptz END)`,
			r.ID, org, r.ProjectID, r.TaskID, r.LuxRunID, wakeable, claimed)
		if wakeable {
			r.WakeWanted, r.ClaimedAt = &claimed, claimed
		}
		runs = append(runs, r)
	}
	return org, runs
}

// Eight previews' resumes or replacements stalled in lux, over a pool of
// four connections: unrelated work still gets a connection, and StopPreview,
// the event follower and an unrelated task update each finish within 500ms.
// Once lux answers, each operation finishes, and the stopped preview stays
// stopped on the Run it had.
func TestAPreviewOperationStalledInLuxHoldsNoConnectionOrRow(t *testing.T) {
	type path struct {
		name     string
		wakeable bool
		gated    func(*http.Request) bool
		run      func(*Previews, wakeRun) error
	}
	suffix := func(s string) func(*http.Request) bool {
		return func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, s) }
	}
	paths := []path{
		{"replacing a woken Run, listing its servers", true, suffix("/servers"), func(p *Previews, r wakeRun) error {
			_, err := p.replaceRun(context.Background(), &r, true)
			return err
		}},
		{"replacing a woken Run, cancelling it", true, suffix("/cancel"), func(p *Previews, r wakeRun) error {
			_, err := p.replaceRun(context.Background(), &r, true)
			return err
		}},
		{"replacing a parked Run", false, suffix("/cancel"), func(p *Previews, r wakeRun) error {
			return p.replaceParked(context.Background(), r.previewRun, []string{"REMOVED"})
		}},
		{"resuming a parked Run", false, suffix("/resume"), func(p *Previews, r wakeRun) error {
			return p.resume(context.Background(), r.previewRun)
		}},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			app, owner := dbtest.Open(t)
			org, runs := previewRows(t, owner, 8, tc.wakeable)
			cfg := app.Pool.Config()
			cfg.MaxConns = 4
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			g := newGatedLux(t, tc.gated)
			p := &Previews{Service: &Service{DB: &db.DB{Pool: pool}, Lux: lux.New(g.URL, "test"),
				Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}

			done := make(chan error, len(runs))
			for _, r := range runs {
				go func() { done <- tc.run(p, r) }()
			}
			reached := 0
			timeout := time.After(10 * time.Second)
		reach:
			for reached < len(runs) {
				select {
				case <-g.entered:
					reached++
				case <-timeout:
					break reach
				}
			}
			if reached < len(runs) {
				t.Errorf("%d of %d operations reached lux; the others waited on the pool", reached, len(runs))
			}

			within := func(what string, fn func(context.Context) error) {
				t.Helper()
				probe, stop := context.WithTimeout(ctx, 500*time.Millisecond)
				defer stop()
				start := time.Now()
				if err := fn(probe); err != nil {
					t.Errorf("%s while lux is stalled: %v after %s", what, err, time.Since(start).Round(time.Millisecond))
				}
			}
			within("SELECT 1", func(c context.Context) error {
				var n int
				return pool.QueryRow(c, `SELECT 1`).Scan(&n)
			})
			within("an unrelated task update", func(c context.Context) error {
				return p.DB.InOrg(c, org, func(tx pgx.Tx) error {
					_, err := tx.Exec(c, `UPDATE tasks SET title = 'Still editable' WHERE id = $1`, runs[7].TaskID)
					return err
				})
			})
			within("StopPreview", func(c context.Context) error {
				_, err := p.StopPreview(c, org, runs[0].TaskID, "test")
				return err
			})
			within("applyEvent", func(c context.Context) error {
				return p.applyEvent(c, runs[1].previewRun, lux.Frame{Kind: "lux", EventID: 1, EventType: "git.checkout",
					EventData: []byte(`{"repo":"app","base":"abc"}`)})
			})

			g.open()
			for range runs {
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("operation: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("operations did not finish once lux answered")
				}
			}
			var status, luxRun string
			var reserved bool
			if err := owner.QueryRow(ctx, `SELECT status::text, COALESCE(lux_run_id, ''), op_token IS NOT NULL FROM runs WHERE id = $1`,
				runs[0].ID).Scan(&status, &luxRun, &reserved); err != nil {
				t.Fatal(err)
			}
			if status != "completed" || luxRun != runs[0].LuxRunID || reserved {
				t.Errorf("the stopped preview: status %s on %q, reserved %v; want completed on %s, its Run left for the sweep to cancel",
					status, luxRun, reserved, runs[0].LuxRunID)
			}
			var moved int
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM runs WHERE organization_id = $1 AND id <> $2 AND op_token IS NULL
				AND ((status = 'running' AND lux_run_id IS NOT NULL) OR lux_generation = 1)`, org, runs[0].ID).Scan(&moved); err != nil {
				t.Fatal(err)
			}
			if moved != len(runs)-1 {
				t.Errorf("%d of the %d other previews were resumed or replaced", moved, len(runs)-1)
			}
		})
	}
}

// slowLux answers a Run's servers after hold, whatever the request's
// context says (as a response already on its way does), and records each
// call. Anything else the code asks of it panics (the nil Client).
type slowLux struct {
	lux.Client
	hold  time.Duration
	mu    sync.Mutex
	calls []string
}

func (c *slowLux) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *slowLux) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func (c *slowLux) Servers(_ context.Context, id string) ([]lux.Server, error) {
	c.record("servers " + id)
	time.Sleep(c.hold)
	return nil, nil
}

func (c *slowLux) Cancel(_ context.Context, id string) error {
	c.record("cancel " + id)
	return nil
}

func (c *slowLux) Get(_ context.Context, id string) (lux.Run, error) {
	c.record("get " + id)
	return lux.Run{}, &lux.Error{Status: 503, Code: "unavailable", Message: "test"}
}

// A replacement whose lux answer comes after its reservation's time for
// calls is up makes no further call (the old Run is not cancelled) and
// records nothing, and lets the row go.
func TestAReplacementPastItsReservationsTimeCallsLuxNoMore(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	_, runs := previewRows(t, owner, 1, true)
	r := runs[0]
	const holdFor = 900 * time.Millisecond
	slow := &slowLux{hold: holdFor}
	p := &Previews{Service: &Service{DB: app, Lux: slow, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, OperationFor: holdFor}
	won, err := p.replaceRun(ctx, &r, true)
	if !errors.Is(err, errLapsed) || won {
		t.Errorf("replaceRun = %v, %v; want the lapse", won, err)
	}
	if got := slow.Calls(); !slices.Equal(got, []string{"servers " + r.LuxRunID}) {
		t.Errorf("lux calls %v; want the servers listed, nothing after", got)
	}
	var luxRun string
	var gen int
	var reserved bool
	if err := owner.QueryRow(ctx, `SELECT COALESCE(lux_run_id, ''), lux_generation, op_token IS NOT NULL FROM runs WHERE id = $1`, r.ID).
		Scan(&luxRun, &gen, &reserved); err != nil {
		t.Fatal(err)
	}
	if luxRun != r.LuxRunID || gen != 0 || reserved {
		t.Errorf("after the lapse: Run %q generation %d reserved %v; want %s, 0, released", luxRun, gen, reserved, r.LuxRunID)
	}
}

// delayedUpdateResult holds the result of a connection's first UPDATE for
// delay before the caller sees it, as a slow network or a descheduled
// process does.
type delayedUpdateResult struct {
	delay time.Duration
	once  sync.Once
}

func (g *delayedUpdateResult) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (g *delayedUpdateResult) TraceQueryEnd(_ context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if d.CommandTag.Update() {
		g.once.Do(func() { time.Sleep(g.delay) })
	}
}

// A reservation whose answer arrives after its time for lux calls is over
// is let go at once: its holder makes no lux call, and another takes the row.
func TestAReservationAnsweredAfterItsTimeMakesNoLuxCall(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	_, rows := previewRows(t, owner, 1, false)
	r := rows[0].previewRun
	cfg := app.Pool.Config()
	cfg.ConnConfig.Tracer = &delayedUpdateResult{delay: 1100 * time.Millisecond}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	late := &Previews{Service: &Service{DB: &db.DB{Pool: pool}, Log: log}, OperationFor: 900 * time.Millisecond}
	op, _, err := late.reserve(ctx, r.Org, r.ID, opReplace, parkedOnSQL, r.LuxRunID, r.Generation)
	if op != nil || !errors.Is(err, errLapsed) {
		left := time.Duration(0)
		if op != nil {
			left = time.Until(op.until)
		}
		t.Errorf("reserve answered past its time = op with %s left locally, %v; want the lapse", left, err)
	}
	var reserved bool
	if err := owner.QueryRow(ctx, `SELECT op_token IS NOT NULL FROM runs WHERE id = $1`, r.ID).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved {
		t.Error("the lapsed reservation was not let go")
	}
	other := &Previews{Service: &Service{DB: app, Log: log}, OperationFor: 900 * time.Millisecond}
	next, _, err := other.reserve(ctx, r.Org, r.ID, opResume, parkedOnSQL, r.LuxRunID, r.Generation)
	if err != nil || next == nil {
		t.Fatalf("another reserve after the lapse = %v, %v; want the row", next, err)
	}
	other.release(ctx, next)
}

// refusingLux refuses a resume once answer is closed, after entered.
type refusingLux struct {
	lux.Client
	entered, answer chan struct{}
}

func (c *refusingLux) Get(context.Context, string) (lux.Run, error) { return lux.Run{State: "stopped"}, nil }

func (c *refusingLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	close(c.entered)
	<-c.answer
	return lux.Run{}, &lux.Error{Status: 409, Code: "not_resumable", Message: "old Run refused"}
}

func (c *refusingLux) Servers(context.Context, string) ([]lux.Server, error) { return nil, nil }

func (c *refusingLux) Cancel(context.Context, string) error { return nil }

// A resume lux refuses only after its reservation lapsed, and another
// replaced the Run meanwhile, fails nothing: the refusal is of a Run and
// generation the preview no longer has.
func TestALapsedResumesRefusalDoesNotFailTheNextGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, owner := dbtest.Open(t)
	_, rows := previewRows(t, owner, 1, false)
	r := rows[0].previewRun
	c := &refusingLux{entered: make(chan struct{}), answer: make(chan struct{})}
	var once sync.Once
	answer := func() { once.Do(func() { close(c.answer) }) }
	defer answer()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := &Previews{Service: &Service{DB: app, Lux: c, Log: log}, OperationFor: 900 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- p.resume(ctx, r) }()
	select {
	case <-c.entered:
	case <-ctx.Done():
		t.Fatal("resume did not reach lux")
	}
	for {
		var lapsed bool
		if err := owner.QueryRow(ctx, `SELECT op_deadline <= now() FROM runs WHERE id = $1`, r.ID).Scan(&lapsed); err != nil {
			t.Fatal(err)
		}
		if lapsed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := p.replaceParked(ctx, r, []string{"REMOVED"}); err != nil {
		t.Fatal(err)
	}
	answer()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var status string
	var generation int
	if err := owner.QueryRow(ctx, `SELECT status::text, lux_generation FROM runs WHERE id = $1`, r.ID).Scan(&status, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || generation != 1 {
		t.Errorf("after the lapsed refusal: status %s generation %d; want the replacement's pending generation 1", status, generation)
	}
}

// A parked preview's resume whose answer comes after lux's events of that
// start were applied keeps what the events said: the last state applied
// stands, and a start that ended leaves the preview parked.
func TestAResumeAnsweredAfterItsEventsKeepsTheAppliedState(t *testing.T) {
	for _, tc := range []struct {
		events []string
		status string
	}{
		{[]string{"resuming", "running", "failed"}, "paused"},
		{[]string{"resuming", "running"}, "running"},
	} {
		t.Run(strings.Join(tc.events, ","), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			app, owner := dbtest.Open(t)
			_, rows := previewRows(t, owner, 1, false)
			r := rows[0].previewRun
			g := newGatedLux(t, func(req *http.Request) bool { return strings.HasSuffix(req.URL.Path, "/resume") })
			p := &Previews{Service: &Service{DB: app, Lux: lux.New(g.URL, "test"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
			done := make(chan error, 1)
			go func() { done <- p.resume(ctx, r) }()
			select {
			case <-g.entered:
			case <-ctx.Done():
				t.Fatal("resume did not reach lux")
			}
			for i, state := range tc.events {
				if err := p.applyEvent(ctx, r, lux.Frame{Kind: "lux", EventID: int64(i + 1), EventType: "state",
					EventData: []byte(`{"state":"` + state + `"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			g.open()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			last := tc.events[len(tc.events)-1]
			var state, status string
			var reserved bool
			if err := owner.QueryRow(ctx, `SELECT lux_state, status::text, op_token IS NOT NULL FROM runs WHERE id = $1`, r.ID).
				Scan(&state, &status, &reserved); err != nil {
				t.Fatal(err)
			}
			if state != last || status != tc.status || reserved {
				t.Errorf("after the answer: lux_state %s status %s reserved %v; want %s, %s, released", state, status, reserved, last, tc.status)
			}
		})
	}
}

// A reservation in force keeps every other wake claim, replacement and
// sweep off the row; once it has lapsed (its holder gone without letting
// go), the row is taken over.
func TestAReservationIsLeftAloneUntilItLapses(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	_, runs := previewRows(t, owner, 1, true)
	r := runs[0]
	recorder := &slowLux{}
	p := &Previews{Service: &Service{DB: app, Lux: recorder, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	reserve := func(deadline string) {
		t.Helper()
		// Another orchestrator's: its claim of the wake long expired, its
		// reservation as given.
		if _, err := owner.Exec(ctx, `UPDATE runs SET wake_claimed_at = now() - interval '1 hour', op_token = 'op_other',
			op_kind = 'replace', op_deadline = now() + $2::interval WHERE id = $1`, r.ID, deadline); err != nil {
			t.Fatal(err)
		}
	}

	reserve("1 hour")
	if _, err := p.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := recorder.Calls(); len(calls) != 0 {
		t.Errorf("a sweep acted on a reserved preview: %v", calls)
	}
	claim := r
	if ok, err := p.claimWake(ctx, &claim); err != nil || ok {
		t.Errorf("claimWake on a reserved preview = %v, %v; want false", ok, err)
	}
	if won, err := p.replaceRun(ctx, &r, true); err != nil || won {
		t.Errorf("replaceRun on a reserved preview = %v, %v; want false", won, err)
	}
	if calls := recorder.Calls(); len(calls) != 0 {
		t.Errorf("lux was called for a reserved preview: %v", calls)
	}

	reserve("-1 second")
	claim = r
	if ok, err := p.claimWake(ctx, &claim); err != nil || !ok {
		t.Fatalf("claimWake past the reservation = %v, %v; want it claimed", ok, err)
	}
	if won, err := p.replaceRun(ctx, &claim, true); err != nil || !won {
		t.Fatalf("replaceRun past the reservation = %v, %v; want it replaced", won, err)
	}
	if got := recorder.Calls(); !slices.Equal(got, []string{"servers " + r.LuxRunID, "cancel " + r.LuxRunID}) {
		t.Errorf("lux calls %v", got)
	}
	var gen int
	var reserved bool
	if err := owner.QueryRow(ctx, `SELECT lux_generation, op_token IS NOT NULL FROM runs WHERE id = $1 AND lux_run_id IS NULL`, r.ID).
		Scan(&gen, &reserved); err != nil {
		t.Fatal(err)
	}
	if gen != 1 || reserved {
		t.Errorf("after the takeover: generation %d reserved %v; want 1, released", gen, reserved)
	}
}
