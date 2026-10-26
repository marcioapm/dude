package automations

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// PostFailedPrefix starts the reason of a run failed because its agent
// called post_to_chat with no Slack installation.
const PostFailedPrefix = "post_to_chat failed: "

// postFailed is why a run whose agent's turn ended must fail: a
// post_to_chat refused because the organisation has no Slack installation
// (the run cannot do what it was for), with no post after it (Slack was
// connected during the run); "" for none.
func postFailed(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var reason string
	err := tx.QueryRow(ctx, `SELECT r.payload->>'reason' FROM events r
		WHERE r.run_id = $1 AND r.event_type = $2 AND r.payload->>'code' = $3
		  AND NOT EXISTS (SELECT 1 FROM events p WHERE p.run_id = $1 AND p.event_type = $4 AND (p.occurred_at, p.cursor) > (r.occurred_at, r.cursor))
		ORDER BY r.occurred_at DESC, r.cursor DESC LIMIT 1`,
		runID, agenttools.EvSlackPostRefused, agenttools.PostNotConnected, agenttools.EvSlackPosted).Scan(&reason)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return PostFailedPrefix + reason, nil
}
