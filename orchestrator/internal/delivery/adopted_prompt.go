package delivery

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// baseNote tells an agent what a branch it took over merges into: its
// changes are those since that base, not since the default branch.
func baseNote(bases map[string]string) string {
	if len(bases) == 0 {
		return ""
	}
	return render("contracts/base", bases)
}

// AdoptedContext is the pull request a task took over, as it stood when
// a reviewer started: what its people already asked for.
type AdoptedContext struct {
	Repo   string
	Number int
	URL    string
	// Its review verdict, its unresolved threads, the checks failing on
	// its head.
	Review            string
	UnresolvedThreads int
	FailingChecks     []string
	// Who asked for changes, and the latest things said on it, oldest
	// first, by people (never dude's own).
	ChangesRequestedBy []string
	Said               []AdoptedComment
}

// AdoptedComment is one thing said on the adopted pull request.
type AdoptedComment struct {
	Author, Path, Body string
	// A bot's (forge.IsBot): context, not a request.
	Bot bool
}

// adoptedSaidLimit bounds the comments a reviewer is shown.
const adoptedSaidLimit = 15

func (a *AdoptedContext) note() string {
	return render("contracts/adopted-pr", struct {
		PR               *AdoptedContext
		ChangesRequested bool
	}{a, a.Review == forge.ReviewChangesRequested && len(a.ChangesRequestedBy) > 0})
}

// continueNote tells an implementer that the branch it starts on is
// someone's unfinished work: carried on, not redone, and possibly done
// already. Done already still answers to the testing requirement after it:
// committing nothing says the goal is met and tested, not just met.
func continueNote(src *Source) string {
	return render("contracts/continue", src)
}

// AdoptedContextTx reads the adopted pull request of the task's attempt as
// last synced; nil when the task took over none.
func AdoptedContextTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (*AdoptedContext, error) {
	var a AdoptedContext
	var checks, reviews []byte
	err := tx.QueryRow(ctx, `SELECT r.name, pr.number, pr.url, pr.review::text, pr.unresolved_threads, pr.checks_json,
			pr.reviews_json
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.adopted AND pr.head_branch = (SELECT tb FROM (SELECT COALESCE(w.state->>'branch', '') AS tb
			FROM workflow_runs w WHERE w.task_id = $1 AND w.workflow_type = $3
			AND COALESCE(NULLIF(w.state->>'attempt', '')::int, $2) = $2 ORDER BY w.created_at DESC LIMIT 1) x)
		ORDER BY pr.created_at LIMIT 1`, taskID, attempt, WorkflowType).
		Scan(&a.Repo, &a.Number, &a.URL, &a.Review, &a.UnresolvedThreads, &checks, &reviews)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cs []forge.Check
	_ = json.Unmarshal(checks, &cs)
	for _, c := range cs {
		if c.Failed() {
			a.FailingChecks = append(a.FailingChecks, c.Name)
		}
	}
	var rs []forge.Review
	_ = json.Unmarshal(reviews, &rs)
	for _, r := range rs {
		if r.State == "CHANGES_REQUESTED" {
			a.ChangesRequestedBy = append(a.ChangesRequestedBy, r.Login)
		}
	}
	rows, err := tx.Query(ctx, `SELECT author, path, body FROM (
			SELECT e.cursor, COALESCE(e.payload->>'author', '') AS author, COALESCE(e.payload->>'path', '') AS path,
				COALESCE(e.payload->>'body', '') AS body
			FROM events e WHERE e.task_id = $1 AND e.event_type = $2 AND (e.payload->>'number')::int = $3
			  AND e.payload->>'repo' = $4 AND NOT COALESCE((e.payload->>'own')::boolean, false)
			ORDER BY e.cursor DESC LIMIT $5) x ORDER BY cursor`,
		taskID, EvPullRequestCommented, a.Number, a.Repo, adoptedSaidLimit)
	if err != nil {
		return nil, err
	}
	said, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Author, Path, Body string }])
	if err != nil {
		return nil, err
	}
	for _, c := range said {
		a.Said = append(a.Said, AdoptedComment{Author: c.Author, Path: c.Path, Body: c.Body, Bot: forge.IsBot(c.Author)})
	}
	return &a, nil
}
