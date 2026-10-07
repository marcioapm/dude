package servers

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// A preview's resume or replacement makes lux HTTP calls (Resume; Servers,
// then Cancel) that may each take up to the client's timeout. None is made
// inside a database transaction: the operation is reserved on the row
// (runs.op_token, op_kind, op_deadline), acted on with no transaction open,
// and recorded only while the reservation and the state it was taken on
// still hold.
//
//	reserve  short transaction: the row held, the caller's condition checked
//	         (the wake is still its claim, or the preview is still parked on
//	         the Run), the preview live, no other reservation in force; the
//	         token set until now() + OperationFor, by the database's clock.
//	act      lux HTTP, under a context that ends the margin before the
//	         reservation does: no call starts after that.
//	finish   short transaction: only while op_token is still this one, the
//	         caller's conditional update; the token cleared either way.
//
// Every other wake claim, resume, replacement and sweep leaves a row with a
// reservation in force alone, and comes back later. A lapsed one is taken
// over: its holder's context ended before it did, so the holder calls lux
// no more. A stop does not wait: StopPreview sets the status, and the
// operation's finish, whose condition includes a live preview, records
// nothing; the sweep then cancels the preview's lux Run as for any stop.

const (
	opResume  = "resume"
	opReplace = "replace"
	// How long a reservation holds when Previews.OperationFor is zero.
	operationFor = 90 * time.Second
	// The time a holder leaves unused at the end of its reservation, so its
	// last lux call has ended before another may take the row; a ninth of
	// a reservation shorter than 90 seconds (tests).
	operationMargin = 10 * time.Second
)

// liveStatus: a preview not stopped, completed, failed or aborted.
const liveStatus = `status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`

// errLapsed: the reservation's time for lux calls ran out before this one.
var errLapsed = errors.New("the preview's reservation for this operation lapsed")

type operation struct {
	id, org, token string
	// When this orchestrator stops calling lux for the operation.
	until time.Time
}

func (p *Previews) operationFor() time.Duration {
	if p.OperationFor > 0 {
		return p.OperationFor
	}
	return operationFor
}

// reserve reserves the preview's row for one operation of kind, when held
// (SQL over runs: $1 is the run id, args are $2 on) is true of it. nil: held
// is false, or another reservation is in force (busy); errLapsed: the
// database's answer came too late to leave any time for lux calls.
func (p *Previews) reserve(ctx context.Context, org, id, kind, held string, args ...any) (op *operation, busy bool, err error) {
	token := ids.New("op")
	err = p.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE(`+held+` AND `+liveStatus+`, false),
				op_token IS NOT NULL AND op_deadline > now() FROM runs WHERE id = $1 FOR UPDATE`,
			append([]any{id}, args...)...).Scan(&ok, &busy); err != nil || !ok || busy {
			return err
		}
		// The remaining time counts from before the UPDATE is sent: a
		// delayed answer shortens the holder's time, never extends it.
		start := time.Now()
		var remaining float64
		if err := tx.QueryRow(ctx, `UPDATE runs SET op_token = $2, op_kind = $3, op_deadline = now() + make_interval(secs => $4)
			WHERE id = $1 RETURNING extract(epoch FROM op_deadline - clock_timestamp())::float8`,
			id, token, kind, p.operationFor().Seconds()).Scan(&remaining); err != nil {
			return err
		}
		margin := min(operationMargin, p.operationFor()/9)
		op = &operation{id: id, org: org, token: token,
			until: start.Add(time.Duration(remaining*float64(time.Second)) - margin)}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if op != nil && !time.Now().Before(op.until) {
		p.release(ctx, op)
		return nil, false, errLapsed
	}
	return op, busy, nil
}

// context is ctx ending when the operation's time for lux calls does.
func (op *operation) context(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, op.until)
}

// call makes one lux call for the operation, never once its time is up.
func (op *operation) call(ctx context.Context, fn func(context.Context) error) error {
	if !time.Now().Before(op.until) {
		return errLapsed
	}
	return fn(ctx)
}

// finish records the operation's outcome with record, only while the
// reservation is still this one, and lets the row go (on an error too).
// false: another took the row over, or record's own condition no longer
// held.
func (p *Previews) finish(ctx context.Context, op *operation, record func(pgx.Tx) (bool, error)) (bool, error) {
	won := false
	err := p.DB.InOrg(ctx, op.org, func(tx pgx.Tx) error {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT op_token IS NOT DISTINCT FROM $2 FROM runs WHERE id = $1 FOR UPDATE`,
			op.id, op.token).Scan(&mine); err != nil || !mine {
			return err
		}
		var err error
		if won, err = record(tx); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE runs SET op_token = NULL, op_kind = NULL, op_deadline = NULL WHERE id = $1 AND op_token = $2`,
			op.id, op.token)
		return err
	})
	if err != nil {
		p.release(ctx, op)
		return false, err
	}
	return won, nil
}

// release lets the row go without recording anything: the operation failed.
func (p *Previews) release(ctx context.Context, op *operation) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := p.DB.InOrg(ctx, op.org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET op_token = NULL, op_kind = NULL, op_deadline = NULL WHERE id = $1 AND op_token = $2`,
			op.id, op.token)
		return err
	}); err != nil {
		p.Log.Warn("releasing a preview's reservation failed; it lapses by itself", "run", op.id, "error", err)
	}
}
