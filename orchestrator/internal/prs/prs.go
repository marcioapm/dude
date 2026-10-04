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
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	// How long a new head of a pull request that has had CI may read as
	// having none before that is believed: CI registering on a push takes
	// a moment, and some CI reports late. Zero means DefaultCIGrace.
	CIGrace time.Duration
}

// DefaultCIGrace: how long CI may take to show up on a new head.
const DefaultCIGrace = 10 * time.Minute

type tracked struct {
	ID, ProjectID, TaskID, RunID, State, Checks, Review, HeadSHA, RepoURL, RepoName, Mergeable string
	Number, BehindBy, UnresolvedThreads                                                        int
	HadCI                                                                                      bool
	HeadSeenAt                                                                                 *time.Time
	FeedbackCursor                                                                             *time.Time
	UpdatedAt                                                                                  time.Time
	ReviewsJSON                                                                                []byte
	ChecksJSON                                                                                 []byte
}

// errRaced: another sync of the same pull request recorded first.
var errRaced = errors.New("pull request synced concurrently")

// Sync reads one PR from GitHub, records what changed, and signals the
// workflow if a change is actionable.
//
// Webhook deliveries, the reconciler and a person's action can each sync
// the same pull request at once. Each compares GitHub with what it read
// before asking; two at once would both record the same change. So the
// write is conditional on nothing having been recorded since the read,
// and the one that lost reads again.
func (s *Syncer) Sync(ctx context.Context, org, prID string) error {
	for range 3 {
		if err := s.sync(ctx, org, prID); !errors.Is(err, errRaced) {
			return err
		}
	}
	return s.sync(ctx, org, prID)
}

