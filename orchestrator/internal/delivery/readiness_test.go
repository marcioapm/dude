package delivery

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The readiness move, run twice each way on a conducted task, as a retried
// or repeated workflow evaluation runs it: the status moves once each way,
// with one notice each, and no reason to wake the conductor.
func TestReadinessIsOneNoticePerActualMove(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_r', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_r', $1, 'prj_r', 1, 'Greet', 'greet', 'review')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{DB: app}
	st := &State{TaskID: "wi_r", ProjectID: "prj_r", Decider: DeciderConductor}
	pr := PullRequestState{Repo: "target", Status: forge.Status{PullRequestRef: forge.PullRequestRef{Number: 1, State: forge.StateOpen},
		Checks: forge.ChecksPassing, Review: forge.ReviewApproved}}
	count := func(sql string, args ...any) int {
		var n int
		if err := owner.QueryRow(ctx, sql, append([]any{"wi_r"}, args...)...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	notices := func(about string) int {
		return count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
			AND payload->>'about' = $2`, about)
	}
	status := func() string {
		var s string
		_ = owner.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = 'wi_r'`).Scan(&s)
		return s
	}

	for range 2 {
		if err := s.moveReadiness(ctx, org, st, []PullRequestState{pr}, true, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := status(); got != "ready_to_merge" {
		t.Errorf("the task is %s, want ready_to_merge", got)
	}
	if n := notices("ready_to_merge"); n != 1 {
		t.Errorf("%d ready-to-merge notices for one move", n)
	}

	pr.Review = forge.ReviewPending
	for range 2 {
		if err := s.moveReadiness(ctx, org, st, []PullRequestState{pr}, false, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := status(); got != "review" {
		t.Errorf("the task is %s, want review", got)
	}
	if n := notices("no_longer_ready"); n != 1 {
		t.Errorf("%d no-longer-ready notices for one move", n)
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`); n != 0 {
		t.Errorf("readiness recorded %d reasons to wake the conductor", n)
	}
}
