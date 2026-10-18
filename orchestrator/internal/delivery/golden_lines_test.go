package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// goldenTexts renders named texts as one golden, each under its name.
func goldenTexts(t *testing.T, name string, lines [][2]string) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("## " + l[0] + "\n\n" + l[1] + "\n\n")
	}
	golden(t, name, b.String())
}

// The rest of what the delivery tells an agent: the conductor's checkout
// and decision lines, answers, restarts, mentions, the publish refusals,
// the GitHub notes and the relation words.
func TestGoldenDeliveryLines(t *testing.T) {
	sync := func(s lux.SyncResult) string { s.Repo, s.Ref = "api", "dude/wi_g/attempt-1"; return CheckoutLine(s) }
	items := []QuestionItem{{Header: "Scope", Question: "Which?\nPick one.", Choices: []Choice{{Label: "A"}, {Label: "B"}}, Multiple: true},
		{Header: "When", Question: "Now?"}}
	answered := Answered{Text: answersBlock(items, []ItemAnswer{{Choices: []int{0, 1}}, {Text: "next week\nor so"}}), Note: "Thanks."}
	st := &State{Iteration: 2, Policy: Policy{MaxReviewIterations: 5}}
	findings := []FindingState{{Severity: "high", Status: "open"}, {Severity: "high", Status: "open"}, {Severity: "low", Status: "open"},
		{Severity: "medium", Status: "resolved"}, {Human: true}}
	mention := Mention{Repo: "api", Number: 12, Feedback: forge.Feedback{ID: "line-comment-3", Author: "bo", URL: "https://x/3", Path: "api/x.go"}}
	blocked := BlockedError{TaskKey: "GLD-7", Blockers: []Blocker{{Key: "GLD-8", Status: "review"}, {Key: "GLD-9", Status: "odd_state"}}}
	one := BlockedError{TaskKey: "GLD-7", Blockers: []Blocker{{Key: "GLD-8", Status: "done"}}}
	rels := &TaskRelationsOut{BlockedBy: []BlockerOut{{Key: "GLD-8", Status: "review"}, {Key: "GLD-1", Status: "done", Merged: true}},
		Blocks: []BlocksOut{{Key: "GLD-10"}}, Related: []string{"GLD-2", "GLD-3"}}
	var lines [][2]string
	add := func(name, text string) { lines = append(lines, [2]string{name, text}) }
	for _, c := range []struct {
		name string
		s    lux.SyncResult
	}{
		{"checkout-kept-off-branch", lux.SyncResult{Status: "kept", Behind: 2}},
		{"checkout-kept-dirty", lux.SyncResult{Status: "kept", Behind: 2, Dirty: true, Ahead: 1}},
		{"checkout-kept-diverged", lux.SyncResult{Status: "kept", Behind: 2, Diverged: true}},
		{"checkout-ahead", lux.SyncResult{Status: "ahead", Ahead: 3}},
		{"checkout-failed", lux.SyncResult{Status: "failed", Error: "fetch\nfailed"}},
		{"checkout-other", lux.SyncResult{Status: "weird", Behind: 1, Ahead: 2}},
		{"checkout-rebase", lux.SyncResult{Status: "kept", Operation: lux.OperationRebase, Behind: 2}},
		{"checkout-sequencer", lux.SyncResult{Operation: lux.OperationSequencer}},
		{"checkout-am", lux.SyncResult{Operation: lux.OperationAm, Behind: 1}},
		{"checkout-unknown-op", lux.SyncResult{Operation: "bisect"}},
	} {
		add(c.name, sync(c.s))
	}
	add("mid-operation", MidOperationRefusal("api", lux.OperationMerge))
	add("mid-operation-unnamed", MidOperationRefusal("api", ""))
	add("answer-one", AnswerDirective("Which?", items[:1], Answered{Text: "A"}, "", false))
	add("answer-one-session", AnswerDirective("Which?", items[:1], Answered{Text: "A", Note: "n"}, "Ana", true))
	add("answer-many", AnswerDirective("", items, answered, "", false))
	add("answer-many-session", AnswerDirective("", items, answered, "Ana", true))
	add("in-their-words", inTheirWords("say \"hi\""))
	add("restart-person", restartLine(Restart{}, ""))
	add("restart-conductor", restartLine(Restart{Conductor: "run_c"}, "go slower"))
	add("restart-dude", restartLine(Restart{ByDude: true}, "its secret changed"))
	add("task-changed", changedTask("Export", "  Do it.  ", []byte(`["one","two"]`)))
	add("task-changed-no-criteria", changedTask("Export", "Do it.", []byte(`[]`)))
	add("escalation-note", escalationNote("one\nmore round", "yes", ""))
	add("escalation-note-decided", escalationNote("p", "a", "retry it"))
	add("decided-by-conductor", decidedBy(&HumanDecision{Conductor: "run_c"}))
	add("decided-by-answer", decidedBy(&HumanDecision{QuestionID: "qst_1"}))
	add("decided-by-banner", decidedBy(&HumanDecision{}))
	add("mention", mention.told("Why\nthis?"))
	add("findings-line", findingsLine(st, findings))
	add("findings-line-none", findingsLine(st, nil))
	add("outcome", Outcome{Succeeded: true, Heads: map[string]string{"web": "2222222bbbb", "api": "1111111aaaa"}, ChangedPaths: []string{"a", "b"}}.line("run_i", "implement"))
	add("outcome-failed", Outcome{}.line("run_f", "fix"))
	add("pr-feedback-line", feedbackLine([]forge.ActionableFeedback{{Source: "review"}, {Source: "review"}, {Source: "checks", Checks: []forge.FailingCheck{{Name: "x"}}}, {Source: "bot"}}))
	add("blocked", blocked.Error())
	add("blocked-one", one.Error())
	add("blocked-agent", blocked.AgentRefusal())
	add("not-a-peer", NotAPeer("GLD-9"))
	add("relation-line", RelationLine(rels))
	add("external-text-note", ExternalTextNote)
	add("github-read-note", GitHubReadNote("abc"))
	add("talk-it-through", TalkItThrough)
	add("refused-writer", refusedWriter)
	add("refused-policy", refusedPolicy)
	add("refused-candidate", refusedCandidate)
	add("refused-behind", RefusedBehind)
	add("refused-nothing", RefusedNothing)
	add("refused-too-big", RefusedTooBig)
	add("refused-binary", RefusedBinary)
	add("untested-head", UntestedHead)
	add("source-changed", sourceChanged("its head moved"))
	for _, p := range []string{PointStart, PointImplemented, PointReviewed, PointFixed, PointBeforePR, PointPRFeedback, "elsewhere"} {
		add("point-"+p, pointWords(p))
	}
	goldenTexts(t, "delivery-lines", lines)
}

