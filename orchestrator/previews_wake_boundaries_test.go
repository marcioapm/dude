package orchestrator_test

// A wake's boundaries with what lux reports meanwhile: an end applied
// after an attach to a running Run and before its acknowledgement, an event
// drain whose pages end at a repeated state, slow archived output that must
// not delay that drain, a sweep whose selected wake is replaced before it
// claims it, and a database that refuses a claimed wake's write.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// afterAttach is lux whose first successful AttachServer answer is held
// until then returns.
type afterAttach struct {
	lux.Client
	once sync.Once
	then func()
}

func (a *afterAttach) AttachServer(ctx context.Context, serverID, runID string) (lux.TenantServer, error) {
	sv, err := a.Client.AttachServer(ctx, serverID, runID)
	if err == nil {
		a.once.Do(a.then)
	}
	return sv, err
}

// A wake for a Run already running attaches its servers; the Run ends
// (crashed, lost its host, stopped or cancelled outside dude) and the
// follower applies the end before the attach's answer reaches the
// acknowledgement. The end stands: the preview is not shown serving and the
// wake stays wanted. The next sweep resumes a Run that ran and can run again
// (failed, lost, stopped), counting no failed start, and replaces one that
// never runs again (terminated, or cancelled from a lux before the rename);
// either then serves. A Run lux ended succeeded is covered by
// TestASucceededPreviewRunIsResumedWhereLuxCan. "refused" is a stopped Run
// whose resume lux refuses (409 not_resumable): replaced as well.
//
// The preview's library image publishes a new version, with a new digest
// and the other "Can run containers", while the Run is up. A resumed Run
// keeps the image and sandbox it started with; a replacement resolves the
// current version and asks lux for nested containers, and an engine store,
// only as that version says.
func TestACrashAppliedAfterAnAttachIsKept(t *testing.T) {
	const nextFinal = "registry.test/dude/custom@sha256:3333333333333333333333333333333333333333333333333333333333333333"
	for _, end := range []string{"failed", "lost", "stopped", "terminated", "cancelled", "refused"} {
		t.Run(end, func(t *testing.T) {
			w := newWorld(t)
			w.lux.CancelledState = end == "cancelled"
			// cancelled turns the flag on; every other row turns it off.
			before := end != "cancelled"
			w.wakeable()
			w.useLayer(imageLayer)
			w.canRunContainers(w.libraryImage("img_preview", "abs-preview", true), before)
			mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_preview' WHERE id = $1`, w.project)
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			web := w.serverID(runID, "web")
			w.open(web)
			w.running(runID, "web")
			r := w.luxRuns()[0]
			if first := submitted(t, r); nested(&first) != before {
				t.Fatalf("the first Run: nested %v, want %v", nested(&first), before)
			}
			w.publishNext("img_preview", nextFinal, !before)
			luxc := w.previews.Lux
			resumer, state := luxc, end
			if end == "refused" {
				resumer = &countingLux{Client: luxc, refuseResume: &lux.Error{Status: 409, Code: "not_resumable",
					Message: "run cannot be resumed"}}
				state = "stopped"
			}
			w.previews.Lux = &afterAttach{Client: resumer, then: func() {
				switch end {
				case "failed":
					w.lux.Crash(r.ID)
				case "lost":
					w.lux.Lose(r.ID)
				case "stopped", "refused":
					if err := luxc.Stop(context.Background(), r.ID); err != nil {
						t.Error(err)
					}
				case "terminated", "cancelled":
					if err := luxc.Cancel(context.Background(), r.ID); err != nil {
						t.Error(err)
					}
				}
				waitFor(t, "the follower to apply the end", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = $2`, runID, state) == 1
				})
			}}
			// A wake outstanding for a Run that is up: its acknowledgement was
			// lost (a restart), or lux asked while the Run was coming up.
			mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now() WHERE id = $1`, runID)
			if _, err := w.previews.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = $2
				AND wake_wanted_at IS NOT NULL AND wake_claimed_at IS NULL`, runID, state); n != 1 {
				t.Fatalf("the attach's answer was written over the end applied after it:\n%s", w.preview(runID))
			}
			w.sweepAgain(runID)
			image := func() (can, ref string) {
				return w.str(`SELECT coalesce(image->>'canRunContainers', 'false') FROM runs WHERE id = $1`, runID),
					w.str(`SELECT image->>'ref' FROM runs WHERE id = $1`, runID)
			}
			if end == "terminated" || end == "cancelled" || end == "refused" {
				w.untilPreview(runID, "a new Run running", func() bool {
					return len(w.luxRuns()) == 2 &&
						w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
				})
				w.open(web)
				if r.Resumed != 0 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND start_failures = 0`, runID) != 1 {
					t.Fatalf("the ended Run: resumed %d, calls %v; want replaced\n%s", r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
				}
				replacement := submitted(t, w.luxRuns()[1])
				if nested(&replacement) != !before {
					t.Errorf("the replacement: nested %v, want the current version's %v", nested(&replacement), !before)
				}
				keepsEngines(t, replacement, !before)
				if replacement.Image.Ref != nextFinal {
					t.Errorf("the replacement runs %s, want the current version's %s", replacement.Image.Ref, nextFinal)
				}
				if can, ref := image(); can != fmt.Sprint(!before) || ref != nextFinal {
					t.Errorf("runs.image canRunContainers %s ref %s, want %v %s", can, ref, !before, nextFinal)
				}
				if got := w.recordedContainers(runID); got != fmt.Sprint(!before) {
					t.Errorf("the replacement's runs.can_run_containers = %s, want %v", got, !before)
				}
				return
			}
			if r.Resumed != 1 {
				t.Fatalf("the ended Run was not resumed: resumed %d, calls %v\n%s", r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
			}
			w.open(web)
			if n := len(w.luxRuns()); n != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") ||
				w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND start_failures = 0`, runID) != 1 {
				t.Fatalf("%d lux runs, calls %v; want the Run that ran resumed\n%s", n, w.lux.CallsOf(r.ID), w.preview(runID))
			}
			// Resumed: lux's stored spec and the recorded image are the first's.
			kept := submitted(t, r)
			if nested(&kept) != before || kept.Image.Ref != finalRef {
				t.Errorf("the resumed Run: nested %v on %s, want %v on %s", nested(&kept), kept.Image.Ref, before, finalRef)
			}
			keepsEngines(t, kept, before)
			if can, ref := image(); can != fmt.Sprint(before) || ref != finalRef {
				t.Errorf("runs.image canRunContainers %s ref %s, want %v %s", can, ref, before, finalRef)
			}
			if got := w.recordedContainers(runID); got != fmt.Sprint(before) {
				t.Errorf("the resumed Run's runs.can_run_containers = %s, want %v", got, before)
			}
		})
	}
}

// A woken preview whose Run fails to start is replaced by a new
// generation; the image's published version stops being able to run
// containers in between. The new generation asks lux as that version says
// and records it, not the failed Run's value.
func TestAPreviewReplacedAfterAFailedStartRecordsItsOwnContainers(t *testing.T) {
	const nextFinal = "registry.test/dude/custom@sha256:3333333333333333333333333333333333333333333333333333333333333333"
	w := newWorld(t)
	w.wakeable()
	w.useLayer(imageLayer)
	w.canRunContainers(w.libraryImage("img_preview", "abs-preview", true), true)
	mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_preview' WHERE id = $1`, w.project)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	w.lux.FailStarts("dude.preview="+runID, 1)
	w.lux.RequestServer(w.serverID(runID, "web"), "/")
	w.untilPreview(runID, "the first start failed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID) == 1
	})
	// The replacement waits out workflow.Backoff(1), a second.
	if n := len(w.luxRuns()); n != 1 || w.recordedContainers(runID) != "true" {
		t.Fatalf("before the replacement: %d lux Runs, runs.can_run_containers %s; want 1, true", n, w.recordedContainers(runID))
	}
	failed := submitted(t, w.luxRuns()[0])
	if !nested(&failed) {
		t.Fatalf("the failed Run did not ask for nested containers")
	}
	w.publishNext("img_preview", nextFinal, false)
	w.untilPreview(runID, "a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running' AND lux_run_id = $2`,
				runID, w.luxRuns()[1].ID) == 1
	})
	if replacement := submitted(t, w.luxRuns()[1]); nested(&replacement) || replacement.Image.Ref != nextFinal {
		t.Errorf("the replacement: nested %v on %s, want false on %s", nested(&replacement), replacement.Image.Ref, nextFinal)
	}
	if got := w.recordedContainers(runID); got != "false" {
		t.Errorf("the replacement's runs.can_run_containers = %s, want false", got)
	}
}

// publishNext publishes version 2 of image, finished with imageLayer as
// final, its "Can run containers" can; version 1 is superseded.
func (w *world) publishNext(image, final string, can bool) {
	w.t.Helper()
	next := "imv_" + image + "_2"
	mustExec(w.t, w.owner, `INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state, user_ref, can_run_containers)
		VALUES ($1, $2, $3, 2, 'FROM debian', 'published', $4, $5)`, next, w.org, image, userRef, can)
	mustExec(w.t, w.owner, `UPDATE image_versions SET state = 'superseded' WHERE image_id = $1 AND id <> $2`, image, next)
	mustExec(w.t, w.owner, `UPDATE images SET published_version_id = $2 WHERE id = $1`, image, next)
	mustExec(w.t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, $2, $3, $4)`,
		w.org, next, imageLayer, final)
}

// slowReplay is lux whose output stream from the start of a Run's records
// takes replay before its first frame, as lux reading and decompressing
// every archived output blob of the Run does, while replaying is set.
type slowReplay struct {
	lux.Client
	replay    time.Duration
	replaying atomic.Bool
}

func (s *slowReplay) Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(lux.Frame) error) error {
	if s.replaying.Load() && cursor == "" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.replay):
		}
	}
	return s.Client.Output(ctx, runID, cursor, afterEvent, fn)
}

// A preview's Run ran and crashed, its crash applied; reading the Run's
// archived output takes longer than the drain's bound. The wake does not
// wait on that output: the next sweep resumes the Run once, and it serves.
func TestACrashedPreviewWakesWhileItsOutputIsSlowToReplay(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r := w.luxRuns()[0]
	w.lux.Crash(r.ID)
	waitFor(t, "the follower to apply the crash", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'failed'`, runID) == 1
	})
	slow := &slowReplay{Client: w.previews.Lux, replay: 400 * time.Millisecond}
	slow.replaying.Store(true)
	w.previews.Lux = slow
	w.previews.DrainFor = 200 * time.Millisecond
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 1 {
		t.Fatalf("the crashed Run was not resumed while its output was slow: resumed %d, calls %v\n%s",
			r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	w.open(web)
	if n := len(w.luxRuns()); n != 1 || r.Resumed != 1 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") ||
		w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND start_failures = 0`, runID) != 1 {
		t.Fatalf("%d lux runs, resumed %d, calls %v; want the Run resumed once\n%s", n, r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
}

// crashedThenFailedResumes leaves a preview's Run that ran and crashed,
// then was resumed n times outside this wake (an accepted resume whose
// acknowledgement was lost), each start failing before it ran. The
// preview's follower is held before the crash, so dude has applied none of
// it; a wake is then wanted.
func (w *world) crashedThenFailedResumes(n int) (runID, web string, r *fakelux.Run) {
	w.t.Helper()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID = w.declare()
	web = w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	r = w.luxRuns()[0]
	held, _ := w.holdOneStream("failed")
	w.t.Cleanup(func() { w.lux.PageEvents(nil) })
	w.lux.Crash(r.ID)
	wait(w.t, held, "the follower held before the crash")
	w.lux.FailStarts("dude.preview="+runID, n)
	for i := 1; i <= n; i++ {
		if _, err := w.previews.Lux.Resume(context.Background(), r.ID, lux.ResumeInput{
			Secrets: []lux.Secret{{Name: "GIT_TOKEN", Value: "fixture"}}}); err != nil {
			w.t.Fatal(err)
		}
		waitFor(w.t, "the resumed start failed", func() bool { return r.Resumed == i && w.lux.State(r.ID) == "failed" })
	}
	mustExec(w.t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
	return runID, web, r
}

// The event drain's first page ends after the crash, before the later
// resume's events: lux's state (failed) is the state applied, but the later
// start's failure is unread. It is counted before the wake decides: the Run
// is not resumed again, it is cancelled and replaced, and the new Run serves.
func TestARepeatedStateAtAPageEndIsNotTheRunsHistory(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web, r := w.crashedThenFailedResumes(1)
	cut := w.pageEventsAt("resuming", pageShort)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !isClosed(cut) {
		t.Fatal("the drain read no page ending before the later resume")
	}
	if r.Resumed != 1 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1`, runID) != 1 {
		t.Fatalf("decided before the later failed start was counted: resumed %d, calls %v\n%s",
			r.Resumed, w.lux.CallsOf(r.ID), w.preview(runID))
	}
	w.untilPreview(runID, "a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 1 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
	w.open(web)
}

// Every page of the drain carries one event, so no page finds lux with
// nothing past dude's cursor: the wake decides nothing (no resume, no
// cancel, no new Run) and is released, backed off. Once lux's pages are
// whole, the failed starts read are the count, and the Run is replaced.
func TestADrainThatNeverReachesAnEmptyPageDecidesNothing(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web, r := w.crashedThenFailedResumes(2)
	// Every page carries one event: the next page starts after it.
	var pages atomic.Int32
	w.lux.PageEvents(func(id string, _ int64, ids []int64) int {
		if id != r.ID {
			return len(ids)
		}
		pages.Add(1)
		return min(len(ids), 1)
	})
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Resumed != 2 || slices.Contains(w.lux.CallsOf(r.ID), "cancel") || len(w.luxRuns()) != 1 || pages.Load() < 2 {
		t.Fatalf("decided on a partial history: resumed %d, calls %v, %d lux runs, %d pages\n%s",
			r.Resumed, w.lux.CallsOf(r.ID), len(w.luxRuns()), pages.Load(), w.preview(runID))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL AND wake_claimed_at IS NULL
		AND next_attempt_at > now() AND lux_run_id = $2`, runID, r.ID); n != 1 {
		t.Fatalf("the wake was not released and backed off:\n%s", w.preview(runID))
	}
	w.lux.PageEvents(nil)
	w.sweepAgain(runID)
	w.untilPreview(runID, "a new Run running", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 2 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
	w.open(web)
}

// claimGate holds the first wake claim a connection sends until release is
// closed: the boundary between a sweep's selection and its claim. The claim
// is recognised by its statement, the only one that sets wake_claimed_at to
// now().
type claimGate struct {
	once             sync.Once
	reached, release chan struct{}
}

func (g *claimGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SET wake_claimed_at = now()") {
		g.once.Do(func() {
			close(g.reached)
			<-g.release
		})
	}
	return ctx
}

func (g *claimGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A sweep selects a wake; before it claims it, the follower applies the
// resumed start's failure and startFailed replaces that wake with a new one
// after its backoff. The stale sweep claims nothing, so the new wake is
// acted on once its backoff is due, not after wakeClaimFor.
func TestAStaleSweepDoesNotHoldTheNextWake(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	held, release := w.holdOneStream("failed")
	w.lux.FailStarts("dude.preview="+runID, 1)
	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "the follower held before the failed start", func() bool {
		return r.Resumed == 1 && isClosed(held)
	})
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
	var selected time.Time
	if err := w.owner.QueryRow(context.Background(), `SELECT wake_wanted_at FROM runs WHERE id = $1`, runID).Scan(&selected); err != nil {
		t.Fatal(err)
	}

	gate := &claimGate{reached: make(chan struct{}), release: make(chan struct{})}
	cfg := w.app.Pool.Config()
	cfg.ConnConfig.Tracer = gate
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	other := &servers.Previews{Service: &servers.Service{DB: &db.DB{Pool: pool}, Lux: w.previews.Lux, Log: quiet,
		PreviewDomain: previewDomain}, Forges: w.previews.Forges, DefaultImage: "default:img"}
	swept, returned := make(chan error, 1), make(chan struct{})
	// In this order on any exit: pool.Close waits on the connection the
	// gate holds, so the gate is released and the sweep has returned first.
	t.Cleanup(func() {
		if !isClosed(gate.release) {
			close(gate.release)
		}
		select {
		case <-returned:
		case <-time.After(30 * time.Second):
			t.Error("the stale sweep did not return")
		}
		other.Stop()
		pool.Close()
	})
	go func() {
		defer close(returned)
		_, err := other.Sweep(context.Background())
		swept <- err
	}()
	wait(t, gate.reached, "the stale sweep at its claim")

	close(release)
	waitFor(t, "startFailed to replace the wake", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 1 AND wake_wanted_at > $2`, runID, selected) == 1
	})
	close(gate.release)
	select {
	case err := <-swept:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the stale sweep did not return")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at > $2 AND wake_claimed_at IS NULL`, runID, selected); n != 1 {
		t.Fatalf("the stale sweep holds the new wake:\n%s", w.preview(runID))
	}
	w.untilPreview(runID, "a new Run running within the backoff", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	if r.Resumed != 1 || !slices.Contains(w.lux.CallsOf(r.ID), "cancel") {
		t.Fatalf("the failed Run: resumed %d, calls %v; want replaced", r.Resumed, w.lux.CallsOf(r.ID))
	}
	w.open(web)
}

