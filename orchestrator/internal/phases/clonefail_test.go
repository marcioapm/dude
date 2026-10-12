package phases

import "testing"

// What the agent and members are told of a failed clone: git's last fatal
// line for the agent; for members, an authentication failure in plain
// words, anything else as git said it.
func TestAFailedCloneIsSaidInPlainWords(t *testing.T) {
	const auth = "git clone: exit status 128: error: unable to read askpass response from '/bin/false'\n" +
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled"
	if got, want := cloneFailedNote("BILL/billing", auth, sessionCloneRemedy),
		"BILL/billing could not be checked out (could not read Username for 'https://github.com': terminal prompts disabled). "+
			"It is not in /workspace; a member can check the repository's URL and access and link it again."; got != want {
		t.Errorf("note:\n got %s\nwant %s", got, want)
	}
	if got, want := cloneFailedNote("billing", "ref not found", taskCloneRemedy),
		"billing could not be checked out (ref not found). It is not in /workspace; "+
			"carry on without it, or ask a person to check the repository's URL and access."; got != want {
		t.Errorf("a task's note:\n got %s\nwant %s", got, want)
	}
	for _, err := range []string{
		auth,
		"git clone: exit status 128: remote: Repository not found.\nfatal: repository 'https://github.com/acme/x/' not found",
		"git clone: exit status 128: remote: Invalid username or token.\nfatal: Authentication failed for 'https://github.com/a/b/'",
		"git clone: exit status 128: fatal: unable to access 'https://github.com/a/b/': The requested URL returned error: 403",
		"git clone: exit status 128: fatal: unable to access 'https://github.com/a/b/': The requested URL returned error: 401",
		"git clone: exit status 128: fatal: could not read Password for 'https://x@github.com': No such device or address",
	} {
		if got := cloneReason(err); got != cloneSignInReason {
			t.Errorf("an auth failure %q: %s", err, got)
		}
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
