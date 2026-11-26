package phases

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// An old brainstorm R1, swapped out for R2, whose follower still settles a
// failed clone holds R1's row. A member's unlink holds R2 (the session's
// live agent) before it touches session_repositories. settleClone locks R2
// before its delete too, so it waits for the unlink, then finds the row
// gone, instead of deleting the row and then waiting on R2 while the
// unlink waits on the row (40P01).
func TestASwappedOutAgentsFailedCloneWaitsForAnUnlink(t *testing.T) {
	w := newResumeWorld(t)
	org := w.run.Org
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_web', $1, $2, 'web', 'https://github.com/acme/web')`,
		org, w.run.ProjectID)
	w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_swap', $1, 'Research')`, org)
	w.exec(`INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_swap', $2, $1)`, org, w.run.ProjectID)
	w.exec(`INSERT INTO session_repositories (session_id, repository_id, organization_id) VALUES ('ssn_swap', 'repo_web', $1)`, org)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, lux_run_id, lux_state, control, ended_at)
		VALUES ('run_r1', $1, 'ssn_swap', 1, 'aborted', 'agent', 'brainstorm', 'lrun_r1', 'running', 'abort', now()),
		('run_r2', $1, 'ssn_swap', 1, 'running', 'agent', 'brainstorm', 'lrun_r2', 'running', 'none', NULL)`, org)
	var repo string
	if err := w.owner.QueryRow(w.ctx, `SELECT lux_name('P-web')`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	r1 := phaseRun{ID: "run_r1", Org: org, SessionID: "ssn_swap", Role: delivery.RoleBrainstorm, Status: "aborted",
		Control: "abort", LuxRunID: "lrun_r1", LuxState: "running"}

	ctx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	unlink, err := w.s.DB.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlink.Rollback(w.ctx) }()
	var unlinkPID int32
	if _, err := unlink.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, org); err != nil {
		t.Fatal(err)
	}
	if err := unlink.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&unlinkPID); err != nil {
		t.Fatal(err)
	}
	// The unlink as linkSession runs it: the live agent's lock, then the write.
	if err := delivery.LockLiveBrainstorm(ctx, unlink, "ssn_swap"); err != nil {
		t.Fatal(err)
	}

	settled := make(chan error, 1)
	go func() {
		settled <- w.s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			// The follower's batch holds its own Run's row (applyBatch).
			if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = 'run_r1' FOR UPDATE`); err != nil {
				return err
			}
			return (&translator{run: r1}).settleClone(ctx, tx, w.s, repo, "failed", "fatal: repository not found")
		})
	}()
	for blocked := false; !blocked; {
		if err := w.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND $1::int = ANY (pg_blocking_pids(pid)))`, unlinkPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-settled:
			t.Fatalf("the clone settled without waiting on the live agent the unlink holds: %v", err)
		case <-ctx.Done():
			t.Fatal("the clone never waited on the unlink")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := delivery.SetSessionLinks(ctx, unlink, org, "ssn_swap",
		[]delivery.Link{{ProjectID: w.run.ProjectID, RepositoryIDs: []string{}}}); err != nil {
		t.Fatalf("the unlink's write: %v", err)
	}
	if err := unlink.Commit(ctx); err != nil {
		t.Fatalf("the unlink: %v", err)
	}
	if err := <-settled; err != nil {
		t.Fatalf("settling the clone: %v", err)
	}
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM session_repositories WHERE session_id = 'ssn_swap'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d repositories still linked (%v)", n, err)
	}
}
