package phases

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func sessionLifecycleWorld(t *testing.T) (*resumeWorld, phaseRun, string, func() string) {
	t.Helper()
	w := newResumeWorld(t)
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix)
		VALUES ('prj_unrelated',$1,'Unrelated','unrelated','U')`, w.run.Org)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		VALUES ('wi_unrelated',$1,'prj_unrelated',1,'Unrelated','Unrelated')`, w.run.Org)
	w.exec(`INSERT INTO workflow_runs (id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES ('wf_unrelated',$1,'wi_unrelated','task.delivery','unrelated','waiting','awaitPullRequest',
		jsonb_build_object('taskId','wi_unrelated','projectId','prj_unrelated','attempt',1,'heads','{"repo":"preserved"}'::jsonb))`, w.run.Org)
	w.exec(`INSERT INTO sessions (id,organization_id,title) VALUES ('ssn_lifecycle',$1,'Research')`, w.run.Org)
	w.exec(`INSERT INTO runs (id,organization_id,session_id,attempt,status,kind,role,lux_state)
		VALUES ('run_brainstorm_lifecycle',$1,'ssn_lifecycle',1,'running','agent','brainstorm','running')`, w.run.Org)
	parent := phaseRun{ID: "run_brainstorm_lifecycle", Org: w.run.Org, SessionID: "ssn_lifecycle", Role: delivery.RoleBrainstorm,
		Status: statusRunning, Control: "none", LuxState: "running"}
	store := &delivery.Store{DB: w.s.DB}
	child, err := store.StartChild(w.ctx, delivery.StartChildInput{Parent: delivery.ChildParent{Kind: delivery.ChildParentSession,
		Run: parent.ref()}, Profile: "investigator", Key: "live"})
	if err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET status='running' WHERE id=$1`, child)
	snapshot := func() string {
		t.Helper()
		var raw string
		if err := w.owner.QueryRow(w.ctx, `SELECT jsonb_build_object('task',to_jsonb(t),'delivery',to_jsonb(w))::text
			FROM tasks t JOIN workflow_runs w ON w.task_id=t.id WHERE t.id='wi_unrelated'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	return w, parent, child, snapshot
}

