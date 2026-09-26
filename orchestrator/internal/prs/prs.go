// Package prs keeps dude's record of each pull request in step with GitHub,
// and tells the delivery workflow when something happened that it must act
// on.
//
// Driven by webhooks, never by polling: asking GitHub about every open PR on
// a timer spends its rate limit faster than a busy organization can afford.
// A delivery says *which* PR changed; dude then reads that one PR. A
// reconciler sweeps open PRs every ~15 minutes as a backstop, because GitHub
// does not guarantee delivery and dude may have been down when one was sent.
//
// Every change is recorded in the ledger; the workflow is signalled only for
// what the classifier says is worth waking an agent for.
package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

type Signaller func(ctx context.Context, org, workflowRunID, name string, payload any, key string) error

type Syncer struct {
	DB     *db.DB
	Forges interface {
		For(ctx context.Context, org string) (*forge.GitHub, error)
	}
	Signal Signaller
	Log    *slog.Logger
	// Logins whose comments are the factory's own. A PAT comments as its
	// owner, so their comments cannot be filtered without also filtering their
	// reviews; only explicitly configured bot accounts are skipped.
	FactoryLogins []string
}

type tracked struct {
	ID, ProjectID, TaskID, RunID, State, Checks, Review, RepoURL, RepoName string
	Number                                                                     int
	FeedbackCursor                                                             *time.Time
}

// Sync reads one PR from GitHub, records what changed, and signals the
// workflow if a change is actionable.
func (s *Syncer) Sync(ctx context.Context, org, prID string) error {
	var pr tracked
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pr.id, pr.project_id, pr.task_id, COALESCE(pr.run_id, ''), pr.state::text,
			pr.checks::text, pr.review::text, r.url, r.name, pr.number, pr.feedback_cursor
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id WHERE pr.id = $1`, prID).
			Scan(&pr.ID, &pr.ProjectID, &pr.TaskID, &pr.RunID, &pr.State, &pr.Checks, &pr.Review,
				&pr.RepoURL, &pr.RepoName, &pr.Number, &pr.FeedbackCursor)
	}); err != nil {
		return err
	}
	slug := forge.SlugFromURL(pr.RepoURL)
	gh, err := s.Forges.For(ctx, org)
	if err != nil || gh == nil || slug == "" {
		return err
	}

	status, err := gh.PullRequest(ctx, slug, pr.Number)
	if err != nil {
		return err
	}
	since := ""
	if pr.FeedbackCursor != nil {
		since = pr.FeedbackCursor.UTC().Format(time.RFC3339)
	}
	listed, err := gh.Feedback(ctx, slug, pr.Number, since)
	if err != nil {
		return err
	}

	var fresh []forge.Feedback
	var workflowRunID string
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		// The cursor narrows the query; the ledger decides what is new.
		// Feedback at the cursor's own second comes back every time, because
		// the listing is inclusive, so anything already recorded is dropped
		// by id.
		for _, f := range listed {
			var seen bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1
				AND event_type = $2 AND payload->>'feedbackId' = $3)`, pr.TaskID, delivery.EvPullRequestCommented, f.ID).
				Scan(&seen); err != nil {
				return err
			}
			if !seen {
				fresh = append(fresh, f)
			}
		}
		cursor := pr.FeedbackCursor
		if n := len(listed); n > 0 {
			if t, err := time.Parse(time.RFC3339, listed[n-1].CreatedAt); err == nil {
				cursor = &t
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET state = $2::pull_request_state, checks = $3::check_state,
			review = $4::review_state, head_sha = $5, last_polled_at = now(), feedback_cursor = $6, updated_at = now(),
			merged_at = CASE WHEN $2 = 'merged' THEN COALESCE(merged_at, now()) ELSE merged_at END,
			closed_at = CASE WHEN $2 = 'closed' THEN COALESCE(closed_at, now()) ELSE closed_at END
			WHERE id = $1`, pr.ID, status.State, status.Checks, status.Review, status.HeadSHA, cursor); err != nil {
			return err
		}

		// One event per thing that changed. "Nothing happened" is not an
		// occurrence, so an unchanged PR records nothing.
		type change struct {
			typ     string
			payload map[string]any
		}
		var changes []change
		if status.Checks != pr.Checks {
			changes = append(changes, change{delivery.EvPullRequestChecks, map[string]any{"from": pr.Checks, "to": status.Checks}})
		}
		if status.Review != pr.Review {
			changes = append(changes, change{delivery.EvPullRequestReviewed, map[string]any{"from": pr.Review, "to": status.Review}})
		}
		if status.State != pr.State {
			typ := delivery.EvPullRequestUpdated
			switch status.State {
			case forge.StateMerged:
				typ = delivery.EvPullRequestMerged
			case forge.StateClosed:
				typ = delivery.EvPullRequestClosed
			}
			changes = append(changes, change{typ, map[string]any{"from": pr.State, "to": status.State, "url": status.URL}})
		}
		for _, f := range fresh {
			var path any
			if f.Path != "" {
				path = f.Path
			}
			changes = append(changes, change{delivery.EvPullRequestCommented, map[string]any{
				"feedbackId": f.ID, "author": f.Author, "body": f.Body, "path": path, "kind": f.Kind}})
		}
		for _, c := range changes {
			c.payload["number"], c.payload["repo"] = pr.Number, pr.RepoName
			if _, err := ledger.Append(ctx, tx, ledger.Event{
				Type: c.typ, OrganizationID: org, ProjectID: pr.ProjectID, TaskID: pr.TaskID, RunID: pr.RunID,
				ActorType: ledger.ActorSystem, ActorID: "forge", Source: ledger.SourceGitHub,
				CorrelationID: pr.TaskID, Payload: c.payload,
			}); err != nil {
				return err
			}
		}
		err := tx.QueryRow(ctx, `SELECT id FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')
			LIMIT 1`, pr.TaskID).Scan(&workflowRunID)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}

	signal := forge.Classify(forge.PriorState{State: pr.State, Checks: pr.Checks, Review: pr.Review}, status, fresh, s.FactoryLogins)
	if signal == nil || workflowRunID == "" {
		return nil
	}
	for i := range signal.Feedback {
		signal.Feedback[i].Repo = pr.RepoName
	}
	ids := make([]string, len(fresh))
	for i, f := range fresh {
		ids[i] = f.ID
	}
	// One signal per distinct change, however many deliveries report it.
	// The head is part of it: checks failing again after a fix is a new
	// failure, and must not be taken for the one already handled.
	key := fmt.Sprintf("pr:%s:%s:%s:%s:%s:%s", pr.ID, signal.Kind, strings.Join(ids, ","), status.Checks, status.State, status.HeadSHA)
	return s.Signal(ctx, org, workflowRunID, delivery.SignalPRFeedback, signal, key)
}

// ProcessDeliveries acts on webhook deliveries the backend stored. Each is
// matched to the PR it concerns and that PR is synced; a delivery about
// something dude does not track is marked done without work.
func (s *Syncer) ProcessDeliveries(ctx context.Context) (int, error) {
	type delivery struct {
		ID, Org, Event string
		Payload        json.RawMessage
		Attempts       int
	}
	var batch []delivery
	if err := s.DB.InSystem(ctx, "webhooks", func(tx pgx.Tx) error {
		// A failed delivery waits before its next try, longer each time, so
		// a forge that is briefly down is not asked ten times in a second.
		rows, err := tx.Query(ctx, `SELECT id, organization_id, event, payload, attempts FROM webhook_deliveries
			WHERE processed_at IS NULL AND attempts < 10
			  AND (next_attempt_at IS NULL OR next_attempt_at <= now())
			ORDER BY received_at LIMIT 20`)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[delivery])
		return err
	}); err != nil {
		return 0, err
	}
	for _, d := range batch {
		err := s.process(ctx, d.Org, d.Event, d.Payload)
		if err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
			if err == nil {
				_, e := tx.Exec(ctx, `UPDATE webhook_deliveries SET processed_at = now(), last_error = NULL WHERE id = $1`, d.ID)
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE webhook_deliveries SET attempts = attempts + 1, last_error = $2,
				next_attempt_at = now() + make_interval(secs => 5 * power(2, attempts)) WHERE id = $1`, d.ID, err.Error())
			return e
		}); err != nil {
			return 0, err
		}
		if err != nil {
			s.Log.Warn("webhook delivery failed", "delivery", d.ID, "event", d.Event, "error", err)
		}
	}
	return len(batch), nil
}

