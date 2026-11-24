package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
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
// of the task made for it on pull request prID, but for those of the
// reviews claimed as skip (a review's own comments are not earlier than it).
func earlierPostsTx(ctx context.Context, tx pgx.Tx, taskID, prID string, skip ...string) (map[string]inlinePost, error) {
	rows, err := tx.Query(ctx, `SELECT c->>'findingId', (c->>'commentId')::bigint, COALESCE(c->>'url', ''), e.payload->>'head'
		FROM events e, jsonb_array_elements(e.payload->'comments') c
		WHERE e.task_id = $1 AND e.event_type = $2 AND e.payload->>'pullRequestId' = $3 AND jsonb_typeof(e.payload->'comments') = 'array'
			AND COALESCE(e.payload->>'claimId', '') <> ALL($4::text[])
		ORDER BY e.occurred_at, e.id`, taskID, EvReviewSubmitted, prID, append([]string{}, skip...))
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

// claimPostedTx says whether review_submitted recorded the review
// submitted under claim with any inline comment read back, and whether
// that record can be trusted (false: unread, see reviewPosts).
func claimPostedTx(ctx context.Context, tx pgx.Tx, taskID, claim string) (posted, read bool, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE(bool_or(jsonb_typeof(payload->'comments') = 'array'), false),
			NOT COALESCE(bool_or((payload->>'unread')::boolean), false)
		FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'claimId' = $3`,
		taskID, EvReviewSubmitted, claim).Scan(&posted, &read)
	return posted, read, err
}

// settledThread is an earlier post whose finding is settled, with what
// was done with its thread already.
type settledThread struct {
	inlinePost
	Status, Note     string
	Replied, Settled bool
}

// kept says the thread's finding was kept as a suggestion (keptSuggestion).
func (t settledThread) kept() bool {
	_, _, ok := keptSuggestion(t.Note)
	return ok && t.Status == "accepted"
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
			var bornKept bool
			err := tx.QueryRow(ctx, `SELECT f.status::text, COALESCE(f.resolution_note, ''),
					EXISTS (SELECT 1 FROM events e WHERE e.task_id = f.task_id AND e.event_type = $3 AND (e.payload->>'commentId')::bigint = $2),
					EXISTS (SELECT 1 FROM events e WHERE e.task_id = f.task_id AND e.event_type IN ($4, $5) AND (e.payload->>'commentId')::bigint = $2),
					EXISTS (SELECT 1 FROM events s, jsonb_array_elements(CASE WHEN jsonb_typeof(s.payload->'comments') = 'array' THEN s.payload->'comments' END) c, events r
						WHERE s.task_id = f.task_id AND s.event_type = $6 AND (c->>'commentId')::bigint = $2
						AND r.task_id = f.task_id AND r.event_type = $7 AND r.payload->>'findingId' = f.id AND r.occurred_at < s.occurred_at)
				FROM review_findings f WHERE f.id = $1`, p.FindingID, p.CommentID, EvReviewThreadReplied, EvReviewThreadResolved,
				EvReviewThreadSyncFailed, EvReviewSubmitted, EvFindingResolved).Scan(&t.Status, &t.Note, &t.Replied, &t.Settled, &bornKept)
			if err != nil {
				return err
			}
			// A comment made for a finding already kept as a suggestion
			// says so itself (inlineText): its thread is left open, for
			// the author, with no reply of dude's. One kept after its
			// comment was posted is replied to once and left open too:
			// replied, it is done with.
			if t.kept() && (bornKept || t.Replied) {
				continue
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
			posted, err := gh.ReplyToLineComment(ctx, pr.Slug, pr.Number, t.CommentID, threadReplyText(t.Status, t.Note, head))
			if err != nil {
				failed("reply", err)
				continue
			}
			if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewThreadReplied, with("status", t.Status, "replyId", posted.ID)); err != nil {
				slog.Warn("recording dude's thread reply failed", "task", st.TaskID, "error", err)
				continue
			}
		}
		if t.kept() {
			// A suggestion is the author's to take or leave: its thread
			// stays open.
			continue
		}
		if err := gh.ResolveReviewThread(ctx, pr.Slug, thread.ID); err != nil {
			failed("resolve", err)
			continue
		}
		_ = w.s.Emit(ctx, sc.OrganizationID, st, EvReviewThreadResolved, with("threadId", thread.ID))
	}
}

// DudesThreadsTx is, for a review-only task, dude's own review threads on
// its pull requests (forge.Threads): its reviews' inline comments
// (review_submitted's comments), each a thread's root with the finding it
// is for, and its replies in them (thread_replied's replyId), dude's own.
// A line comment answering a root is addressed to dude
// (forge.RepliesToDude). nil for a writing task: its routing is a
// mention's alone.
func DudesThreadsTx(ctx context.Context, tx pgx.Tx, taskID string) (*forge.Threads, error) {
	if ok, err := ReviewOnlyTx(ctx, tx, taskID); err != nil || !ok {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT c.id, c.finding, c.root FROM events e, LATERAL (
			SELECT (x->>'commentId')::bigint, COALESCE(x->>'findingId', ''), true FROM jsonb_array_elements(
				CASE WHEN e.event_type = $2 AND jsonb_typeof(e.payload->'comments') = 'array' THEN e.payload->'comments' END) x
			UNION ALL SELECT substring(e.payload->>'replyId' FROM '^line-comment-([0-9]+)$')::bigint, '', false WHERE e.event_type = $3
		) c(id, finding, root)
		WHERE e.task_id = $1 AND e.event_type IN ($2, $3) AND c.id IS NOT NULL`,
		taskID, EvReviewSubmitted, EvReviewThreadReplied)
	if err != nil {
		return nil, err
	}
	out := &forge.Threads{Roots: map[int64]string{}, Own: map[int64]bool{}}
	var id int64
	var finding string
	var root bool
	_, err = pgx.ForEachRow(rows, []any{&id, &finding, &root}, func() error {
		switch {
		case !root:
			out.Own[id] = true
		case out.Roots[id] == "":
			out.Roots[id] = finding
		}
		return nil
	})
	return out, err
}

