package orchestrator_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Resizing a parked Run on resume (lux#51), through the fake lux's HTTP
// API: what dude sends, what lux's spec then holds, and what runs.machine
// records of lux's answer.

const gibB = int64(1) << 30

// Tiny is the size Jervasion's previews were moved to: half a CPU, 1 GiB
// of memory, 5 GiB of disk, in the default pool.
func (w *world) tiny() {
	mustExec(w.t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib)
		VALUES ('msz_tiny', $1, 'Tiny', 0.5, 1024, 5)`, w.org)
}

// resumeAgent asks for a paused agent Run to be resumed, as a person does
// (POST /internal/runs/{id}/resume), and sweeps until lux has the resume.
func (w *world) resumeAgent(runID string, r *fakelux.Run) {
	w.t.Helper()
	before := w.luxCalls(r.ID, "resume")
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		w.t.Fatalf("resume: %d %v", status, out)
	}
	w.until("lux to have the resume", func() bool { return w.luxCalls(r.ID, "resume") > before })
}

// resumeResources is the resources each resume lux accepted carried; nil
// for one that carried none.
func (w *world) resumeResources(luxRunID string) []*lux.Resources {
	var out []*lux.Resources
	for _, raw := range w.lux.ResumeResourcesOf(luxRunID) {
		var res *lux.Resources
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &res); err != nil {
				w.t.Fatal(err)
			}
		}
		out = append(out, res)
	}
	return out
}

// luxResources is what lux's stored spec of the Run holds now.
func (w *world) luxResources(luxRunID string) lux.Resources {
	var spec lux.Spec
	if err := json.Unmarshal(w.lux.SpecOf(luxRunID), &spec); err != nil || spec.Resources == nil {
		w.t.Fatalf("lux's spec: %v", err)
	}
	return *spec.Resources
}

func (w *world) machineOf(runID string) map[string]any {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT machine FROM runs WHERE id = $1`, runID).Scan(&raw); err != nil {
		w.t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// previewLux is the lux Run of a preview.
func (w *world) previewLux(runID string) string {
	return w.str(`SELECT lux_run_id FROM runs WHERE id = $1`, runID)
}

// wakeAgain opens the asleep preview's server and waits for it to serve.
func (w *world) wakeAgain(runID, web string) {
	w.t.Helper()
	w.open(web)
	w.until("woken", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
}

// The JERV-2 case: a preview parked on Standard, its project's previews
// moved to Tiny, wakes on Tiny. The wake's resume sends Tiny's resources
// (the disk too: lux grows a disk, and shrinks it only when the saved
// state fits, as it does here), lux's spec has them, and runs.machine
// records what lux's answer says it applied.
func TestAWokenPreviewResumesOnTheSizeItsProjectNamesNow(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	if m := w.machineOf(runID); m["name"] != "Standard" || m["cpus"] != 2.0 {
		t.Fatalf("the preview started on %v, want Standard", m)
	}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tiny {
		t.Errorf("the wake sent %v, want Tiny's resources", got)
	}
	if got := w.luxResources(luxRun); got != tiny {
		t.Errorf("lux's spec = %+v, want Tiny's", got)
	}
	m := w.machineOf(runID)
	if m["name"] != "Tiny" || m["sizeId"] != "msz_tiny" || m["from"] != "project" || m["cpus"] != 0.5 || m["memoryMiB"] != 1024.0 ||
		m["diskGiB"] != 5.0 || m["note"] != nil || m["diskKept"] != nil {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}

	// Asleep and woken again on the same size: nothing is sent.
	w.lux.Idle(web)
	w.until("asleep again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 2 || got[1] != nil {
		t.Errorf("an unchanged size sent %v on the second wake, want nothing", got)
	}

	// Tiny's disk alone grows: that is a change, and lux grows the disk.
	mustExec(t, w.owner, `UPDATE machine_sizes SET disk_gib = 10 WHERE id = 'msz_tiny'`)
	w.lux.Idle(web)
	w.until("asleep a third time", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.wakeAgain(runID, web)
	tiny.Disk = 10 * gibB
	if got := w.resumeResources(luxRun); len(got) != 3 || got[2] == nil || *got[2] != tiny {
		t.Errorf("a disk-only change sent %v on the third wake, want Tiny with 10 GiB", got)
	}
	if m := w.machineOf(runID); m["diskGiB"] != 10.0 || m["cpus"] != 0.5 {
		t.Errorf("runs.machine = %v, want Tiny with 10 GiB", m)
	}
}

// A paused agent Run resumed by a person after its role's size changed
// resumes on the new size; resumed again with the size unchanged, it sends
// no resources.
func TestAnAgentRunResumesOnItsRolesSizeAndSendsNothingWhenUnchanged(t *testing.T) {
	w := newWorld(t)
	w.tiny()
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	r := w.lux.Runs()[0]

	// Unchanged: Standard, as it started.
	w.resumeAgent(runID, r)
	if got := w.resumeResources(r.ID); len(got) != 1 || got[0] != nil {
		t.Errorf("an unchanged size sent %v, want nothing", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 0 {
		t.Errorf("%d run.resized for an unchanged size", n)
	}

	w.until("running again", func() bool { return w.lux.State(r.ID) == "running" })
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_tiny"') WHERE id = $1`, w.project)
	w.pauseAgain(runID, r)
	w.resumeAgent(runID, r)
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(r.ID); len(got) != 2 || got[1] == nil || *got[1] != tiny {
		t.Errorf("the resume sent %v, want Tiny's", got)
	}
	if got := w.luxResources(r.ID); got != tiny {
		t.Errorf("lux's spec = %+v, want Tiny's", got)
	}
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["cpus"] != 0.5 || m["diskGiB"] != 5.0 || m["from"] != "project" {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}
}

// pauseAgain pauses a running agent Run as a person would and waits for
// lux to stop it.
func (w *world) pauseAgain(runID string, r *fakelux.Run) {
	w.t.Helper()
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		w.t.Fatalf("pause: %d %v", status, out)
	}
	w.until("lux to stop it", func() bool {
		return w.lux.State(r.ID) == "stopped" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
}

// A smaller disk lux will not apply (its saved state needs more): the Run
// resumes with Tiny's CPUs and memory and keeps its disk, and runs.machine
// says the disk lux kept, not the one asked for, with lux's reason. The
// next wake asks again (the state may fit by then); lux keeping the disk
// again changes nothing, so nothing is recorded: one run.resized, and the
// memory limit of the placement on Tiny stays.
func TestADiskShrinkLuxRefusesRecordsTheDiskItKept(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	w.lux.DiskUse = 8 * gibB // needs 10 GiB: more than Tiny's 5
	w.lux.MemoryShare = 0.95
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	if got := w.luxResources(luxRun); got != (lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 20 * gibB}) {
		t.Errorf("lux's spec = %+v, want Tiny's CPUs and memory on Standard's disk", got)
	}
	m := w.machineOf(runID)
	kept, _ := m["diskKept"].(map[string]any)
	if m["name"] != "Tiny" || m["cpus"] != 0.5 || m["memoryMiB"] != 1024.0 || m["diskGiB"] != 20.0 || kept == nil ||
		kept["requestedGiB"] != 5.0 || kept["reason"] != "its saved state used up to 8.0 GiB; a smaller disk must be at least 10.0 GiB (that plus max(25%, 1 GiB))" {
		t.Errorf("runs.machine = %v, want Tiny with the 20 GiB disk lux kept and why", m)
	}

	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 2 || got[1] == nil || *got[1] != tiny {
		t.Errorf("the second wake sent %v, want Tiny's again", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Errorf("%d run.resized after lux kept the disk twice, want 1", n)
	}
	// 1 GiB, of which the container gets 95%, to a MiB: the placement on
	// Tiny's (epoch 2), recorded as the second wake read it.
	if m := w.machineOf(runID); m["sinceEpoch"] != 2.0 || m["memoryLimit"] != float64(int64(972)<<20) {
		t.Errorf("runs.machine = %v, want since epoch 2 with Tiny's memory limit", m)
	}
}

// sleepAgain lets the woken preview go idle and waits for it to be parked.
func (w *world) sleepAgain(runID, web string) {
	w.t.Helper()
	w.lux.Idle(web)
	w.until("asleep again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
}

// A size in another pool cannot be had by a resume: lux binds a Run to the
// pool it was submitted to. The preview wakes on the size it has, nothing
// is sent, and its record says why, naming both; the memory limit lux gave
// it, still its size's, is kept.
func TestAWakeIntoASizeInAnotherPoolKeepsTheSizeAndSaysWhy(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.lux.MemoryShare = 0.95
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_big', $1, 'Big', 8, 16384, 40, $2)`, w.org, bigPool)
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_big"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] != nil {
		t.Errorf("the wake sent %v, want nothing", got)
	}
	if got := w.luxResources(luxRun); got.CPUs != 2 {
		t.Errorf("lux's spec = %+v, want Standard's", got)
	}
	m := w.machineOf(runID)
	want := "Its settings now name Big, in another pool: a stopped Run cannot change pools, so it keeps Standard. A new Run gets Big."
	// Standard's 8 GiB, of which the container gets 95%, to a MiB.
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["note"] != want || m["memoryLimit"] != float64(int64(7782)<<20) {
		t.Errorf("runs.machine = %v, want Standard kept with the reason and its memory limit", m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized' AND payload->'machine'->>'note' = $2`, runID, want); n != 1 {
		t.Errorf("%d run.resized with the reason, want 1", n)
	}
}

// defaultPool is the fake lux's default pool's id (fakelux.DefaultPools).
const defaultPool = "pool_d4f7k2m9q1x8"

// A size naming lux's default pool by id is in the same pool as one naming
// none, either way round: a preview on Standard (no pool) resizes to Lean,
// which names the default by id (the Run's pool, as lux reports it); one
// on Lean resizes to Tiny (no pool), lux's pool list saying the Run's
// pool is its default.
func TestASizeNamingTheDefaultPoolByIdIsTheSamePoolAsNone(t *testing.T) {
	lean := lux.Resources{CPUs: 1, Memory: 2 << 30, Disk: 10 * gibB}
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	for name, tc := range map[string]struct {
		from, to string
		want     lux.Resources
		name     string
	}{
		"none to the default by id": {"", "msz_lean", lean, "Lean"},
		"the default by id to none": {"msz_lean", "msz_tiny", tiny, "Tiny"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.tiny()
			mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
				VALUES ('msz_lean', $1, 'Lean', 1, 2048, 10, $2)`, w.org, defaultPool)
			if tc.from != "" {
				mustExec(t, w.owner, `UPDATE projects SET preview_settings = jsonb_build_object('machineSize', $2::text) WHERE id = $1`, w.project, tc.from)
			}
			runID, web := w.asleepPreview()
			luxRun := w.previewLux(runID)

			mustExec(t, w.owner, `UPDATE projects SET preview_settings = jsonb_build_object('machineSize', $2::text) WHERE id = $1`, w.project, tc.to)
			w.wakeAgain(runID, web)
			if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tc.want {
				t.Errorf("the wake sent %v, want %s's", got, tc.name)
			}
			if m := w.machineOf(runID); m["name"] != tc.name || m["cpus"] != tc.want.CPUs || m["note"] != nil {
				t.Errorf("runs.machine = %v, want %s", m, tc.name)
			}
		})
	}
}

// poolsFailing is a lux whose pool list fails while fail is set; calls
// counts the lists asked for.
type poolsFailing struct {
	lux.Client
	fail  atomic.Bool
	calls atomic.Int32
}

func (c *poolsFailing) Pools(ctx context.Context) ([]lux.Pool, error) {
	c.calls.Add(1)
	if c.fail.Load() {
		return nil, &lux.Error{Status: 503, Code: "unavailable", Message: "down"}
	}
	return c.Client.Pools(ctx)
}

// A preview in a named pool whose settings move to a size naming none
// (lux's default) needs lux's pool list to tell. While it fails, the wake
// goes on on the Run's size and nothing is recorded: not knowing is not
// "another pool". Once lux answers, the record says the size is in
// another pool; later wakes to the same size do not ask lux again, and
// record nothing new.
func TestAWakeWhenLuxsPoolsCannotBeReadSendsAndRecordsNothing(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_big', $1, 'Big', 8, 16384, 40, $2)`, w.org, bigPool)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_big"}' WHERE id = $1`, w.project)
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	pools := &poolsFailing{Client: w.previews.Lux}
	pools.fail.Store(true)
	w.previews.Lux = pools
	before := w.machineOf(runID)

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] != nil {
		t.Errorf("the wake sent %v, want nothing", got)
	}
	if m := w.machineOf(runID); m["name"] != "Big" || m["note"] != nil || len(m) != len(before) {
		t.Errorf("runs.machine = %v, want it as it was: %v", m, before)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 0 {
		t.Errorf("%d run.resized while lux's pools could not be read", n)
	}
	if pools.calls.Load() == 0 {
		t.Fatal("lux's pools were never asked for")
	}

	pools.fail.Store(false)
	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	want := "Its settings now name Tiny, in another pool: a stopped Run cannot change pools, so it keeps Big. A new Run gets Tiny."
	if m := w.machineOf(runID); m["name"] != "Big" || m["note"] != want {
		t.Errorf("runs.machine = %v, want Big with note %q", m, want)
	}
	asked := pools.calls.Load()

	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 3 || got[1] != nil || got[2] != nil {
		t.Errorf("the wakes sent %v, want nothing", got)
	}
	if n := pools.calls.Load(); n != asked {
		t.Errorf("lux's pools were asked for %d more times for a size already found elsewhere", n-asked)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Errorf("%d run.resized, want 1", n)
	}
}

// diskOnly is a lux that took a resume's disk and nothing else of its
// resources, saying nothing of it in its answer (with NoResize, a lux from
// before lux#51).
type diskOnly struct{ lux.Client }

func (c diskOnly) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	if in.Resources != nil {
		in.Resources = &lux.Resources{Disk: in.Resources.Disk}
	}
	return c.Client.Resume(ctx, id, in)
}

// A lux that applied only part of the size leaves the Run on neither size:
// runs.machine has the numbers lux's spec has, under the size it had, with
// a note naming the size its settings name. The next wake asks again and
// lux leaves those numbers as they are: the note stays, and nothing new is
// recorded.
func TestAPartlyAppliedSizeKeepsItsNameWithLuxsNumbers(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	w.lux.NoResize = true
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	sizeID := w.machineOf(runID)["sizeId"]
	w.previews.Lux = diskOnly{w.previews.Lux}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	partial := "Its settings name Tiny; lux applied only part of it, as shown."
	m := w.machineOf(runID)
	if m["name"] != "Standard" || m["sizeId"] != sizeID || m["from"] != "default" || m["cpus"] != 2.0 || m["memoryMiB"] != 8192.0 ||
		m["diskGiB"] != 5.0 || m["note"] != partial || m["sinceEpoch"] != 2.0 {
		t.Errorf("runs.machine = %v, want Standard's name with lux's 2 CPUs, 8 GiB and 5 GiB disk", m)
	}

	w.sleepAgain(runID, web)
	w.wakeAgain(runID, web)
	if got := w.resumeResources(luxRun); len(got) != 2 || got[1] == nil || got[1].Disk != 5*gibB {
		t.Fatalf("the second wake sent %v, want Tiny's disk again", got)
	}
	if m := w.machineOf(runID); m["note"] != partial || m["sinceEpoch"] != 2.0 || m["diskGiB"] != 5.0 {
		t.Errorf("after the second wake runs.machine = %v, want the partial note kept", m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, runID); n != 1 {
		t.Errorf("%d run.resized after the same partial apply twice, want 1", n)
	}
}

// answersNoSpec is a lux whose resume answer says nothing of the Run's
// resources.
type answersNoSpec struct{ lux.Client }

func (c answersNoSpec) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	res, err := c.Client.Resume(ctx, id, in)
	res.Resize, res.Spec.Resources = nil, nil
	return res, err
}

// An answer that says nothing of the Run's resources is not taken as the
// request: lux's Run is read again, and what it has is recorded.
func TestAResumeAnswerWithoutResourcesIsReadAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	w.previews.Lux = answersNoSpec{w.previews.Lux}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["cpus"] != 0.5 || m["diskGiB"] != 5.0 {
		t.Errorf("runs.machine = %v, want Tiny as lux's Run says", m)
	}
}

// An eager (not wakeable) preview parked by the previews loop resumes on
// the size its project names now when a person starts one of its servers.
func TestAParkedEagerPreviewResumesOnTheSizeItsProjectNamesNow(t *testing.T) {
	w := newWorld(t)
	w.tiny()
	runID := w.parkedPreview()
	luxRun := w.previewLux(runID)
	if m := w.machineOf(runID); m["name"] != "Standard" {
		t.Fatalf("the preview started on %v, want Standard", m)
	}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = preview_settings || '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	mustExec(t, w.owner, `UPDATE runs SET pending_starts = '{docs}' WHERE id = $1`, runID)
	w.until("the preview to be resumed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tiny {
		t.Errorf("the resume sent %v, want Tiny's resources", got)
	}
	if got := w.luxResources(luxRun); got != tiny {
		t.Errorf("lux's spec = %+v, want Tiny's", got)
	}
	if m := w.machineOf(runID); m["name"] != "Tiny" || m["sizeId"] != "msz_tiny" || m["cpus"] != 0.5 || m["diskGiB"] != 5.0 {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}
}

// resumingAlready has the first resume of each Run reach lux twice: once
// without resources, as a resume whose answer was lost, then as asked. lux
// is then resuming the Run with no resources, and refuses others (409).
// asked is what dude sent, in order, and errs what lux answered each.
type resumingAlready struct {
	lux.Client
	mu    sync.Mutex
	seen  map[string]bool
	asked []*lux.Resources
	errs  []error
}

func (c *resumingAlready) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	c.mu.Lock()
	first := !c.seen[id]
	c.seen[id] = true
	c.mu.Unlock()
	if first {
		earlier := in
		earlier.Resources = nil
		if _, err := c.Client.Resume(ctx, id, earlier); err != nil {
			return lux.Run{}, err
		}
	}
	res, err := c.Client.Resume(ctx, id, in)
	c.mu.Lock()
	c.asked, c.errs = append(c.asked, in.Resources), append(c.errs, err)
	c.mu.Unlock()
	return res, err
}

// A wake whose resume finds the Run already resuming with no resources: lux
// refuses the new size (409 not_resumable), and the wake goes on as the
// same resume without them, as lux's docs say a retry is. The wake does not
// fail; runs.machine records the size lux's spec has, and says the new one
// was not applied, with lux's words.
func TestA409WhileResumingDoesNotFailTheWake(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	luxRun := w.previewLux(runID)
	// The resume lux takes first stays resuming for the whole exchange:
	// its placement waits StartAfter/5 before it is assigned.
	w.lux.StartAfter = 2 * time.Second
	already := &resumingAlready{Client: w.previews.Lux, seen: map[string]bool{}}
	w.previews.Lux = already

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	already.mu.Lock()
	asked, errs := already.asked, already.errs
	already.mu.Unlock()
	if len(asked) != 2 || len(errs) != 2 {
		t.Fatalf("dude asked %v, lux answered %v; want two resumes", asked, errs)
	}
	if le, ok := lux.AsError(errs[0]); asked[0] == nil || *asked[0] != tiny || !ok || le.Status != 409 || asked[1] != nil || errs[1] != nil {
		t.Errorf("dude asked %v, lux answered %v; want Tiny refused 409, then the same resume without resources taken", asked, errs)
	}
	if got := w.resumeResources(luxRun); len(got) != 1 || got[0] != nil {
		t.Errorf("lux started resumes with %v, want one, without resources", got)
	}
	if got := w.luxResources(luxRun); got.CPUs != 2 {
		t.Errorf("lux's spec = %+v, want Standard's", got)
	}
	m := w.machineOf(runID)
	want := "Its settings now name Tiny, but this resume kept Standard (lux said: run is resuming already with other resources: they can only change before it is resumed). The next resume tries again."
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["note"] != want {
		t.Errorf("runs.machine = %v\nwant Standard with note %q", m, want)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND error IS NULL AND status = 'running'`, runID); n != 1 {
		t.Errorf("the wake failed:\n%s", w.describeRuns())
	}
}

// A lux from before lux#51 refuses cpus and memory on a resume (an unknown
// field, 400): the Run resumes without them, on the size lux's spec still
// has, and that is what is recorded, with the reason.
func TestALuxThatCannotResizeResumesTheRunAsItIs(t *testing.T) {
	w := newWorld(t)
	w.tiny()
	w.lux.NoResize = true
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	r := w.lux.Runs()[0]

	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_tiny"') WHERE id = $1`, w.project)
	w.resumeAgent(runID, r)

	if got := w.luxResources(r.ID); got.CPUs != 2 || got.Disk != 20*gibB {
		t.Errorf("lux's spec = %+v, want Standard's", got)
	}
	m := w.machineOf(runID)
	note, _ := m["note"].(string)
	if m["name"] != "Standard" || m["cpus"] != 2.0 || note != "Its settings now name Tiny, but this resume kept Standard: this lux cannot change a stopped Run's CPUs or memory." {
		t.Errorf("runs.machine = %v", m)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID); n != 1 {
		t.Errorf("the Run did not resume:\n%s", w.describeRuns())
	}
}

// ignoresResources is a lux that takes a resume and drops its resources
// unread: its answer carries no resize, and its spec is as it was.
type ignoresResources struct{ lux.Client }

func (c ignoresResources) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	in.Resources = nil
	return c.Client.Resume(ctx, id, in)
}

// What is recorded is what lux's Run says after the resume, never what was
// asked: a lux that ignored the resources leaves the Run on Standard, and
// runs.machine says Standard.
func TestALuxThatIgnoresResourcesIsRecordedAsItsSpecSays(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	runID, web := w.asleepPreview()
	w.previews.Lux = ignoresResources{w.previews.Lux}

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	m := w.machineOf(runID)
	want := "Its settings now name Tiny, but this resume kept Standard (lux did not apply it)."
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["diskGiB"] != 20.0 || m["note"] != want {
		t.Errorf("runs.machine = %v, want Standard as lux's spec says", m)
	}
}

// A session's agent parked by the sweep and resumed by a chat message
// resumes on the size the organisation's brainstorm role names now. On a
// lux without sync modes, the session resume's retry without its sync
// carries that size too.
func TestAParkedSessionResumesOnTheSizeItsRoleNamesNow(t *testing.T) {
	for name, noModes := range map[string]bool{"with sync modes": false, "without sync modes": true} {
		t.Run(name, func(t *testing.T) {
			s := newSessionWorld(t)
			s.tiny()
			s.syncer.ConductorWarm = time.Hour
			brainstormOn := func(size string) {
				mustExec(t, s.owner, `UPDATE organizations SET default_agent_models = jsonb_set(default_agent_models, '{brainstorm}',
					COALESCE(default_agent_models->'brainstorm', '{}'::jsonb) || jsonb_build_object('machineSize', $2::text)) WHERE id = $1`, s.org, size)
			}
			// Migration 086 seeds brainstorm on Small; it starts on Standard here.
			brainstormOn(s.str(`SELECT id FROM machine_sizes WHERE organization_id = $1 AND name = 'Standard'`, s.org))
			id := s.session()
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
			run := s.started(id)
			s.until("the brainstorm running", func() bool {
				return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, run) == 1
			})
			if m := s.machineOf(run); m["name"] != "Standard" {
				t.Fatalf("the brainstorm started on %v, want Standard", m)
			}
			mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours',
				agent_active_at = now() - interval '3 hours', files_changed_at = now() - interval '3 hours',
				stall_reported_at = now() - interval '3 hours' WHERE id = $1`, run)
			s.until("the session parked", func() bool {
				return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'session'
					AND lux_state = 'stopped'`, run) == 1
			})

			brainstormOn("msz_tiny")
			s.lux.NoSyncModes = noModes
			luxRun := s.previewLux(run)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
			s.until("lux to have the resume", func() bool { return s.luxCalls(luxRun, "resume") > 0 })
			s.until("the resize recorded", func() bool {
				return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, run) > 0
			})

			tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
			if got := s.resumeResources(luxRun); len(got) != 1 || got[0] == nil || *got[0] != tiny {
				t.Errorf("the resumes lux took carried %v, want one with Tiny's", got)
			}
			if m := s.machineOf(run); m["name"] != "Tiny" || m["sizeId"] != "msz_tiny" || m["cpus"] != 0.5 || m["sinceEpoch"] != 2.0 {
				t.Errorf("runs.machine = %v, want Tiny since epoch 2", m)
			}
			if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized'`, run); n != 1 {
				t.Errorf("%d run.resized, want 1", n)
			}
		})
	}
}

