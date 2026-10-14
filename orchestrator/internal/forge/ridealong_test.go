package forge

import (
	"reflect"
	"slices"
	"testing"
)

func bot(id, author, body string) Feedback {
	return Feedback{ID: id, Author: author, Body: body, Kind: KindComment}
}

// A bot's comment never wakes a fixer: alone, it is not actionable.
func TestABotCommentAloneWakesNoFixer(t *testing.T) {
	for _, author := range []string{"coderabbitai[bot]", "deploy-bot", "github-actions"} {
		if s := Classify(prior, open, []Feedback{bot("c1", author, "Please rename foo.")}, nil); s != nil {
			t.Errorf("%s: signal = %+v, want nil", author, s)
		}
	}
}

// When a requested reviewer's review wakes a fixer, the bots' comments
// since the last fix ride along, marked as bots'; never otherwise.
func TestBotCommentsRideAlongOnlyWithARequestedReviewersReview(t *testing.T) {
	review := ActionableFeedback{Source: "review", ID: "review-9", Author: "Jerv", Body: "Rename this.", Kind: KindChangesRequested}
	line := ActionableFeedback{Source: "review", ID: "line-comment-3", Author: "jerv", Body: "And this.", Kind: KindLineComment}
	comment := ActionableFeedback{Source: "review", ID: "issue-comment-4", Author: "jerv", Body: "Please fix.", Kind: KindComment}
	checks := ActionableFeedback{Source: "checks", Body: "CI is failing."}
	bots := []Feedback{
		bot("issue-comment-1", "coderabbitai[bot]", "Consider a constant."),
		bot("issue-comment-2", "alice", "A person's comment is not a bot's."),
		bot("issue-comment-5", "deploy-bot", ""),
		{ID: "line-comment-6", Author: "sonar-bot", Body: "Complexity 12.", Path: "greet.go", Kind: KindLineComment},
	}
	want := []ActionableFeedback{review,
		{Source: SourceBot, ID: "issue-comment-1", Author: "coderabbitai[bot]", Body: "Consider a constant.", Kind: KindComment},
		{Source: SourceBot, ID: "line-comment-6", Author: "sonar-bot", Body: "Complexity 12.", Path: "greet.go", Kind: KindLineComment}}

	if got := RideAlong([]ActionableFeedback{review}, []string{"jerv"}, bots); !reflect.DeepEqual(got, want) {
		t.Errorf("a requested reviewer's review: %+v", got)
	}
	if got := RideAlong([]ActionableFeedback{line}, []string{"jerv"}, bots); len(got) != 3 {
		t.Errorf("a requested reviewer's line comment: %+v", got)
	}
	for name, batch := range map[string][]ActionableFeedback{
		"someone not requested":                {review},
		"a requested reviewer's plain comment": {comment},
		"failing checks":                       {checks},
	} {
		requested := []string{"jerv"}
		if name == "someone not requested" {
			requested = []string{"ana"}
		}
		if got := RideAlong(batch, requested, bots); !reflect.DeepEqual(got, batch) {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// One already in the batch is not added twice.
	withIt := append([]ActionableFeedback{review}, want[1])
	if got := RideAlong(withIt, []string{"jerv"}, bots); len(got) != 3 {
		t.Errorf("a bot comment twice: %+v", got)
	}
}

// Who was requested: asked and not reviewed, or asked again after
// reviewing; never a team, never someone who reviewed and was not asked again.
func TestRequestedIsWhoOwesAReview(t *testing.T) {
	got := Requested([]Review{{Login: "ana", State: "REQUESTED"}, {Login: "jerv", State: "APPROVED", Rerequested: true},
		{Login: "bo", State: "COMMENTED"}, {Login: "acme/core", State: "REQUESTED", Team: true}})
	if !slices.Equal(got, []string{"ana", "jerv"}) {
		t.Errorf("requested = %v", got)
	}
}
