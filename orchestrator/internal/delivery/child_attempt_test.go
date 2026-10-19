package delivery

import (
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestChildEffectiveAttempt(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "explicit"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			w := newChildWorld(t)
			if legacy {
				w.exec(`UPDATE workflow_runs SET state=state-'attempt' WHERE id='wf_life'`)
			}
			id := w.child("current")
			if retry := w.child("current"); retry != id {
				t.Fatal("retry changed child")
			}
			next, err := w.s.RestartChild(w.ctx, w.org, "wi_life", id, "restart")
			if err != nil {
				t.Fatal(err)
			}
			if replay, err := w.s.RestartChild(w.ctx, w.org, "wi_life", id, "restart"); err != nil || replay != next {
				t.Fatalf("restart replay %s %v", replay, err)
			}
			w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,phase,role) VALUES('run_attempt4',$1,'prj_life','wi_life',4,'completed','review','reviewer')`, w.org)
			if !legacy {
				w.exec(`UPDATE workflow_runs SET state=jsonb_set(state,'{attempt}','4') WHERE id='wf_life'`)
			}
			in := w.in
			in.Key = "stale"
			if _, err := w.s.StartChild(w.ctx, in); !IsRefusal(err) {
				t.Fatalf("stale parent admitted: %v", err)
			}
			if _, err := w.s.RestartChild(w.ctx, w.org, "wi_life", next, "stale"); !IsRefusal(err) {
				t.Fatalf("stale restart: %v", err)
			}
			if _, err := w.s.SettleChild(w.ctx, w.org, next, ChildOutcome{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			var stale bool
			if err := w.owner.QueryRow(w.ctx, `SELECT stale FROM child_results WHERE child_run_id=$1`, next).Scan(&stale); err != nil || !stale {
				t.Fatalf("late result stale=%v %v", stale, err)
			}
			if w.count("child_result_outbox") != 0 {
				t.Fatal("stale result woke parent")
			}
		})
	}
}

func TestNewStateKeepsAttempt(t *testing.T) {
	w := newChildWorld(t)
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		st, err := NewState(w.ctx, tx, "wi_life", "prj_life", DefaultPolicy(), 3)
		if err == nil && st.Attempt != 3 {
			t.Errorf("attempt=%d, want 3", st.Attempt)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
