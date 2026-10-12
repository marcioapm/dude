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
		"git clone: exit status 128: git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
	} {
		if got := cloneReason("https://github.com/acme/web", err); got != cloneSignInReason {
			t.Errorf("an auth failure %q: %s", err, got)
		}
	}
	// GitHub is named only for a repository on github.com.
	for url, want := range map[string]string{
		"https://GitHub.com/acme/web.git":       cloneSignInReason,
		"ssh://git@github.com/acme/web.git":     cloneSignInReason,
		"git@github.com:acme/web.git":           cloneSignInReason,
		"https://gitlab.com/acme/web.git":       cloneHostSignInReason,
		"https://gitlab.com/github.com/web.git": cloneHostSignInReason,
		"git@gitlab.com:acme/web.git":           cloneHostSignInReason,
		"git://127.0.0.1/acme/web.git":          cloneHostSignInReason,
		"":                                      cloneHostSignInReason,
	} {
		if got := cloneReason(url, auth); got != want {
			t.Errorf("an auth failure at %q: %s", url, got)
		}
	}
	if got := cloneReason("https://github.com/acme/web", "ref not found"); got != "git said: ref not found." {
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
