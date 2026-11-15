package delivery

import "testing"

// A pull request is read in every form GitHub shows it; anything else is
// refused.
func TestParsePRReadsEachWayAPullRequestIsNamed(t *testing.T) {
	for _, c := range []struct{ in, owner, repo string }{
		{"12", "", ""},
		{" #12 ", "", ""},
		{"dude#12", "", "dude"},
		{"marcioapm/dude-ro-s5u#12", "marcioapm", "dude-ro-s5u"},
		{"https://github.com/marcioapm/dude/pull/12", "marcioapm", "dude"},
		{"https://github.com/marcioapm/dude/pull/12/files", "marcioapm", "dude"},
		{"https://github.com/marcioapm/dude/pull/12#discussion_r123", "marcioapm", "dude"},
		{"https://github.com/marcioapm/dude/pull/12?w=1", "marcioapm", "dude"},
		{"github.com/marcioapm/dude/pull/12", "marcioapm", "dude"},
	} {
		owner, repo, n, err := parsePR(c.in)
		if err != nil || owner != c.owner || repo != c.repo || n != 12 {
			t.Errorf("parsePR(%q) = %q, %q, %d, %v; want %q, %q, 12", c.in, owner, repo, n, err, c.owner, c.repo)
		}
	}
	for _, in := range []string{"", "owner/repo#", "#0", "x#y", "a/b/c#1", "https://github.com/marcioapm/dude/issues/12",
		"https://github.com/marcioapm/dude/pull/x", "https://github.com/marcioapm/dude/pull/12x"} {
		if _, _, _, err := parsePR(in); err == nil {
			t.Errorf("parsePR(%q) was read", in)
		}
	}
}
