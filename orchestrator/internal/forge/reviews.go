package forge

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Review events, as GitHub's create-review endpoint takes them.
const (
	EventApprove        = "APPROVE"
	EventRequestChanges = "REQUEST_CHANGES"
	EventComment        = "COMMENT"
)

// ReviewComment is one inline comment of a review.
type ReviewComment struct {
	Path      string // repo-relative
	Line      int    // last line of the range, in the file at Side
	Side      string // "RIGHT" (the head) | "LEFT" (the base); "" is RIGHT
	StartLine int    // 0, or the first line of a multi-line range
	StartSide string // "" = Side
	Body      string
}

// sides is the comment's Side and StartSide with their defaults filled in.
func (c ReviewComment) sides() (side, startSide string) {
	side = cmp.Or(c.Side, "RIGHT")
	return side, cmp.Or(c.StartSide, side)
}

// SubmitReview is a review to post on a pull request.
type SubmitReview struct {
	Slug   string
	Number int
	// The reviewed head: required, never "". GitHub takes the pull
	// request's head at the moment the call arrives without it, which is
	// not necessarily the head that was reviewed.
	CommitID string
	Event    string // EventApprove | EventRequestChanges | EventComment
	Body     string // ends with ReviewMarker(taskID, claimID)
	Comments []ReviewComment
}

// SubmittedReview is a review GitHub holds.
type SubmittedReview struct {
	ID                           int64
	NodeID, URL, State, CommitID string
}

// ReviewMarker ends the body of every review dude submits: hidden, as
// ReplyMarker, naming the task and the claim the review was posted under,
// so a review whose answer was lost can be found again (FindOwnReview).
func ReviewMarker(taskID, claimID string) string {
	return "<!-- dude:review task=" + taskID + " claim=" + claimID + " -->"
}

var reviewMarkerLine = regexp.MustCompile(`^<!-- dude:review task=[A-Za-z0-9_-]+ claim=[A-Za-z0-9_-]+ -->$`)

// ReviewMarked says the feedback ends in a ReviewMarker, read as Own reads
// ReplyMarker. Anyone can type it: alone it says nothing of whose it is.
func ReviewMarked(f Feedback) bool {
	last, ok := markerLine(f.Body)
	return ok && reviewMarkerLine.MatchString(last)
}

// reviewMarked says body ends in marker, read as Own reads ReplyMarker
// (markerLine).
func reviewMarked(body, marker string) bool {
	last, ok := markerLine(body)
	return ok && last == marker
}

// The ways GitHub refuses a review write that a caller acts on.
const (
	// A comment's line is not in the diff (422): retry with every comment
	// folded into the body.
	RefusalLineNotInDiff = "line_not_in_diff"
	// The poster authored the pull request (422): APPROVE and
	// REQUEST_CHANGES are refused; COMMENT is not.
	RefusalOwnPullRequest = "own_pull_request"
	// GitHub would not dismiss the review (403/422): branch protection, a
	// ruleset, or a review that cannot be dismissed. Recorded, not retried.
	RefusalDismissal = "dismissal_refused"
)

// ReviewRefusal is GitHub refusing a review write in one of the ways a
// caller tells apart (Kind, one of the Refusal* review kinds above). It
// unwraps to GitHub's *Error.
type ReviewRefusal struct {
	Kind string
	Err  *Error
}

func (r *ReviewRefusal) Error() string { return r.Kind + ": " + r.Err.Error() }
func (r *ReviewRefusal) Unwrap() error { return r.Err }

// LineNotInDiff says GitHub refused a review because a comment's line is
// not part of the pull request's diff.
func LineNotInDiff(err error) bool { return reviewRefusalIs(err, RefusalLineNotInDiff) }

// OwnPullRequest says GitHub refused to approve or request changes because
// the poster authored the pull request.
func OwnPullRequest(err error) bool { return reviewRefusalIs(err, RefusalOwnPullRequest) }

// DismissRefused says GitHub refused to dismiss a review.
func DismissRefused(err error) bool { return reviewRefusalIs(err, RefusalDismissal) }

func reviewRefusalIs(err error, kind string) bool {
	var r *ReviewRefusal
	return errors.As(err, &r) && r.Kind == kind
}

