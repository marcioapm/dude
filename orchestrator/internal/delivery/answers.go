package delivery

import (
	"context"
	"encoding/json"
	"errors"
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
// on the fixer's Run, with all it needs to be posted later.
//
// Posting (answers_post.go). Once the fixer's fix is published
// (git.commit_created on its Run) and its Run has ended, the delivery's
// awaitPRFix or movedOnPR posts them (Store.PostFixerAnswers): its replies
// in the order made, then its resolutions, only when every pull request
// they are on is open and its branch head, read from GitHub, contains the
// fixer's published commit. A fixer that never published posts nothing.
// They are withheld only on positive evidence the fix is not there; answers
// dude cannot check stay held (EvAnswerUnverified says why) until
// answerVerifyBound past the fixer's end. Posting never fails its step.
//
// Settlement. Each answer is settled once, by one record on the fixer's
// Run whose causation_id is the answer's EvAnswerHeld id: its reply's
// chat.message, its resolution's pull_request.action, or an EvAnswerFailed
// (failed, withheld or uncertain). Under the task's Chat lock, a poster
// claims an answer (EvAnswerClaimed) before calling GitHub and holds no
// transaction across it; another poster finds the claim fresh and leaves
// it. A claim older than answerClaimLease is taken over; the posted comment
// carries forge.AnswerMarker, so the poster taking it over looks for that
// comment first (forge.FindAnswer) rather than posting twice.
//
// The end (answers_end.go). A delivery that ends — merged, closed,
// stopped — withholds the answers nobody claimed. One a poster ever claimed
// may be on GitHub: it is never withheld there, but given a row in
// answer_reconciliations, which also stops any new claim on it. Its poster
// records what happened; if the poster is gone, the pr-reconciler's pass
// reads GitHub and settles it posted, withheld or uncertain. Either way the
// Chat, and a live conductor, are told once.

// Ledger events of a fixer's held answers, on its Run.
const (
	// A fixer's reply or resolution, held. Payload: heldAnswer.
	EvAnswerHeld = "pull_request.answer_held"
	// A poster's claim on a held answer before GitHub is called.
	// Payload: { answerId, released? }: released gives it back after a
	// transient failure.
	EvAnswerClaimed = "pull_request.answer_claimed"
	// A held answer that will not be posted. Payload: { answerId, kind,
	// error, withheld? uncertain? }.
	EvAnswerFailed = "pull_request.answer_failed"
	// Why a fixer's answers could not be verified on the pull request this
	// pass: they stay held. Payload: { why }. Once per reason in a row.
	EvAnswerUnverified = "pull_request.answer_unverified"
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

// answerClaimLease is how long a claim on a held answer stands: past it,
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

// heldOf is one held answer and its id.
type heldOf struct {
	ID string
	heldAnswer
}

// label is what a held answer answers, as a wake names it.
func (h heldOf) label() string {
	if h.Kind == answerResolve {
		return "resolving " + h.Thread
	}
	return h.InReplyTo
}

// holdAnswerTx records a fixer's held call on its Run; its event id.
func holdAnswerTx(ctx context.Context, tx pgx.Tx, ref RunRef, a heldAnswer) (string, error) {
	raw, _ := json.Marshal(a)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	return ledger.Append(ctx, tx, ref.Event(EvAnswerHeld, ledger.ActorAgent, payload))
}

// now is the store's clock (Store.Now, else time.Now): every time the
// held answers compare or write — a claim's age, the verify bound, the
// end's reconciliation and its retries — is read on it.
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// isHeld (SQL) says events e is an EvAnswerHeld, the type a literal so a
// generic plan proves events_run_answers_idx (migration 269).
func isHeld(e string) string { return e + `.event_type = '` + EvAnswerHeld + `'` }

// answerRecord (SQL) says events e names held answer id (SQL) as its
// cause, among types: by run and causation_id, the leading columns of
// events_run_answers_idx (migration 269), the run the held answer's.
func answerRecord(e, run, id string, types ...string) string {
	in := ""
	for i, t := range types {
		if i > 0 {
			in += ", "
		}
		in += "'" + t + "'"
	}
	return e + `.run_id = ` + run + ` AND ` + e + `.causation_id = ` + id + ` AND ` + e + `.event_type IN (` + in + `)`
}

// answerSettled (SQL) says the EvAnswerHeld event h is settled.
func answerSettled(h string) string {
	return `EXISTS (SELECT 1 FROM events s WHERE ` + answerRecord("s", h+".run_id", h+".id", EvChatMessage, EvPullRequestAction, EvAnswerFailed) + `)`
}

// reconciling (SQL) says the EvAnswerHeld event h was claimed when its
// delivery ended (answer_reconciliations, migration 268, by its key): the
// reconciliation settles it, never a new claim.
func reconciling(h string) string {
	return `EXISTS (SELECT 1 FROM answer_reconciliations x WHERE x.answer_id = ` + h + `.id)`
}

// reconcilingTx says held answer id is reconciling (reconciling).
func reconcilingTx(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var x bool
	err := tx.QueryRow(ctx, `SELECT `+reconciling("h")+` FROM (SELECT $1::text AS id) h`, id).Scan(&x)
	return x, err
}

// lastClaimSQL is the latest claim on a held answer ($1 the Run, $2 the
// answer): when, and whether it was given back.
var lastClaimSQL = `SELECT c.occurred_at, COALESCE((c.payload->>'released')::bool, false) FROM events c
	WHERE ` + answerRecord("c", "$1", "$2", EvAnswerClaimed) + ` ORDER BY c.cursor DESC LIMIT 1`

// lastClaimTx is the latest claim on held answer id of Run runID: when,
// whether it was given back, and whether there is one (claimed).
func lastClaimTx(ctx context.Context, tx pgx.Tx, runID, id string) (at time.Time, released, claimed bool, err error) {
	err = tx.QueryRow(ctx, lastClaimSQL, runID, id).Scan(&at, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, false, false, nil
	}
	return at, released, err == nil, err
}

// answerEvent is a held answer's claim or settling record on ref's Run,
// caused by the answer.
func answerEvent(ref RunRef, typ, answerID string, payload map[string]any) ledger.Event {
	e := ref.Event(typ, ledger.ActorSystem, payload)
	e.CausationID = answerID
	return e
}
