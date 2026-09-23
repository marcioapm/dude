package forge

import (
	"regexp"
	"strings"
	"unicode"
)

// PriorState is what dude last recorded about a pull request.
type PriorState struct {
	State  string
	Checks string
}

// Signal is what the workflow is told about a change on a pull request.
// Nil, most of the time.
type Signal struct {
	// "terminal" (merged or closed) or "actionable" (something to fix).
	Kind     string               `json:"kind"`
	State    string               `json:"state,omitempty"`
	Feedback []ActionableFeedback `json:"feedback,omitempty"`
}

// ActionableFeedback is one thing a fixer is sent to address.
type ActionableFeedback struct {
	// "review" or "checks".
	Source string `json:"source"`
	Author string `json:"author,omitempty"`
	Body   string `json:"body"`
	Path   string `json:"path,omitempty"`
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
	if isBot(f.Author) || strings.TrimSpace(f.Body) == "" {
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
// asking for a change, and a check that turned red. Returns nil otherwise,
// which is the common case and the reason this exists.
func Classify(prior PriorState, current Status, feedback []Feedback, factoryLogins []string) *Signal {
	if current.State == StateMerged || current.State == StateClosed {
		if prior.State == current.State {
			return nil
		}
		return &Signal{Kind: "terminal", State: current.State}
	}

	var actionable []ActionableFeedback
	for _, f := range feedback {
		if IsActionableComment(f, factoryLogins) {
			actionable = append(actionable, ActionableFeedback{Source: "review", Author: f.Author, Body: f.Body, Path: f.Path})
		}
	}
	// A check turning red is worth a fixer; one already red is not news, and
	// waking again would spend the loop's budget on a failure the last fix
	// already saw.
	if current.Checks == ChecksFailing && prior.Checks != ChecksFailing {
		actionable = append(actionable, ActionableFeedback{
			Source: "checks",
			Body:   "Continuous integration is failing on this branch. Find out why and fix it.",
		})
	}
	if len(actionable) == 0 {
		return nil
	}
	return &Signal{Kind: "actionable", Feedback: actionable}
}
