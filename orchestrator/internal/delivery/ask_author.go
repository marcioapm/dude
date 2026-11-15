package delivery

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A review-only task's conductor asks the pull request's author on GitHub
// (ask_author, design §7). The question is a review_questions row
// addressed to 'author' (migration 267): open, it holds dude's verdict
// (gateQuestionsTx counts it; decideVerdict posts a COMMENT naming it);
// answered or withdrawn by the conductor (answer_review_question), the
// verdict is weighed again at the same head. It is posted as reply.go
// posts a reply: checked under the Chat lock, posted with no transaction
// open, recorded after.

// HolderAuthor is review_questions.addressee for a question to the pull
// request's author.
const HolderAuthor = "author"

// askMarker hides the question's id in its comment, so a post whose
// answer was lost is found again (RecoverAsksTx).
func askMarker(id string) string { return "<!-- dude:ask id=" + id + " -->" }

// The author question events, on the task.
const (
	// {questionId, findingId?, askedOf, commentId?, url?, lost?}
	EvAuthorAsked = "review.author_asked"
	// {questionId, reason, findingId?}
	EvAuthorQuestionTimedOut = "review.author_question_timed_out"
)

// AuthorQuestionTimeout is how long an author question waits before it
// is withdrawn (Store.AuthorQuestionTimeout overrides it).
const AuthorQuestionTimeout = 3 * 24 * time.Hour

// AskAuthor is ask_author's input.
type AskAuthor struct {
	PR, Question, FindingID string
}

// AskPost is an author question checked and recorded open, not yet posted.
type AskPost struct {
	ReplyPost
	QuestionID, Login, FindingID string
}

// maxAuthorQuestion bounds a question's words.
const maxAuthorQuestion = 4000

