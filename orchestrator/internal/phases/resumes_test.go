package phases

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// placementLux is lux as far as a resume's timing goes: Get answers the
// Run with the placements set, or an error, and counts what was asked.
type placementLux struct {
	lux.Client
	mu   sync.Mutex
	run  lux.Run
	err  error
	gets int
}

func (f *placementLux) Get(context.Context, string) (lux.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	return f.run, f.err
}

func (f *placementLux) set(run lux.Run, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run, f.err = run, err
}

func (f *placementLux) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// resumeWorld is one Run, parked for a person on epoch 1 and answered,
// and a syncer whose lux answers Get from placementLux.
type resumeWorld struct {
	t     *testing.T
	ctx   context.Context
	s     *Syncer
	lux   *placementLux
	owner *pgx.Conn
	run   phaseRun
}

func newResumeWorld(t *testing.T) *resumeWorld {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state, dude_pause, lux_stop_reason)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'paused', 'lrun_1', 'stopped', 'person', 'pause')`,
		`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, answer, answered_at)
			VALUES ('q_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'Sorted?', 'answered', 'yes', now() - interval '2 seconds')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	fake := &placementLux{}
	run := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "implement",
		Status: statusPaused, Control: "none", DudePause: "person", LuxRunID: "lrun_1", LuxState: "stopped"}
	s := &Syncer{DB: app, Lux: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return &resumeWorld{t: t, ctx: ctx, s: s, lux: fake, owner: owner, run: run}
}

func at(base time.Time, ms int) *time.Time {
	t := base.Add(time.Duration(ms) * time.Millisecond)
	return &t
}

// stoppedOnHost1 is the Run as lux reports it once stopped on epoch 1.
func stoppedOnHost1(base time.Time) lux.Run {
	bytes := int64(7 << 20)
	return lux.Run{ID: "lrun_1", State: "stopped", Epoch: 1, Placements: []lux.Placement{{
		Epoch: 1, HostName: "host-a", State: "exited", WorkloadStartedAt: at(base, -60_000),
		StopRequestedAt: at(base, -5000), ExitedAt: at(base, -4000), SnapshotDoneAt: at(base, -3500),
		UploadedAt: at(base, -3000), SnapshotBytes: &bytes}}}
}

// runningAgain is the Run as lux reports it running on epoch 2, on host.
func runningAgain(base time.Time, host string) lux.Run {
	r := stoppedOnHost1(base)
	r.State, r.Epoch, r.Host = "running", 2, host
	r.Placements = append(r.Placements, lux.Placement{Epoch: 2, HostName: host, State: "running",
		AssignedAt: at(base, 100), ImageReadyAt: at(base, 200), VolumesRestoredAt: at(base, 400),
		ContainerStartedAt: at(base, 500), WorkloadStartedAt: at(base, 600)})
	return r
}

// resume records the resume as whilePaused does: its row before lux is
// asked, then the transaction that moves the Run back to running, with
// lux's answer resumed (its epoch 0: as foreseen).
func (w *resumeWorld) resume(before lux.Run) {
	w.t.Helper()
	w.resumeAnswered(before, lux.Run{})
}

func (w *resumeWorld) resumeAnswered(before, resumed lux.Run) {
	w.t.Helper()
	foreseen := w.s.resumeAsked(w.ctx, w.run, before)
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		w.s.resumeAccepted(w.ctx, tx, w.run, foreseen, resumed)
		_, err := tx.Exec(w.ctx, `UPDATE runs SET status = 'running', lux_state = 'resuming', dude_pause = NULL,
			lux_stop_reason = NULL, control = 'none', control_requested_at = NULL WHERE id = $1`, w.run.ID)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// The frames lux sends a resumed Run, in the order it sends them: the
// agent's records, then lux's running state.
func busy(epoch int) lux.Frame   { return record(epoch, "lux.activity", map[string]any{"activity": "busy"}) }
func spoke(epoch int) lux.Frame  { return record(epoch, "acp.agent_message_chunk", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "On it."}}) }
func running(epoch int) lux.Frame { return luxState(epoch, "running") }

func record(epoch int, typ string, data map[string]any) lux.Frame {
	raw, _ := json.Marshal(data)
	return lux.Frame{Kind: "record", Epoch: epoch, Event: &lux.RecordEvent{Type: typ, Data: raw}}
}