// The database refuses a wake's write after it claimed the wake: the
// acknowledgement of a resumed Run, or the retirement of a cancelled one
// (a Postgres trigger raising on that UPDATE). The wake is released,
// still wanted and backed off, not left claimed; once the database takes
// the write, the next sweep wakes the preview, which serves on the
// resumed Run, or on one new Run.
func TestAWakeIsReleasedAfterADatabaseError(t *testing.T) {
	for _, stage := range []string{"acknowledgement", "retirement"} {
		t.Run(stage, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			runID, web := w.asleepPreview()
			r := w.luxRuns()[0]
			refused := "OLD.wake_wanted_at IS NOT NULL AND NEW.wake_wanted_at IS NULL"
			if stage == "retirement" {
				if err := w.previews.Lux.Cancel(context.Background(), r.ID); err != nil {
					t.Fatal(err)
				}
				refused = "OLD.lux_run_id IS NOT NULL AND NEW.lux_run_id IS NULL"
			}
			mustExec(t, w.owner, `CREATE FUNCTION refuse_wake_write() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN IF `+refused+` THEN RAISE EXCEPTION 'database unavailable'; END IF; RETURN NEW; END $$`)
			mustExec(t, w.owner, `CREATE TRIGGER refuse_wake_write BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION refuse_wake_write()`)
			mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = now(), next_attempt_at = NULL WHERE id = $1`, runID)
			if _, err := w.previews.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL AND wake_claimed_at IS NULL
				AND next_attempt_at > now()`, runID); n != 1 {
				t.Fatalf("the wake was left claimed or dropped after the database refused its write:\n%s", w.preview(runID))
			}
			mustExec(t, w.owner, `DROP TRIGGER refuse_wake_write ON runs`)
			w.sweepAgain(runID)
			w.open(web)
			want := 1
			if stage == "retirement" {
				want = 2
			}
			if n := len(w.luxRuns()); n != want {
				t.Fatalf("%d lux runs, want %d\n%s", n, want, w.preview(runID))
			}
		})
	}
}
