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

// ConductReply posts the live conductor's reply on a pull request of its
// own task, as dude's GitHub login, and records it in Chat. Allowed
// whoever decides, and on a task that ended: it answers, and changes
// nothing. GitHub refusing it is the conductor's to hear (a Refusal), and
// nothing is recorded. The Chat lock is held across the post, so a
// conductor superseded meanwhile posts nothing.
func ConductReply(ctx context.Context, tx pgx.Tx, ref RunRef, gh *forge.GitHub, in Reply) (Replied, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return Replied{}, err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return Replied{}, err
	}
	if !live {
		return Replied{}, refusef("you are no longer this task's conductor: another took over from you. Post nothing")
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case text == "":
		return Replied{}, refusef("say what to post: text is required")
	case len(text) > ChatMessageMax:
		return Replied{}, refusef("a reply is at most %d bytes, as a Chat message is", ChatMessageMax)
	}
	repo, number, err := parsePR(in.PR)
	if err != nil {
		return Replied{}, err
	}
	var url string
	err = tx.QueryRow(ctx, `SELECT r.name, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.number = $2 AND ($3 = '' OR r.name = $3) ORDER BY pr.created_at DESC LIMIT 1`,
		ref.TaskID, number, repo).Scan(&repo, &url)
	if db.IsNotFound(err) {
		return Replied{}, refusef("%s is not a pull request of your task: you reply only on your task's (pull_requests lists them)", in.PR)
	}
	if err != nil {
		return Replied{}, err
	}
	slug := forge.SlugFromURL(url)
	if gh == nil || slug == "" {
		return Replied{}, refusef("%s#%d is not on a GitHub dude is connected to: there is nowhere to post", repo, number)
	}
	inReplyTo := strings.TrimSpace(in.InReplyTo)
	var author, said string
	if inReplyTo != "" {
		// Its words as last read: an edit since it was first recorded is
		// what was answered.
		err := tx.QueryRow(ctx, `SELECT COALESCE(payload->>'author', ''), COALESCE(payload->>'body', '') FROM events
			WHERE task_id = $1 AND event_type IN ($2, $3) AND payload->>'feedbackId' = $4
			  AND (payload->>'number')::int = $5 AND payload->>'repo' = $6 ORDER BY cursor DESC LIMIT 1`,
			ref.TaskID, EvPullRequestCommented, EvPullRequestCommentEdited, inReplyTo, number, repo).Scan(&author, &said)
		if db.IsNotFound(err) {
			return Replied{}, refusef("%s is not a comment on %s#%d (pull_requests lists its feedback)", inReplyTo, repo, number)
		}
		if err != nil {
			return Replied{}, err
		}
	}
	var posted forge.Posted
	if id, ok := strings.CutPrefix(inReplyTo, "line-comment-"); ok {
		n, perr := strconv.ParseInt(id, 10, 64)
		if perr != nil {
			return Replied{}, refusef("%s is not a line comment's id", inReplyTo)
		}
		posted, err = gh.ReplyToLineComment(ctx, slug, number, n, text+"\n\n"+forge.ReplyMarker)
	} else {
		body := text
		if inReplyTo != "" {
			body = fmt.Sprintf("> @%s: %s\n\n%s", author, clip(firstLine(said), 200), text)
		}
		posted, err = gh.Comment(ctx, slug, number, body+"\n\n"+forge.ReplyMarker)
	}
	if forge.Refused(err) || forge.Transient(err) {
		return Replied{}, refusef("GitHub did not take the reply, and nothing was posted: %v", err)
	}
	if err != nil {
		return Replied{}, err
	}
	g := map[string]any{"repo": repo, "number": number, "feedbackId": posted.ID, "url": posted.URL}
	if inReplyTo != "" {
		g["inReplyTo"] = inReplyTo
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvChatMessage, ledger.ActorAgent,
		map[string]any{"text": text, "by": "conductor", "github": g}))
	return Replied{Repo: repo, Number: number, CommentID: posted.ID, URL: posted.URL}, err
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
