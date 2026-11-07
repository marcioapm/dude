package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

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

// A missing or unparseable report is rejected without asking the
// conductor: nothing is asked and nothing is pending.
func TestChecksMissingOrUnparseableReportsRejectWithoutAsking(t *testing.T) {
	for _, text := range []string{"done", "checks: [broken"} {
		t.Run(text, func(t *testing.T) {
			w := newHarnessWorld(t)
			conducted(w)
			checksReply(w, text)
			endTurn(w)
			got, err := w.s.judgeChecks(context.Background(), w.run)
			if err != nil || got.Verified || got.pending || w.countEvents(delivery.EvRunChecksAsked) != 0 {
				t.Fatalf("%+v %v asked %d", got, err, w.countEvents(delivery.EvRunChecksAsked))
			}
		})
	}
}

// An accepted turn is read back from the ledger by a restarted syncer,
// which asks nobody.
func TestChecksRestartRetainsOnlyAcceptedCurrentTurn(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	w.gate()
	if _, err := w.decide(cond, backedAll(w.run.ID, w.turn())); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.gate(); !ok {
		t.Fatal("the backed turn did not pass")
	}
	// An independent Syncer consults the ledger, not an in-memory result.
	restarted := &Syncer{DB: w.s.DB}
	ok, acted, err := restarted.checksBeforePush(context.Background(), w.run)
	if err != nil || !ok || acted || w.countEvents(delivery.EvRunChecksAsked) != 1 {
		t.Fatalf("restart: %v %v %v, asked %d", ok, acted, err, w.countEvents(delivery.EvRunChecksAsked))
	}
}

// A restart between a turn's end and the conductor's verdict never passes
// it: a pending ask stays pending, recording nothing and asking once; a
// rejection is read back as one, deciding nothing again.
func TestChecksARestartNeverPassesAnUndecidedOrRejectedTurn(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, passingChecks)
	endTurn(w)
	if ok, _ := w.gate(); ok {
		t.Fatal("passed before the verdict")
	}
	restarted := &Syncer{DB: w.s.DB}
	if ok, acted, err := restarted.checksBeforePush(context.Background(), w.run); ok || acted || err != nil {
		t.Fatalf("restarted while pending: pass %v acted %v, %v", ok, acted, err)
	}
	if n, asked := w.countEvents(delivery.EvRunChecksJudged), w.countEvents(delivery.EvRunChecksAsked); n != 0 || asked != 1 {
		t.Fatalf("%d verdicts recorded, asked %d times, while pending", n, asked)
	}
	unbacked := backedAll(w.run.ID, w.turn())
	unbacked.Checks[0].Verdict = delivery.VerdictUnbacked
	if _, err := w.decide(cond, unbacked); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := restarted.checksBeforePush(context.Background(), w.run); ok || err != nil {
		t.Fatalf("decided unbacked: pass %v, %v", ok, err)
	}
	after := &Syncer{DB: w.s.DB}
	if ok, acted, err := after.checksBeforePush(context.Background(), w.run); ok || acted || err != nil {
		t.Fatalf("after the rejection: pass %v acted %v, %v", ok, acted, err)
	}
	if n := w.countEvents(delivery.EvRunChecksJudged); n != 1 {
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

// The send-back lists only the checks that were not verified; the judged
// event keeps every verdict, passed ones included.
func TestChecksSendBackListsOnlyUnverifiedChecks(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	checksReply(w, strings.ReplaceAll(passingChecks, "verify", "make check"))
	endTurn(w)
	w.gate()
	// The conductor decides make check unbacked, nothing missing.
	unbacked := delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
		Checks:  []delivery.ConductedCheck{{Command: "make check", Verdict: delivery.VerdictUnbacked, Reason: "fixture evidence"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "nothing else changed"}}
	if _, err := w.decide(cond, unbacked); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := w.s.checksBeforePush(context.Background(), w.run); ok || err != nil {
		t.Fatalf("pass %v, %v", ok, err)
	}
	var payload struct {
		Message     string          `json:"message"`
		DirectiveID string          `json:"directiveId"`
		Checks      []checksVerdict `json:"checks"`
	}
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2`,
		w.run.ID, delivery.EvRunChecksJudged).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	want := "Your checks report was not verified. Nothing from this Run has been pushed.\n" +
		"\n- make check: fixture evidence (verdict: unbacked)\n" +
		"\nRun the missing or unsuccessful checks as separate commands, read their output, and correct the report. " +
		"End your next reply with a checks: YAML block. If you cannot proceed, ask a person what you need."
	if payload.Message != want {
		t.Fatalf("send-back:\n%q\nwant:\n%q", payload.Message, want)
	}
	var sent string
	if err := w.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE id = $1`, payload.DirectiveID).Scan(&sent); err != nil || sent != want {
		t.Fatalf("directive %q (%v)", sent, err)
	}
	if len(payload.Checks) != 2 || payload.Checks[0].Command != "make check" || payload.Checks[0].Result != "unverified" ||
		payload.Checks[1].Command != "missing checks" || payload.Checks[1].Result != "pass" {
		t.Fatalf("judged checks %+v", payload.Checks)
	}
}

