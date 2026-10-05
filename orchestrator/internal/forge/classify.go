package forge

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Signal is what the workflow is told about a change on a pull request.
// Nil, most of the time.
type Signal struct {
	// "terminal" (merged or closed), "actionable" (something to fix),
	// "readiness" (its approval, checks, mergeability or threads changed:
	// it may now be ready to merge, or no longer), "conflict" (it no longer
	// merges: a person resolves it), or "ci_stuck" (checks pending on its
	// head for longer than the organization allows).
	Kind     string               `json:"kind"`
	State    string               `json:"state,omitempty"`
	Feedback []ActionableFeedback `json:"feedback,omitempty"`
	// It turned conflicting as well: set on an actionable signal too, so
	// the conflict is not lost to the fix it arrived with.
	Conflict bool `json:"conflict,omitempty"`
	// Which pull request, for a person reading the escalation.
	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number,omitempty"`
}

// ActionableFeedback is one thing a fixer is sent to address.
type ActionableFeedback struct {
	// "review" or "checks".
	Source string `json:"source"`
	// Which repository's pull request it is on, when a task has several.
	Repo   string `json:"repo,omitempty"`
	Author string `json:"author,omitempty"`
	Body   string `json:"body"`
	Path   string `json:"path,omitempty"`
	// What kind of feedback it was (Feedback.Kind): a submitted review
	// starts a new round of fixes, a comment does not.
	Kind string `json:"kind,omitempty"`
	// For failing checks: which, where their logs are, and what they said.
	Checks []FailingCheck `json:"checks,omitempty"`
}

// IsReview says it came through GitHub's review — its text, or a line
// comment, which GitHub files under a review: a new round, as a person
// sees it. A conversation comment, or CI, is not.
func (f ActionableFeedback) IsReview() bool {
	return f.Kind == KindReview || f.Kind == KindChangesRequested || f.Kind == KindLineComment
}

// FailingCheck is one check that failed, as a fixer is told about it.
type FailingCheck struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	// The end of what it reported (its output and annotations), when the
	// checks API has it.
	Log string `json:"log,omitempty"`
	// The check run, to read its log from.
	RunID int64 `json:"runId,omitempty"`
}

// Words that carry approval or thanks and nothing else.
//
// A comment made only of these — in any order, with any punctuation — is a
// courtesy: "LGTM so far, thanks!" asks for no more than "LGTM". Anything
// else might be a request, so it is passed on.
//
// A vocabulary rather than an attempt at understanding, on purpose: a comment
// that slips through costs one fix Run, bounded by the PR loop's budget,
// while a real request filtered out is silently ignored — the worse failure.
// So the list stays short and the default is "actionable".
var courtesyWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`lgtm looks look good great nice fine ok okay to me so far thanks thank
		you ty cheers approved approve ship it work job well done all very really much this is the for now
		👍 🚀 ✅ 🎉 ❤️ ❤`) {
		courtesyWords[w] = true
	}
}

// Question marks and imperatives are how requests are spelled.
var looksLikeARequest = regexp.MustCompile(`(?i)\?|\b(please|could|can you|should|would|change|rename|fix|add|remove|why)\b`)

func isCourtesy(body string) bool {
	if looksLikeARequest.MatchString(body) {
		return false
	}
	words := courtesyTokens(strings.ToLower(body))
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !courtesyWords[w] {
			return false
		}
	}
	return true
}

// courtesyTokens splits a comment into words and emoji, dropping punctuation
// and variation selectors.
func courtesyTokens(s string) []string {
	var out []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '\'':
			word = append(word, r)
		case r == '️' || r == '‍':
			// Emoji modifiers: part of the emoji before them.
		case unicode.Is(unicode.So, r):
			flush()
			out = append(out, string(r))
		default:
			flush()
		}
	}
	flush()
	return out
}

// Bots post status, coverage and deploy-preview comments; none are requests.
func isBot(author string) bool {
	return strings.HasSuffix(author, "[bot]") || strings.HasSuffix(author, "-bot") || author == "github-actions"
}

// IsActionableComment decides whether one piece of feedback asks for a change.
func IsActionableComment(f Feedback, factoryLogins []string) bool {
	if isBot(f.Author) || strings.TrimSpace(f.Body) == "" || Own(f) {
		return false
	}
	// The factory's own comments are not feedback on the factory's work.
	for _, login := range factoryLogins {
		if f.Author == login {
			return false
		}
	}
	// A review that requested changes is a request however briefly worded.
	if f.Kind == KindChangesRequested {
		return true
	}
	return !isCourtesy(f.Body)
}