// reviewAnswer reads GitHub's answer to a review write (op "submit" or
// "dismiss"): nil for success, including a dismissal of a review already
// dismissed; a *ReviewRefusal for the refusals callers act on; else err.
// All of GitHub's wording dude matches on is here. GitHub puts the detail
// of a 422 in its "errors" list ("Unprocessable Entity" as the message),
// so both are read.
func reviewAnswer(op string, err error) error {
	var e *Error
	if err == nil || !errors.As(err, &e) || Transient(err) {
		return err
	}
	text := strings.ToLower(e.Message + "\n" + e.Details)
	switch op {
	case "submit":
		if e.Status != 422 {
			return err
		}
		switch {
		case strings.Contains(text, "your own pull request"):
			return &ReviewRefusal{Kind: RefusalOwnPullRequest, Err: e}
		case strings.Contains(text, "must be part of the diff"), strings.Contains(text, "part of the same hunk"),
			strings.Contains(text, "could not be resolved"), strings.Contains(text, "diff hunk can't be blank"):
			return &ReviewRefusal{Kind: RefusalLineNotInDiff, Err: e}
		}
	case "dismiss":
		switch {
		case e.Status == 422 && strings.Contains(text, "already") && strings.Contains(text, "dismissed"):
			return nil
		case e.Status == 403 || e.Status == 422:
			return &ReviewRefusal{Kind: RefusalDismissal, Err: e}
		}
	}
	return err
}

type ghReviewCommentIn struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// SubmitReview posts a review on a pull request, pinned to in.CommitID.
// An empty CommitID or an unknown event is refused here, before GitHub is
// asked. LineNotInDiff and OwnPullRequest tell GitHub's refusals apart.
func (g *GitHub) SubmitReview(ctx context.Context, in SubmitReview) (SubmittedReview, error) {
	if in.CommitID == "" {
		return SubmittedReview{}, errors.New("a review names the commit it reviewed: commit_id is empty")
	}
	switch in.Event {
	case EventApprove, EventRequestChanges, EventComment:
	default:
		return SubmittedReview{}, fmt.Errorf("unknown review event %q", in.Event)
	}
	body := struct {
		CommitID string              `json:"commit_id"`
		Event    string              `json:"event"`
		Body     string              `json:"body"`
		Comments []ghReviewCommentIn `json:"comments,omitempty"`
	}{CommitID: in.CommitID, Event: in.Event, Body: in.Body}
	for _, c := range in.Comments {
		side, startSide := c.sides()
		out := ghReviewCommentIn{Path: c.Path, Line: c.Line, Side: side, Body: c.Body}
		if c.StartLine > 0 {
			out.StartLine, out.StartSide = c.StartLine, startSide
		}
		body.Comments = append(body.Comments, out)
	}
	var out ghReview
	err := g.do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/reviews", in.Slug, in.Number), body, &out)
	if err != nil {
		return SubmittedReview{}, reviewAnswer("submit", err)
	}
	return out.submitted(), nil
}

func (r ghReview) submitted() SubmittedReview {
	return SubmittedReview{ID: r.ID, NodeID: r.NodeID, URL: r.HTMLURL, State: r.State, CommitID: r.CommitID}
}

// DismissReview dismisses a review with message. A review already
// dismissed is success; a refusal is a ReviewRefusal (DismissRefused),
// never worth retrying.
func (g *GitHub) DismissReview(ctx context.Context, slug string, number int, reviewID int64, message string) error {
	err := g.do(ctx, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d/dismissals", slug, number, reviewID),
		map[string]string{"message": message, "event": "DISMISS"}, nil)
	return reviewAnswer("dismiss", err)
}

// FindOwnReview recovers a review whose answer was lost: the newest one by
// dude's account (Login) whose body ends with ReviewMarker(taskID,
// claimID). A person's review quoting the marker is not dude's, nor is one
// by dude's account that only quotes it.
func (g *GitHub) FindOwnReview(ctx context.Context, slug string, number int, taskID, claimID string) (SubmittedReview, bool, error) {
	self, err := g.Login(ctx)
	if err != nil {
		return SubmittedReview{}, false, err
	}
	reviews, err := pages[ghReview](ctx, g, fmt.Sprintf("/repos/%s/pulls/%d/reviews", slug, number))
	if err != nil {
		return SubmittedReview{}, false, err
	}
	marker := ReviewMarker(taskID, claimID)
	for i := len(reviews) - 1; i >= 0; i-- {
		r := reviews[i]
		if self != "" && strings.EqualFold(login(r.User), self) && reviewMarked(r.Body, marker) {
			return r.submitted(), true, nil
		}
	}
	return SubmittedReview{}, false, nil
}

