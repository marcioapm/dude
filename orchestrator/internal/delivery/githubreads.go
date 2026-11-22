package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// The hourly GitHub read budget a task's agents share (agenttools'
// GitHub tools spend it, and so do a conductor's waits on a pull request
// dude does not track): its size, how its reads are counted, and its lock.

// EvGitHubRead is one read made on GitHub for an agent: by its tool, or
// for its wait (payload tool "wait_for").
const EvGitHubRead = "github.read"

// EvToolAttempted is a counted attempt of a tool (agenttools'
// AttemptEventType): the GitHub read budget counts these, by tool.
const EvToolAttempted = "agent.tool.attempted"

// GitHubReadsPerHour is the budget: reads in the last hour.
const GitHubReadsPerHour = 60

// GitHubReadsInHour filters the events a budget counts: the GitHub read
// tools' attempts, refresh_pull_requests' (one per pull request it syncs)
// and a wait's reads of a pull request (tool wait_for, marked attempted, so
// a Run's call counts are not spent by them). The scope and time are served
// by the tool-agnostic partial indexes of counted attempts (migrations
// 197-199); the tool list is a filter over those few rows, so a new read
// tool needs no index.
const GitHubReadsInHour = ` AND event_type = 'agent.tool.attempted' AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'github_run_jobs', 'github_branch_runs', 'refresh_pull_requests', 'pull_requests', 'wait_for')
		AND occurred_at > now() - interval '1 hour'`

// LockGitHubReadsTx takes the lock a budget's reads are counted under,
// until tx ends: key is "task:<id>", "session:<id>" or "automation:<id>".
func LockGitHubReadsTx(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('github_reads:' || $1))`, key)
	return err
}

// taskGitHubReadsTx are the reads the task's budget spent in the last hour.
func taskGitHubReadsTx(ctx context.Context, tx pgx.Tx, taskID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE task_id = $1`+GitHubReadsInHour, taskID).Scan(&n)
	return n, err
}