// Classify decides what a change on a pull request means, without a model.
//
// Most of what happens on a PR — an approval, a green check, a bot comment —
// only updates state. Two things are worth waking a fixer for: a person
// asking for a change, and a check that turned red; and one is worth
// stopping for a person: the branch turning conflicting. Returns nil
// otherwise, which is the common case and the reason this exists.
//
// feedback is what may wake a fixer: its authors already passed the
// organization's rule for who may (MayWake). A comment addressed to dude
// is a message to the task's conductor, not a fixer's (AddressedToDude).
func Classify(prior, current Status, feedback []Feedback, factoryLogins []string) *Signal {
	if current.State == StateMerged || current.State == StateClosed {
		if prior.State == current.State {
			return nil
		}
		return &Signal{Kind: "terminal", State: current.State}
	}

	var actionable []ActionableFeedback
	for _, f := range feedback {
		if IsActionableComment(f, factoryLogins) && !AddressedToDude(f, factoryLogins) {
			actionable = append(actionable, ActionableFeedback{Source: "review", Author: f.Author, Body: f.Body, Path: f.Path, Kind: f.Kind})
		}
	}
	// A check turning red is worth a fixer; one already red is not news, and
	// waking again would spend the loop's budget on a failure the last fix
	// already saw.
	if current.Checks == ChecksFailing && prior.Checks != ChecksFailing {
		f := ActionableFeedback{Source: "checks",
			Body: "Continuous integration is failing on this branch. Find out why and fix it."}
		for _, c := range current.CheckList {
			if c.Failed() {
				f.Checks = append(f.Checks, FailingCheck{Name: c.Name, URL: c.URL, RunID: c.RunID})
			}
		}
		actionable = append(actionable, f)
	}
	conflict := current.Mergeable == MergeConflicting && prior.Mergeable != MergeConflicting
	if len(actionable) > 0 {
		return &Signal{Kind: "actionable", Feedback: actionable, Conflict: conflict}
	}
	if conflict {
		return &Signal{Kind: "conflict"}
	}
	// Ready or no longer: the task may be ready to merge, or no longer.
	// Worth telling the workflow, not a fixer.
	if Ready(current) != Ready(prior) {
		return &Signal{Kind: "readiness"}
	}
	return nil
}

// Ready says a pull request has what merging it needs: approved; its
// checks passing — or none configured, which GitHub reports as unknown;
// no conflict with its base; and no review thread left unresolved. The
// factory merges only when a person says so; this is what a person is told.
func Ready(s Status) bool { return len(Blockers(s)) == 0 }

// A 403 without rate-limit markers is also what SSO enforcement and pending
// organization token approval answer, so this advises rather than diagnoses.
// GitHub offers fine-grained tokens no permission that reads check runs.
const checkRunsForbiddenBlocker = "GitHub refused the check-runs read; a fine-grained token cannot read check runs, so use a classic token with the repo scope (or a GitHub App once supported), and check SSO authorization, organization token approval and the token's repository access"

// Blockers says, in a person's words, what keeps a pull request from
// being merged: nothing, when it is ready.
func Blockers(s Status) []string {
	var out []string
	if s.State == StateDraft {
		// GitHub merges no draft: it is marked ready for review first.
		out = append(out, "it is a draft")
	}
	unreadable := CheckDiagnostic(s.CheckList) == CheckRunsForbidden
	switch {
	case s.Checks == ChecksFailing:
		out = append(out, "checks are failing")
		if unreadable {
			out = append(out, checkRunsForbiddenBlocker)
		}
	case s.Checks == ChecksPending && unreadable:
		out = append(out, checkRunsForbiddenBlocker)
	case s.Checks == ChecksPending:
		out = append(out, "checks are pending")
	}
	switch s.Review {
	case ReviewChangesRequested:
		out = append(out, "changes were requested")
	case ReviewApproved:
	default:
		out = append(out, "nobody has approved it")
	}
	if s.Mergeable == MergeConflicting {
		out = append(out, "it conflicts with its base")
	}
	if s.UnresolvedThreads > 0 {
		out = append(out, fmt.Sprintf("%d review thread(s) unresolved", s.UnresolvedThreads))
	}
	return out
}
