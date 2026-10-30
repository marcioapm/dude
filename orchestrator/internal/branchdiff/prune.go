package branchdiff

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// PruneBatch is how many snapshots one call of prune_branch_snapshots
// deletes at most: one transaction's worth, short enough that a capture
// waiting on a row it holds waits little.
const PruneBatch = 200

// Prune deletes the task branch snapshots nothing can still read
// (migration 231's prune_branch_snapshots: not a lineage's latest, not
// named by a capture request or a feedback item, superseded over a day
// ago), a batch per transaction until a batch comes back short, and logs
// how many went. Returns 0 once a pass has ended on a short batch, so the
// loop waits its interval rather than running at once a pass that finds
// nothing; what it skips (held by another orchestrator's pruner, or a
// capture) is left for the next pass. A pass cut short (ctx done, or an
// error) returns what it pruned with the error.
func Prune(ctx context.Context, d *db.DB, log *slog.Logger) (int, error) {
	n, err := PruneUpTo(ctx, d, PruneBatch)
	if n > 0 && log != nil {
		log.Info("branch snapshots pruned", "pruned", n)
	}
	if err != nil {
		return n, err
	}
	return 0, nil
}

// PruneUpTo is Prune with batches of limit snapshots, without the log:
// returns how many went over the whole pass.
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
