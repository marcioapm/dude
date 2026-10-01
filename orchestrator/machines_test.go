package orchestrator_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The fake lux's pool "big" (fakelux.DefaultPools), by lux's id.
const bigPool = "pool_b8r2n5w1c7z3"

// Each phase Run goes to lux on the size its role names — the project's,
// the organization's, the fixer following the implementer, else the
// default — with its pool's id when the size names one, and records what
// it ran on, with the pool's name in lux then.
func TestAPhaseRunsOnItsRolesMachineSizeAndRecordsIt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_large', $1, 'Large', 6.5, 23040, 120, $2), ('msz_small', $1, 'Small', 1.5, 3584, 20, NULL)`, w.org, bigPool)
	mustExec(t, w.owner, `UPDATE organizations SET default_agent_models = default_agent_models || '{"implementer":{"machineSize":"msz_small"}}'::jsonb
		WHERE id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_large"') WHERE id = $1`, w.project)

	wi := w.task()
	w.deliver(wi)
	var impl, review *lux.Spec
	w.until("the implementer and a reviewer to reach lux", func() bool {
		impl, review = w.specOf("implement"), w.specOf("review")
		return impl != nil && review != nil
	})
	if impl.Resources == nil || *impl.Resources != (lux.Resources{CPUs: 6.5, Memory: 23040 << 20, Disk: 120 << 30}) {
		t.Errorf("implementer resources = %+v, want the project's Large", impl.Resources)
	}
	if impl.Placement == nil || *impl.Placement != (lux.PlacementSpec{PoolID: bigPool}) {
		t.Errorf("implementer placement = %+v, want big's id and no name", impl.Placement)
	}
	// The reviewer names none: the default size, Standard, in the default pool.
	if review.Resources == nil || *review.Resources != (lux.Resources{CPUs: 2, Memory: 8 << 30, Disk: 20 << 30}) || review.Placement != nil {
		t.Errorf("reviewer resources %+v placement %+v, want Standard and no pool", review.Resources, review.Placement)
	}

	var raw []byte
	if err := w.owner.QueryRow(ctx, `SELECT machine FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var machine map[string]any
	_ = json.Unmarshal(raw, &machine)
	want := map[string]any{"sizeId": "msz_large", "name": "Large", "cpus": 6.5, "memoryMiB": 23040.0, "diskGiB": 120.0,
		"poolId": bigPool, "pool": "big", "from": "project"}
	for k, v := range want {
		if machine[k] != v {
			t.Errorf("runs.machine[%s] = %v, want %v (%s)", k, machine[k], v, raw)
		}
	}
}

// A Run keeps the record of the size it started on: its size edited and
// then removed while it is parked, the resume (which builds its spec again
// from the settings as they are now) neither fails nor rewrites runs.machine,
// and lux keeps the resources it was submitted with.
func TestAResumedRunKeepsTheSizeItStartedOn(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_large', $1, 'Large', 6.5, 23040, 120, $2)`, w.org, bigPool)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_large"') WHERE id = $1`, w.project)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)

	mustExec(t, w.owner, `UPDATE machine_sizes SET name = 'Huge', cpus = 16 WHERE id = 'msz_large'`)
	mustExec(t, w.owner, `DELETE FROM machine_sizes WHERE id = 'msz_large'`)
	// lux reports a limit from now on; only the resume reads it below.
	w.lux.MemoryShare = 0.95
	r := w.lux.Runs()[0]
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, out)
	}
	// The syncer alone, so no artifacts sweep can be what writes the limit.
	for i := 0; r.Resumed == 0 && i < 200; i++ {
		if _, err := w.syncer.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.Resumed != 1 {
		t.Fatalf("the Run was not resumed\n%s", w.describeRuns())
	}
	// Large's 23040 MiB, of which the container gets 95%, to a MiB.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND (machine->>'memoryLimit')::bigint = $2`, runID, int64(21888)<<20); n != 1 {
		t.Errorf("the resume did not record the memory limit lux reported")
	}
	w.pump()

	var name, from string
	var cpus float64
	if err := w.owner.QueryRow(ctx, `SELECT machine->>'name', machine->>'from', (machine->>'cpus')::float8 FROM runs WHERE id = $1`, runID).
		Scan(&name, &from, &cpus); err != nil {
		t.Fatal(err)
	}
	if name != "Large" || from != "project" || cpus != 6.5 {
		t.Errorf("after the size changed and the Run resumed it says %s from %s, %g CPUs; want Large from project, 6.5", name, from, cpus)
	}
	if spec := submitted(t, r); spec.Resources == nil || spec.Resources.CPUs != 6.5 || spec.Placement == nil || spec.Placement.PoolID != bigPool {
		t.Errorf("lux's spec for the resumed Run: resources %+v placement %+v, want Large's", spec.Resources, spec.Placement)
	}
}

