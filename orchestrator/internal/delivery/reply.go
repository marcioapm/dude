package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
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
	// The pull request: its number, repo#number, owner/repo#number or URL.
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
	// Who posted it: the conductor. A fixer is refused (#485).
	by string
}

// actingRun is the Run calling a PR tool: who it is, its status, and the
// feedback ids a fixer was given (bots' left out: they are not its to answer).
type actingRun struct {
	role, phase, status string
	batch               []string
}

// ended says the Run is over. lux's lifecycle trails the agent's records,
// so a Run still starting is acting, and live.
func (r actingRun) ended() bool {
	return r.status == "completed" || r.status == "failed" || r.status == "aborted"
}

func actingRunTx(ctx context.Context, tx pgx.Tx, ref RunRef) (actingRun, error) {
	var r actingRun
	var feedback []byte
	if err := tx.QueryRow(ctx, `SELECT COALESCE(role::text, ''), COALESCE(phase::text, ''), status::text, COALESCE(pr_feedback, '[]')
		FROM runs WHERE id = $1 AND task_id = $2`, ref.RunID, ref.TaskID).Scan(&r.role, &r.phase, &r.status, &feedback); err != nil {
		return r, err
	}
	var items []forge.ActionableFeedback
	_ = json.Unmarshal(feedback, &items)
	for _, f := range items {
		if f.ID != "" && f.Source != forge.SourceBot {
			r.batch = append(r.batch, f.ID)
		}
	}
	return r, nil
}

// CheckReplyTx is step 1: the reply as it will be posted, or a Refusal.
func CheckReplyTx(ctx context.Context, tx pgx.Tx, ref RunRef, gh *forge.GitHub, in Reply) (ReplyPost, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return ReplyPost{}, err
	}
	caller, err := actingRunTx(ctx, tx, ref)
	if err != nil {
		return ReplyPost{}, err
	}
	// A fixer is told which of its batch's comments it may name, and is
	// then refused: it does not post (#485).
	if caller.phase == PhaseFix {
		batch := caller.batch
		switch {
		case caller.ended():
			return ReplyPost{}, refusef(rYourRunPostsNothing, caller.status)
		case len(batch) == 0:
			return ReplyPost{}, refuseText(rYouReplyOnlyPullRequestFeedback)
		case !slices.Contains(batch, strings.TrimSpace(in.InReplyTo)):
			return ReplyPost{}, refusef(rInReplyMustBeOneComments, strings.Join(batch, ", "))
		}
		// Its fix is not published while it runs: a reply would claim
		// what the pull request does not have yet. The conductor answers
		// once the published fix is verified (#485).
		return ReplyPost{}, refusef(rFixNotOnPullRequestYet, strings.TrimSpace(in.InReplyTo))
	} else if caller.role != RoleConductor {
		return ReplyPost{}, refuseText(rTaskConductorRepliesOnItsPull)
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
		AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
		return ReplyPost{}, err
	}
	if !live {
		return ReplyPost{}, refuseText(rYouAreNoLongerThisTask5)
	}
	if strings.TrimSpace(in.InReplyTo) == "" {
		// T4: a review-only task's conductor posts no review of its own:
		// dude's review is the output. Replies in a thread stay its.
		if n, ok, err := reviewOnlyTx(ctx, tx, ref.TaskID); err != nil {
			return ReplyPost{}, err
		} else if ok {
			return ReplyPost{}, refusef(rReviewOnlyNoOwnComment, n)
		}
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case text == "":
		return ReplyPost{}, refuseText(rSayWhatPostTextRequired)
	case len(text) > ChatMessageMax:
		return ReplyPost{}, refusef(rReplyAtMostBytesAsChat, ChatMessageMax)
	}
	repo, number, slug, _, err := replyTargetTx(ctx, tx, ref.TaskID, gh, in.PR)
	if err != nil {
		return ReplyPost{}, err
	}
	p := ReplyPost{ref: ref, repo: repo, number: number, slug: slug, text: text, inReplyTo: strings.TrimSpace(in.InReplyTo), by: "conductor"}
	var author, said string
	if p.inReplyTo != "" {
		// Its words as last read: an edit since it was first recorded is
		// what was answered.
		err := tx.QueryRow(ctx, `SELECT COALESCE(payload->>'author', ''), COALESCE(payload->>'body', '') FROM events
			WHERE task_id = $1 AND event_type IN ($2, $3) AND payload->>'feedbackId' = $4
			  AND (payload->>'number')::int = $5 AND payload->>'repo' = $6 ORDER BY cursor DESC LIMIT 1`,
			ref.TaskID, EvPullRequestCommented, EvPullRequestCommentEdited, p.inReplyTo, number, repo).Scan(&author, &said)
		if db.IsNotFound(err) {
			return ReplyPost{}, refusef(rNotCommentOnPullRequestsLists, p.inReplyTo, repo, number)
		}
		if err != nil {
			return ReplyPost{}, err
		}
	}
	if id, ok := strings.CutPrefix(p.inReplyTo, "line-comment-"); ok {
		if p.thread, err = strconv.ParseInt(id, 10, 64); err != nil {
			return ReplyPost{}, refusef(rNotLineCommentId, p.inReplyTo)
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
		return forge.Posted{}, refusef(rGithubDidNotTakeReplyAnd, err)
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
		map[string]any{"text": p.text, "by": p.by, "github": g}))
	return Replied{Repo: p.repo, Number: p.number, CommentID: posted.ID, URL: posted.URL}, err
}

