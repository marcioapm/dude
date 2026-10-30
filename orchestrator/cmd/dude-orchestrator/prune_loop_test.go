package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/branchdiff"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The branch-snapshots-prune loop runs one pass over a backlog, then waits
// its interval: no second pass at once that finds nothing.
func TestThePruneLoopWaitsAfterAPassOverABacklog(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_lp', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_lp', $1, 'prj_lp', 1, 'T', 'G')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_lp', $1, 'prj_lp', 'api', 'https://example.com/acme/api')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	prev := any(nil)
	for i := 1; i <= 3; i++ {
		id, rel := fmt.Sprintf("tbs_lp%d", i), "fast-forward"
		if i == 1 {
			rel = "initial"
		}
		if _, err := owner.Exec(ctx, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
			target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
			VALUES ($1, $2, 'wi_lp', 1, 'repo_lp', 'api', 'dude/wi_lp', 'main', $3, $3, $4, '[]', 'c', '{}', $5, $6,
			now() - make_interval(days => 5 - $7::int))`, id, org, fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 300+i), rel, prev, i); err != nil {
			t.Fatal(err)
		}
		prev = id
	}
	var calls atomic.Int32
	first := make(chan struct{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := loop{"branch-snapshots-prune", time.Hour, func(ctx context.Context) (int, error) {
		n, err := branchdiff.Prune(ctx, app, log)
		if calls.Add(1) == 1 {
			close(first)
		}
		return n, err
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.run(ctx, log, nil)
	}()
	<-first
	// A second pass at once would take a few milliseconds; give it far more.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done
	if n := calls.Load(); n != 1 {
		t.Errorf("%d passes, want 1 then a wait for the interval", n)
	}
	var left int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM task_branch_snapshots`).Scan(&left); err != nil || left != 1 {
		t.Errorf("left %d (%v), want the latest", left, err)
	}
}