// CheckAskAuthorTx is step 1: the caller is the task's live conductor, the
// task is review only, the pull request is its; the question is inserted
// open, with no comment yet, in this transaction.
func CheckAskAuthorTx(ctx context.Context, tx pgx.Tx, ref RunRef, gh *forge.GitHub, in AskAuthor) (AskPost, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return AskPost{}, err
	}
	n, ok, err := reviewOnlyTx(ctx, tx, ref.TaskID)
	if err != nil {
		return AskPost{}, err
	}
	if !ok {
		return AskPost{}, refuseText(rAskAuthorReviewOnly)
	}
	question := strings.TrimSpace(in.Question)
	switch {
	case question == "":
		return AskPost{}, refuseText(rAskAuthorQuestionRequired)
	case len(question) > maxAuthorQuestion:
		return AskPost{}, refusef(rAskAuthorTooLong, maxAuthorQuestion)
	}
	repo, number, slug, prID, err := replyTargetTx(ctx, tx, ref.TaskID, gh, in.PR)
	if err != nil {
		return AskPost{}, err
	}
	d, err := ReadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return AskPost{}, err
	}
	login := ""
	if d != nil && d.State.Source != nil {
		login = d.State.Source.Author
	}
	if login == "" {
		return AskPost{}, refusef(rAskAuthorUnknownAuthor, n)
	}
	p := AskPost{ReplyPost: ReplyPost{ref: ref, repo: repo, number: number, slug: slug, text: question, by: "conductor"},
		QuestionID: ids.New(ids.ReviewQuestion), Login: login, FindingID: strings.TrimSpace(in.FindingID)}
	if p.FindingID != "" {
		posts, err := earlierPostsTx(ctx, tx, ref.TaskID, prID)
		if err != nil {
			return AskPost{}, err
		}
		post, ok := posts[p.FindingID]
		if !ok {
			return AskPost{}, refusef(rAskAuthorNoThread, p.FindingID)
		}
		p.thread, p.inReplyTo = post.CommentID, fmt.Sprintf("line-comment-%d", post.CommentID)
	}
	p.body = "@" + login + " " + question + "\n\n" + askMarker(p.QuestionID) + "\n\n" + forge.ReplyMarker
	if _, err := tx.Exec(ctx, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, finding_id, question,
			addressee, asked_of)
		VALUES ($1, $2, $3, $4, (SELECT COALESCE(max(n), 0) + 1 FROM review_questions WHERE run_id = $4), 'conductor', NULLIF($5, ''), $6, $7, $8)`,
		p.QuestionID, ref.Org, ref.TaskID, ref.RunID, p.FindingID, question, HolderAuthor, login); err != nil {
		return AskPost{}, err
	}
	return p, nil
}

// recheckReviewOnlyTx has a review-only task's pull request wait run its
// verdict again at the reviewed head (SignalKindRecheck), when it waits
// there; key makes the signal once. A writing task is never signalled.
func recheckReviewOnlyTx(ctx context.Context, tx pgx.Tx, org, taskID, key string) error {
	if ok, err := ReviewOnlyTx(ctx, tx, taskID); err != nil || !ok {
		return err
	}
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil || !d.Live() || d.Step != "awaitPullRequest" {
		return err
	}
	return workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalPRFeedback, map[string]any{"kind": SignalKindRecheck}, "recheck:"+key)
}

// AskPosted is GitHub's answer to an author question's post: the comment,
// or why GitHub refused it, or that its answer was lost.
type AskPosted struct {
	forge.Posted
	Refused, Lost string
}

// PostAsk is step 2, with no transaction open. GitHub refusing is not an
// error here: the question is withdrawn (WithdrawRefusedAskTx). An answer
// lost leaves it open, its comment found by the next sync.
func PostAsk(ctx context.Context, gh *forge.GitHub, p AskPost) AskPosted {
	var posted forge.Posted
	var err error
	if p.thread != 0 {
		posted, err = gh.ReplyToLineComment(ctx, p.slug, p.number, p.thread, p.body)
	} else {
		posted, err = gh.Comment(ctx, p.slug, p.number, p.body)
	}
	switch {
	case err == nil:
		return AskPosted{Posted: posted}
	case forge.Refused(err):
		return AskPosted{Refused: err.Error()}
	}
	return AskPosted{Lost: err.Error()}
}

// WithdrawRefusedAskTx withdraws a question GitHub did not take, and
// answers the conductor's Refusal.
func WithdrawRefusedAskTx(ctx context.Context, tx pgx.Tx, p AskPost, why string) error {
	if _, err := tx.Exec(ctx, `UPDATE review_questions SET status = 'withdrawn', answered_at = now(), github_refused = true
		WHERE id = $1 AND status = 'open'`, p.QuestionID); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, p.ref.Event(EvReviewQuestionWithdrawn, ledger.ActorAgent,
		map[string]any{"questionId": p.QuestionID, "by": "conductor", "reason": "GitHub did not take it: " + why}))
	return err
}

// RefuseAsk is the Refusal a question GitHub did not take answers with.
func RefuseAsk(why string) error { return refusef(rAskAuthorGithubRefused, why) }

// AskedOut is ask_author's answer.
type AskedOut struct {
	QuestionID string `json:"questionId"`
	Status     string `json:"status"`
	CommentID  string `json:"commentId,omitempty"`
	URL        string `json:"url,omitempty"`
	Next       string `json:"next"`
}

// RecordAskTx is step 3: the comment on the question and in Chat; an
// answer lost is said, and the question stays open with no comment.
func RecordAskTx(ctx context.Context, tx pgx.Tx, p AskPost, got AskPosted) (AskedOut, error) {
	payload := map[string]any{"questionId": p.QuestionID, "askedOf": p.Login}
	if p.FindingID != "" {
		payload["findingId"] = p.FindingID
	}
	out := AskedOut{QuestionID: p.QuestionID, Status: "open"}
	// The verdict at the reviewed head is weighed again: an open question
	// holds an approval back.
	if err := recheckReviewOnlyTx(ctx, tx, p.ref.Org, p.ref.TaskID, "ask:"+p.QuestionID); err != nil {
		return out, err
	}
	if got.Lost != "" {
		payload["lost"] = got.Lost
		out.Next = fmt.Sprintf(mAskAuthorLost, got.Lost)
		_, err := ledger.Append(ctx, tx, p.ref.Event(EvAuthorAsked, ledger.ActorAgent, payload))
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE review_questions SET github_comment_id = $2, github_url = NULLIF($3, '') WHERE id = $1`,
		p.QuestionID, got.ID, got.URL); err != nil {
		return out, err
	}
	payload["commentId"], payload["url"] = got.ID, got.URL
	if _, err := ledger.Append(ctx, tx, p.ref.Event(EvAuthorAsked, ledger.ActorAgent, payload)); err != nil {
		return out, err
	}
	if _, err := RecordReplyTx(ctx, tx, p.ReplyPost, got.Posted); err != nil {
		return out, err
	}
	out.CommentID, out.URL, out.Next = got.ID, got.URL, mAskAuthorNext
	return out, nil
}