// DiffLines is where a file's diff has hunks. Known is false when GitHub
// sent no patch (a diff too large, a binary file): no line is known to be
// in it.
type DiffLines struct {
	Known bool
	Hunks []Hunk
}

// Hunk is the lines one hunk shows on each side (added, removed and
// context): Left..Left+LeftLines-1 in the base, Right.. in the head.
type Hunk struct{ Left, LeftLines, Right, RightLines int }

// hunk is the index of the hunk showing line on side, and whether one does.
func (d DiffLines) hunk(side string, line int) (int, bool) {
	for i, h := range d.Hunks {
		start, n := h.Right, h.RightLines
		if side == "LEFT" {
			start, n = h.Left, h.LeftLines
		}
		if line >= start && line < start+n {
			return i, true
		}
	}
	return 0, false
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parsePatch reads a file's patch (hunks only, as GitHub's files listing
// gives it) into DiffLines: each hunk is what its header says it spans.
// Only a header starts with "@@"; a hunk's lines start with ' ', + or -.
func parsePatch(patch string) DiffLines {
	d := DiffLines{Known: true}
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue // most lines; the regexp is for headers only
		}
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := func(s string) int {
			if s == "" {
				return 1 // "-l" alone is one line
			}
			v, _ := strconv.Atoi(s)
			return v
		}
		d.Hunks = append(d.Hunks, Hunk{Left: n(m[1]), LeftLines: n(m[2]), Right: n(m[3]), RightLines: n(m[4])})
	}
	return d
}

// PullDiffLines is, per path the pull request changes, the lines of its
// diff's hunks (DiffLines), from GitHub's files listing at the pull
// request's current head. A file GitHub sent no patch for is not Known.
func (g *GitHub) PullDiffLines(ctx context.Context, slug string, number int) (map[string]DiffLines, error) {
	// GitHub lists at most 3000 files, 30 pages of 100.
	files, err := pagesUpTo[ghFile](ctx, g, fmt.Sprintf("/repos/%s/pulls/%d/files", slug, number), 30)
	if err != nil {
		return nil, err
	}
	out := make(map[string]DiffLines, len(files))
	for _, f := range files {
		if f.Patch == "" {
			out[f.Filename] = DiffLines{}
			continue
		}
		out[f.Filename] = parsePatch(f.Patch)
	}
	return out, nil
}

// PlaceComments splits a review's comments by where GitHub will take
// them: inline, a line in a hunk of its file's diff on its side; folded,
// the rest, for the review's body (FoldComments). A multi-line range
// needs both ends in the same hunk, else it collapses to Line alone. A
// file with no known diff folds every comment on it.
func PlaceComments(comments []ReviewComment, diff map[string]DiffLines) (inline, folded []ReviewComment) {
	for _, c := range comments {
		side, startSide := c.sides()
		d := diff[c.Path] // a path not in the diff is not Known
		h, in := d.hunk(side, c.Line)
		if !d.Known || c.Line <= 0 || !in {
			folded = append(folded, c)
			continue
		}
		if c.StartLine > 0 {
			hs, inStart := d.hunk(startSide, c.StartLine)
			if !inStart || hs != h || startSide == side && c.StartLine >= c.Line {
				c.StartLine, c.StartSide = 0, ""
			}
		}
		inline = append(inline, c)
	}
	return inline, folded
}

// FoldComments adds comments that cannot be inline to a review's body,
// under "Not on a changed line", as "path:line — text". A ReviewMarker
// ending body stays last.
func FoldComments(body string, folded []ReviewComment) string {
	if len(folded) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString("\n\n**Not on a changed line**\n")
	for _, c := range folded {
		where := c.Path
		if c.Line > 0 {
			where += ":" + strconv.Itoa(c.Line)
		}
		b.WriteString("\n- `" + where + "` — " + strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n  "))
	}
	section := b.String()
	trimmed := strings.TrimRight(body, " \t\r\n")
	if i := strings.LastIndex(trimmed, "\n"); strings.HasPrefix(strings.TrimSpace(trimmed[i+1:]), "<!-- dude:review ") {
		return strings.TrimRight(trimmed[:max(i, 0)], " \t\r\n") + section + "\n\n" + strings.TrimSpace(trimmed[i+1:])
	}
	return trimmed + section
}
