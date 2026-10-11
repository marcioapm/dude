package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ReplayEventTypes are the types, in cursor order, of the events Replay
// reads from the ledger for the talker.
func ReplayEventTypes(ctx context.Context, tx pgx.Tx, of Talker) ([]string, error) {
	events, err := replayEvents(ctx, tx, of)
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types, err
}
