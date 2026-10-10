package phases

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// evContextCompacted: the agent's harness compacted its conversation;
// recordCompacted: lux's record of it.
const (
	evContextCompacted = "agent.context.compacted"
	recordCompacted    = "lux.compacted"
)

// recordLuxCompaction is lux's own record of a compaction, the same for
// every harness: lux.compacted {sessionId, trigger, preTokens?, postTokens?,
// summary?, summaryTruncated?}. The summary is kept whole (lux caps it): it
// is what a conversation replayed from the ledger starts from. It is the
// only record of a compaction taken: a harness's own announcement, relayed
// raw, comes at no fixed distance from it and carries no summary.
func (t *translator) recordLuxCompaction(ctx context.Context, tx pgx.Tx, s *Syncer, data map[string]any) error {
	payload := map[string]any{}
	if trigger, _ := data["trigger"].(string); trigger != "" {
		payload["trigger"] = trigger
	}
	for _, k := range []string{"preTokens", "postTokens"} {
		if n, ok := data[k].(float64); ok {
			payload[k] = int64(n)
		}
	}
	if summary, _ := data["summary"].(string); summary != "" {
		payload["summary"] = summary
	}
	if cut, _ := data["summaryTruncated"].(bool); cut {
		payload["summaryTruncated"] = true
	}
	return s.event(ctx, tx, t.run, evContextCompacted, ledger.ActorAgent, payload)
}
