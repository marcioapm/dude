package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every false positive here costs a fix Run; every false negative is a
// person's request silently ignored. The tests pin both directions.

var open = Status{PullRequestRef: PullRequestRef{Number: 1, State: StateOpen, HeadSHA: "abc"}, Checks: ChecksPassing, Review: "pending"}
var prior = PriorState{State: StateOpen, Checks: ChecksPassing}

func comment(body string) Feedback {
	return Feedback{ID: body, Author: "alice", Body: body, Kind: KindComment, CreatedAt: "2026-09-22T10:00:00Z"}
}

func TestAPersonAskingForAChangeIsActionable(t *testing.T) {
	if s := Classify(prior, open, []Feedback{comment("Please rename `foo` to `bar`.")}, nil); s == nil || s.Kind != "actionable" {
		t.Fatalf("signal = %+v", s)
	}
}

func TestAChangesRequestedReviewIsARequestHoweverBrief(t *testing.T) {
	f := comment("ok")
	f.Kind = KindChangesRequested
	if s := Classify(prior, open, []Feedback{f}, nil); s == nil || s.Kind != "actionable" {
		t.Fatalf("signal = %+v", s)
	}
}

func TestChecksTurningRedWakeAFixer(t *testing.T) {
	failing := open
	failing.Checks = ChecksFailing
	s := Classify(prior, failing, nil, nil)
	if s == nil || s.Feedback[0].Source != "checks" {
		t.Fatalf("signal = %+v", s)
	}
}

func TestChecksThatWereAlreadyRedDoNot(t *testing.T) {
	failing := open
	failing.Checks = ChecksFailing
	if s := Classify(PriorState{State: StateOpen, Checks: ChecksFailing}, failing, nil, nil); s != nil {
		t.Fatalf("signal = %+v, want nil", s)
	}
}

func TestALineCommentKeepsItsFile(t *testing.T) {
	f := comment("This leaks the token")
	f.Kind, f.Path = KindLineComment, "src/push.go"
	if s := Classify(prior, open, []Feedback{f}, nil); s == nil || s.Feedback[0].Path != "src/push.go" {
		t.Fatalf("signal = %+v", s)
	}
}

// An approval or checks going green wake no fixer: they may make the work
// item ready to merge, and that is all the workflow is told.
func TestApprovalsAndGreenChecksWakeNoFixer(t *testing.T) {
	approved := open
	approved.Review = ReviewApproved
	if s := Classify(prior, approved, nil, nil); s == nil || s.Kind != "readiness" || len(s.Feedback) > 0 {
		t.Errorf("approval: %+v", s)
	}
	// Green, but not approved: nothing is ready, nothing to say.
	if s := Classify(PriorState{State: StateOpen, Checks: ChecksFailing}, open, nil, nil); s != nil {
		t.Errorf("green: %+v", s)
	}
	// Approved already, checks going green: now it is ready.
	approved.Checks = ChecksPassing
	if s := Classify(PriorState{State: StateOpen, Checks: ChecksPending, Review: ReviewApproved}, approved, nil, nil); s == nil || s.Kind != "readiness" {
		t.Errorf("approved then green: %+v", s)
	}
}

func TestCourtesyIsNotARequest(t *testing.T) {
	// "LGTM so far, thanks!" once slipped through an anchored pattern and woke
	// a fixer for nothing, in the end-to-end test.
	for _, body := range []string{"LGTM", "lgtm!", "Thanks", "Looks good to me.", "👍", "Ship it",
		"LGTM so far, thanks!", "Great work, thank you 🎉", "looks good for now", "❤️"} {
		if IsActionableComment(comment(body), nil) {
			t.Errorf("%q was treated as a request", body)
		}
	}
}

func TestARequestAmongCourtesiesIsStillARequest(t *testing.T) {
	for _, body := range []string{"LGTM, but please rename foo", "Looks good. Why is this async?",
		"thanks — can you add a test", "Great work, fix the typo in the README", "Thanks! Could you also add a test?"} {
		if !IsActionableComment(comment(body), nil) {
			t.Errorf("%q was filtered out", body)
		}
	}
}

func TestBotsAndTheFactoryItselfAreNotFeedback(t *testing.T) {
	for _, author := range []string{"dependabot[bot]", "codecov-bot", "github-actions"} {
		f := comment("Coverage dropped 2%")
		f.Author = author
		if IsActionableComment(f, nil) {
			t.Errorf("bot %s was treated as a reviewer", author)
		}
	}
	f := comment("Addressed in abc123")
	f.Author = "dude-factory"
	if IsActionableComment(f, []string{"dude-factory"}) {
		t.Error("the factory's own comment woke a fixer")
	}
}

