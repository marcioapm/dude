package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// thirty is a file of 30 numbered lines.
func thirty(change map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		line := fmt.Sprintf("line %d", i)
		if c, ok := change[i]; ok {
			line = c
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// reviewFake is a repository whose main has a.txt (30 lines), and a pull
// request by alice from feature changing its lines 3 and 25: two hunks,
// lines 1-6 and 22-28 on each side.
func reviewFake(t *testing.T) (*fakegithub.Server, *GitHub, int, string) {
	t.Helper()
	bare := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", bare}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--bare", "--initial-branch=main")
	git("update-ref", "refs/heads/main", git("-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", git("mktree"), "-m", "Initial"))
	fake := fakegithub.New(bare, "acme/repo")
	if fake.CommitFiles("main", "Add a", map[string]string{"a.txt": thirty(nil)}) == "" {
		t.Fatal("commit on main failed")
	}
	n := fake.OpenAsPerson("alice", "feature", "main", "Change a")
	head := fake.CommitFiles("feature", "Change a", map[string]string{"a.txt": thirty(map[int]string{3: "three", 25: "twenty-five"})})
	if head == "" {
		t.Fatal("commit on feature failed")
	}
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, NewGitHub(Credential{Secret: "x", APIBaseURL: srv.URL}), n, head
}

// commit_id is always sent, and a review naming no commit is refused
// before GitHub is asked: GitHub would pin it to whatever the head is when
// the call arrives.
func TestASubmittedReviewIsPinnedToTheReviewedCommit(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"node_id":"PRR_7","state":"COMMENTED","commit_id":"abc","html_url":"https://x/7"}`))
	}))
	defer srv.Close()
	g := NewGitHub(Credential{Secret: "x", APIBaseURL: srv.URL})
	ctx := context.Background()

	if _, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: 1, Event: EventComment, Body: "b"}); err == nil {
		t.Fatal("a review with no commit_id was accepted")
	}
	if len(got) != 0 {
		t.Fatalf("a review with no commit_id reached GitHub: %v", got)
	}
	out, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: 1, CommitID: "abc", Event: EventComment, Body: "b",
		Comments: []ReviewComment{{Path: "a.txt", Line: 5, StartLine: 3, Body: "x"}}})
	if err != nil || out.ID != 7 || out.NodeID != "PRR_7" || out.URL != "https://x/7" || out.CommitID != "abc" {
		t.Fatalf("submit: %+v %v", out, err)
	}
	if len(got) != 1 || got[0]["commit_id"] != "abc" || got[0]["event"] != "COMMENT" {
		t.Fatalf("sent: %v", got)
	}
	c := got[0]["comments"].([]any)[0].(map[string]any)
	if c["line"] != 5.0 || c["side"] != "RIGHT" || c["start_line"] != 3.0 || c["start_side"] != "RIGHT" {
		t.Fatalf("comment sent as %v, want line/side/start_line/start_side", c)
	}
}

func TestPlacementKeepsOnlyCommentsInAHunkInline(t *testing.T) {
	patch := "@@ -1,6 +1,6 @@\n line 1\n line 2\n-line 3\n+three\n line 4\n line 5\n line 6\n" +
		"@@ -22,7 +22,8 @@ func x\n line 22\n line 23\n line 24\n-line 25\n+twenty-five\n+extra\n line 26\n line 27\n line 28\n"
	d := parsePatch(patch)
	diff := map[string]DiffLines{"a.txt": d, "big.txt": {}}
	if _, ok := d.hunk("RIGHT", 27); !ok {
		t.Fatalf("right 27 is in the second hunk: %+v", d)
	}
	if _, ok := d.hunk("LEFT", 29); ok {
		t.Fatalf("left 29 is past the second hunk: %+v", d)
	}
	in := []ReviewComment{
		{Path: "a.txt", Line: 3, Body: "in the first hunk"},
		{Path: "a.txt", Line: 3, Side: "LEFT", Body: "the removed line"},
		{Path: "a.txt", Line: 12, Body: "between hunks"},
		{Path: "a.txt", Line: 25, StartLine: 22, Body: "a range in one hunk"},
		{Path: "a.txt", Line: 25, StartLine: 4, Body: "a range across hunks"},
		{Path: "big.txt", Line: 1, Body: "no patch"},
		{Path: "other.txt", Line: 1, Body: "not in the pull request"},
	}
	inline, folded := PlaceComments(in, diff)
	want := []ReviewComment{in[0], in[1], in[3], {Path: "a.txt", Line: 25, Body: "a range across hunks"}}
	if !reflect.DeepEqual(inline, want) {
		t.Fatalf("inline:\n%+v\nwant\n%+v", inline, want)
	}
	if !reflect.DeepEqual(folded, []ReviewComment{in[2], in[5], in[6]}) {
		t.Fatalf("folded: %+v", folded)
	}
	body := FoldComments("Looks fine.\n\n"+ReviewMarker("wi_1", "c1"), folded)
	if !reviewMarked(body, ReviewMarker("wi_1", "c1")) || !strings.Contains(body, "**Not on a changed line**") ||
		!strings.Contains(body, "`a.txt:12` — between hunks") || !strings.Contains(body, "`big.txt:1` — no patch") {
		t.Fatalf("folded body:\n%s", body)
	}
}

// PullDiffLines reads each file's hunks from GitHub's patch; a file whose
// patch GitHub left out is not known.
func TestPullDiffLinesReadsThePatch(t *testing.T) {
	_, g, n, _ := reviewFake(t)
	got, err := g.PullDiffLines(context.Background(), "acme/repo", n)
	if err != nil {
		t.Fatal(err)
	}
	a := got["a.txt"]
	if !a.Known {
		t.Fatalf("a.txt: %+v", a)
	}
	for _, l := range []int{1, 3, 6, 22, 25, 28} {
		if _, ok := a.hunk("RIGHT", l); !ok {
			t.Errorf("right line %d not in a hunk", l)
		}
	}
	if _, ok := a.hunk("RIGHT", 12); ok {
		t.Error("right line 12 is in no hunk")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"filename":"huge.json","status":"modified","additions":90000}]`))
	}))
	defer srv.Close()
	got, err = NewGitHub(Credential{Secret: "x", APIBaseURL: srv.URL}).PullDiffLines(context.Background(), "acme/repo", 1)
	if d, ok := got["huge.json"]; err != nil || !ok || d.Known {
		t.Fatalf("no patch: %+v %v", got, err)
	}
	inline, folded := PlaceComments([]ReviewComment{{Path: "huge.json", Line: 1}}, got)
	if len(inline) != 0 || len(folded) != 1 {
		t.Fatalf("a comment on a file with no patch: inline %v folded %v", inline, folded)
	}
}

