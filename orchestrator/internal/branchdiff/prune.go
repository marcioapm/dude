package branchdiff

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// PruneBatch is how many snapshots one call of prune_branch_snapshots
// deletes at most: one transaction's worth, short enough that a capture
// waiting on a row it holds waits little.
const PruneBatch = 200

// Prune deletes the task branch snapshots nothing can still read
// (migration 231's prune_branch_snapshots: not a lineage's latest, not
// named by a capture request or a feedback item, older than a day), a
// batch per transaction until a batch comes back short. Returns how many
// went. The loop runs it every few minutes; what it skips (held by another
// orchestrator's pruner, or a capture) is left for the next pass.
func Prune(ctx context.Context, d *db.DB) (int, error) {
	return PruneUpTo(ctx, d, PruneBatch)
}

// PruneUpTo is Prune with batches of limit snapshots.
func PruneUpTo(ctx context.Context, d *db.DB, limit int) (int, error) {
	total := 0
	for ctx.Err() == nil {
		var n int
		if err := d.InSystem(ctx, "branch-snapshots-prune", func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT prune_branch_snapshots($1)`, limit).Scan(&n)
		}); err != nil {
			return total, err
		}
		total += n
		if n < limit {
			return total, nil
		}
	}
	return total, ctx.Err()
}