func TestMergeAndCloseEndTheLoopOnce(t *testing.T) {
	for _, state := range []string{StateMerged, StateClosed} {
		cur := open
		cur.State = state
		s := Classify(prior, cur, []Feedback{comment("Please change this")}, nil)
		if s == nil || s.Kind != "terminal" || s.State != state {
			t.Errorf("%s: %+v", state, s)
		}
		if s := Classify(PriorState{State: state}, cur, nil, nil); s != nil {
			t.Errorf("%s seen twice reported again: %+v", state, s)
		}
	}
}

func TestSlugFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/api.git":      "acme/api",
		"git@github.com:acme/api.git":          "acme/api",
		"git://127.0.0.1:9418/acme/target.git": "acme/target",
		"/tmp/remote.git":                      "",
	} {
		if got := SlugFromURL(in); got != want {
			t.Errorf("SlugFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A blip must be retried; a refusal must not be retried into the same wall.
func TestTransientErrors(t *testing.T) {
	for err, want := range map[error]bool{
		&Error{Status: 502, Message: "Bad Gateway"}:                               true,
		&Error{Status: 429, Message: "slow down"}:                                 true,
		&Error{Status: 403, Message: "API rate limit exceeded"}:                   true,
		fmt.Errorf("compare: %w", &Unreachable{errors.New("connection refused")}): true,
		&Error{Status: 422, Message: "Update is not a fast forward"}:              false,
		&Error{Status: 403, Message: "Resource not accessible by integration"}:    false,
		errors.New("lux reported no push result"):                                 false,
	} {
		if got := Transient(err); got != want {
			t.Errorf("Transient(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestAlreadyExistsIsOnlyTheDuplicatePR(t *testing.T) {
	if !(&Error{Status: 422, Message: "A pull request already exists for acme:branch."}).AlreadyExists() {
		t.Error("a duplicate PR was not recognised")
	}
	if (&Error{Status: 422, Message: "No commits between main and branch"}).AlreadyExists() {
		t.Error("another validation error was taken for a duplicate PR")
	}
}

func TestCheckRunsCountAndTheWorstWins(t *testing.T) {
	for _, c := range []struct{ status, conclusion, want string }{
		{"queued", "", ChecksPending}, {"in_progress", "", ChecksPending},
		{"completed", "success", ChecksPassing}, {"completed", "skipped", ChecksPassing},
		{"completed", "neutral", ChecksPassing}, {"completed", "failure", ChecksFailing},
		{"completed", "cancelled", ChecksFailing}, {"completed", "timed_out", ChecksFailing},
	} {
		if got := checkRunState(c.status, c.conclusion); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.status, c.conclusion, got, c.want)
		}
	}
	if worseChecks(ChecksUnknown, ChecksPassing) != ChecksPassing ||
		worseChecks(ChecksPassing, ChecksPending) != ChecksPending ||
		worseChecks(ChecksFailing, ChecksPending) != ChecksFailing {
		t.Error("worseChecks ranks wrong")
	}
}

// A token without Checks: read still reads the pull request: its statuses
// and reviews are what it has.
func TestCheckRunsItCannotReadAreNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"}}`)
		case r.URL.Path == "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, `{"state":"success","total_count":1}`)
		case r.URL.Path == "/repos/acme/api/commits/abc/check-runs":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"Resource not accessible by personal access token"}`)
		case r.URL.Path == "/repos/acme/api/pulls/1/reviews":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	st, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).PullRequest(context.Background(), "acme/api", 1)
	if err != nil || st.Checks != ChecksPassing {
		t.Fatalf("status %+v, err %v", st, err)
	}
}

// Ready on a new head is news: the workflow waits for its own head.
func TestReadyOnANewHeadIsNews(t *testing.T) {
	approved := open
	approved.Review = ReviewApproved
	was := PriorState{State: StateOpen, Checks: ChecksPassing, Review: ReviewApproved, HeadSHA: "old"}
	if s := Classify(was, approved, nil, nil); s == nil || s.Kind != "readiness" {
		t.Errorf("ready on a new head: %+v", s)
	}
	was.HeadSHA = approved.HeadSHA
	if s := Classify(was, approved, nil, nil); s != nil {
		t.Errorf("ready on the same head again: %+v", s)
	}
}
