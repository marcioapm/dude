package phases

import "testing"

// What the agent and members are told of a failed clone: git's last fatal
// line for the agent; for members, an authentication failure in plain
// words, anything else as git said it.
func TestAFailedCloneIsSaidInPlainWords(t *testing.T) {
	const auth = "git clone: exit status 128: error: unable to read askpass response from '/bin/false'\n" +
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled"
	if got, want := cloneFailedNote("BILL", "billing", auth),
		"BILL/billing could not be checked out (could not read Username for 'https://github.com': terminal prompts disabled). "+
			"It is not in /workspace; a member can check the repository's URL and access and link it again."; got != want {
		t.Errorf("note:\n got %s\nwant %s", got, want)
	}
	if got := cloneReason(auth); got != cloneSignInReason {
		t.Errorf("an auth failure: %s", got)
	}
	if got := cloneReason("git clone: exit status 128: remote: Repository not found.\nfatal: repository 'https://github.com/acme/x/' not found"); got != cloneSignInReason {
		t.Errorf("a repository GitHub hides: %s", got)
	}
	if got := cloneReason("ref not found"); got != "git said: ref not found." {
		t.Errorf("another failure: %s", got)
	}
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if got := []rune(shortCloneError(string(long))); len(got) != 200 || got[199] != '…' {
		t.Errorf("a long error is %d runes", len(got))
	}
	if got := shortCloneError(""); got != "no reason given" {
		t.Errorf("an empty error: %q", got)
	}
}