// The words ask_author answers with.
const (
	mAskAuthorNext = "Asked. The question holds dude's approval while it is open. The author's answer reaches you as a message from the pull request; close the question with answer_review_question (its id) once it is answered, or withdraw it."
	mAskAuthorLost = "GitHub's answer to the post was lost (%s): the comment may not have posted. The question is open; dude finds the comment on the next sync of the pull request. Do not ask again unless findings shows it never posted."
)

// replyTargetTx is the task's pull request named pr, for reply_on_pull_request
// (CheckReplyTx) and ask_author alike: its repository's name in dude, its
// number, GitHub slug and id.
func replyTargetTx(ctx context.Context, tx pgx.Tx, taskID string, gh *forge.GitHub, pr string) (repo string, number int, slug, prID string, err error) {
	owner, name, number, err := parsePR(pr)
	if err != nil {
		return "", 0, "", "", err
	}
	var url string
	prID, err = taskPullRequest(ctx, tx, taskID, number, owner, name)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT r.name, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = $1`, prID).Scan(&repo, &url)
	}
	if db.IsNotFound(err) {
		return "", 0, "", "", refusef(rNotPullRequestYourTaskYou2, pr)
	}
	if err != nil {
		return "", 0, "", "", err
	}
	slug = forge.SlugFromURL(url)
	if gh == nil || slug == "" {
		return "", 0, "", "", refusef(rNotOnGithubDudeConnectedThere, repo, number)
	}
	return repo, number, slug, prID, nil
}

// AuthorAsks are a review-only task's open questions to its author: what
// the syncer routes the author's answers by, and what a wake names.
type AuthorAsks struct {
	// Logins asked on the conversation (no finding's thread), each with
	// when its oldest open question there was asked: their conversation
	// comment made since is an answer, addressed to the conductor.
	Conversation map[string]time.Time
	Open         []AuthorAsk
}

// AuthorAsk is one open question to the author.
type AuthorAsk struct {
	ID, Login, Question, FindingID, URL string
	At                                  time.Time
	// Its post's answer was lost: no comment yet (RecoverAsksTx).
	Lost bool
}

// Lost says a question's post answer was lost, its comment still to be
// found (RecoverAsksTx); false for nil.
func (a *AuthorAsks) Lost() bool {
	return a != nil && slices.ContainsFunc(a.Open, func(q AuthorAsk) bool { return q.Lost })
}

// line is the question as a wake and the verdict name it.
func (a AuthorAsk) line() string {
	s := "@" + a.Login + ", " + clip(oneLine(a.Question), 200)
	if a.URL != "" {
		s += " (" + a.URL + ")"
	}
	return s
}

// Answers says f is the asked author's conversation comment while a
// question to them waits there: addressed to the conductor, mention or
// not, and theirs to send whoever else may wake (the author may always
// answer dude's question to them). Only a comment made since the oldest
// open question to them there (GitHub's second, less askClockGrace): an
// older one, or an edit of one, is not an answer. nil asks (a writing
// task's) never.
func (a *AuthorAsks) Answers(f forge.Feedback) bool {
	if a == nil || f.Kind != forge.KindComment || forge.Own(f) || strings.TrimSpace(f.Body) == "" {
		return false
	}
	made, err := time.Parse(time.RFC3339, f.CreatedAt)
	if err != nil {
		return false
	}
	for l, asked := range a.Conversation {
		if strings.EqualFold(l, f.Author) && !made.Before(asked.Truncate(time.Second).Add(-askClockGrace)) {
			return true
		}
	}
	return false
}

// askClockGrace is how far before a question's created_at (dude's DB
// clock) a time from another clock (GitHub's, the orchestrator's) still
// counts as since it: the clocks may be skewed. Erring early costs at most
// one extra message to the conductor.
const askClockGrace = time.Minute

// Lines is the wake's list of the open questions; "" for none or nil.
func (a *AuthorAsks) Lines() []AuthorAsk {
	if a == nil {
		return nil
	}
	return a.Open
}

// AuthorAsksTx is a review-only task's open author questions, of its
// current attempt; nil for a writing task, whose routing does not change.
func AuthorAsksTx(ctx context.Context, tx pgx.Tx, taskID string) (*AuthorAsks, error) {
	if ok, err := ReviewOnlyTx(ctx, tx, taskID); err != nil || !ok {
		return nil, err
	}
	attempt := 0
	if d, err := ReadDelivery(ctx, tx, taskID); err != nil {
		return nil, err
	} else if d != nil {
		attempt = d.State.Attempt
	}
	open, err := openAuthorAsksTx(ctx, tx, taskID, attempt)
	if err != nil {
		return nil, err
	}
	out := &AuthorAsks{Conversation: map[string]time.Time{}, Open: open}
	for _, q := range open {
		if at, ok := out.Conversation[q.Login]; q.FindingID == "" && (!ok || q.At.Before(at)) {
			out.Conversation[q.Login] = q.At
		}
	}
	return out, nil
}

// askOfAttempt (SQL, over review_questions q, the attempt as $3): a
// question asked by a Run of that attempt, as gateQuestionsTx and
// openReviewQuestionsTx scope theirs; 0 (a state from before attempts)
// takes any. An earlier attempt's question to the author holds, routes
// and times out nothing in a later one.
const askOfAttempt = `($3 = 0 OR EXISTS (SELECT 1 FROM runs k WHERE k.id = q.run_id AND k.attempt = $3))`

func openAuthorAsksTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) ([]AuthorAsk, error) {
	rows, err := tx.Query(ctx, `SELECT q.id, COALESCE(q.asked_of, ''), q.question, COALESCE(q.finding_id, ''), COALESCE(q.github_url, ''),
		q.created_at, q.github_comment_id IS NULL FROM review_questions q WHERE q.task_id = $1 AND q.status = 'open' AND q.addressee = $2 AND `+askOfAttempt+`
		ORDER BY q.created_at, q.id`, taskID, HolderAuthor, attempt)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[AuthorAsk])
}

// settledAuthorAsksTx is the ids of the task's author questions no longer
// open, oldest first: any of them may have held the verdict (posted, its
// answer lost, or its record never written). Only a post GitHub refused
// (WithdrawRefusedAskTx, github_refused) held nothing.
func settledAuthorAsksTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT q.id FROM review_questions q WHERE q.task_id = $1 AND q.status <> 'open' AND q.addressee = $2
		AND `+askOfAttempt+` AND NOT q.github_refused
		ORDER BY q.created_at, q.id`, taskID, HolderAuthor, attempt)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// authorAsksLines is the wake's list of the open author questions.
