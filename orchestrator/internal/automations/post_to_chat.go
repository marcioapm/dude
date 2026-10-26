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
// (the run cannot do what it was for); "" for none.
func postFailed(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var reason string
	err := tx.QueryRow(ctx, `SELECT payload->>'reason' FROM events
		WHERE run_id = $2 AND event_type = $1 AND payload->>'code' = $3
		ORDER BY occurred_at LIMIT 1`, agenttools.EvChatPostRefused, runID, agenttools.PostNotConnected).Scan(&reason)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return PostFailedPrefix + reason, nil
}
