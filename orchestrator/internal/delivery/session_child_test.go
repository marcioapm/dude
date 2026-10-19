package delivery

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestSessionChildAdmissionAndParentFence(t *testing.T) {
	ctx := context.Background()
	if err := dbtest.Reachable(ctx); err != nil {
		t.Fatal(err)
	}
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_child', $1, 'Research')`, org)
	exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role)
		VALUES ('run_brainstorm', $1, 'ssn_child', 1, 'running', 'agent', 'brainstorm')`, org)
	exec(`INSERT INTO model_tiers (id, organization_id, name, model) VALUES ('tier_session_child', $1, 'Child', 'fake/scripted')`, org)
	s := &Store{DB: app}
	in := StartChildInput{Parent: ChildParent{Kind: ChildParentSession, Run: RunRef{Org: org, SessionID: "ssn_child", RunID: "run_brainstorm"}}, Profile: "investigator", Key: "one", Tier: "Child"}
	id, err := s.StartChild(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.StartChild(ctx, in)
	if err != nil || again != id {
		t.Fatalf("idempotency: %s %v", again, err)
	}
	var task, session, source, parent, profile string
	if err := owner.QueryRow(ctx, `SELECT COALESCE(task_id,''), COALESCE(session_id,''), source_kind, parent_run_id, child_profile FROM runs WHERE id=$1`, id).
		Scan(&task, &session, &source, &parent, &profile); err != nil {
		t.Fatal(err)
	}
	if task != "" || session != "ssn_child" || source != "session" || parent != "run_brainstorm" || profile != "investigator" {
		t.Fatalf("metadata: %q %q %q %q %q", task, session, source, parent, profile)
	}
	if replacement, err := s.RestartChild(ctx, org, "", id, "restart"); !IsRefusal(err) || replacement != "" {
		t.Fatalf("session restart: replacement=%q err=%v", replacement, err)
	}
	for _, profile := range []string{"implementer", "fixer", "simplifier", "reviewer", "qa_browser"} {
		bad := in
		bad.Profile, bad.Key, bad.Tier = profile, profile, ""
		if _, err := s.StartChild(ctx, bad); !IsRefusal(err) {
			t.Fatalf("profile %s: %v", profile, err)
		}
	}
	if err := s.EndChildParent(ctx, org, "", "run_brainstorm"); !IsRefusal(err) {
		t.Fatalf("live parent end: %v", err)
	}
	if won, err := s.SettleChild(ctx, org, id, ChildOutcome{Status: "completed"}); err != nil || !won {
		t.Fatalf("settlement: %v %v", won, err)
	}
	if n, err := s.EnqueueChildResults(ctx); err != nil || n != 1 {
		t.Fatalf("result delivery: %d %v", n, err)
	}
	var directiveSession, directiveParent string
	if err := owner.QueryRow(ctx, `SELECT r.session_id, d.run_id FROM child_result_outbox o JOIN directives d ON d.id=o.directive_id JOIN runs r ON r.id=d.run_id WHERE o.child_run_id=$1`, id).Scan(&directiveSession, &directiveParent); err != nil {
		t.Fatal(err)
	}
	if directiveSession != "ssn_child" || directiveParent != "run_brainstorm" {
		t.Fatalf("result owner: %s %s", directiveSession, directiveParent)
	}
	in.Key = "late"
	late, err := s.StartChild(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE runs SET status='completed', ended_at=now() WHERE id='run_brainstorm'`)
	if err := s.EndChildParent(ctx, org, "", "run_brainstorm"); err != nil {
		t.Fatal(err)
	}
	var stopped string
	if err := owner.QueryRow(ctx, `SELECT child_stop_reason FROM runs WHERE id=$1`, late).Scan(&stopped); err != nil {
		t.Fatal(err)
	}
	if stopped != "parent-ended" {
		t.Fatalf("stop reason %q", stopped)
	}
	exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role)
		VALUES ('run_brainstorm_new', $1, 'ssn_child', 1, 'running', 'agent', 'brainstorm')`, org)
	if won, err := s.SettleChild(ctx, org, late, ChildOutcome{Status: "completed"}); err != nil || !won {
		t.Fatalf("late settlement: %v %v", won, err)
	}
	var stale bool
	if err := owner.QueryRow(ctx, `SELECT stale FROM child_results WHERE child_run_id=$1`, late).Scan(&stale); err != nil || !stale {
		t.Fatalf("late stale=%v: %v", stale, err)
	}
	if n, err := s.EnqueueChildResults(ctx); err != nil || n != 0 {
		t.Fatalf("late result delivered: %d %v", n, err)
	}
	var delivered int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM directives WHERE run_id='run_brainstorm_new'`).Scan(&delivered); err != nil || delivered != 0 {
		t.Fatalf("new parent received %d old results: %v", delivered, err)
	}
	in.Parent.Run.RunID = "run_brainstorm_new"
	in.Key = "one"
	newChild, err := s.StartChild(ctx, in)
	if err != nil || newChild == id {
		t.Fatalf("key leaked across parent Runs: %s %v", newChild, err)
	}
}

func TestSessionChildForgedReferenceCannotChooseParent(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('session_real', $1, 'Real'), ('session_other', $1, 'Other')`, org)
	exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role) VALUES
		('run_real', $1, 'session_real', 1, 'running', 'agent', 'brainstorm'),
		('run_other', $1, 'session_other', 1, 'running', 'agent', 'brainstorm')`, org)
	s := &Store{DB: app}
	in := StartChildInput{Parent: ChildParent{Kind: ChildParentSession, Run: RunRef{Org: org, SessionID: "session_other", RunID: "run_real"}}, Profile: "investigator", Key: "forged"}
	if _, err := s.StartChild(ctx, in); !IsRefusal(err) {
		t.Fatalf("mismatched session accepted: %v", err)
	}
	in.Parent.Run.SessionID = "session_real"
	otherOrg := dbtest.Org(t, owner)
	exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('session_foreign', $1, 'Foreign')`, otherOrg)
	exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role)
		VALUES ('run_foreign', $1, 'session_foreign', 1, 'running', 'agent', 'brainstorm')`, otherOrg)
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_forged', $1, 'Task', 'task', 'TASK')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('task_forged', $1, 'prj_forged', 1, 'Task', 'Investigate')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role)
		VALUES ('run_task_forged', $1, 'prj_forged', 'task_forged', 1, 'running', 'agent', 'conductor')`, org)
	for _, ref := range []RunRef{
		{Org: org, SessionID: "session_other", RunID: "run_real"},
		{Org: org, SessionID: "session_foreign", RunID: "run_foreign"},
		{Org: org, SessionID: "session_real", RunID: "run_task_forged"},
	} {
		bad := in
		bad.Parent.Run = ref
		err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := StartChildTx(ctx, tx, bad)
			return err
		})
		if !IsRefusal(err) {
			t.Fatalf("forged primitive reference %+v: %v", ref, err)
		}
	}
	if _, err := s.StartChild(ctx, in); err != nil {
		t.Fatal(err)
	}
	var parent, session string
	if err := owner.QueryRow(ctx, `SELECT parent_run_id, session_id FROM runs WHERE source_kind='session'`).Scan(&parent, &session); err != nil {
		t.Fatal(err)
	}
	if parent != "run_real" || session != "session_real" {
		t.Fatalf("forged parent selected: %s %s", parent, session)
	}
}
