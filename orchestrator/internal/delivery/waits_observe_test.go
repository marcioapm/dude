package delivery

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The sweep's observation step (waits_observe.go): which conditions are
// due a reading, and how a reading is stored.

// observe is one pass of the sweep's observation step.
func (w *childWorld) observe(f Forges) int {
	w.t.Helper()
	n, err := ObserveWaits(w.ctx, w.s.DB, f)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// ageObservations sets every stored observation's time back by d.
func (w *childWorld) ageObservations(d time.Duration) {
	w.t.Helper()
	w.exec(`UPDATE conductor_waits SET conditions = (SELECT jsonb_agg(CASE WHEN c ? 'obs'
			THEN jsonb_set(c, '{obs,at}', to_jsonb((c->'obs'->>'at')::timestamptz - make_interval(secs => $1)))
			ELSE c END ORDER BY i) FROM jsonb_array_elements(conditions) WITH ORDINALITY AS x(c, i))
		WHERE ended_at IS NULL`, d.Seconds())
}

// obs is the stored observation of the wait's condition i, "" for none.
func (w *childWorld) obs(id string, i int) string {
	w.t.Helper()
	var raw *string
	if err := w.owner.QueryRow(w.ctx, `SELECT (conditions->$2::int->'obs')::text FROM conductor_waits WHERE id = $1`, id, i).Scan(&raw); err != nil {
		w.t.Fatal(err)
	}
	if raw == nil {
		return ""
	}
	return *raw
}

func (w *childWorld) obsField(id string, i int, field string) string {
	w.t.Helper()
	var v *string
	if err := w.owner.QueryRow(w.ctx, `SELECT conditions->$2::int->'obs'->>$3 FROM conductor_waits WHERE id = $1`, id, i, field).Scan(&v); err != nil {
		w.t.Fatal(err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// A wait that ends between the observation's select and its write is not
// written.
func TestAnObservationIsNotWrittenToAnEndedWait(t *testing.T) {
	w := newWaitWorld(t)
	w.exec(`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, wake_all, deadline_at, since_cursor, call_key)
		VALUES ('cwt_obs', $1, 'wi_life', 'run_parent', 3, $2::jsonb, true, now() + interval '1 hour', 0, 'c1')`, w.org,
		`[{"kind": "url", "url": "https://example.com/", "where": "example.com/", "status": "2xx"}]`)
	var due []WaitObservation
	if err := w.s.DB.InSystem(w.ctx, "test", func(tx pgx.Tx) error {
		var err error
		due, err = DueWaitObservationsTx(w.ctx, tx)
		return err
	}); err != nil || len(due) != 1 {
		t.Fatalf("%d due, %v", len(due), err)
	}
	w.exec(`UPDATE conductor_waits SET ended_at = now(), ended_by = 'replaced' WHERE id = 'cwt_obs'`)
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		wrote, err := recordObservationTx(w.ctx, tx, due[0], waitObs{Status: 200})
		if wrote {
			t.Error("wrote to an ended wait")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w.obs("cwt_obs", 0) != "" {
		t.Fatal("the ended wait's condition changed")
	}
	// Open, it is written.
	w.exec(`UPDATE conductor_waits SET ended_at = NULL, ended_by = NULL WHERE id = 'cwt_obs'`)
	// Open, but the condition at the index is not the kind read: not written.
	other := due[0]
	other.cond.Kind = condPREnded
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		wrote, err := recordObservationTx(w.ctx, tx, other, waitObs{State: "merged"})
		if wrote {
			t.Error("wrote a reading of another kind")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w.obs("cwt_obs", 0) != "" {
		t.Fatalf("a reading of another kind changed the condition: %s", w.obs("cwt_obs", 0))
	}
	// Open, and the same kind: written.
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		wrote, err := recordObservationTx(w.ctx, tx, due[0], waitObs{Status: 200})
		if !wrote {
			t.Error("an open wait was not written")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w.obsField("cwt_obs", 0, "status") != "200" || w.obsField("cwt_obs", 0, "at") == "" {
		t.Fatalf("obs %s", w.obs("cwt_obs", 0))
	}
}

// The observation select picks at most a batch, oldest reading first, of
// open waits only.
func TestDueObservationsAreBoundedOldestFirst(t *testing.T) {
	w := newWaitWorld(t)
	for i := range 4 {
		conds := make([]map[string]any, 0, 8)
		for j := range 8 {
			conds = append(conds, map[string]any{"kind": condURL, "url": "https://example.com/", "where": "example.com/", "status": "2xx",
				"obs": map[string]any{"at": time.Now().Add(-time.Duration(10+10*i+j) * time.Minute).UTC()}})
		}
		raw, _ := json.Marshal(conds)
		task := fmt.Sprintf("wi_obs%d", i)
		w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ($1, $2, 'prj_life', $3, 'T', 'G', 'running')`, task, w.org, 10+i)
		w.exec(`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, wake_all, deadline_at, since_cursor, call_key)
			VALUES ($1, $2, $3, 'run_parent', 3, $4::jsonb, true, now() + interval '1 hour', 0, $1)`, "cwt_"+task, w.org, task, raw)
	}
	// The oldest readings are on an ended wait: never selected.
	w.exec(`UPDATE conductor_waits SET ended_at = now(), ended_by = 'replaced' WHERE id = 'cwt_wi_obs3'`)
	var due []WaitObservation
	if err := w.s.DB.InSystem(w.ctx, "test", func(tx pgx.Tx) error {
		var err error
		due, err = DueWaitObservationsTx(w.ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(due) != waitObserveBatch || due[0].WaitID != "cwt_wi_obs2" || due[0].Index != 7 || due[waitObserveBatch-1].WaitID != "cwt_wi_obs0" {
		t.Fatalf("%d due, first %+v, last %+v", len(due), due[0], due[len(due)-1])
	}
}