// threadReplyText is the reply syncThreads posts in dude's thread for a
// finding of status ("accepted" is withdrawn, or kept as a suggestion) at
// head: "Withdrawn: <note>.", "Kept as a suggestion (<gate>): <why>." or
// "Fixed at <sha>.". dudesThreadReply recognises each.
func threadReplyText(status, note, head string) string {
	if status == "accepted" {
		if gate, why, ok := keptSuggestion(note); ok {
			return keptText(gate, why)
		}
		return "Withdrawn: " + withdrawnNote(note) + "."
	}
	return fmt.Sprintf("Fixed at %s.", short(head))
}

// dudesThreadReply is the words of a reply syncThreads posts in dude's
// thread (threadReplyText): "Fixed at <sha>.", "Withdrawn: <note>." or
// "Kept as a suggestion (<gate>): <why>.".
var dudesThreadReply = regexp.MustCompile(`^(?:Fixed at \S+|(?s:Withdrawn: .*)|(?s:Kept as a suggestion \([a-z ]+\): .*))\.$`)

// OwnThreadReplies adds to a review-only task's threads (DudesThreadsTx)
// dude's replies in them not recorded yet: a reply posted and listed
// before its thread_replied event, or whose event was lost. They are line
// comments by self, the account dude posts as, in the words syncThreads
// posts. Asks GitHub who self is only when one may be; a writing task's
// (nil) threads are left as they are.
func OwnThreadReplies(ctx context.Context, gh *forge.GitHub, t *forge.Threads, listed []forge.Feedback) error {
	if t == nil {
		return nil
	}
	self := ""
	for _, f := range listed {
		if _, ours := t.Roots[f.InReplyTo]; !ours || f.Kind != forge.KindLineComment || f.InReplyTo == 0 || !dudesThreadReply.MatchString(f.Body) {
			continue
		}
		if self == "" {
			var err error
			if self, err = gh.Login(ctx); err != nil {
				return err
			}
		}
		if strings.EqualFold(f.Author, self) {
			t.Own[forge.LineCommentID(f.ID)] = true
		}
	}
	return nil
}

// threadFindingTx is the finding dude's thread is about, as the wake of
// a reply in it names it: nil when it is no finding's. One kept as a
// suggestion carries its note (Kept), so the wake shows it as one.
func threadFindingTx(ctx context.Context, tx pgx.Tx, findingID string) (*verdictFinding, error) {
	if findingID == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, severity::text, title, description, suggested_fix, COALESCE(acceptance, ''),
			COALESCE(file, ''), COALESCE(line, 0), COALESCE(trivial, false), introduced, false, '',
			CASE WHEN status = 'accepted' THEN COALESCE(resolution_note, '') ELSE '' END
		FROM review_findings WHERE id = $1`, findingID)
	if err != nil {
		return nil, err
	}
	f, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[verdictFinding])
	if db.IsNotFound(err) {
		return nil, nil
	}
	if _, _, kept := f.suggestion(); !kept {
		// Withdrawn as wrong or a duplicate: shown as it was filed.
		f.Kept = ""
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