// The conductor's facts on work taken over, a test's verdict and people's
// open feedback.
func TestGoldenDecisionFacts(t *testing.T) {
	review := &Source{HeadBranch: "ana/csv", BaseBranch: "main"}
	carry := &Source{HeadBranch: "ana/csv", BaseBranch: "main", Mode: ModeContinue}
	var lines [][2]string
	add := func(name, text string) { lines = append(lines, [2]string{name, text}) }
	add("took-over-review", tookOverLine(review, "0123456789abcdef", 4))
	add("took-over-continue", tookOverLine(carry, "0123456789abcdef", 4))
	add("test-failed", testFailedLine("run_t", []int{1, 3}, 2, 1))
	add("test-passed", testPassedLine("run_t"))
	add("nothing-to-add", nothingToAdd)
	for _, when := range []string{feedbackOpen, feedbackAfterFix, feedbackAfterIntegration, feedbackBeforePR, feedbackReadyToRead} {
		add("feedback-"+when, feedbackOpenLine(when, 3))
	}
	goldenTexts(t, "decision-facts", lines)
}

// Why a publish is refused as it lands, what it did not move, and the
// first words of a conductor an unblocked task's Talk it through starts.
func TestGoldenPublishAndUnblockWords(t *testing.T) {
	var lines [][2]string
	add := func(name, text string) { lines = append(lines, [2]string{name, text}) }
	for _, c := range []struct{ c, what string }{{"not-conductor", ""}, {"read-only", "lux cannot sync it"},
		{"no-delivery", ""}, {"ended", "done"}, {"started-over", ""}} {
		add("publish-refused-"+c.c, publishRefusedWhy(c.c, c.what))
	}
	add("not-moved", strings.Join(notMovedParts(map[string]string{"web": "the branch\nmoved"}, map[string]string{"api": "1111111aaaa"}), "; "))
	add("talk-when-unblocked", talkWhenUnblocked("Ana", "GLD-8"))
	goldenTexts(t, "publish-unblock-words", lines)
}
