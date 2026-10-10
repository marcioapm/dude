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

// A lux from before lux.compacted: each harness's own announcement is the
// one event, Claude's with its trigger and tokens, Codex's with none —
// recorded once the next record shows no lux record follows, in this
// batch or the next, and after a restart of the follower.
func TestAHarnessesOwnCompactionIsRecordedOnAnOlderLux(t *testing.T) {
	w := newHarnessWorld(t)
	w.feed(claudeBoundary(), idle)
	got := compactions(w)
	if len(got) != 1 || got[0].Payload["trigger"] != "auto" || got[0].Payload["preTokens"] != float64(167012) ||
		got[0].Payload["postTokens"] != float64(9120) || got[0].Payload["summary"] != nil {
		t.Errorf("Claude's: %v", got)
	}
	w.feed(codexCompaction())
	if got := compactions(w); len(got) != 0 {
		t.Errorf("recorded before the next record: %v", got)
	}
	w.restart()
	w.feed(idle)
	if got := compactions(w); len(got) != 1 || len(got[0].Payload) != 0 {
		t.Errorf("Codex's: %v", got)
	}
}

// A lux that sends lux.compacted right after the harness's own line: one
// event, lux's, with its summary — whether the two come in one batch or
// two, and for every compaction after.
func TestOneCompactionIsOneEventWhenLuxRecordsItToo(t *testing.T) {
	luxs := rec("lux.compacted", map[string]any{"sessionId": "ses_a", "trigger": "auto", "preTokens": 167012, "postTokens": 9120,
		"summary": "Billing per run."})
	for _, harness := range []struct {
		name string
		line map[string]any
	}{{"claude", claudeBoundary()}, {"codex", codexCompaction()}} {
		t.Run(harness.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			w.feed(harness.line, luxs, idle)
			w.feed(harness.line)
			w.restart()
			w.feed(luxs, idle)
			// A later compaction lux records again, its line alone a moment
			// before: still one each.
			w.feed(harness.line, idle, luxs)
			got := compactions(w)
			if len(got) != 3 {
				t.Fatalf("%d events for three compactions: %v", len(got), got)
			}
			for _, e := range got {
				if e.Payload["summary"] != "Billing per run." {
					t.Errorf("not lux's record: %v", e.Payload)
				}
			}
		})
	}
}