func luxState(epoch int, state string) lux.Frame {
	raw, _ := json.Marshal(map[string]any{"state": state})
	return lux.Frame{Kind: "lux", Epoch: epoch, EventType: "state", EventData: raw}
}

// follow applies frames as followOutput does — each in a batch of its own,
// with a translator loaded from the database — and runs each batch's
// follow-up before the next, so the order is the test's.
func (w *resumeWorld) follow(frames ...lux.Frame) {
	w.t.Helper()
	t := &translator{run: w.run}
	t.run.Status, t.run.LuxState = statusRunning, "resuming"
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error { return t.load(w.ctx, tx) }); err != nil {
		w.t.Fatal(err)
	}
	// As followOutput does: a failure is the syncer's to log.
	_ = w.s.timeResumes(w.ctx, w.run, 0)
	for _, f := range frames {
		if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
			if err := t.apply(w.ctx, tx, w.s, f); err != nil {
				return err
			}
			return t.save(w.ctx, tx, "", 0)
		}); err != nil {
			w.t.Fatalf("applying a frame failed the Run's batch: %v", err)
		}
		if t.resumes != nil {
			w.s.resumeFollowUp(w.run, t.resumes)
			t.resumes = nil
		}
	}
}

// row is the resume's columns, by name, as text: what a later write must
// leave alone.
func (w *resumeWorld) row(epoch int) map[string]any {
	w.t.Helper()
	rows, err := w.owner.Query(w.ctx, `SELECT * FROM run_resumes WHERE run_id = $1 AND epoch = $2`, w.run.ID, epoch)
	if err != nil {
		w.t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil || len(got) != 1 {
		w.t.Fatalf("resume %d: %d rows, %v", epoch, len(got), err)
	}
	return got[0]
}

func (w *resumeWorld) timed() []map[string]any {
	w.t.Helper()
	rows, err := w.owner.Query(w.ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`,
		w.run.ID, evResumeTimed)
	if err != nil {
		w.t.Fatal(err)
	}
	raws, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		w.t.Fatal(err)
	}
	var out []map[string]any
	for _, raw := range raws {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		out = append(out, p)
	}
	return out
}

// A resume's row is written once: replaying the stream from the start, a
// sync step recording the same resume again, and lux reporting other
// placement times later move no recorded timestamp, and the resume is
// timed once.
func TestAReplayOrARepeatedStepMovesNoRecordedTimestamp(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	w.follow(busy(2), spoke(2), running(2))
	first := w.row(2)
	for _, col := range []string{"woken_at", "requested_at", "running_at", "busy_at", "first_output_at"} {
		if first[col] == nil {
			t.Errorf("%s was not recorded", col)
		}
	}
	// Every field lux reported, as lux reported it: each is distinct in the
	// fixture, so one stored in another's column shows.
	reported := runningAgain(base, "host-b")
	stopped, resumed := reported.Placements[0], reported.Placements[1]
	for col, want := range map[string]any{
		"assigned_at": *resumed.AssignedAt, "image_ready_at": *resumed.ImageReadyAt,
		"volumes_restored_at": *resumed.VolumesRestoredAt, "container_started_at": *resumed.ContainerStartedAt,
		"workload_started_at": *resumed.WorkloadStartedAt, "host_name": "host-b",
		"stopped_host_name": "host-a", "stop_requested_at": *stopped.StopRequestedAt,
		"exited_at": *stopped.ExitedAt, "snapshot_done_at": *stopped.SnapshotDoneAt,
		"uploaded_at": *stopped.UploadedAt, "snapshot_bytes": *stopped.SnapshotBytes, "moved": true,
	} {
		got := first[col]
		if wt, ok := want.(time.Time); ok {
			if gt, ok := got.(time.Time); !ok || !gt.Equal(wt) {
				t.Errorf("%s = %v, lux reported %v", col, got, wt)
			}
		} else if got != want {
			t.Errorf("%s = %v, lux reported %v", col, got, want)
		}
	}
	if n := len(w.timed()); n != 1 {
		t.Fatalf("%d run.resume.timed after the first output, want 1", n)
	}

	// lux reports every placement time differently now, on another host.
	later := base.Add(time.Hour)
	w.lux.set(runningAgain(later, "host-c"), nil)
	time.Sleep(20 * time.Millisecond)
	// The sync step that resumed it, run again on a Run still paused (its
	// transaction's answer lost), records the same resume.
	if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET status = 'paused', control = 'resume', control_requested_at = now()
		WHERE id = $1`, w.run.ID); err != nil {
		t.Fatal(err)
	}
	w.resume(stoppedOnHost1(later))
	w.follow(busy(2), spoke(2), running(2), busy(2), spoke(2))
	if err := w.s.timeResumes(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	if again := w.row(2); !reflect.DeepEqual(first, again) {
		t.Errorf("a replay moved the row:\nbefore %v\nafter  %v", first, again)
	}
	if n := len(w.timed()); n != 1 {
		t.Errorf("%d run.resume.timed after a replay, want 1", n)
	}
}

// The event's numbers are the row's, in milliseconds, each phase between
// the two stamps the spec names; a phase whose end is unknown is left out,
// not zero.
func TestTheTimedEventCarriesEachPhaseInMilliseconds(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	if _, err := w.owner.Exec(w.ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at,
		assigned_at, image_ready_at, volumes_restored_at, container_started_at, workload_started_at, host_name, stopped_host_name,
		moved, running_at, busy_at, first_output_at)
		VALUES ($1, $2, 2, 'answer', $3, $4, $5, $6, $7, $8, $9, 'host-b', 'host-a', true, $10, $11, $12)`,
		w.run.ID, w.run.Org, base, at(base, 120), at(base, 420), at(base, 1420), at(base, 2620), at(base, 3000), at(base, 3420),
		at(base, 5520), at(base, 5920), at(base, 7520)); err != nil {
		t.Fatal(err)
	}
	// A second resume lux said less about: no image, no restore.
	if _, err := w.owner.Exec(w.ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at,
		assigned_at, workload_started_at, running_at, busy_at, first_output_at)
		VALUES ($1, $2, 3, 'person', $3, $4, $5, $6, $7, NULL, $8)`,
		w.run.ID, w.run.Org, base, at(base, 50), at(base, 150), at(base, 950), at(base, 1950), at(base, 2950)); err != nil {
		t.Fatal(err)
	}
	if err := w.s.timeResumes(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	got := w.timed()
	if len(got) != 2 {
		t.Fatalf("%d events, want one per resume", len(got))
	}
	byEpoch := map[float64]map[string]any{}
	for _, p := range got {
		byEpoch[p["epoch"].(float64)] = p
	}
	want := map[string]any{"epoch": 2.0, "cause": "answer", "moved": true, "hostName": "host-b", "totalMs": 7520.0, "untilBusyMs": 5920.0,
		"phases": map[string]any{"react": 120.0, "schedule": 300.0, "image": 1000.0, "restore": 1200.0, "start": 800.0,
			"reload": 2100.0, "take": 400.0, "firstOutput": 1600.0}}
	if !reflect.DeepEqual(byEpoch[2], want) {
		t.Errorf("epoch 2:\n got %v\nwant %v", byEpoch[2], want)
	}
	// No image or restore stamps, no busy: those phases, the start that
	// begins at an unknown restore, and the time until busy are unknown.
	want = map[string]any{"epoch": 3.0, "cause": "person", "moved": nil, "hostName": nil, "totalMs": 2950.0,
		"phases": map[string]any{"react": 50.0, "schedule": 100.0, "reload": 1000.0}}
	if !reflect.DeepEqual(byEpoch[3], want) {
		t.Errorf("epoch 3:\n got %v\nwant %v", byEpoch[3], want)
	}
}

// lux not saying something leaves it NULL, lux not answering at all
// leaves the placements NULL, and neither holds up the Run: its stream is
// recorded, and the resume is still timed by what is known. lux is asked
// at most twice: when the Run runs again, and at the first output while
// something is missing.
func TestWhatLuxDoesNotSayStaysUnknownAndTheRunGoesOn(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	partial := runningAgain(base, "host-a")
	partial.Placements[1].ImageReadyAt, partial.Placements[1].VolumesRestoredAt = nil, nil
	w.lux.set(partial, nil)
	w.follow(running(2), busy(2), spoke(2), busy(2), spoke(2))
	row := w.row(2)
	if row["image_ready_at"] != nil || row["volumes_restored_at"] != nil {
		t.Errorf("unreported stamps recorded: %v %v", row["image_ready_at"], row["volumes_restored_at"])
	}
	if row["assigned_at"] == nil || row["first_output_at"] == nil {
		t.Errorf("what lux did report was not recorded: %v", row)
	}
	if n := w.lux.asked(); n != 2 {
		t.Errorf("lux was asked %d times, want 2: when running, and once more at the first output", n)
	}
	got := w.timed()
	if len(got) != 1 {
		t.Fatalf("%d events", len(got))
	}
	phases := got[0]["phases"].(map[string]any)
	for _, unknown := range []string{"image", "restore", "start"} {
		if _, ok := phases[unknown]; ok {
			t.Errorf("phase %s reported though lux never said", unknown)
		}
	}

	// lux unreachable: nothing of the placement is known, the Run's own
	// state is still recorded.
	w2 := newResumeWorld(t)
	w2.resume(stoppedOnHost1(base))
	w2.lux.set(lux.Run{}, errors.New("lux is down"))
	w2.follow(busy(2), spoke(2), running(2))
	row = w2.row(2)
	if row["assigned_at"] != nil || row["first_output_at"] == nil || row["busy_at"] == nil {
		t.Errorf("with lux down: %v", row)
	}
	var luxState string
	var busyAt *time.Time
	_ = w2.owner.QueryRow(w2.ctx, `SELECT lux_state, agent_busy_at FROM runs WHERE id = $1`, w2.run.ID).Scan(&luxState, &busyAt)
	if luxState != "running" || busyAt == nil {
		t.Errorf("the Run's stream was held up: lux_state %s, busy %v", luxState, busyAt)
	}
}

// A timing write that fails is undone on its own: the Run's batch commits
// everything else.
func TestATimingWriteThatFailsDoesNotFailTheRun(t *testing.T) {
	w := newResumeWorld(t)
	w.resume(stoppedOnHost1(time.Now()))
	w.lux.set(runningAgain(time.Now(), "host-a"), nil)
	// Every write to the table now fails.
	if _, err := w.owner.Exec(w.ctx, `REVOKE UPDATE ON run_resumes FROM dude_app`); err != nil {
		t.Fatal(err)
	}
	w.follow(running(2), busy(2), spoke(2))
	var luxState string
	var busyAt *time.Time
	_ = w.owner.QueryRow(w.ctx, `SELECT lux_state, agent_busy_at FROM runs WHERE id = $1`, w.run.ID).Scan(&luxState, &busyAt)
	if luxState != "running" || busyAt == nil {
		t.Errorf("a failed timing write failed the Run's batch: lux_state %s, busy %v", luxState, busyAt)
	}
}

// Another organization sees none of a Run's resumes, and cannot write one
// in its name.
func TestAnotherOrganizationCannotSeeOrWriteAResume(t *testing.T) {
	w := newResumeWorld(t)
	w.resume(stoppedOnHost1(time.Now()))
	other := dbtest.Org(t, w.owner)
	var seen int
	if err := w.s.DB.InOrg(w.ctx, other, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM run_resumes`).Scan(&seen)
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Errorf("another organization sees %d resumes", seen)
	}
	err := w.s.DB.InOrg(w.ctx, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(w.ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause) VALUES ($1, $2, 9, 'person')`,
			w.run.ID, w.run.Org)
		return err
	})
	if err == nil {
		t.Errorf("another organization wrote a resume in this one's name")
	}
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM run_resumes`).Scan(&seen)
	}); err != nil || seen != 1 {
		t.Errorf("its own organization sees %d resumes (%v)", seen, err)
	}
}

// A resume belongs to its Run's organization: another organization cannot
// write one about this one's Run under its own name, and so cannot take
// the Run's (run_id, epoch) from it.
func TestAResumeCannotNameAnotherOrganizationsRun(t *testing.T) {
	w := newResumeWorld(t)
	other := dbtest.Org(t, w.owner)
	err := w.s.DB.InOrg(w.ctx, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(w.ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause) VALUES ($1, $2, 2, 'person')`,
			w.run.ID, other)
		return err
	})
	if err == nil {
		t.Fatalf("another organization wrote a resume of this one's Run")
	}
	w.resume(stoppedOnHost1(time.Now()))
	if n := len(w.timed()); n != 0 {
		t.Fatalf("%d events", n)
	}
	if row := w.row(2); row["organization_id"] != w.run.Org {
		t.Errorf("the Run's resume is under %v", row["organization_id"])
	}
}
