package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A conductor started for a task a person stopped and picked back up: Try
// again on its attempt, or the next attempt's (Start over), briefed with
// the person's note and, for a new attempt, an account of the last one.

var pickedUpTmpl = mustTemplate("notices/told/picked-up")

// earlierRunsMax bounds the Runs the account of the last attempt names.
const earlierRunsMax = 12

// pickedUp is what notices/told/picked-up is written from.
type pickedUp struct {
	Who, Note string
	Attempt   int
	Earlier   *earlierAttempt
}

type earlierAttempt struct {
	N            int
	Runs         []string
	PullRequests []earlierPR
}

type earlierPR struct {
	Repo   string
	Number int
	Kept   bool
}

// PickUp is a pick-up's new conductor: who picked the task up and why;
// Attempt is the attempt started over (0 for Try again on the same one),
// Earlier the one before it.
type PickUp struct {
	Writer       Writer
	Note         string
	Attempt      int
	Earlier      int
	ClosedPRs    []string
	Org, Project string
	TaskID       string
}

// StartPickedUpConductor starts a new conductor for a task picked back up,
// on p.Attempt (or the task's current attempt), briefed by dude. The
// caller holds the task's Chat lock.
func StartPickedUpConductor(ctx context.Context, tx pgx.Tx, p PickUp) (string, error) {
	who := "A person"
	if p.Writer.Person != "" {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM people WHERE id = $1`, p.Writer.Person).Scan(&name); err == nil {
			who = firstName(name)
		}
	}
	data := pickedUp{Who: who, Note: p.Note, Attempt: p.Attempt}
	if p.Attempt > 0 && p.Earlier > 0 {
		e, err := earlierAccount(ctx, tx, p.TaskID, p.Earlier, p.ClosedPRs)
		if err != nil {
			return "", err
		}
		data.Earlier = &e
	}
	dude := Writer{ActorType: ledger.ActorSystem, ActorID: "dude", Name: "dude"}
	id, err := StartConductor(ctx, tx, p.Org, p.Project, p.TaskID, dude, render(pickedUpTmpl, data))
	if err != nil || p.Attempt == 0 {
		return id, err
	}
	// The new attempt has no Run yet: the conductor is its first.
	_, err = tx.Exec(ctx, `UPDATE runs SET attempt = $2 WHERE id = $1`, id, p.Attempt)
	return id, err
}

// earlierAccount is what attempt n did, in short: each agent Run's phase
// (or role) and how it ended, and its pull requests, closed or kept.
func earlierAccount(ctx context.Context, tx pgx.Tx, taskID string, n int, closed []string) (earlierAttempt, error) {
	out := earlierAttempt{N: n}
	rows, err := tx.Query(ctx, `SELECT COALESCE(phase::text, role::text, 'agent') || COALESCE(' (' || category || ')', '') || ' ' || status::text
		FROM runs WHERE task_id = $1 AND attempt = $2 AND kind = 'agent' AND source_kind IS NULL
		ORDER BY created_at LIMIT $3`, taskID, n, earlierRunsMax)
	if err != nil {
		return out, err
	}
	if out.Runs, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT r.name, p.number, p.adopted OR NOT (p.id = ANY($2)) FROM pull_requests p
		JOIN repositories r ON r.id = p.repository_id
		WHERE p.task_id = $1 AND p.state IN ('open', 'draft') ORDER BY p.created_at`, taskID, closed)
	if err != nil {
		return out, err
	}
	out.PullRequests, err = pgx.CollectRows(rows, pgx.RowToStructByPos[earlierPR])
	return out, err
}
