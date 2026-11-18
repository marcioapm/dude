package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A pull request fixer's answers (#485). A fixer's reply_on_pull_request
// and resolve_thread, on comments in its batch, are checked as the
// conductor's are, against dude's own records, and then held rather than
// posted: its commits pass the checks gate and are published only after its
// turn, and a "fixed" reply or a resolved thread before then names a fix
// the pull request does not have. Each held call is an EvAnswerHeld event
// on the fixer's Run, with all it needs to be posted later; the Run's
// events, in order, are the order it made them.
//
// Once the fixer's fix is published (git.commit_created on its Run) and its
// Run has ended, the delivery's step waiting on it (awaitPRFix) posts them
// (Store.PostFixerAnswers): its replies in the order it made them, then its
// threads resolved, each through the reply and action paths' own steps,
// and only when every pull request they are on is open and its head, read
// from GitHub, contains the fixer's published commit (ancestry, not
// equality). A fixer that never published posts nothing, ever. One whose
// fix a force-push dropped, or whose pull request ended, posts nothing:
// its answers are withheld, and its conductor told. Fixers post in the
// order they published. When the task has a live conductor it is woken
// with what was posted, resolved, failed or withheld (WakeRound,
// answersLine), and may add to or correct the answers.
//
// Exactly once: each answer is settled by one record on the fixer's Run,
// its chat.message (a reply) or pull_request.action (a resolution) naming
// its answerId, or an EvAnswerFailed. Under the task's Chat lock, a reply
// is claimed (EvAnswerClaimed) before GitHub is called, and no transaction
// is held across GitHub: a second poster meanwhile finds the claim fresh
// and leaves it (answerBusy). The posted comment carries the answer's id
// (forge.AnswerMarker), so a poster that finds an earlier claim — one
// that crashed between GitHub's answer and the record, or whose answer was
// lost — looks for that comment first (forge.FindAnswer) and records it
// rather than posting again. A claim older than answerClaimLease is taken
// over: its holder is gone. A resolution needs no claim: resolving a
// resolved thread is a no-op, and its record is written once, under the
// lock. GitHub down or limiting (forge.Transient) leaves the answer to the
// step's retry; GitHub refusing it (a deleted comment, forge.Refused) fails
// it, recorded and reported.

// Ledger events of a fixer's held answers, on its Run.
const (
	// A fixer's reply or resolution, held. Payload: { kind (reply,
	// resolve_thread), repo, number, prId, inReplyTo (a reply) or thread
	// (a resolution), text (a reply) }.
	EvAnswerHeld = "pull_request.answer_held"
	// A poster's claim on a held reply before GitHub is called. Payload:
	// { answerId, released? }: released gives it back after a transient
	// failure.
	EvAnswerClaimed = "pull_request.answer_claimed"
	// A held answer that will not be posted. Payload: { answerId, kind,
	// error, withheld? }: withheld when the fix is not on the pull request.
	EvAnswerFailed = "pull_request.answer_failed"
)

// Kinds of held answers.
const (
	answerReply   = "reply"
	answerResolve = ActionResolveThread
)

// What a fixer's held call answers.
const (
	AnswerHeldReply   = "held: not posted yet. dude posts this reply on the pull request once your fix is published there; do not post it again, or elsewhere"
	AnswerHeldResolve = "held: not resolved yet. dude resolves this thread once your fix is published on the pull request; do not ask again"
)

// RoleFixer is who a fixer's answer dude posted is by (its chat.message).
const RoleFixer = "fixer"

// answerClaimLease is how long a claim on a held reply stands: past it,
// its poster is taken for gone. Above forge's request timeout, with room.
const answerClaimLease = 2 * time.Minute

// heldAnswer is an EvAnswerHeld's payload.
type heldAnswer struct {
	Kind      string `json:"kind"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	PRID      string `json:"prId,omitempty"`
	InReplyTo string `json:"inReplyTo,omitempty"`
	Thread    string `json:"thread,omitempty"`
	Text      string `json:"text,omitempty"`
}

// holdAnswerTx records a fixer's held call on its Run; its event id.
func holdAnswerTx(ctx context.Context, tx pgx.Tx, ref RunRef, a heldAnswer) (string, error) {
	raw, _ := json.Marshal(a)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return ledger.Append(ctx, tx, ref.Event(EvAnswerHeld, ledger.ActorAgent, payload))
}