// GitHub will not let an account approve its own pull request: a caller
// tells that 422 apart and downgrades to COMMENT, which GitHub takes.
func TestApprovingOnesOwnPullRequestIsRecognised(t *testing.T) {
	fake, g, n, head := reviewFake(t)
	fake.Set(func(s *fakegithub.Server) { s.Poster = "alice" })
	ctx := context.Background()
	for _, event := range []string{EventApprove, EventRequestChanges} {
		_, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: event, Body: "b"})
		if !OwnPullRequest(err) || LineNotInDiff(err) || Transient(err) {
			t.Fatalf("%s on alice's own pull request: %v, want OwnPullRequest", event, err)
		}
	}
	if _, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventComment, Body: "b"}); err != nil {
		t.Fatalf("COMMENT on one's own pull request: %v", err)
	}
}

// A comment on a line outside the diff fails the whole review with a 422
// a caller tells apart, to retry with the comments in the body.
func TestACommentOutsideTheDiffIsRecognised(t *testing.T) {
	fake, g, n, head := reviewFake(t)
	ctx := context.Background()
	_, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventRequestChanges, Body: "b",
		Comments: []ReviewComment{{Path: "a.txt", Line: 3, Body: "fine"}, {Path: "a.txt", Line: 12, Body: "untouched"}}})
	if !LineNotInDiff(err) || OwnPullRequest(err) {
		t.Fatalf("a comment on line 12: %v, want LineNotInDiff", err)
	}
	if len(fake.Pull(n).Reviews) != 0 {
		t.Fatalf("the refused review was kept: %+v", fake.Pull(n).Reviews)
	}
	_, err = g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventComment, Body: "b",
		Comments: []ReviewComment{{Path: "a.txt", Line: 25, StartLine: 3, Body: "a range across hunks"}}})
	if !LineNotInDiff(err) {
		t.Fatalf("a range across hunks: %v, want LineNotInDiff", err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Message != "Unprocessable Entity" || !strings.Contains(e.Details, "same hunk") {
		t.Fatalf("GitHub's errors list is kept as Details: %#v", e)
	}
	out, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventRequestChanges, Body: "b",
		Comments: []ReviewComment{{Path: "a.txt", Line: 25, StartLine: 22, Body: "this range"}}})
	if err != nil || out.State != "CHANGES_REQUESTED" || out.CommitID != head {
		t.Fatalf("an inline comment in a hunk: %+v %v", out, err)
	}
	lc := fake.Pull(n).LineComments
	if len(lc) != 1 || lc[0].ReviewID != out.ID || lc[0].Author != fakegithub.Login || lc[0].Line != 25 {
		t.Fatalf("line comments: %+v", lc)
	}
}

