package phases

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A run.checks.judged row the judge recorded before the conductor decided
// checks (callId, judgePassed, judgeReason, waiverReason) is still this
// turn's decision: the gate reads it back and decides as it did then,
// asking nobody and recording nothing; and the unchanged-push guard reads
// it as a judged turn, not an unchanged one.
func TestAJudgeEraDecisionIsReadAsBefore(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
	}{{"passed by the judge", true}, {"rejected by the judge", false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			conducted(w)
			checksReply(w, failingTsc)
			endTurn(w)
			payload := map[string]any{"turn": w.turn(), "round": 0, "verified": tc.verified, "judgePassed": tc.verified,
				"message": "judged by the model", "checks": []map[string]any{{
					"command": "npx tsc --noEmit", "result": "pre-existing", "reason": "95 errors", "callId": "mcall_old1",
					"judgeReason": "the same 95 errors at the base", "waiverReason": "not the waived failure"}}}
			if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
				return w.s.event(context.Background(), tx, w.run, delivery.EvRunChecksJudged, ledger.ActorSystem, payload)
			}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if pass, acted := w.gate(); pass != tc.verified || acted {
					t.Fatalf("pass %v acted %v, want pass %v from the old row", pass, acted, tc.verified)
				}
			}
			if n := w.judgedCount(); n != 1 {
				t.Errorf("%d judgements, want the old row alone", n)
			}
			if n := w.countEvents(delivery.EvRunChecksAsked); n != 0 {
				t.Errorf("asked the conductor %d times about a judged turn", n)
			}
			got := recordedJudgement(w)
			if got.Verified != tc.verified || got.ConductorPassed != tc.verified || got.Unchanged || len(got.Verdicts) != 1 ||
				got.Verdicts[0].JudgeReason != "the same 95 errors at the base" || got.Verdicts[0].Result != "pre-existing" {
				t.Errorf("read %+v", got)
			}
			// The guard on a push after an unchanged turn lets a judged one be.
			r := w.run
			r.PushResult = json.RawMessage(`{"repositories":[{"name":"api","before":"a","after":"b"}]}`)
			if refused, err := w.s.unchangedPushRefused(context.Background(), r); err != nil || refused {
				t.Errorf("refused %v, %v: an old judged turn read as unchanged", refused, err)
			}
		})
	}
}
