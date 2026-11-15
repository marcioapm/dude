package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// dude's own threads (design §6.4): each inline comment of dude's reviews
// is recorded with the finding it is for (review_submitted's comments).
// After each verdict, a thread whose finding is settled gets a reply and
// is resolved: fixed or superseded, "Fixed at <sha>."; withdrawn by the
// conductor, "Withdrawn: <note>.". A finding still open is not commented
// on again; the review's body lists it. A thread a person resolved is
// left alone. Best effort: a failure is recorded, never retried, and
// never holds the verdict.

// The thread events, on the task: {pullRequestId, findingId, commentId, head, ...}.
const (
	EvReviewThreadReplied    = "review.thread_replied"     // {status, replyId}
	EvReviewThreadResolved   = "review.thread_resolved"    // {threadId}
	EvReviewThreadSyncFailed = "review.thread_sync_failed" // {step, status, message}
)

// inlinePost is an inline comment of dude's for a finding, at the head its
// review was posted at.
type inlinePost struct {
	FindingID string `json:"findingId"`
	CommentID int64  `json:"commentId"`
	URL       string `json:"url,omitempty"`
	Head      string `json:"-"`
}

// postedComments is review_submitted's record of the review's line
// comments read back (got): "comments", each with the finding it is for,
// matched by path and body as postReview made them (ids[i] is
// comments[i]'s), and "unmatched", the findings whose comment was not found.
func postedComments(got []forge.PostedComment, comments []forge.ReviewComment, ids []string) map[string]any {
	used := make([]bool, len(comments))
	var posts []inlinePost
	for _, c := range got {
		for i, m := range comments {
			if !used[i] && i < len(ids) && m.Path == c.Path && strings.TrimSpace(m.Body) == strings.TrimSpace(c.Body) {
				used[i] = true
				posts = append(posts, inlinePost{FindingID: ids[i], CommentID: c.ID, URL: c.URL})
				break
			}
		}
	}
	var unmatched []string
	for i, id := range ids {
		if i >= len(used) || !used[i] {
			unmatched = append(unmatched, id)
		}
	}
	out := map[string]any{}
	if len(posts) > 0 {
		out["comments"] = posts
	}
	if len(unmatched) > 0 {
		out["unmatched"] = unmatched
	}
	return out
}

// earlierPostsTx is, per finding, the latest inline comment dude's reviews
// of the task made for it on pull request prID.
func earlierPostsTx(ctx context.Context, tx pgx.Tx, taskID, prID string) (map[string]inlinePost, error) {
	rows, err := tx.Query(ctx, `SELECT c->>'findingId', (c->>'commentId')::bigint, COALESCE(c->>'url', ''), e.payload->>'head'
		FROM events e, jsonb_array_elements(e.payload->'comments') c
		WHERE e.task_id = $1 AND e.event_type = $2 AND e.payload->>'pullRequestId' = $3 AND jsonb_typeof(e.payload->'comments') = 'array'
		ORDER BY e.occurred_at, e.id`, taskID, EvReviewSubmitted, prID)
	if err != nil {
		return nil, err
	}
	out := map[string]inlinePost{}
	var p inlinePost
	_, err = pgx.ForEachRow(rows, []any{&p.FindingID, &p.CommentID, &p.URL, &p.Head}, func() error {
		out[p.FindingID] = p
		return nil
	})
	return out, err
}

// settledThread is an earlier post whose finding is settled, with what
// was done with its thread already.
type settledThread struct {
	inlinePost
	Status, Note     string
	Replied, Settled bool
}

