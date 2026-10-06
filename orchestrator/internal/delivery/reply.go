package delivery

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Reply is reply_on_pull_request: the conductor's answer on one of its
// task's pull requests.
type Reply struct {
	// The pull request: its number, or repo#number.
	PR   string
	Text string
	// The feedback answered (issue-comment-…, line-comment-…, review-…):
	// a line comment's thread is replied in; anything else is quoted.
	InReplyTo string
}

// Replied is a reply posted: where, and as which feedback id.
type Replied struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	CommentID string `json:"commentId"`
	URL       string `json:"url"`
}

// ReplyPost is a reply checked and not yet posted.
//
// A conductor's reply on a pull request is three steps, so that no lock is
// held while GitHub answers (up to forge's 15 s timeout):
//
//  1. CheckReplyTx, under the task's Chat lock: the caller is the task's live
//     conductor, the pull request is the task's, the comment answered is on
//     it. The transaction then commits.
//  2. PostReply, with no transaction open.
//  3. RecordReplyTx, in a short transaction of its own: the chat.message on
//     the posting Run, unconditionally. A conductor ended or replaced after
//     step 1 did post the comment, so it is recorded as that Run's and the
//     call answers with it, as for any reply.
//
// Nothing is reserved between the steps: a reply changes nothing another
// action reads, and dude's own comment is told apart when read back by its
// marker (forge.ReplyMarker), not by its record. Two replies of one
// conductor at once are not serialised: each posts its own comment and
// records its own message, possibly in a different order from GitHub's.
// Allowed whoever decides, and on a task that ended. GitHub refusing it is
// the conductor's to hear (a Refusal), and nothing is recorded.
type ReplyPost struct {
	ref       RunRef
	repo      string
	number    int
	slug      string
	text      string
	inReplyTo string
	// The line comment whose thread is replied in; 0 posts on the
	// conversation.
	thread int64
	body   string
}

// CheckReplyTx is step 1: the reply as it will be posted, or a Refusal.
func CheckReplyTx(ctx context.Context, tx pgx.Tx, ref RunRef, gh *forge.GitHub, in Reply) (ReplyPost, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return ReplyPost{}, err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return ReplyPost{}, err
	}
	if !live {
		return ReplyPost{}, refusef("you are no longer this task's conductor: another took over from you. Post nothing")
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case text == "":
		return ReplyPost{}, refusef("say what to post: text is required")
	case len(text) > ChatMessageMax:
		return ReplyPost{}, refusef("a reply is at most %d bytes, as a Chat message is", ChatMessageMax)
	}
	repo, number, err := parsePR(in.PR)
	if err != nil {
		return ReplyPost{}, err
	}
	var url string
	err = tx.QueryRow(ctx, `SELECT r.name, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.number = $2 AND ($3 = '' OR r.name = $3) ORDER BY pr.created_at DESC LIMIT 1`,
		ref.TaskID, number, repo).Scan(&repo, &url)
	if db.IsNotFound(err) {
		return ReplyPost{}, refusef("%s is not a pull request of your task: you reply only on your task's (pull_requests lists them)", in.PR)
	}
	if err != nil {
		return ReplyPost{}, err
	}
	slug := forge.SlugFromURL(url)
	if gh == nil || slug == "" {
		return ReplyPost{}, refusef("%s#%d is not on a GitHub dude is connected to: there is nowhere to post", repo, number)
	}
	p := ReplyPost{ref: ref, repo: repo, number: number, slug: slug, text: text, inReplyTo: strings.TrimSpace(in.InReplyTo)}
	var author, said string
	if p.inReplyTo != "" {
		// Its words as last read: an edit since it was first recorded is
		// what was answered.
		err := tx.QueryRow(ctx, `SELECT COALESCE(payload->>'author', ''), COALESCE(payload->>'body', '') FROM events
			WHERE task_id = $1 AND event_type IN ($2, $3) AND payload->>'feedbackId' = $4
			  AND (payload->>'number')::int = $5 AND payload->>'repo' = $6 ORDER BY cursor DESC LIMIT 1`,
			ref.TaskID, EvPullRequestCommented, EvPullRequestCommentEdited, p.inReplyTo, number, repo).Scan(&author, &said)
		if db.IsNotFound(err) {
			return ReplyPost{}, refusef("%s is not a comment on %s#%d (pull_requests lists its feedback)", p.inReplyTo, repo, number)
		}
		if err != nil {
			return ReplyPost{}, err
		}
	}
	if id, ok := strings.CutPrefix(p.inReplyTo, "line-comment-"); ok {
		if p.thread, err = strconv.ParseInt(id, 10, 64); err != nil {
			return ReplyPost{}, refusef("%s is not a line comment's id", p.inReplyTo)
		}
		p.body = text + "\n\n" + forge.ReplyMarker
		return p, nil
	}
	p.body = text
	if p.inReplyTo != "" {
		p.body = fmt.Sprintf("> @%s: %s\n\n%s", author, clip(firstLine(said), 200), text)
	}
	p.body += "\n\n" + forge.ReplyMarker
	return p, nil
}

// PostReply is step 2: the comment posted on GitHub. Called with no
// transaction open.
func PostReply(ctx context.Context, gh *forge.GitHub, p ReplyPost) (forge.Posted, error) {
	var posted forge.Posted
	var err error
	if p.thread != 0 {
		posted, err = gh.ReplyToLineComment(ctx, p.slug, p.number, p.thread, p.body)
	} else {
		posted, err = gh.Comment(ctx, p.slug, p.number, p.body)
	}
	if forge.Refused(err) || forge.Transient(err) {
		return forge.Posted{}, refusef("GitHub did not take the reply, and nothing was posted: %v", err)
	}
	return posted, err
}

// RecordReplyTx is step 3: the posted reply in Chat, on the Run that
// posted it, whether or not it is still the task's conductor.
func RecordReplyTx(ctx context.Context, tx pgx.Tx, p ReplyPost, posted forge.Posted) (Replied, error) {
	g := map[string]any{"repo": p.repo, "number": p.number, "feedbackId": posted.ID, "url": posted.URL}
	if p.inReplyTo != "" {
		g["inReplyTo"] = p.inReplyTo
	}
	_, err := ledger.Append(ctx, tx, p.ref.Event(EvChatMessage, ledger.ActorAgent,
		map[string]any{"text": p.text, "by": "conductor", "github": g}))
	return Replied{Repo: p.repo, Number: p.number, CommentID: posted.ID, URL: posted.URL}, err
}

// parsePR reads a pull request as the conductor names it: 12, #12, or
// repo#12.
func parsePR(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	repo, num, found := strings.Cut(s, "#")
	if !found {
		repo, num = "", s
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return "", 0, refusef("name the pull request by its number, or repo#number: %q is neither", s)
	}
	return strings.TrimSpace(repo), n, nil
}
