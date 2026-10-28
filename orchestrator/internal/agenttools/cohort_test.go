package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Production JSON dispatch + real PostgreSQL settlement/outbox: not a real
// model acceptance test or a full lux stack test.
func TestCohortDispatchAndInspection(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_cohort_parent", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_cohort_parent'`)
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES ('wf_cohort',$1,$2,'task.delivery','cohort','waiting','conductorDecision','{"attempt":1}')`, f.org, f.item)
	var batch struct {
		CohortID string
		Runs     []struct{ RunID, Profile string }
	}
	brief := `{"key":"parallel","runs":[{"profile":"investigator","prompt":"investigate A"},{"profile":"reviewer","prompt":"review B"},{"profile":"investigator","note":"check C"}]}`
	if status := f.postAs(t, token, "start_agents", brief, &batch); status != 200 || len(batch.Runs) != 3 {
		t.Fatalf("start %d %+v", status, batch)
	}
	store := &delivery.Store{DB: f.app}
	ctx := context.Background()
	for i, r := range batch.Runs {
		report := fmt.Sprintf("report member %d: passing claims do not override failure ```nested````", i)
		if i == 0 {
			report += strings.Repeat("界", 17000)
		}
		mustExec(t, f.owner, `INSERT INTO events (id,organization_id,task_id,run_id,event_type,actor_type,actor_id,payload) VALUES ('evt_' || $3,$1,$2,$3,'agent.message','agent',$3,jsonb_build_object('text',$4::text))`, f.org, f.item, r.RunID, report)
		status := "completed"
		if i == 1 {
			status = "failed"
		}
		if _, err := store.SettleChild(ctx, f.org, r.RunID, delivery.ChildOutcome{Status: status}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnqueueChildResults(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM directives WHERE run_id='run_cohort_parent'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 2 {
			want = 1
		}
		if n != want {
			t.Fatalf("after %d: %d directives want %d", i+1, n, want)
		}
		var read map[string]any
		if code := f.postAs(t, token, "read_run", fmt.Sprintf(`{"run":%q}`, r.RunID), &read); code != 200 {
			t.Fatalf("read %d %+v", code, read)
		}
		if read["status"] != status || read["reportPresent"] != true || read["settled"] != true {
			t.Fatalf("inspection %+v", read)
		}
		if i == 0 && read["reportClipped"] != true {
			t.Fatal("large report not labeled clipped")
		}
		if !strings.Contains(fmt.Sprint(read["report"]), "report member") {
			t.Fatal("missing report")
		}
		raw, _ := json.Marshal(read)
		if len(raw) > 64000 {
			t.Fatal("inspection oversized")
		}
	}
	if code, _ := f.post(t, token, "read_run", `{"run":"unknown"}`); code != 422 {
		t.Fatalf("unknown run %d", code)
	}
	var before int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.post(t, token, "start_agents", `{"runs":[{"profile":"investigator"},{"profile":"invalid"}]}`); code != 422 {
		t.Fatalf("invalid second %d", code)
	}
	var after int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("invalid request created partial children")
	}
	worker := f.run(t, "run_cohort_worker", "implementer", "running")
	for _, tool := range []string{"start_agents", "read_run"} {
		if code, _ := f.post(t, worker, tool, `{}`); code != 404 {
			t.Fatalf("worker %s: %d", tool, code)
		}
	}
}