func (s *Syncer) sync(ctx context.Context, org, prID string) error {
	var pr tracked
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pr.id, pr.project_id, pr.task_id, COALESCE(pr.run_id, ''), pr.state::text,
			pr.checks::text, pr.review::text, COALESCE(pr.head_sha, ''), r.url, r.name, pr.mergeable_state,
			pr.number, pr.behind_by, pr.unresolved_threads, pr.had_ci, pr.head_seen_at, pr.feedback_cursor,
			pr.reviews_json, pr.checks_json, pr.updated_at
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id WHERE pr.id = $1`, prID).
			Scan(&pr.ID, &pr.ProjectID, &pr.TaskID, &pr.RunID, &pr.State, &pr.Checks, &pr.Review, &pr.HeadSHA,
				&pr.RepoURL, &pr.RepoName, &pr.Mergeable, &pr.Number, &pr.BehindBy, &pr.UnresolvedThreads,
				&pr.HadCI, &pr.HeadSeenAt, &pr.FeedbackCursor, &pr.ReviewsJSON, &pr.ChecksJSON, &pr.UpdatedAt)
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
	// No CI reports unknown, and so does a new head before CI has
	// registered on it. On a pull request that has had CI, a head within
	// its grace is CI yet to start, not a green light; past it, unknown is
	// what it says — a commit CI skips (path filters, [skip ci]) — or the
	// pull request would wait on it forever.
	headSeenAt := time.Now()
	newHead := status.HeadSHA != pr.HeadSHA
	if !newHead && pr.HeadSeenAt != nil {
		headSeenAt = *pr.HeadSeenAt
	}
	grace := s.CIGrace
	if grace == 0 {
		grace = DefaultCIGrace
	}
	hadCI := pr.HadCI || status.Checks != forge.ChecksUnknown
	if status.Checks == forge.ChecksUnknown && pr.HadCI && time.Since(headSeenAt) < grace {
		status.Checks = forge.ChecksPending
	}
	// A head dude did not push: a person pushed, or GitHub's Update
	// branch did. The next fix starts from it (delivery prFix).
	var pusher string
	if newHead && pr.HeadSHA != "" && isOpen(status.State) {
		ours, err := s.pushedByDude(ctx, org, pr.TaskID, status.HeadSHA)
		if err != nil {
			return err
		}
		if !ours {
			if pusher, err = gh.CommitAuthor(ctx, slug, status.HeadSHA); err != nil {
				return err
			}
		}
	}
	since := ""
	if pr.FeedbackCursor != nil {
		since = pr.FeedbackCursor.UTC().Format(time.RFC3339)
	}
	listed, err := gh.Feedback(ctx, slug, status, since)
	if err != nil {
		return err
	}

	// fresh is feedback not recorded yet; edited is feedback recorded
	// before, whose words now address dude and have reached no conductor
	// yet: a comment edited into a mention. A mention is delivered once
	// per comment, by Chat's own record of it (delivery.MentionTx), not by
	// whether the comment was recorded.
	var fresh, edited []forge.Feedback
	var workflowRunID, workflowStep string
	var recorded []string // the events this sync appended: what changed
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		// The cursor narrows the query; the ledger decides what is new.
		// Feedback at the cursor's own second comes back every time, because
		// the listing is inclusive, so anything already recorded is dropped
		// by id.
		ids := make([]string, len(listed))
		for i, f := range listed {
			ids[i] = f.ID
		}
		rows, err := tx.Query(ctx, `SELECT payload->>'feedbackId' FROM events WHERE task_id = $1
			AND event_type = $2 AND payload->>'feedbackId' = ANY($3)`, pr.TaskID, delivery.EvPullRequestCommented, ids)
		if err != nil {
			return err
		}
		seen, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT payload->'github'->>'feedbackId' FROM events WHERE task_id = $1
			AND event_type = $2 AND payload->'github'->>'feedbackId' = ANY($3)`, pr.TaskID, delivery.EvChatMessage, ids)
		if err != nil {
			return err
		}
		mentioned, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, f := range listed {
			switch {
			case !slices.Contains(seen, f.ID):
				fresh = append(fresh, f)
			case !slices.Contains(mentioned, f.ID) && forge.AddressedToDude(f, s.FactoryLogins):
				edited = append(edited, f)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Who may wake a fixer: asked of GitHub (cached), before anything is
	// recorded, so each comment says whether it could.
	waking, err := s.wakers(ctx, org, gh, slug, append(slices.Clip(fresh), edited...))
	if err != nil {
		return err
	}
	if status.ThreadsUnknown {
		status.UnresolvedThreads = pr.UnresolvedThreads
	}
	// GitHub works mergeability out lazily: unknown for a moment after a
	// push or main moving. That is not a reading; the last one stands, or a
	// conflicting pull request would read as ready in between.
	if status.Mergeable == forge.MergeUnknown {
		status.Mergeable, status.BehindBy = pr.Mergeable, pr.BehindBy
	}
	checksJSON, _ := json.Marshal(db.NonNil(status.CheckList))
	reviewsJSON, _ := json.Marshal(db.NonNil(status.Reviews))
	if !isOpen(status.State) {
		// What it was when it closed is what it stays.
		status.Mergeable, status.BehindBy, status.UnresolvedThreads = pr.Mergeable, pr.BehindBy, pr.UnresolvedThreads
	}

	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		cursor := pr.FeedbackCursor
		if n := len(listed); n > 0 {
			// An edited comment lists by its edit but sorts by its creation:
			// the cursor never moves back to it.
			if t, err := time.Parse(time.RFC3339, listed[n-1].CreatedAt); err == nil && (cursor == nil || t.After(*cursor)) {
				cursor = &t
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE pull_requests SET state = $2::pull_request_state, checks = $3::check_state,
			review = $4::review_state, head_sha = $5, last_polled_at = now(), feedback_cursor = $6, updated_at = now(),
			had_ci = $8, head_seen_at = $7, mergeable_state = $9, behind_by = $10, checks_json = $11::jsonb,
			reviews_json = $12::jsonb, unresolved_threads = $13,
			merged_at = CASE WHEN $2 = 'merged' THEN COALESCE(merged_at, now()) ELSE merged_at END,
			closed_at = CASE WHEN $2 = 'closed' THEN COALESCE(closed_at, now()) ELSE closed_at END
			WHERE id = $1 AND updated_at = $14`, pr.ID, status.State, status.Checks, status.Review, status.HeadSHA, cursor, headSeenAt, hadCI,
			status.Mergeable, status.BehindBy, checksJSON, reviewsJSON, status.UnresolvedThreads, pr.UpdatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errRaced
		}

		// One event per thing that changed. "Nothing happened" is not an
		// occurrence, so an unchanged PR records nothing.
		type change struct {
			typ     string
			payload map[string]any
		}
		var changes []change
		// Whether the checks could be read at all changes what a person is
		// told even while the rollup stays pending; job details do not.
		fromDiagnostic, diagnostic := storedDiagnostic(pr.ChecksJSON), forge.CheckDiagnostic(status.CheckList)
		if status.Checks != pr.Checks || diagnostic != fromDiagnostic {
			p := map[string]any{"from": pr.Checks, "to": status.Checks}
			if failing := failingNames(status.CheckList); len(failing) > 0 {
				p["failing"] = failing
			}
			if fromDiagnostic != "" {
				p["fromDiagnostic"] = fromDiagnostic
			}
			if diagnostic != "" {
				p["diagnostic"] = diagnostic
			}
			changes = append(changes, change{delivery.EvPullRequestChecks, p})
		}
		if byWhom := newReviews(pr.ReviewsJSON, status.Reviews); status.Review != pr.Review || len(byWhom) > 0 {
			changes = append(changes, change{delivery.EvPullRequestReviewed, map[string]any{"from": pr.Review, "to": status.Review,
				"reviews": byWhom}})
		}
		if status.Mergeable != pr.Mergeable && status.Mergeable != forge.MergeUnknown || status.BehindBy != pr.BehindBy {
			changes = append(changes, change{delivery.EvPullRequestMergeable, map[string]any{"from": pr.Mergeable,
				"to": status.Mergeable, "behindBy": status.BehindBy}})
		}
		if pusher != "" {
			changes = append(changes, change{delivery.EvPullRequestPushed, map[string]any{"author": pusher,
				"from": pr.HeadSHA, "to": status.HeadSHA}})
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
			p := map[string]any{"feedbackId": f.ID, "author": f.Author, "body": f.Body, "path": path, "kind": f.Kind}
			if f.URL != "" {
				p["url"] = f.URL
			}
			addressed := forge.AddressedToDude(f, s.FactoryLogins)
			switch {
			case forge.Own(f):
				p["own"] = true
			case !waking[f.Author] && (addressed || forge.IsActionableComment(f, s.FactoryLogins)):
				// Shown on the task, not acted on: say why.
				p["ignored"] = "not_permitted"
			case addressed:
				// A message to the task's conductor, not a fixer's.
				p["addressed"] = "conductor"
			}
			changes = append(changes, change{delivery.EvPullRequestCommented, p})
		}
		for _, c := range changes {
			c.payload["number"], c.payload["repo"] = pr.Number, pr.RepoName
			id, err := ledger.Append(ctx, tx, ledger.Event{
				Type: c.typ, OrganizationID: org, ProjectID: pr.ProjectID, TaskID: pr.TaskID, RunID: pr.RunID,
				ActorType: ledger.ActorSystem, ActorID: "forge", Source: ledger.SourceGitHub,
				CorrelationID: pr.TaskID, Payload: c.payload,
			})
			if err != nil {
				return err
			}
			recorded = append(recorded, id)
		}
		// Comments addressed to dude, by people who may address it: each a
		// message to the task's conductor, with the record of it, so a
		// comment recorded is never one whose message was lost.
		for _, f := range append(slices.Clip(fresh), edited...) {
			if !waking[f.Author] || !forge.AddressedToDude(f, s.FactoryLogins) {
				continue
			}
			if _, err := delivery.MentionTx(ctx, tx, delivery.Mention{Org: org, ProjectID: pr.ProjectID, TaskID: pr.TaskID,
				Repo: pr.RepoName, Number: pr.Number, Feedback: f}); err != nil {
				return err
			}
		}
		// The delivery the pull request is from: a task started over has a
		// delivery per attempt, each on its own branch, and an earlier
		// attempt's pull request must not wake the next one.
		var decider string
		err = tx.QueryRow(ctx, `SELECT w.id, w.step, COALESCE(w.state->>'decider', '') FROM workflow_runs w
			JOIN pull_requests p ON p.id = $2
			WHERE w.task_id = $1 AND w.status IN ('running', 'waiting')
			  AND COALESCE(w.state->>'branch', p.head_branch) = p.head_branch
			LIMIT 1`, pr.TaskID, pr.ID).Scan(&workflowRunID, &workflowStep, &decider)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		// Its end is the conductor's to close out, recorded here with the
		// end itself: the workflow may be waiting on something else (a
		// person's decision on a sibling closed), and hear of it never.
		if status.State != pr.State && decider == delivery.DeciderConductor {
			return delivery.CloseOutTx(ctx, tx, org, pr.TaskID, pr.RepoName, pr.Number, status.State)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var permitted []forge.Feedback
	for _, f := range fresh {
		if waking[f.Author] {
			permitted = append(permitted, f)
		}
	}
	prior := forge.Status{PullRequestRef: forge.PullRequestRef{State: pr.State, HeadSHA: pr.HeadSHA}, Checks: pr.Checks,
		Review: pr.Review, Mergeable: pr.Mergeable, UnresolvedThreads: pr.UnresolvedThreads}
	signal := forge.Classify(prior, status, permitted, s.FactoryLogins)
	stuckFor := 0 // how many of the organization's patiences checks have been pending on this head
	if signal == nil && status.Checks == forge.ChecksPending && isOpen(status.State) {
		stuckFor = int(time.Since(headSeenAt) / gh.Settings.CIStuck())
	}
	if stuckFor > 0 {
		// Pending past the organization's patience on this head: CI that
		// never reports (a runner gone, a required check nobody runs).
		signal = &forge.Signal{Kind: "ci_stuck"}
	}
	if signal != nil && workflowRunID != "" {
		if err := s.signal(ctx, org, pr, status, signal, permitted, recorded, stuckFor, workflowRunID); err != nil {
			return err
		}
	}
	// Behind and clean: brought up to date on GitHub, if the organization
	// wants that — after the workflow has heard what this sync found, so a
	// failure here loses nothing, and only while it waits on the pull
	// request: a fix at work starts from the old head, and a merge commit
	// under it would refuse its push.
	if status.Mergeable == forge.MergeBehind && gh.Settings.WhenBehind == "update" && workflowStep == "awaitPullRequest" {
		s.updateBranch(ctx, org, gh, slug, pr, status.HeadSHA)
	}
	return nil
}

// signal tells the workflow what a sync found. One signal per distinct
// change, however many deliveries report it.
func (s *Syncer) signal(ctx context.Context, org string, pr tracked, status forge.Status, signal *forge.Signal,
	permitted []forge.Feedback, recorded []string, stuckFor int, workflowRunID string) error {
	signal.Repo, signal.Number = pr.RepoName, pr.Number
	for i := range signal.Feedback {
		signal.Feedback[i].Repo = pr.RepoName
	}
	ids := make([]string, len(permitted))
	for i, f := range permitted {
		ids[i] = f.ID
	}
	// One signal per distinct change, however many deliveries report it.
	// The head is part of it: checks failing again after a fix is a new
	// failure, and must not be taken for the one already handled.
	key := fmt.Sprintf("pr:%s:%s:%s:%s:%s:%s", pr.ID, signal.Kind, strings.Join(ids, ","), status.Checks, status.State,
		status.HeadSHA)
	// Readiness can come and go on the same head (approved, dismissed,
	// approved again), and so can a conflict (resolved, then main moves
	// into it again): each time is its own change, recorded as its own
	// event. A second delivery of the same change records none, and is
	// not classified as a change at all.
	if signal.Kind == "readiness" || signal.Kind == "conflict" {
		key += ":" + strings.Join(recorded, ",")
	}
	// Still stuck after a person chose to wait: asked again once as long
	// has passed again, not at every sync, and not never.
	if signal.Kind == "ci_stuck" {
		key += fmt.Sprintf(":stuck:%d", stuckFor)
	}
	return s.Signal(ctx, org, workflowRunID, delivery.SignalPRFeedback, signal, key)
}

// pushedByDude says whether one of the task's Runs pushed a commit: lux
// reports each push before dude fast-forwards the branch to it, so a sync
// in between sees dude's own commit as the new head.
func (s *Syncer) pushedByDude(ctx context.Context, org, taskID, sha string) (bool, error) {
	var ours bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r,
			jsonb_array_elements(COALESCE(r.push_result->'results', '[]'::jsonb)) x
			WHERE r.task_id = $1 AND x->>'commit' = $2)`, taskID, sha).Scan(&ours)
	})
	return ours, err
}

