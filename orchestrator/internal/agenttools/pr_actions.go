package agenttools

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The GitHub actions dude takes on a task's own pull requests for its
// agents (delivery.PRAction): checked in the call's transaction, done with
// none open, recorded after. Who may call each is its capability's
// (capabilities.PRAct, Conduct); delivery decides which action each may take.

// Calls a Run may make to each in its lifetime, counting those GitHub refused.
const (
	reviewRequestsPerRun = 20
	threadsPerRun        = 50
	readiesPerRun        = 5
	rerunsPerRun         = 10
	notesPerRun          = 20
)

type requestReviewIn struct {
	PR     string   `json:"pr"`
	Logins []string `json:"logins"`
}

type resolveThreadIn struct {
	PR     string `json:"pr"`
	Thread string `json:"thread"`
}

type prOnlyIn struct {
	PR string `json:"pr"`
}

type pullRequestNoteIn struct {
	PR   string `json:"pr"`
	Text string `json:"text"`
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

func pullRequestNote(ctx context.Context, tx pgx.Tx, c Caller, in pullRequestNoteIn) (delivery.PRActionCheck, error) {
	return checkPRAction(ctx, tx, c, delivery.PRAction{Action: delivery.ActionNote, PR: in.PR, Text: in.Text})
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
	around(define("request_review", requestReview).before(forgeFor).limit(reviewRequestsPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("resolve_thread", resolveThread).before(forgeFor).limit(threadsPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("mark_ready", markReady).before(forgeFor).limit(readiesPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("rerun_failed_checks", rerunFailedChecks).before(forgeFor).limit(rerunsPerRun).countingAttempts(), doPRAction, recordPRAction),
	around(define("pull_request_note", pullRequestNote).before(forgeFor).limit(notesPerRun).countingAttempts(), doPRAction, recordPRAction),
}