var (
	prURL  = regexp.MustCompile(`^(?:https?://)?[^/\s]+/([\w.-]+)/([\w.-]+)/pull/(\d+)(?:[/?#]\S*)?$`)
	prRepo = regexp.MustCompile(`^(?:([\w.-]+)/)?([\w.-]+)$`)
)

// parsePR reads a pull request as the conductor names it: 12, #12,
// repo#12, owner/repo#12, or its URL (https://github.com/owner/repo/pull/12,
// with /files, a query or a #fragment after it). The owner is "" unless
// named.
func parsePR(s string) (owner, repo string, number int, err error) {
	s = strings.TrimSpace(s)
	name, num := "", s
	if m := prURL.FindStringSubmatch(s); m != nil {
		owner, repo, num = m[1], m[2], m[3]
	} else if before, after, found := strings.Cut(s, "#"); found {
		name, num = strings.TrimSpace(before), after
	}
	if name != "" {
		m := prRepo.FindStringSubmatch(name)
		if m == nil {
			return "", "", 0, refusef(rNamePullRequestByItsNumber, s)
		}
		owner, repo = m[1], m[2]
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return "", "", 0, refusef(rNamePullRequestByItsNumber, s)
	}
	return owner, repo, n, nil
}

// namedBy is whether the repository at url, named name in dude, is the one
// a pull request was named in (parsePR's owner and repo). Named with an
// owner, the owner and repo are GitHub's, matched against the end of its
// clone URL as GitHub does, case aside; dude's name may differ. Named
// repo#N, the repo is dude's name. Named by number alone, any repository is.
func namedBy(url, name, owner, repo string) bool {
	if owner == "" {
		return repo == "" || name == repo
	}
	slugOwner, slugRepo, _ := strings.Cut(forge.SlugFromURL(url), "/")
	return strings.EqualFold(slugOwner, owner) && strings.EqualFold(slugRepo, repo)
}

// taskPullRequest is the id of the task's pull request numbered number in
// the repository named (namedBy), the newest if several are; pgx.ErrNoRows
// if none is, which the caller refuses as it does a missing row.
func taskPullRequest(ctx context.Context, tx pgx.Tx, taskID string, number int, owner, repo string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT pr.id, r.name, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.number = $2 ORDER BY pr.created_at DESC`, taskID, number)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, url string
		if err := rows.Scan(&id, &name, &url); err != nil {
			return "", err
		}
		if namedBy(url, name, owner, repo) {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", pgx.ErrNoRows
}
