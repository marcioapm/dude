package delivery

// The conductor's facts and what agents are told outside a prompt:
// agents/wakes/facts.md, agents/notices/told.md and
// agents/refusals/words.md.
var (
	checkoutTmpl       = mustTemplate("wakes/facts/checkout")
	checkoutOwnTmpl    = mustTemplate("wakes/facts/checkout-own")
	checkoutDirty      = render(mustTemplate("wakes/facts/checkout-dirty"), nil)
	checkoutKeptAsIs   = render(mustTemplate("wakes/facts/checkout-kept-as-is"), nil)
	operationTmpl      = mustTemplate("wakes/facts/operation")
	operationNameTmpl  = mustTemplate("wakes/facts/operation-name")
	reviewRoundTmpl    = mustTemplate("wakes/facts/review-round")
	outcomeTmpl        = mustTemplate("wakes/facts/outcome")
	prFeedbackFactTmpl = mustTemplate("wakes/facts/pr-feedback")
	failingCheck       = render(mustTemplate("wakes/facts/failing-check"), nil)
	tookOverTmpl       = mustTemplate("wakes/facts/took-over")
	testFailedTmpl     = mustTemplate("wakes/facts/test-failed")
	testPassedTmpl     = mustTemplate("wakes/facts/test-passed")
	feedbackOpenTmpl   = mustTemplate("wakes/facts/feedback-open")

	answerTmpl         = mustTemplate("notices/told/answer")
	answerItemTmpl     = mustTemplate("notices/told/answer-item")
	inTheirWordsTmpl   = mustTemplate("notices/told/in-their-words")
	restartLineTmpl    = mustTemplate("notices/told/restarted")
	taskChangedTmpl    = mustTemplate("notices/told/task-changed")
	mentionTmpl        = mustTemplate("notices/told/mention")
	githubReadNoteTmpl = mustTemplate("notices/told/github-read")

	midOperationTmpl   = mustTemplate("refusals/words/mid-operation")
	escalationNoteTmpl = mustTemplate("refusals/words/escalation-note")
	decidedByTmpl      = mustTemplate("refusals/words/decided-by")
	blockedTmpl        = mustTemplate("refusals/words/blocked")
	blockedAgentTmpl   = mustTemplate("refusals/words/blocked-agent")
	blockerTmpl        = mustTemplate("refusals/words/blocker")
	notAPeerTmpl       = mustTemplate("refusals/words/not-a-peer")
	relationLineTmpl   = mustTemplate("refusals/words/relation-line")
	sourceChangedTmpl  = mustTemplate("refusals/words/source-changed")
)
