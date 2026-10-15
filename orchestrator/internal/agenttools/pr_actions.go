package agenttools

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The GitHub actions dude takes on a task's own pull requests for its
// agents (delivery.PRAction): checked in the call's transaction, done with
// none open, recorded after.

// prActors may call them; delivery decides which action each may take.
var prActors = []string{delivery.RoleConductor, "implementer"}

// Calls a Run may make to each in its lifetime, counting those GitHub refused.
const (
	reviewRequestsPerRun = 20
	threadsPerRun        = 50
	readiesPerRun        = 5
	rerunsPerRun         = 10
)

type requestReviewIn struct {
	PR     string   `json:"pr" jsonschema:"the pull request: its number, or repo#number, one of your task's"`
	Logins []string `json:"logins" jsonschema:"GitHub logins to ask for a review; collaborators on the repository only"`
}

type resolveThreadIn struct {
	PR     string `json:"pr" jsonschema:"the pull request: its number, or repo#number, one of your task's"`
	Thread string `json:"thread" jsonschema:"the review thread: its GitHub id (PRRT_…), or the id of a line comment in it (line-comment-…)"`
}

type prOnlyIn struct {
	PR string `json:"pr" jsonschema:"the pull request: its number, or repo#number, one of your task's"`
}

func checkPRAction(ctx context.Context, tx pgx.Tx, c Caller, in delivery.PRAction) (delivery.PRActionCheck, error) {
	gh, err := githubOf(c)
	if err != nil {
		return delivery.PRActionCheck{}, err
	}
	return conducted(delivery.CheckPRActionTx(ctx, tx, c.run(), gh, in))
}

func requestReview(ctx context.Context, tx pgx.Tx, c Caller, in requestReviewIn) (delivery.PRActionCheck, error) {
	return checkPRAction(ctx, tx, c, delivery.PRAction{Action: delivery.ActionRequestReview, PR: in.PR, Logins: in.Logins})
}

func resolveThread(ctx context.Context, tx pgx.Tx, c Caller, in resolveThreadIn) (delivery.PRActionCheck, error) {
	return checkPRAction(ctx, tx, c, delivery.PRAction{Action: delivery.ActionResolveThread, PR: in.PR,
		Thread: strings.TrimSpace(in.Thread)})
}

func markReady(ctx context.Context, tx pgx.Tx, c Caller, in prOnlyIn) (delivery.PRActionCheck, error) {
	return checkPRAction(ctx, tx, c, delivery.PRAction{Action: delivery.ActionMarkReady, PR: in.PR})
}

func rerunFailedChecks(ctx context.Context, tx pgx.Tx, c Caller, in prOnlyIn) (delivery.PRActionCheck, error) {
	return checkPRAction(ctx, tx, c, delivery.PRAction{Action: delivery.ActionRerunChecks, PR: in.PR})
}

func doPRAction(ctx context.Context, c Caller, p delivery.PRActionCheck) (delivery.PRActionDone, error) {
	gh, err := githubOf(c)
	if err != nil {
		return delivery.PRActionDone{}, err
	}
	return conducted(delivery.DoPRAction(ctx, gh, p))
}

func recordPRAction(ctx context.Context, tx pgx.Tx, _ Caller, p delivery.PRActionCheck, done delivery.PRActionDone) (map[string]any, error) {
	return delivery.RecordPRActionTx(ctx, tx, p, done)
}

var prActionTools = []tool{
	around(define("request_review", "Ask people for a review of one of your task's pull requests, on GitHub, as dude. "+
		"Only the repository's collaborators: anyone else is refused and named. Refused for another task's pull request.",
		conductors, requestReview).before(forgeFor).limit(reviewRequestsPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("resolve_thread", "Resolve a review thread on one of your task's pull requests, once what it asked for "+
		"is done (say so in it first, with reply_on_pull_request in_reply_to the thread's comment). A fixer answering pull "+
		"request feedback resolves only the threads in its feedback. Refused for another pull request's thread.",
		prActors, resolveThread).before(forgeFor).limit(threadsPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("mark_ready", "Mark one of your task's draft pull requests ready for review, on GitHub. Only when "+
		"the person agreed it is ready.", conductors, markReady).before(forgeFor).limit(readiesPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("rerun_failed_checks", "Run the failed checks on one of your task's pull requests again, on GitHub: "+
		"once per head, for a failure that is not the code's (a flaky runner, an outage). A real failure is a fix's.",
		conductors, rerunFailedChecks).before(forgeFor).limit(rerunsPerRun).countingAttempts(), doPRAction, recordPRAction),
}