func isOpen(state string) bool { return state == forge.StateOpen || state == forge.StateDraft }

func failingNames(checks []forge.Check) []string {
	var out []string
	for _, c := range checks {
		if c.Failed() {
			out = append(out, c.Name)
		}
	}
	return out
}

// storedDiagnostic is the diagnostic recorded in checks_json by the last
// sync; an unreadable column is none, as rows from before diagnostics are.
func storedDiagnostic(checksJSON []byte) string {
	var checks []forge.Check
	_ = json.Unmarshal(checksJSON, &checks)
	return forge.CheckDiagnostic(checks)
}

// newReviews: the reviewers whose word changed since the last sync, for
// the activity ("Cy requested changes").
func newReviews(before []byte, after []forge.Review) []forge.Review {
	var prior []forge.Review
	_ = json.Unmarshal(before, &prior)
	was := map[string]string{}
	for _, r := range prior {
		was[r.Login] = r.State
	}
	out := []forge.Review{}
	for _, r := range after {
		if was[r.Login] != r.State && r.State != "REQUESTED" {
			out = append(out, r)
		}
	}
	return out
}

// permissionTTL: how long what GitHub said of a login is believed. Long
// enough that a busy pull request asks once; short enough that access
// granted or taken away counts the same day.
const permissionTTL = time.Hour