// process syncs every tracked PR a delivery is about.
func (s *Syncer) process(ctx context.Context, org, event string, payload json.RawMessage) error {
	var p struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		// pull_request, pull_request_review, pull_request_review_comment
		PullRequest *struct {
			Number int `json:"number"`
		} `json:"pull_request"`
		// issue_comment: a PR's conversation is its issue's.
		Issue *struct {
			Number      int             `json:"number"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		// status, check_suite, check_run: about a commit, not a PR number.
		SHA        string `json:"sha"`
		CheckSuite *struct {
			HeadSHA string `json:"head_sha"`
		} `json:"check_suite"`
		CheckRun *struct {
			HeadSHA string `json:"head_sha"`
		} `json:"check_run"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode %s delivery: %w", event, err)
	}
	var number int
	var sha string
	switch {
	case p.PullRequest != nil:
		number = p.PullRequest.Number
	case p.Issue != nil && len(p.Issue.PullRequest) > 0:
		number = p.Issue.Number
	case p.CheckSuite != nil:
		sha = p.CheckSuite.HeadSHA
	case p.CheckRun != nil:
		sha = p.CheckRun.HeadSHA
	case p.SHA != "":
		sha = p.SHA
	default:
		return nil // not about a pull request
	}

	var prIDs []string
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT pr.id, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.state IN ('draft', 'open') AND (pr.number = $1 OR pr.head_sha = $2)`, number, sha)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, url string
			if err := rows.Scan(&id, &url); err != nil {
				return err
			}
			// The number alone is ambiguous across repositories.
			if strings.EqualFold(forge.SlugFromURL(url), p.Repository.FullName) {
				prIDs = append(prIDs, id)
			}
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range prIDs {
		if err := s.Sync(ctx, org, id); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile syncs open PRs not looked at within `every`: the backstop for
// deliveries GitHub never sent or dude never received.
func (s *Syncer) Reconcile(ctx context.Context, every time.Duration) (int, error) {
	type due struct{ ID, Org string }
	var batch []due
	if err := s.DB.InSystem(ctx, "pr-reconciler", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id FROM pull_requests
			WHERE state IN ('draft', 'open') AND (last_polled_at IS NULL OR last_polled_at < now() - $1::interval)
			ORDER BY last_polled_at NULLS FIRST LIMIT 20`, every.String())
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[due])
		return err
	}); err != nil {
		return 0, err
	}
	for _, pr := range batch {
		if err := s.Sync(ctx, pr.Org, pr.ID); err != nil {
			s.Log.Warn("reconciling pull request failed", "pr", pr.ID, "error", err)
			// Recorded, so the next pass moves on instead of hammering a
			// forge that is refusing.
			_ = s.DB.InOrg(ctx, pr.Org, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE pull_requests SET last_polled_at = now() WHERE id = $1`, pr.ID)
				return err
			})
		}
	}
	return len(batch), nil
}
