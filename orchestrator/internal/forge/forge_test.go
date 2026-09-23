package forge

import "testing"

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

func TestApprovalsAndGreenChecksWakeNobody(t *testing.T) {
	approved := open
	approved.Review = "approved"
	if s := Classify(prior, approved, nil, nil); s != nil {
		t.Errorf("approval: %+v", s)
	}
	if s := Classify(PriorState{State: StateOpen, Checks: ChecksFailing}, open, nil, nil); s != nil {
		t.Errorf("green: %+v", s)
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