// wakers says, for each author of fresh feedback, whether their comments
// may wake a fixer under the organization's rule.
//
// GitHub refusing to say (a token without the scope to read members or
// collaborators) is "may not": the comment is recorded and shown, not
// acted on, and the pull request goes on syncing. Only a failure GitHub
// may not repeat fails the sync, to be tried again.
func (s *Syncer) wakers(ctx context.Context, org string, gh *forge.GitHub, slug string, fresh []forge.Feedback) (map[string]bool, error) {
	out := map[string]bool{}
	who := gh.Settings.WhoCanWake
	for _, f := range fresh {
		if _, done := out[f.Author]; done {
			continue
		}
		if who == forge.WakeAnyone {
			out[f.Author] = true
			continue
		}
		permission, err := s.cached(ctx, org, "collaborator", slug, f.Author, func() (string, error) {
			return gh.Permission(ctx, slug, f.Author)
		})
		if forge.Refused(err) {
			s.Log.Warn("GitHub would not say whether a commenter may wake a fixer", "login", f.Author, "error", err)
			permission = "none"
		} else if err != nil {
			return nil, err
		}
		member := false
		if who == forge.WakeMembers && !forge.CanWrite(permission) {
			owner, _, _ := strings.Cut(slug, "/")
			v, err := s.cached(ctx, org, "member", owner, f.Author, func() (string, error) {
				ok, err := gh.Member(ctx, owner, f.Author)
				if ok {
					return "member", err
				}
				return "none", err
			})
			if forge.Refused(err) {
				s.Log.Warn("GitHub would not say whether a commenter is a member", "login", f.Author, "error", err)
			} else if err != nil {
				return nil, err
			}
			member = v == "member"
		}
		out[f.Author] = forge.MayWake(who, permission, member)
	}
	return out, nil
}

