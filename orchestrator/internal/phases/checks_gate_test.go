package phases

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

type checksFake struct {
	backed, missing string
	err             error
	calls           int
}

func (f *checksFake) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	f.calls++
	d := f.backed
	if f.calls%2 == 0 {
		d = f.missing
	}
	return judge.Judgement{Decision: d, CallID: "call_test", Reasoning: "fixture evidence"}, f.err
}

func checksReply(w *harnessWorld, text string) {
	w.t.Helper()
	err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
		return w.s.event(context.Background(), tx, w.run, evAgentMessage, "agent", map[string]any{"text": text})
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

const passingChecks = "```yaml\nchecks:\n  - command: verify\n    result: pass\n    evidence: successful\n```"

func TestChecksJudgementsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, backed, missing string
		err                   error
		want                  bool
	}{
		{"accepted", judge.Yes, judge.No, nil, true},
		{"backed unsure", judge.Unsure, judge.No, nil, false},
		{"missing unsure", judge.Yes, judge.Unsure, nil, false},
		{"unbacked", judge.No, judge.No, nil, false},
		{"missing", judge.Yes, judge.Yes, nil, false},
		{"zero", "", "", nil, false},
		{"timeout", "", "", errors.New("timeout"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			checksReply(w, passingChecks)
			w.s.Judge = &checksFake{backed: tc.backed, missing: tc.missing, err: tc.err}
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified != tc.want {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
func TestChecksMissingAndSelfReportedFailuresRejectWithoutJudge(t *testing.T) {
	for _, text := range []string{"done", "checks: [broken", "checks:\n - command: verify\n   result: fail", "checks:\n - command: verify\n   result: not-run"} {
		t.Run(text, func(t *testing.T) {
			w := newHarnessWorld(t)
			checksReply(w, text)
			fake := &checksFake{backed: judge.Yes, missing: judge.No}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified || fake.calls != 0 {
				t.Fatalf("%+v %v calls %d", got, err, fake.calls)
			}
		})
	}
}
func TestChecksRestartRetainsOnlyAcceptedCurrentTurn(t *testing.T) {
	w := newHarnessWorld(t)
	checksReply(w, passingChecks)
	fake := &checksFake{backed: judge.Yes, missing: judge.No}
	w.s.Judge = fake
	ok, _, err := w.s.checksBeforePush(context.Background(), w.run)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	// An independent Syncer consults the ledger, not an in-memory result.
	restarted := &Syncer{DB: w.s.DB, Judge: &checksFake{err: errors.New("must not be called")}}
	ok, _, err = restarted.checksBeforePush(context.Background(), w.run)
	if err != nil || !ok {
		t.Fatalf("restart: %v %v", ok, err)
	}
}
func TestChecksNonGatedPhaseDeclaration(t *testing.T) {
	if delivery.ChecksGated[delivery.PhaseReview] {
		t.Fatal("review is gated")
	}
}