func authorAsksLines(open []AuthorAsk) string {
	if len(open) == 0 {
		return ""
	}
	lines := make([]string, len(open))
	for i, q := range open {
		lines[i] = "- " + q.ID + ": " + q.line()
	}
	return render(authorAsksTmpl, strings.Join(lines, "\n"))
}

// RecoverAsksTx fills in the comment of an open author question whose
// post's answer was lost: dude's own comment (forge.Own, recorded own by
// the sync) carrying its askMarker, recorded since the question was
// asked (less askClockGrace). A review-only task's sync calls it, only when one is lost
// (AuthorAsks.Lost).
func RecoverAsksTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `UPDATE review_questions q SET github_comment_id = e.payload->>'feedbackId', github_url = e.payload->>'url'
		FROM events e WHERE q.task_id = $1 AND q.status = 'open' AND q.addressee = $2 AND q.github_comment_id IS NULL
		  AND e.task_id = $1 AND e.event_type = $3 AND e.payload->>'own' = 'true' AND e.occurred_at >= q.created_at - $4 * interval '1 second'
		  AND strpos(e.payload->>'body', '<!-- dude:ask id=' || q.id || ' -->') > 0`, taskID, HolderAuthor, EvPullRequestCommented, askClockGrace.Seconds())
	return err
}

// authorQuestionTimeoutTx is the project's author_question_timeout: its
// delivery policy's authorQuestionTimeoutMinutes, a positive whole number,
// else 3 days (absent, not a whole number, out of range or not positive:
// never an error, since every wait pass reads it); the Store's own (tests)
// over both.
func (s *Store) authorQuestionTimeoutTx(ctx context.Context, tx pgx.Tx, projectID string) (time.Duration, error) {
	if s.AuthorQuestionTimeout > 0 {
		return s.AuthorQuestionTimeout, nil
	}
	var minutes float64
	if err := tx.QueryRow(ctx, `SELECT CASE WHEN jsonb_typeof(delivery_policy->'authorQuestionTimeoutMinutes') = 'number'
			THEN (delivery_policy->>'authorQuestionTimeoutMinutes')::float8 ELSE 0 END FROM projects WHERE id = $1`,
		projectID).Scan(&minutes); err != nil && !db.IsNotFound(err) {
		return 0, err
	}
	if minutes >= 1 && minutes <= maxAuthorQuestionMinutes && minutes == float64(int64(minutes)) {
		return time.Duration(minutes) * time.Minute, nil
	}
	return AuthorQuestionTimeout, nil
}