// cached answers a question about a login from forge_permissions, asking
// GitHub when the answer is missing or stale.
func (s *Syncer) cached(ctx context.Context, org, kind, scope, login string, ask func() (string, error)) (string, error) {
	var value string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT value FROM forge_permissions WHERE kind = $1 AND scope = $2 AND login = $3
			AND checked_at > now() - $4::interval`, kind, scope, login, permissionTTL.String()).Scan(&value)
	})
	if err == nil {
		return value, nil
	}
	if !db.IsNotFound(err) {
		return "", err
	}
	if value, err = ask(); err != nil {
		return "", err
	}
	return value, s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO forge_permissions (organization_id, kind, scope, login, value)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (organization_id, kind, scope, login)
			DO UPDATE SET value = EXCLUDED.value, checked_at = now()`, org, kind, scope, login, value)
		return err
	})
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

	// A comment or review on any tracked pull request, merged or closed
	// too: a mention there still reaches the task's conductor, and a
	// terminal state reads no fixer feedback (forge.Classify).
	discussed := event == "issue_comment" || event == "pull_request_review" || event == "pull_request_review_comment"
	var prIDs []string
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		// A closed pull request too, when GitHub says something happened to
		// it (reopened): otherwise its reopening would never be read.
		rows, err := tx.Query(ctx, `SELECT pr.id, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE (pr.state IN ('draft', 'open') OR $3 AND pr.state = 'closed' OR $4) AND (pr.number = $1 OR pr.head_sha = $2)`,
			number, sha, event == "pull_request", discussed && number > 0)
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

