package phases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

type checksFake struct {
	backed, missing string
	err             error
	calls           int
	// unrecorded: answers with no CallID, a judgement model_calls never
	// recorded.
	unrecorded bool
}

func (f *checksFake) Judge(_ context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	f.calls++
	d := f.backed
	if f.calls%2 == 0 {
		d = f.missing
	}
	if f.err != nil {
		// An error rejects whatever comes with it: here a yes, recorded.
		return judge.Judgement{Decision: d, CallID: "call_test"}, f.err
	}
	id := "call_test"
	if f.unrecorded {
		id = ""
	}
	return judge.Judgement{Decision: d, CallID: id, Reasoning: "fixture evidence"}, nil
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
		unrecorded            bool
	}{
		{"accepted", judge.Yes, judge.No, nil, true, false},
		{"backed unsure", judge.Unsure, judge.No, nil, false, false},
		{"missing unsure", judge.Yes, judge.Unsure, nil, false, false},
		{"unbacked", judge.No, judge.No, nil, false, false},
		{"missing", judge.Yes, judge.Yes, nil, false, false},
		{"zero, what judge.Service returns failing", "", "", nil, false, false},
		{"timeout", judge.Yes, judge.No, errors.New("timeout"), false, false},
		{"recorded nowhere", judge.Yes, judge.No, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			checksReply(w, passingChecks)
			w.s.Judge = &checksFake{backed: tc.backed, missing: tc.missing, err: tc.err, unrecorded: tc.unrecorded}
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

// interruptingJudge stands for an orchestrator stopped mid-judgement: its
// context ends while the model thinks.
type interruptingJudge struct {
	calls int
	stop  context.CancelFunc
}

func (j *interruptingJudge) Judge(ctx context.Context, _ string, a judge.Ask) (judge.Judgement, error) {
	j.calls++
	j.stop()
	return judge.Judgement{}, ctx.Err()
}

// A restart between a turn's end and its judgement never passes it: what
// was judged is read back, a rejection staying one, and a judgement cut
// short records nothing, so the next process judges the turn again.
func TestChecksARestartNeverPassesAnUnjudgedOrRejectedTurn(t *testing.T) {
	w := newHarnessWorld(t)
	checksReply(w, passingChecks)
	w.feed(map[string]any{"type": "lux.activity", "data": map[string]any{"activity": "busy"}},
		map[string]any{"type": "lux.activity", "data": map[string]any{"activity": "idle"}})
	// Cut short: no verdict is recorded and nothing passes.
	ctx, stop := context.WithCancel(context.Background())
	cut := &interruptingJudge{stop: stop}
	w.s.Judge = cut
	if ok, _, err := w.s.checksBeforePush(ctx, w.run); ok || err == nil || cut.calls == 0 {
		t.Fatalf("an interrupted judgement: pass %v, %v", ok, err)
	}
	if n := w.countEvents(checksJudgedEvent); n != 0 {
		t.Fatalf("%d verdicts recorded for an interrupted judgement", n)
	}
	// The next process judges it again, here rejecting it...
	again := &Syncer{DB: w.s.DB, Judge: &checksFake{backed: judge.No, missing: judge.No}}
	if ok, _, err := again.checksBeforePush(context.Background(), w.run); ok || err != nil {
		t.Fatalf("judged again: pass %v, %v", ok, err)
	}
	// ...and one after that reads the rejection, asking nobody.
	after := &Syncer{DB: w.s.DB, Judge: &checksFake{err: errors.New("must not be called")}}
	if ok, _, err := after.checksBeforePush(context.Background(), w.run); ok || err != nil {
		t.Fatalf("after the rejection: pass %v, %v", ok, err)
	}
	if n := w.countEvents(checksJudgedEvent); n != 1 {
		t.Fatalf("%d verdicts, want the one rejection", n)
	}
}

func (w *harnessWorld) countEvents(typ string) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`,
		w.run.ID, typ).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// The gate reads only the end of a long reply: the newest report after
// more than checksReplyMax of earlier text is judged, and a malformed
// newest report is never replaced by an earlier valid one.
func TestChecksReadOnlyTheEndOfALongReply(t *testing.T) {
	filler := strings.Repeat("working on it\n", 2<<10)
	older := func(w *harnessWorld) {
		checksReply(w, passingChecks)
		for size := 0; size <= checksReplyMax; size += len(filler) + 1 {
			checksReply(w, filler)
		}
	}
	t.Run("the newest report", func(t *testing.T) {
		w := newHarnessWorld(t)
		older(w)
		checksReply(w, "Done.")
		checksReply(w, strings.ReplaceAll(passingChecks, "verify", "make check"))
		var reply string
		if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
			reply, err = delivery.RunReplyTailTx(context.Background(), tx, w.run.ID, checksReplyMax)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(reply) > checksReplyMax+len(filler)+1 || strings.Contains(reply, "command: verify") {
			t.Fatalf("read %d bytes, the earliest report among them: %v", len(reply), strings.Contains(reply, "command: verify"))
		}
		fake := &checksFake{backed: judge.Yes, missing: judge.No}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || !got.Verified || fake.calls != 2 || got.Verdicts[0].Command != "make check" {
			t.Fatalf("%+v %v calls %d", got, err, fake.calls)
		}
	})
	t.Run("a malformed newest report", func(t *testing.T) {
		w := newHarnessWorld(t)
		older(w)
		checksReply(w, passingChecks)
		checksReply(w, "checks: [broken")
		fake := &checksFake{backed: judge.Yes, missing: judge.No}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || got.Verified || fake.calls != 0 {
			t.Fatalf("%+v %v calls %d", got, err, fake.calls)
		}
	})
	// One message longer than checksReplyMax is read only for its end.
	long := strings.Repeat(filler, checksReplyMax/len(filler)+1)
	t.Run("a report at the start of one oversized message", func(t *testing.T) {
		w := newHarnessWorld(t)
		checksReply(w, passingChecks+"\n"+long)
		fake := &checksFake{backed: judge.Yes, missing: judge.No}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || got.Verified || fake.calls != 0 {
			t.Fatalf("%+v %v calls %d", got, err, fake.calls)
		}
	})
	t.Run("a report at the end of one oversized message", func(t *testing.T) {
		w := newHarnessWorld(t)
		checksReply(w, long+passingChecks)
		fake := &checksFake{backed: judge.Yes, missing: judge.No}
		w.s.Judge = fake
		got, err := w.s.judgeChecks(context.Background(), w.run)
		if err != nil || !got.Verified || fake.calls != 2 {
			t.Fatalf("%+v %v calls %d", got, err, fake.calls)
		}
	})
}

// A report of more than checksCountMax checks is sent back unjudged,
// saying why; one of exactly checksCountMax is judged.
func TestChecksTooManyChecksAreSentBackUnjudged(t *testing.T) {
	report := func(n int) string {
		var b strings.Builder
		b.WriteString("checks:\n")
		for i := range n {
			fmt.Fprintf(&b, "  - command: check %d\n    result: pass\n    evidence: ok\n", i)
		}
		return b.String()
	}
	for _, tc := range []struct {
		n, calls int
		judged   bool
	}{{checksCountMax + 1, 0, false}, {checksCountMax, checksCountMax + 1, true}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			w := newHarnessWorld(t)
			checksReply(w, report(tc.n))
			fake := &checksFake{backed: judge.Yes, missing: judge.No}
			w.s.Judge = fake
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || fake.calls != tc.calls {
				t.Fatalf("%+v %v calls %d", got, err, fake.calls)
			}
			if tc.judged {
				return
			}
			want := fmt.Sprintf("the report lists %d checks, more than the %d that are verified", tc.n, checksCountMax)
			if got.Verified || !strings.Contains(checksText.Render(got.Verdicts), want) {
				t.Fatalf("%+v: the send-back lacks %q", got, want)
			}
		})
	}
}

func TestChecksNonGatedPhaseDeclaration(t *testing.T) {
	if delivery.ChecksGated[delivery.PhaseReview] {
		t.Fatal("review is gated")
	}
}