// A conductor resumed on a lux without sync modes is resumed again without
// its sync (Syncer.resume's fallback): that retry still carries the size
// its role names now.
func TestAResumeRetriedWithoutSyncModesStillResizes(t *testing.T) {
	e := newEditing(t)
	e.tiny()
	e.wokenWith(e.task, "after implement")
	e.syncer.ConductorWarm = 1
	e.until("the conductor parked", func() bool { _, status, _ := e.conductor(e.task); return status == "paused" })
	e.syncer.ConductorWarm = 1 << 40
	mustExec(t, e.owner, `UPDATE projects SET agent_models = agent_models ||
		jsonb_build_object('conductor', COALESCE(agent_models->'conductor', '{}') || '{"machineSize":"msz_tiny"}') WHERE id = $1`, e.project)
	e.lux.NoSyncModes = true
	if status, _ := e.chat(e.task, "resume without modes"); status != 200 {
		t.Fatalf("chat %d", status)
	}
	e.heard("resume without modes")

	luxRun := e.luxRunOf(e.cond)
	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	got := e.resumeResources(luxRun)
	if len(got) == 0 || got[len(got)-1] == nil || *got[len(got)-1] != tiny {
		t.Errorf("the resumes lux took carried %v, want the last Tiny's", got)
	}
	if m := e.machineOf(e.cond); m["name"] != "Tiny" || m["cpus"] != 0.5 {
		t.Errorf("runs.machine = %v, want Tiny", m)
	}
}
