package delivery

import "testing"

// A pull request's fix is checked on a branch of its own beside the task
// branch, which a run's own branch name can never be.
func TestTheCandidateBranchIsBesideTheTaskBranch(t *testing.T) {
	if got := CandidateBranch("t1", 2); got != "dude/t1/attempt-2-candidate" {
		t.Errorf("CandidateBranch = %q", got)
	}
	st := &State{TaskID: "t1", Branch: "dude/t1/attempt-3"}
	if got := st.candidateBranch(); got != "dude/t1/attempt-3-candidate" {
		t.Errorf("a delivery's candidate = %q, want beside its own branch", got)
	}
	if got := (&State{TaskID: "t1"}).candidateBranch(); got != "dude/t1/attempt-1-candidate" {
		t.Errorf("a delivery naming no branch: %q", got)
	}
	adopted := &State{TaskID: "t1", Attempt: 2, Branch: "feature/half", Source: &Source{Kind: SourcePR, HeadBranch: "feature/half"}}
	if got := adopted.candidateBranch(); got != "dude/t1/attempt-2-candidate" {
		t.Errorf("an adopted source's candidate = %q, want under dude/, never beside the person's branch", got)
	}
}

// Refreshing a pull request's description after a promotion replaces
// dude's marked Review and Verification sections and keeps everything
// else, a person's edits and an unmarked heading of theirs included; a
// marked section the body lacks is appended.
func TestAPullRequestsSectionsAreReplacedAndTheRestKept(t *testing.T) {
	body := "Say hello\n\nA person's note.\n\n## Review\nTheir own review notes.\n\n" + markedReview("## Review\nold review") +
		"\n\n" + markedVerification("## Verification\nTested at `aaa`.") + "\n\n" + signOff + "\n\nOne of 2 pull requests."
	got := replaceVerification(replaceReview(body, "## Review\nnew review"), "## Verification\nTested at `bbb`.")
	want := "Say hello\n\nA person's note.\n\n## Review\nTheir own review notes.\n\n" + markedReview("## Review\nnew review") +
		"\n\n" + markedVerification("## Verification\nTested at `bbb`.") + "\n\n" + signOff + "\n\nOne of 2 pull requests."
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// A description that had no marked Review gets one, after the rest.
	plain := "Say hello\n\n" + signOff
	if got := replaceReview(plain, "## Review\n1 finding."); got != plain+"\n\n"+markedReview("## Review\n1 finding.") {
		t.Errorf("added: %q", got)
	}
	// The body dude opens with carries both sections marked, before the sign-off.
	opened := prBody("Say hello", nil, "## Review\nr", "## Verification\nv")
	if want := "Say hello\n\n" + markedReview("## Review\nr") + "\n\n" + markedVerification("## Verification\nv") + "\n\n" + signOff; opened != want {
		t.Errorf("opened:\n%q\nwant:\n%q", opened, want)
	}
	if again := replaceVerification(replaceReview(opened, "## Review\nr"), "## Verification\nv"); again != opened {
		t.Errorf("a refresh with the same sections changed it:\n%q", again)
	}
}
