package agenttools

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// ---- wait_for -----------------------------------------------------------------

type waitForIn struct {
	Conditions []delivery.WaitCondition `json:"conditions,omitempty"`
	Wake       string                   `json:"wake,omitempty"`
	Deadline   string                   `json:"deadline"`
	Note       string                   `json:"note,omitempty"`
}

type waitForOut struct {
	WaitID     string     `json:"waitId,omitempty"`
	DeadlineAt *time.Time `json:"deadlineAt,omitempty"`
	// It holds already: what holds, and no wait opened.
	Holds  bool   `json:"holds,omitempty"`
	Digest string `json:"digest,omitempty"`
	Next   string `json:"next"`
}

// waitFor is wait_for (capabilities.Wait): the conductor says what it waits
// on (delivery.OpenConductorWaitTx), keyed by the tool call so a retried
// call is the same wait.
func waitFor(ctx context.Context, tx pgx.Tx, c Caller, in waitForIn) (waitForOut, error) {
	opened, err := conducted(delivery.OpenConductorWaitTx(ctx, tx, c.run(), delivery.WaitAsk{
		Conditions: in.Conditions, Wake: in.Wake, Deadline: in.Deadline, Note: in.Note, CallKey: "tool:" + c.CallID}))
	if err != nil {
		return waitForOut{}, err
	}
	if opened.Holds {
		return waitForOut{Holds: true, Digest: opened.Digest, Next: waitForNext(true, time.Time{}, opened.DecisionPoint)}, nil
	}
	at := opened.DeadlineAt.UTC()
	return waitForOut{WaitID: opened.WaitID, DeadlineAt: &at, Next: waitForNext(false, at, opened.DecisionPoint)}, nil
}

// waitForNext is what wait_for tells the conductor to do next: carry on if
// it holds already, else end its turn; decision, the decision the delivery
// waits on from it, which waiting does not take.
func waitForNext(holds bool, deadline time.Time, decision string) string {
	at := ""
	if !deadline.IsZero() {
		at = deadline.Format(time.RFC3339)
	}
	return waitForResult.Render(struct {
		Holds              bool
		Deadline, Decision string
	}{holds, at, decision})
}
