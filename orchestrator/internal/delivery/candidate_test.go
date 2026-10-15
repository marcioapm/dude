package delivery

import (
	"strings"
	"testing"
)

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

// Refreshing a pull request's description replaces its Review and
// Verification sections and keeps everything else, a person's edits
// included; a section the fresh text lacks goes before the sign-off.
func TestAPullRequestsSectionsAreReplacedAndTheRestKept(t *testing.T) {
	body := "Say hello\n\nA person's note.\n\n## Review\nold review\n\n## Verification\nTested at `aaa`.\n\n" + signOff +
		"\n\nOne of 2 pull requests."
	fresh := "Say hello\n\n## Review\nnew review\n\n## Verification\nTested at `bbb`.\n\n" + signOff
	got := body
	for _, h := range []string{"## Review", "## Verification"} {
		got = withSection(got, h, section(fresh, h))
	}
	want := "Say hello\n\nA person's note.\n\n## Review\nnew review\n\n## Verification\nTested at `bbb`.\n\n" + signOff +
		"\n\nOne of 2 pull requests."
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// A description that had no Verification gets one before the sign-off.
	plain := "Say hello\n\n" + signOff
	if got := withSection(plain, "## Verification", "## Verification\nTested."); got != "Say hello\n\n## Verification\nTested.\n\n"+signOff {
		t.Errorf("added: %q", got)
	}
	if got := withSection(plain, "## Verification", ""); got != plain {
		t.Errorf("nothing to add changed it: %q", got)
	}
	if s := section(body, "## Nothing"); s != "" || !strings.HasPrefix(section(body, "## Review"), "## Review\nold") {
		t.Errorf("section read wrong: %q", s)
	}
}