// A review whose answer was lost is found again by its marker, and only
// dude's account's: a person's review ending in the marker, or dude's
// quoting it, is not it.
func TestFindOwnReviewRecoversALostAnswerAndIgnoresQuotes(t *testing.T) {
	fake, g, n, head := reviewFake(t)
	ctx := context.Background()
	marker := ReviewMarker("wi_1", "claim_1")
	fake.ReviewSaying(n, "bob", "COMMENTED", "dude said this:\n\n"+marker)
	if _, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventComment,
		Body: "quoting:\n\n> " + marker}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventComment,
		Body: "in a fence:\n\n```\n" + marker}); err != nil {
		t.Fatal(err)
	}
	if got, found, err := g.FindOwnReview(ctx, "acme/repo", n, "wi_1", "claim_1"); err != nil || found {
		t.Fatalf("before dude's review: %+v %v %v, want none", got, found, err)
	}

	fake.Set(func(s *fakegithub.Server) { s.ReviewTimeoutOnce = true })
	_, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventApprove,
		Body: "Approved.\n\n" + marker})
	if err == nil || !Transient(err) {
		t.Fatalf("a dropped answer: %v, want an ambiguous (transient) error", err)
	}
	got, found, err := g.FindOwnReview(ctx, "acme/repo", n, "wi_1", "claim_1")
	reviews := fake.Pull(n).Reviews
	last := reviews[len(reviews)-1]
	if err != nil || !found || got.ID != last.ID || got.State != "APPROVED" || got.CommitID != head {
		t.Fatalf("after the lost answer: %+v %v %v, want review %+v", got, found, err, last)
	}
	if _, found, _ := g.FindOwnReview(ctx, "acme/repo", n, "wi_1", "claim_2"); found {
		t.Fatal("another claim's review was found")
	}
}

// A refused dismissal is told apart and not retried; one already
// dismissed is done.
func TestDismissalRefusedIsTypedAndAlreadyDismissedIsDone(t *testing.T) {
	fake, g, n, head := reviewFake(t)
	ctx := context.Background()
	out, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventApprove, Body: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	fake.Set(func(s *fakegithub.Server) { s.RefuseDismissal = true })
	err = g.DismissReview(ctx, "acme/repo", n, out.ID, "stale")
	if !DismissRefused(err) || Transient(err) {
		t.Fatalf("a dismissal protection forbids: %v, want DismissRefused", err)
	}
	if !strings.Contains(err.Error(), "protected branch rules") {
		t.Fatalf("GitHub's words are kept: %v", err)
	}
	fake.Set(func(s *fakegithub.Server) { s.RefuseDismissal = false })
	if err := g.DismissReview(ctx, "acme/repo", n, out.ID, "stale"); err != nil {
		t.Fatal(err)
	}
	if st := fake.Pull(n).Reviews[0].State; st != "DISMISSED" {
		t.Fatalf("state %s, want DISMISSED", st)
	}
	if err := g.DismissReview(ctx, "acme/repo", n, out.ID, "stale"); err != nil {
		t.Fatalf("dismissing again: %v, want done", err)
	}
	fake.Set(func(s *fakegithub.Server) { s.ReviewFail5xx = 1 })
	if _, err := g.SubmitReview(ctx, SubmitReview{Slug: "acme/repo", Number: n, CommitID: head, Event: EventComment, Body: "b"}); !Transient(err) {
		t.Fatalf("a 5xx: %v, want transient", err)
	}
	if len(fake.Pull(n).Reviews) != 1 {
		t.Fatalf("a failed submission was kept: %+v", fake.Pull(n).Reviews)
	}
}

// The matching of GitHub's words is one place, and reads the "errors" list
// a 422 carries as well as its message.
func TestReviewAnswerReadsGitHubsWords(t *testing.T) {
	for _, c := range []struct {
		op   string
		err  *Error
		kind string
		nil_ bool
	}{
		{"submit", &Error{Status: 422, Message: "Unprocessable Entity", Details: "Pull request review thread line must be part of the diff"}, RefusalLineNotInDiff, false},
		{"submit", &Error{Status: 422, Message: "Unprocessable Entity", Details: "Line could not be resolved"}, RefusalLineNotInDiff, false},
		{"submit", &Error{Status: 422, Message: "Unprocessable Entity", Details: "Pull request review thread start line must be part of the same hunk as the line."}, RefusalLineNotInDiff, false},
		{"submit", &Error{Status: 422, Message: "Unprocessable Entity", Details: "Can not approve your own pull request"}, RefusalOwnPullRequest, false},
		{"submit", &Error{Status: 422, Message: "Validation Failed", Details: "Body is too long"}, "", false},
		{"submit", &Error{Status: 403, Message: "Can not approve your own pull request"}, "", false},
		{"dismiss", &Error{Status: 422, Message: "Can not dismiss a review that has already been dismissed"}, "", true},
		{"dismiss", &Error{Status: 403, Message: "protected branch rules"}, RefusalDismissal, false},
		{"dismiss", &Error{Status: 422, Message: "Can not dismiss a commented pull request review"}, RefusalDismissal, false},
		{"dismiss", &Error{Status: 404, Message: "Not Found"}, "", false},
		{"dismiss", &Error{Status: 502, Message: "Server Error"}, "", false},
	} {
		got := reviewAnswer(c.op, c.err)
		var r *ReviewRefusal
		switch {
		case c.nil_ && got != nil, !c.nil_ && got == nil:
			t.Errorf("%s %v: %v", c.op, c.err, got)
		case c.kind != "" && !reviewRefusalIs(got, c.kind):
			t.Errorf("%s %v: %v, want %s", c.op, c.err, got, c.kind)
		case c.kind == "" && errors.As(got, &r):
			t.Errorf("%s %v: %v, want GitHub's error as is", c.op, c.err, got)
		}
	}
}