// endedPollWindow: how long after a pull request is merged or closed the
// reconciler still reads it, for a mention whose webhook was lost. Past
// it, only a webhook reads it: polling every ended pull request forever
// would spend the rate limit on conversations that have gone quiet.
const endedPollWindow = 7 * 24 * time.Hour

// Reconcile syncs open PRs, and recently ended ones, not looked at within
// `every`: the backstop for deliveries GitHub never sent or dude never
// received.
func (s *Syncer) Reconcile(ctx context.Context, every time.Duration) (int, error) {
	type due struct{ ID, Org string }
	var batch []due
	if err := s.DB.InSystem(ctx, "pr-reconciler", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id FROM pull_requests
			WHERE (state IN ('draft', 'open') OR COALESCE(merged_at, closed_at) > now() - $2::interval)
			  AND (last_polled_at IS NULL OR last_polled_at < now() - $1::interval)
			ORDER BY last_polled_at NULLS FIRST LIMIT 20`, every.String(), endedPollWindow.String())
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

// updateBranch brings a pull request that fell behind up to date on
// GitHub, once per head: recorded as dude's action, so the next sync of
// the same head does not ask again. A failure GitHub may not repeat (a
// rate limit, an outage) is not recorded, and the next sync tries again;
// a refusal is, with GitHub's reason. Best effort either way: what the
// sync read is recorded already.
func (s *Syncer) updateBranch(ctx context.Context, org string, gh *forge.GitHub, slug string, pr tracked, head string) {
	var tried bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
			AND payload->>'action' = 'update-branch' AND payload->>'head' = $3)`, pr.TaskID, delivery.EvPullRequestAction, head).Scan(&tried)
	}); err != nil || tried {
		return
	}
	err := gh.UpdateBranch(ctx, slug, pr.Number, head)
	if err != nil && forge.Transient(err) {
		s.Log.Info("updating a pull request's branch failed; the next sync tries again", "pr", pr.ID, "error", err)
		return
	}
	payload := map[string]any{"action": "update-branch", "head": head, "number": pr.Number, "repo": pr.RepoName}
	if err != nil {
		payload["refused"] = err.Error()
	}
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvPullRequestAction, OrganizationID: org,
			ProjectID: pr.ProjectID, TaskID: pr.TaskID, ActorType: ledger.ActorSystem, ActorID: "workflow",
			Source: ledger.SourceOrchestrator, CorrelationID: pr.TaskID, Payload: payload})
		return err
	}); err != nil {
		s.Log.Warn("recording a branch update failed", "pr", pr.ID, "error", err)
	}
}