// The gate reads only the end of a long reply: the newest report after
// more than checksReplyMax of earlier text is the one decided, and a
// malformed newest report is never replaced by an earlier valid one.
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
		_, checks, done, err := w.s.parsedChecks(context.Background(), w.run)
		if err != nil || done || len(checks) != 1 || checks[0].Command != "make check" {
			t.Fatalf("%+v %v done %v", checks, err, done)
		}
	})
	t.Run("a malformed newest report", func(t *testing.T) {
		w := newHarnessWorld(t)
		older(w)
		checksReply(w, passingChecks)
		checksReply(w, "checks: [broken")
		got, _, done, err := w.s.parsedChecks(context.Background(), w.run)
		if err != nil || !done || got.Verified || got.Verdicts[0].Command != "checks:" {
			t.Fatalf("%+v %v done %v", got, err, done)
		}
	})
	// One message longer than checksReplyMax is read only for its end.
	long := strings.Repeat(filler, checksReplyMax/len(filler)+1)
	t.Run("a report at the start of one oversized message", func(t *testing.T) {
		w := newHarnessWorld(t)
		checksReply(w, passingChecks+"\n"+long)
		got, _, done, err := w.s.parsedChecks(context.Background(), w.run)
		if err != nil || !done || got.Verified || got.Verdicts[0].Command != "checks:" {
			t.Fatalf("%+v %v done %v", got, err, done)
		}
	})
	t.Run("a report at the end of one oversized message", func(t *testing.T) {
		w := newHarnessWorld(t)
		checksReply(w, long+passingChecks)
		_, checks, done, err := w.s.parsedChecks(context.Background(), w.run)
		if err != nil || done || len(checks) != 1 {
			t.Fatalf("%+v %v done %v", checks, err, done)
		}
	})
}

// A report of more than checksCountMax checks is sent back undecided,
// saying why; one of exactly checksCountMax goes on to be decided.
func TestChecksTooManyChecksAreSentBackUndecided(t *testing.T) {
	report := func(n int) string {
		var b strings.Builder
		b.WriteString("checks:\n")
		for i := range n {
			fmt.Fprintf(&b, "  - command: check %d\n    result: pass\n    evidence: ok\n", i)
		}
		return b.String()
	}
	for _, tc := range []struct {
		n      int
		judged bool
	}{{checksCountMax + 1, false}, {checksCountMax, true}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			w := newHarnessWorld(t)
			checksReply(w, report(tc.n))
			got, checks, done, err := w.s.parsedChecks(context.Background(), w.run)
			if err != nil || done == tc.judged {
				t.Fatalf("%+v %v done %v", got, err, done)
			}
			if tc.judged {
				if len(checks) != tc.n {
					t.Fatalf("%d checks parsed, want %d", len(checks), tc.n)
				}
				return
			}
			want := fmt.Sprintf("the report lists %d checks, more than the %d that are verified", tc.n, checksCountMax)
			if got.Verified || !strings.Contains((checksDecision{Verdicts: got.Verdicts}).sendBack(), want) {
				t.Fatalf("%+v: the send-back lacks %q", got, want)
			}
		})
	}
}
