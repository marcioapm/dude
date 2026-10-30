package agenttools

import "github.com/marciomartins/dude/orchestrator/internal/delivery"

// What the tools answer an agent, past a refusal: delivery/agents/results/.
var (
	askResult               = delivery.MustText("results/ask-person")
	sessionAskResult        = delivery.MustText("results/session-ask")
	requestRepositoryResult = delivery.MustText("results/request-repository")
	waitResult              = delivery.MustText("results/wait-for-reply")
	waitForResult           = delivery.MustText("results/wait-for")
	findingsSettled         = delivery.MustText("results/findings-settled")
	machineMoved            = delivery.MustText("results/machine-moved")
	syncBaseResult          = delivery.MustText("results/sync-base")
	relationsPresent        = delivery.MustText("results/update-task-present")

	replyCameNext        = delivery.MustText("results/wait-for-reply-came").Render(nil)
	steerNext            = delivery.MustText("results/steer").Render(nil)
	restartNext          = delivery.MustText("results/restart-run").Render(nil)
	stopNext             = delivery.MustText("results/stop-run").Render(nil)
	recoverNext          = delivery.MustText("results/recover-run").Render(nil)
	publishNext          = delivery.MustText("results/publish").Render(nil)
	relationsWrittenNext = delivery.MustText("results/update-task-relations").Render(nil)
	taskUpdatedNext      = delivery.MustText("results/update-task").Render(nil)
	proposedNext         = delivery.MustText("results/propose").Render(nil)
	relationProposedNext = delivery.MustText("results/relate").Render(nil)
	feedbackResolvedNext = delivery.MustText("results/feedback-resolve").Render(nil)
)