// maxAuthorQuestionMinutes bounds the setting (deliveryPolicySchema's
// authorQuestionTimeoutMinutes): 90 days.
const maxAuthorQuestionMinutes = 90 * 24 * 60

// expireAuthorAsks is the review-only wait's timer for questions to the
// author, on dude's clock: each open one past the timeout is settled, once.
// Tied to a finding, it is withdrawn and the finding stands: the verdict
// is weighed again. Free-standing, it stays open and the conductor is
// woken once to withdraw it (the verdict proceeds) or ask the owner.
// Returns whether the verdict is
// due again, and when the next one times out (zero: none).
func (w *steps) expireAuthorAsks(ctx context.Context, sc workflow.StepContext, st *State) (recheck bool, next time.Time, err error) {
	if w.s == nil || w.s.DB == nil {
		// A wait run with no store (the wait's unit tests): nothing asked.
		return false, time.Time{}, nil
	}
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		timeout, err := w.s.authorQuestionTimeoutTx(ctx, tx, st.ProjectID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT q.id, COALESCE(q.asked_of, ''), COALESCE(q.finding_id, ''), q.created_at, q.created_at <= now() - $4 * interval '1 second'
			FROM review_questions q WHERE q.task_id = $1 AND q.status = 'open' AND q.addressee = $2 AND `+askOfAttempt+`
			  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.task_id = q.task_id AND e.event_type = $5 AND e.payload->>'questionId' = q.id)
			ORDER BY q.created_at, q.id`,
			st.TaskID, HolderAuthor, st.Attempt, timeout.Seconds(), EvAuthorQuestionTimedOut)
		if err != nil {
			return err
		}
		type ask struct {
			ID, Login, FindingID string
			At                   time.Time
			Due                  bool
		}
		asks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ask])
		if err != nil {
			return err
		}
		for _, a := range asks {
			if !a.Due {
				if at := a.At.Add(timeout); next.IsZero() || at.Before(next) {
					next = at
				}
				continue
			}
			reason := fmt.Sprintf("no answer from @%s in %s", a.Login, spokenDays(timeout))
			payload := map[string]any{"questionId": a.ID, "reason": reason}
			if a.FindingID != "" {
				payload["findingId"] = a.FindingID
				if _, err := tx.Exec(ctx, `UPDATE review_questions SET status = 'withdrawn', answered_at = now() WHERE id = $1 AND status = 'open'`, a.ID); err != nil {
					return err
				}
				recheck = true
			} else {
				// Still open, still the author's: the conductor decides, with
				// no further timer (the event keeps it from timing out again).
				if _, err := RecordWakeTx(ctx, tx, sc.OrganizationID, st.TaskID, "question", "author-question:"+a.ID+":timed-out",
					render(authorAskTimedOutTmpl, struct{ ID, Reason string }{a.ID, reason})); err != nil {
					return err
				}
			}
			if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvAuthorQuestionTimedOut, OrganizationID: sc.OrganizationID,
				ProjectID: st.ProjectID, TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
				CorrelationID: st.TaskID, Payload: payload}); err != nil {
				return err
			}
		}
		return nil
	})
	return recheck, next, err
}

// withAskTimer has a review-only wait also wake when its next open author
// question times out (expireAuthorAsks): the timer is dude's, no agent's.
func (w *steps) withAskTimer(ctx context.Context, sc workflow.StepContext, st *State, res workflow.Result) (workflow.Result, error) {
	var due *time.Time
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		timeout, err := w.s.authorQuestionTimeoutTx(ctx, tx, st.ProjectID)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT min(q.created_at) + $4 * interval '1 second' FROM review_questions q
			WHERE q.task_id = $1 AND q.status = 'open' AND q.addressee = $2 AND `+askOfAttempt+`
			  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.task_id = q.task_id AND e.event_type = $5 AND e.payload->>'questionId' = q.id)`,
			st.TaskID, HolderAuthor, st.Attempt, timeout.Seconds(), EvAuthorQuestionTimedOut).Scan(&due)
	}); err != nil {
		return res, err
	}
	if due != nil && len(res.AwaitSignals) > 0 && (res.SleepUntil.IsZero() || due.Before(res.SleepUntil)) {
		res.SleepUntil = *due
	}
	return res, nil
}

// spokenDays is a timeout as a person says it: "3 days", else spoken.
func spokenDays(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		if d == 24*time.Hour {
			return "1 day"
		}
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return spoken(d)
}