// syncThreads replies in and resolves dude's threads whose findings are
// settled, after the verdict at head was posted.
func (w *steps) syncThreads(ctx context.Context, sc workflow.StepContext, st *State, gh *forge.GitHub, pr PullRequestState, head string) {
	if gh == nil {
		return
	}
	var due []settledThread
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		posts, err := earlierPostsTx(ctx, tx, st.TaskID, pr.ID)
		if err != nil || len(posts) == 0 {
			return err
		}
		for _, p := range posts {
			t := settledThread{inlinePost: p}
			err := tx.QueryRow(ctx, `SELECT f.status::text, COALESCE(f.resolution_note, ''),
					EXISTS (SELECT 1 FROM events e WHERE e.task_id = f.task_id AND e.event_type = $3 AND (e.payload->>'commentId')::bigint = $2),
					EXISTS (SELECT 1 FROM events e WHERE e.task_id = f.task_id AND e.event_type IN ($4, $5) AND (e.payload->>'commentId')::bigint = $2)
				FROM review_findings f WHERE f.id = $1`, p.FindingID, p.CommentID, EvReviewThreadReplied, EvReviewThreadResolved,
				EvReviewThreadSyncFailed).Scan(&t.Status, &t.Note, &t.Replied, &t.Settled)
			if err != nil {
				return err
			}
			if t.Status != "open" && !t.Settled {
				due = append(due, t)
			}
		}
		return nil
	}); err != nil {
		slog.Warn("reading dude's threads to keep in step failed", "task", st.TaskID, "error", err)
		return
	}
	for _, t := range due {
		base := map[string]any{"pullRequestId": pr.ID, "findingId": t.FindingID, "commentId": t.CommentID, "head": head}
		with := func(kv ...any) map[string]any {
			out := maps.Clone(base)
			for i := 0; i+1 < len(kv); i += 2 {
				out[kv[i].(string)] = kv[i+1]
			}
			return out
		}
		failed := func(step string, err error) {
			status, msg := forgeFailure(err)
			_ = w.s.Emit(ctx, sc.OrganizationID, st, EvReviewThreadSyncFailed, with("step", step, "status", status, "message", msg))
		}
		thread, err := gh.ThreadOfComment(ctx, pr.Slug, pr.Number, t.CommentID)
		if err != nil {
			failed("read", err)
			continue
		}
		if thread == nil || thread.Resolved {
			// A person resolved it (or it is gone): theirs, left alone.
			continue
		}
		// Crash recovery only: a step that died between the reply and the
		// resolve (a failed resolve is recorded, settling the thread) only
		// resolves on its next run.
		if !t.Replied {
			text := fmt.Sprintf("Fixed at %s.", short(head))
			if t.Status == "accepted" {
				text = "Withdrawn: " + withdrawnNote(t.Note) + "."
			}
			posted, err := gh.ReplyToLineComment(ctx, pr.Slug, pr.Number, t.CommentID, text)
			if err != nil {
				failed("reply", err)
				continue
			}
			if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewThreadReplied, with("status", t.Status, "replyId", posted.ID)); err != nil {
				slog.Warn("recording dude's thread reply failed", "task", st.TaskID, "error", err)
				continue
			}
		}
		if err := gh.ResolveReviewThread(ctx, pr.Slug, thread.ID); err != nil {
			failed("resolve", err)
			continue
		}
		_ = w.s.Emit(ctx, sc.OrganizationID, st, EvReviewThreadResolved, with("threadId", thread.ID))
	}
}

// DudesThreadsTx is, for a review-only task, the GitHub ids of dude's own
// line comments on its pull requests, each with the finding it is for
// ("" for one that is no finding's): its reviews' inline comments
// (review_submitted's comments), its replies in those threads
// (thread_replied's replyId) and its conductor's replies (a chat.message
// posted on GitHub). A line comment answering one of them is addressed to
// dude (forge.RepliesToDude). nil for a writing task: its routing is a
// mention's alone.
func DudesThreadsTx(ctx context.Context, tx pgx.Tx, taskID string) (map[int64]string, error) {
	if ok, err := ReviewOnlyTx(ctx, tx, taskID); err != nil || !ok {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT c.id, c.finding FROM events e, LATERAL (
			SELECT (x->>'commentId')::bigint, COALESCE(x->>'findingId', '') FROM jsonb_array_elements(
				CASE WHEN e.event_type = $2 AND jsonb_typeof(e.payload->'comments') = 'array' THEN e.payload->'comments' END) x
			UNION ALL SELECT substring(e.payload->>'replyId' FROM '^line-comment-([0-9]+)$')::bigint, '' WHERE e.event_type = $3
			UNION ALL SELECT substring(e.payload->'github'->>'feedbackId' FROM '^line-comment-([0-9]+)$')::bigint, ''
				WHERE e.event_type = $4 AND e.payload->>'by' = 'conductor') c(id, finding)
		WHERE e.task_id = $1 AND e.event_type IN ($2, $3, $4) AND c.id IS NOT NULL`,
		taskID, EvReviewSubmitted, EvReviewThreadReplied, EvChatMessage)
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	var id int64
	var finding string
	_, err = pgx.ForEachRow(rows, []any{&id, &finding}, func() error {
		if out[id] == "" {
			out[id] = finding
		}
		return nil
	})
	return out, err
}

// threadFindingTx is the finding dude's thread is about, as the wake of
// a reply in it names it: nil when it is no finding's.
func threadFindingTx(ctx context.Context, tx pgx.Tx, findingID string) (*verdictFinding, error) {
	if findingID == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, severity::text, title, description, suggested_fix, COALESCE(acceptance, ''),
			COALESCE(file, ''), COALESCE(line, 0), COALESCE(trivial, false), introduced
		FROM review_findings WHERE id = $1`, findingID)
	if err != nil {
		return nil, err
	}
	f, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[verdictFinding])
	if db.IsNotFound(err) {
		return nil, nil
	}
	return &f, err
}

// withdrawnNote is the conductor's reason a finding was withdrawn, as the
// thread's reply ("Withdrawn: <note>.") gives it. A writing task's reason
// is free text, which may start "withdrawn:"; a review-only task's starts
// with its grounds, "wrong: <evidence>" or "duplicate: <id> …". The
// leading word is dropped, except that a duplicate reads "duplicate of
// <id> …".
func withdrawnNote(note string) string {
	note = strings.TrimPrefix(note, DismissedByConductor)
	for _, p := range []struct{ word, as string }{{"withdrawn:", ""}, {"wrong:", ""}, {"duplicate:", "duplicate of "}} {
		// Compared bytewise: a rune whose lowercase is longer cannot match.
		if n := len(p.word); len(note) >= n && strings.EqualFold(note[:n], p.word) {
			note = p.as + strings.TrimSpace(note[n:])
			break
		}
	}
	return strings.TrimRight(strings.TrimSpace(note), ".")
}