func TestBrainstormTerminalPathsFenceSessionChildren(t *testing.T) {
	for _, path := range []string{"end", "replace", "fail", "fail-kept", "stream-end", "turn-failure", "lux-stop", "broken-timeout"} {
		t.Run(path, func(t *testing.T) {
			w, parent, child, snapshot := sessionLifecycleWorld(t)
			before := snapshot()
			store := &delivery.Store{DB: w.s.DB}
			// An already settled result must also be rejected when its outbox is consumed after the parent ends.
			settled, err := store.StartChild(w.ctx, delivery.StartChildInput{Parent: delivery.ChildParent{Kind: delivery.ChildParentSession,
				Run: parent.ref()}, Profile: "investigator", Key: "settled"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.SettleChild(w.ctx, parent.Org, settled, delivery.ChildOutcome{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			next := ""
			switch path {
			case "end":
				w.exec(`UPDATE runs SET lux_state='terminated' WHERE id=$1`, parent.ID)
				err = w.s.endConductor(w.ctx, parent, "its container stopped")
			case "replace":
				err = w.s.DB.InOrg(w.ctx, parent.Org, func(tx pgx.Tx) error {
					if err := delivery.LockSession(w.ctx, tx, parent.SessionID); err != nil {
						return err
					}
					var err error
					next, _, err = delivery.ReplaceBrainstorm(w.ctx, tx, parent.ref(), delivery.SessionModelApplied{Model: "fake/scripted", Harness: "opencode"},
						delivery.Writer{}, delivery.Opening{Message: "Continue the research", Shown: ""})
					return err
				})
			case "fail":
				err = w.s.fail(w.ctx, parent, "cannot construct the run")
			case "fail-kept":
				err = w.s.failKept(w.ctx, parent, "cannot resume the run")
			case "stream-end":
				err = w.s.applyBatch(w.ctx, &translator{run: parent}, []lux.Frame{luxState(1, "terminated")})
			case "turn-failure":
				tr := &translator{run: parent}
				tr.FailedTurns = maxFailedTurnResumes
				err = w.s.applyBatch(w.ctx, tr, []lux.Frame{record(1, "acp.turn_end", map[string]any{"error": "context exhausted"})})
			case "lux-stop":
				tr := &translator{run: parent}
				tr.Broken = &brokenTurn{Error: "agent process exited", AgentError: "process exited", Epoch: 1}
				err = w.s.applyBatch(w.ctx, tr, []lux.Frame{{Kind: "lux", Epoch: 1, EventType: "exited", EventData: []byte(`{"reason":"oom"}`)}})
			case "broken-timeout":
				w.exec(`UPDATE runs SET harness_state=jsonb_build_object('brokenTurn',
					jsonb_build_object('error','agent process exited','epoch',1,'at',now()-interval '2 minutes')) WHERE id=$1`, parent.ID)
				err = w.s.failBroken(w.ctx, parent, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			var status, stop, control string
			var keep bool
			if err := w.owner.QueryRow(w.ctx, `SELECT p.status::text,c.child_stop_reason,c.control::text,c.keep
				FROM runs p JOIN runs c ON c.parent_run_id=p.id WHERE p.id=$1 AND c.id=$2`, parent.ID, child).
				Scan(&status, &stop, &control, &keep); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if path == "end" {
				want = "completed"
			} else if path == "replace" {
				want = "aborted"
			}
			if status != want || stop != "parent-ended" || control != "abort" || keep {
				t.Fatalf("parent=%s child stop=%s control=%s keep=%v", status, stop, control, keep)
			}
			if next == "" {
				err = w.s.DB.InOrg(w.ctx, parent.Org, func(tx pgx.Tx) error {
					if err := delivery.LockSession(w.ctx, tx, parent.SessionID); err != nil {
						return err
					}
					var err error
					next, _, err = delivery.StartBrainstorm(w.ctx, tx, parent.Org, parent.SessionID, delivery.Writer{}, delivery.Opening{Message: "Continue"})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.SettleChild(w.ctx, parent.Org, child, delivery.ChildOutcome{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			if n, err := store.EnqueueChildResults(w.ctx); err != nil || n != 0 {
				t.Fatalf("old results delivered=%d err=%v", n, err)
			}
			var stale, directives int
			if err := w.owner.QueryRow(w.ctx, `SELECT (SELECT count(*) FROM child_results WHERE child_run_id=ANY($1) AND stale),
				(SELECT count(*) FROM directives WHERE run_id=$2)`, []string{child, settled}, next).Scan(&stale, &directives); err != nil {
				t.Fatal(err)
			}
			if stale != 2 || directives != 0 {
				t.Fatalf("stale=%d successor directives=%d", stale, directives)
			}
			if snapshot() != before {
				t.Fatal("session parent/child ending mutated unrelated task or delivery")
			}
		})
	}
}

func TestBrainstormFailureDoesNotAcquireSessionLockAfterParentRow(t *testing.T) {
	w, parent, child, _ := sessionLifecycleWorld(t)
	tx, err := w.owner.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(w.ctx)
	if err := delivery.LockSession(w.ctx, tx, parent.SessionID); err != nil {
		t.Fatal(err)
	}
	// A caller can hold the session lock while waiting for this failure's
	// parent row. The failure must finish without waiting for that caller.
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()
	if err := w.s.fail(ctx, parent, "construction refused"); err != nil {
		t.Fatal(err)
	}
	var stop string
	if err := w.owner.QueryRow(w.ctx, `SELECT child_stop_reason FROM runs WHERE id=$1`, child).Scan(&stop); err != nil || stop != "parent-ended" {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
}

func TestBrainstormParkedFailedTurnKeepsChildrenLive(t *testing.T) {
	w, parent, child, _ := sessionLifecycleWorld(t)
	if err := w.s.applyBatch(w.ctx, &translator{run: parent}, []lux.Frame{record(1, "acp.turn_end", map[string]any{"error": "temporary failure"})}); err != nil {
		t.Fatal(err)
	}
	var status, control string
	var stop *string
	if err := w.owner.QueryRow(w.ctx, `SELECT p.status::text,c.child_stop_reason,c.control::text FROM runs p
		JOIN runs c ON c.parent_run_id=p.id WHERE p.id=$1 AND c.id=$2`, parent.ID, child).Scan(&status, &stop, &control); err != nil {
		t.Fatal(err)
	}
	if status != "running" || stop != nil || control != "none" {
		t.Fatalf("nonterminal failure stopped children: parent=%s stop=%v control=%s", status, stop, control)
	}
}

func TestSessionChildFailureAndEndLeaveTaskDeliveryUntouched(t *testing.T) {
	for _, outcome := range []string{"fail", "finish"} {
		t.Run(outcome, func(t *testing.T) {
			w, parent, child, snapshot := sessionLifecycleWorld(t)
			before := snapshot()
			r := phaseRun{ID: child, Org: parent.Org, SessionID: parent.SessionID, SourceKind: string(delivery.ChildParentSession),
				Phase: delivery.PhaseInvestigate, Role: "investigator", Status: statusRunning, Control: "none"}
			if outcome == "fail" {
				if err := w.s.fail(w.ctx, r, "investigation failed"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := w.s.finish(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			want := "completed"
			if outcome == "fail" {
				want = "failed"
			}
			if _, err := (&delivery.Store{DB: w.s.DB}).SettleChild(w.ctx, parent.Org, child, delivery.ChildOutcome{Status: want}); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM runs WHERE id=$1`, child).Scan(&status); err != nil || status != want {
				t.Fatalf("status=%s want=%s err=%v", status, want, err)
			}
			if snapshot() != before {
				t.Fatal("session child changed unrelated task or delivery")
			}
		})
	}
}
