package phases

import (
	"context"
	"encoding/json"

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
// is what a conversation replayed from the ledger starts from. A harness's
// own announcement held for it (holdCompaction) is this compaction, and is
// dropped; from now on this lux's records are the only ones taken.
func (t *translator) recordLuxCompaction(ctx context.Context, tx pgx.Tx, s *Syncer, data map[string]any) error {
	t.compactHeld = nil
	t.luxCompacts = true
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

// holdCompaction takes a harness's own announcement of a compaction
// (Claude Code's compact_boundary, Codex's contextCompaction item), the
// fallback for a lux that sends no lux.compacted. lux relays the harness's
// line and writes its own record right after it: the announcement is held
// until the next record, which either is that lux.compacted (and it is
// dropped) or is not (and it is recorded, settleCompaction). A lux that
// has sent one is never waited for again.
func (t *translator) holdCompaction(ctx context.Context, tx pgx.Tx, s *Syncer, payload map[string]any) error {
	if t.luxCompacts {
		return nil
	}
	if err := t.settleCompaction(ctx, tx, s); err != nil {
		return err
	}
	t.compactHeld = payload
	return nil
}

// settleCompaction records a held announcement no lux.compacted followed.
func (t *translator) settleCompaction(ctx context.Context, tx pgx.Tx, s *Syncer) error {
	held := t.compactHeld
	if held == nil {
		return nil
	}
	t.compactHeld = nil
	return s.event(ctx, tx, t.run, evContextCompacted, ledger.ActorAgent, held)
}

// claudeCompaction is a compact_boundary system line's announcement:
// {compact_metadata: {trigger, pre_tokens, post_tokens}}.
func claudeCompaction(raw json.RawMessage) map[string]any {
	var line struct {
		Meta struct {
			Trigger    string `json:"trigger"`
			PreTokens  *int64 `json:"pre_tokens"`
			PostTokens *int64 `json:"post_tokens"`
		} `json:"compact_metadata"`
	}
	_ = json.Unmarshal(raw, &line)
	payload := map[string]any{}
	if line.Meta.Trigger != "" {
		payload["trigger"] = line.Meta.Trigger
	}
	if line.Meta.PreTokens != nil {
		payload["preTokens"] = *line.Meta.PreTokens
	}
	if line.Meta.PostTokens != nil {
		payload["postTokens"] = *line.Meta.PostTokens
	}
	return payload
}
