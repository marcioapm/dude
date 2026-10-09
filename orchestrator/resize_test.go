package orchestrator_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

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
	before := len(w.lux.ResumeResourcesOf(r.ID))
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		w.t.Fatalf("resume: %d %v", status, out)
	}
	for i := 0; len(w.lux.ResumeResourcesOf(r.ID)) == before; i++ {
		if i == 200 {
			w.t.Fatalf("the Run was not resumed\n%s", w.describeRuns())
		}
		if _, err := w.syncer.Sweep(context.Background()); err != nil {
			w.t.Fatal(err)
		}
	}
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
// says the disk lux kept, not the one asked for, with lux's reason.
func TestADiskShrinkLuxRefusesRecordsTheDiskItKept(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.tiny()
	w.lux.DiskUse = 8 * gibB // needs 10 GiB: more than Tiny's 5
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
}

// A size in another pool cannot be had by a resume: lux binds a Run to the
// pool it was submitted to. The preview wakes on the size it has, nothing
// is sent, and its record says why, naming both.
func TestAWakeIntoASizeInAnotherPoolKeepsTheSizeAndSaysWhy(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
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
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["note"] != want {
		t.Errorf("runs.machine = %v, want Standard kept with the reason", m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.resized' AND payload->'machine'->>'note' = $2`, runID, want); n != 1 {
		t.Errorf("%d run.resized with the reason, want 1", n)
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
	already := &resumingAlready{Client: w.previews.Lux, seen: map[string]bool{}}
	w.previews.Lux = already

	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_tiny"}' WHERE id = $1`, w.project)
	w.wakeAgain(runID, web)

	tiny := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 5 * gibB}
	already.mu.Lock()
	asked, errs := already.asked, already.errs
	already.mu.Unlock()
	if le, ok := lux.AsError(errs[0]); len(asked) != 2 || asked[0] == nil || *asked[0] != tiny || !ok || le.Status != 409 ||
		asked[1] != nil || errs[1] != nil {
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
	if m["name"] != "Standard" || m["cpus"] != 2.0 || note != `Its settings now name Tiny, but this resume kept Standard (lux said: invalid JSON body: json: unknown field "cpus"). The next resume tries again.` {
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
	want := "Its settings now name Tiny, but this resume kept Standard (lux did not apply it). The next resume tries again."
	if m["name"] != "Standard" || m["cpus"] != 2.0 || m["diskGiB"] != 20.0 || m["note"] != want {
		t.Errorf("runs.machine = %v, want Standard as lux's spec says", m)
	}
}
