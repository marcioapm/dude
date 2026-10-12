package phases

import (
	"fmt"
	"strings"
)

// A repository a resume added to a session's Run whose clone failed: what
// its agent and members are told (settleClone).

// cloneAuthFailure is git's word for a clone it could not authenticate.
// GitHub answers a private repository it cannot see "not found".
var cloneAuthFailure = []string{"could not read Username", "could not read Password", "Authentication failed",
	"terminal prompts disabled", "Repository not found", "returned error: 403", "returned error: 401"}

// cloneSignInReason is what members are told of a clone git could not
// authenticate: the usual cause is the URL (a host GitHub redirects, where
// git sends no credentials) or the token's access.
const cloneSignInReason = "dude couldn't sign in to GitHub for this repository: check its URL and that dude's GitHub access covers it."

// shortCloneError is lux's clone error as one line: git's last fatal
// message when it has one (lux joins git's stderr after "git clone: exit
// status N: "), else the whole of it, bounded to 200 characters.
func shortCloneError(err string) string {
	short := strings.TrimSpace(err)
	if i := strings.LastIndex(short, "fatal: "); i >= 0 {
		short = short[i+len("fatal: "):]
	}
	short = strings.Join(strings.Fields(short), " ")
	if r := []rune(short); len(r) > 200 {
		short = string(r[:199]) + "…"
	}
	if short == "" {
		return "no reason given"
	}
	return short
}

// cloneReason is why the clone failed, in plain words for the members.
func cloneReason(err string) string {
	for _, s := range cloneAuthFailure {
		if strings.Contains(err, s) {
			return cloneSignInReason
		}
	}
	return "git said: " + shortCloneError(err) + "."
}

// cloneFailedNote is what the agent is told of it: repository is KEY/name
// on a session's Run, the bare name on a task's (task repositories have no
// project key), and remedy what can be done about it there.
func cloneFailedNote(repository, err, remedy string) string {
	return fmt.Sprintf("%s could not be checked out (%s). It is not in /workspace; %s", repository, shortCloneError(err), remedy)
}

const (
	sessionCloneRemedy = "a member can check the repository's URL and access and link it again."
	taskCloneRemedy    = "carry on without it, or ask a person to check the repository's URL and access."
)
