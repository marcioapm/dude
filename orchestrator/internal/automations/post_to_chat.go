package automations

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// postFailed is why a run whose agent's turn ended must fail: a
// post_to_chat refused because the organisation has no Slack installation
// (the run cannot do what it was for); "" for none.
func postFailed(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var said string
	err := tx.QueryRow(ctx, `SELECT payload->>'error' FROM events
		WHERE run_id = $2 AND event_type = $1 AND payload->>'code' = $3
		ORDER BY occurred_at LIMIT 1`, agenttools.EvChatPostRefused, runID, agenttools.PostNotConnected).Scan(&said)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return "post_to_chat failed: " + firstSentence(said), nil
}

// firstSentence is s up to its first ": " (the refusal's fact, without
// what to do about it).
func firstSentence(s string) string {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ':' && s[i+1] == ' ' {
			return s[:i]
		}
	}
	return s
}
