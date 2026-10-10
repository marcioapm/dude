package phases

import (
	"strings"
	"testing"
)

func compactions(w *harnessWorld) []harnessEvent { return ofType(w.events(), evContextCompacted) }

// lux's own record of a compaction, for any harness, becomes one
// agent.context.compacted with what it carried: the summary whole, the
// token counts when given, and nothing it did not give.
func TestLuxsCompactionRecordIsKeptWithItsSummary(t *testing.T) {
	w := newHarnessWorld(t)
	summary := strings.Repeat("The person wants usage-based billing. ", 2000)
	w.feed(rec("lux.compacted", map[string]any{"sessionId": "ses_a", "trigger": "auto", "preTokens": 167012, "postTokens": 9120,
		"summary": summary, "summaryTruncated": true}))
	got := compactions(w)
	if len(got) != 1 {
		t.Fatalf("compactions %v", got)
	}
	p := got[0].Payload
	if p["trigger"] != "auto" || p["preTokens"] != float64(167012) || p["postTokens"] != float64(9120) ||
		p["summary"] != summary || p["summaryTruncated"] != true {
		t.Errorf("payload: trigger %v pre %v post %v truncated %v, summary %d bytes of %d",
			p["trigger"], p["preTokens"], p["postTokens"], p["summaryTruncated"], len(p["summary"].(string)), len(summary))
	}
	// OpenCode's: a session id and nothing else.
	w.feed(rec("lux.compacted", map[string]any{"sessionId": "ses_a", "trigger": ""}))
	got = compactions(w)
	if len(got) != 1 || len(got[0].Payload) != 0 {
		t.Errorf("a bare record: %v", got)
	}
}

// claudeBoundary is Claude Code's stream-json line for an automatic
// compaction, as lux relays it.
func claudeBoundary() map[string]any {
	return rec("claude.system", map[string]any{"type": "system", "subtype": "compact_boundary", "session_id": "ses_a",
		"compact_metadata": map[string]any{"trigger": "auto", "pre_tokens": 167012, "post_tokens": 9120}})
}

func codexCompaction() map[string]any {
	return rec("codex.item/completed", map[string]any{"item": map[string]any{"type": "contextCompaction", "id": "item_7"}})
}

var idle = rec("lux.activity", map[string]any{"activity": "idle"})

// A harness's own announcement of a compaction, as lux relays it, records
// nothing: only lux's record does (a lux from before lux.compacted records
// no compaction).
func TestAHarnessesOwnCompactionLineRecordsNothing(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(claudeBoundary(), idle)
	w.feed(codexCompaction())
	w.restart()
	w.feed(idle, rec("lux.activity", map[string]any{"activity": "busy"}), idle)
	if got := compactions(w); len(got) != 0 {
		t.Errorf("compactions from the harnesses' lines alone: %v", got)
	}
}

// One compaction is one event, lux's with its summary, in the orders lux
// produces: its warning (no summary) before its record, and Codex's
// record written up to seconds later, after other records of the agent.
func TestOneCompactionIsOneEventLuxs(t *testing.T) {
	luxs := rec("lux.compacted", map[string]any{"sessionId": "ses_a", "trigger": "auto", "preTokens": 167012, "postTokens": 9120,
		"summary": "Billing per run."})
	warning := rec("lux.warning", map[string]any{"message": "compaction summary unavailable"})
	busy := rec("lux.activity", map[string]any{"activity": "busy"})
	others := rec("codex.item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": "item_8", "text": "Done."}})
	for _, c := range []struct {
		name    string
		batches [][]map[string]any
	}{
		{"claude, lux's warning first", [][]map[string]any{{claudeBoundary(), warning, luxs, idle}}},
		{"codex, lux's warning first", [][]map[string]any{{codexCompaction(), warning, luxs, idle}}},
		{"claude, records between", [][]map[string]any{{claudeBoundary(), busy, idle}, {luxs}}},
		{"codex, records between, follower restarted", [][]map[string]any{{codexCompaction(), others, busy}, nil, {idle, luxs}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			for _, batch := range c.batches {
				if batch == nil {
					w.restart()
					continue
				}
				w.feed(batch...)
			}
			got := compactions(w)
			if len(got) != 1 {
				t.Fatalf("%d events for one compaction: %v", len(got), got)
			}
			if got[0].Payload["summary"] != "Billing per run." {
				t.Errorf("not lux's record: %v", got[0].Payload)
			}
		})
	}
}