// The memory limit lux reports for a Run's container is kept on its record
// once the Run has stopped, so a finished Run still says what it got; one
// recorded is not overwritten.
func TestAFinishedRunKeepsTheMemoryLimitLuxGaveIt(t *testing.T) {
	w := newWorld(t)
	w.lux.MemoryShare = 0.95
	wi := w.task()
	w.deliver(wi)
	// Standard, the default: 8 GiB, of which the container gets 95%, to a MiB.
	want := int64(7782) << 20
	w.until("the implementer to finish with its limit kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'
			AND (machine->>'memoryLimit')::bigint = $2`, wi, want) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET machine = jsonb_set(machine, '{memoryLimit}', '1') WHERE task_id = $1 AND phase = 'implement'`, wi)
	mustExec(t, w.owner, `UPDATE runs SET artifacts_due_at = now(), artifacts_next_at = now() WHERE task_id = $1 AND phase = 'implement'`, wi)
	w.pump()
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND machine->>'memoryLimit' = '1'`, wi); n != 1 {
		t.Error("a limit already recorded was written again")
	}
}

// The fixer names no size of its own: it runs on the implementer's.
func TestTheFixerRunsOnTheImplementersSize(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib)
		VALUES ('msz_large', $1, 'Large', 8, 16384, 80)`, w.org)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,machineSize}', '"msz_large"') WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	var fix *lux.Spec
	w.until("the fixer to reach lux", func() bool { fix = w.specOf("fix"); return fix != nil })
	if fix.Resources == nil || fix.Resources.CPUs != 8 {
		t.Errorf("fixer resources = %+v, want the implementer's Large", fix.Resources)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND machine->>'from' = 'implementer'`, wi); n == 0 {
		t.Error("the fixer's run does not say its size is the implementer's")
	}
}

// A branch preview runs on the size its project's preview settings name,
// in its pool by id, and records it.
func TestABranchPreviewRunsOnItsProjectsPreviewSize(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id)
		VALUES ('msz_xl', $1, 'XL', 16, 49152, 200, $2)`, w.org, bigPool)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"machineSize":"msz_xl"}' WHERE id = $1`, w.project)
	wi := w.task()
	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the preview to be submitted", func() bool { return len(w.lux.Runs()) == 1 })
	spec := submitted(t, w.lux.Runs()[0])
	if spec.Resources == nil || *spec.Resources != (lux.Resources{CPUs: 16, Memory: 48 << 30, Disk: 200 << 30}) {
		t.Errorf("preview resources = %+v", spec.Resources)
	}
	if spec.Placement == nil || *spec.Placement != (lux.PlacementSpec{PoolID: bigPool}) {
		t.Errorf("preview placement = %+v", spec.Placement)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND machine->>'name' = 'XL' AND machine->>'pool' = 'big'`, runID); n != 1 {
		t.Errorf("the preview did not record its size:\n%s", w.describeRuns())
	}
}

// The machine sizes page reads lux's pools through the orchestrator: as
// lux lists them, and none with the reason when lux cannot be read.
func TestLuxsPoolsAreServedToTheBackend(t *testing.T) {
	w := newWorld(t)
	code, body := w.get("/internal/lux/pools", w.org)
	if code != 200 {
		t.Fatalf("pools = %d %s", code, body)
	}
	var got struct {
		Pools []struct {
			ID        string        `json:"id"`
			Name      string        `json:"name"`
			IsDefault bool          `json:"isDefault"`
			HostSize  *lux.HostSize `json:"hostSize"`
		} `json:"pools"`
		Problem *string `json:"problem"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Problem != nil || len(got.Pools) != 3 || !got.Pools[0].IsDefault || got.Pools[0].HostSize == nil || got.Pools[0].HostSize.CPUs != 16 ||
		got.Pools[1].ID != bigPool || got.Pools[1].Name != "big" {
		t.Errorf("pools = %s", body)
	}

	down := httptest.NewServer((&api.Server{DB: w.app, Lux: failingPools{w.syncer.Lux}, Token: "svc", Log: quiet}).Handler())
	t.Cleanup(down.Close)
	w.api = down.URL
	code, body = w.get("/internal/lux/pools", w.org)
	got.Problem = nil
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 || got.Problem == nil || len(got.Pools) != 0 {
		t.Errorf("lux unreachable: %d %s", code, body)
	}
}

type failingPools struct{ lux.Client }

func (failingPools) Pools(context.Context) ([]lux.Pool, error) {
	return nil, &lux.Error{Status: 0, Code: "unreachable", Message: "dial tcp: connection refused"}
}
