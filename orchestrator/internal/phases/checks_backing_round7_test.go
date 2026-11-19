package phases

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// shellEnderCalls are echoed calls whose command may end the shell before
// its echo runs (#484 round 6): exec replaces the shell, exit and return leave
// it, and a sourced or eval'd script, or one run by command or builtin
// (which run exec, exit, . and the rest as themselves), may exit it. The
// output's last exit= line is then the script's own: run.sh here is
// "echo exit=0; exit 1", so each call printed exit=0 and exited 1.
var shellEnderCalls = []struct{ name, reported, recorded string }{
	{"exec", "exec ./run.sh", "exec ./run.sh; echo exit=$?"},
	{"exit", "exit 1", "exit 1; echo exit=$?"},
	{"return", "return 1", "return 1; echo exit=$?"},
	{"dot", ". ./run.sh", ". ./run.sh; echo exit=$?"},
	{"source", "source ./run.sh", "source ./run.sh; echo exit=$?"},
	{"eval", "eval ./run.sh", "eval ./run.sh; echo exit=$?"},
	{"command", "command exec ./run.sh", "command exec ./run.sh; echo exit=$?"},
	{"builtin", "builtin exit 1", "builtin exit 1; echo exit=$?"},
	{"exec after a cd", "cd api && exec ./run.sh", "cd api && exec ./run.sh; echo exit=$?"},
	{"exec under pipefail", "exec ./run.sh | tail", "set -o pipefail; exec ./run.sh | tail; echo exit=$?"},
	{"exec after an assignment", "A=1 exec ./run.sh", "A=1 exec ./run.sh; echo exit=$?"},
}

// The matcher backs none of them.
func TestAnEchoAfterAShellEnderBacksNothing(t *testing.T) {
	for _, c := range shellEnderCalls {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := sameCommand(c.reported, c.recorded); ok {
				t.Fatalf("%q backs %q", c.recorded, c.reported)
			}
		})
	}
}

// Nor does dude's reading of the ledger, with no exit code recorded (the
// call completed, its output ending in exit=0) or with exit 0.
func TestAnEchoAfterAShellEnderIsNotItsExit(t *testing.T) {
	for _, c := range shellEnderCalls {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			completionOnly(w, "k1", c.recorded, "completed", "exit=0")
			var got []string
			if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
				got, _, err = recordedCalls(context.Background(), tx, w.run.ID, []delivery.Check{{Command: c.reported, Result: delivery.CheckPass}}, nil)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != "none recorded" {
				t.Fatalf("read %q for %q after %q, want none recorded", got, c.reported, c.recorded)
			}
		})
	}
}

// A descendant of the command that outlives it may print after the echo:
// "(sleep 1; echo exit=0) & echo FAIL; exit 1" run as "./t.sh; echo
// exit=$?" prints FAIL, exit=1, exit=0, and the call exits 0. An output
// with more than one exit= line names no exit (#484 round 6).
func TestAnEchoedStatusPrintedTwiceIsNotTheExit(t *testing.T) {
	for _, c := range []struct {
		name, output string
		exit         int // -1: no exit code recorded (completionOnly)
		want         string
	}{
		{"a descendant's exit=0 after it, exit 0", "FAIL\nexit=1\nexit=0\n", 0, "unclear"},
		{"a descendant's exit=0 after it, completed", "FAIL\nexit=1\nexit=0\n", -1, "unclear"},
		{"the same status twice", "exit=0\nexit=0", 0, "unclear"},
		{"one echoed status", "FAIL\nexit=1\n", 0, "exit 1"},
		{"one echoed status, completed", "ok\nexit=0\n", -1, "exit 0"},
		// A line that only contains exit= is not one.
		{"another line naming exit=", "want exit=0 got 1\nexit=1", 0, "exit 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			const call = "./t.sh; echo exit=$?"
			var got []string
			if c.exit < 0 {
				completionOnly(w, "k1", call, "completed", c.output)
				if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
					got, _, err = recordedCalls(context.Background(), tx, w.run.ID, []delivery.Check{{Command: "./t.sh", Result: delivery.CheckPass}}, nil)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				got = w.recordedAfter([][]any{{call, c.exit, c.output}}, "./t.sh", delivery.CheckPass, nil)
			}
			if len(got) != 1 || !strings.HasPrefix(got[0], c.want) {
				t.Fatalf("read %q, want %s…", got, c.want)
			}
		})
	}
}

// The send-back's guidance on reporting a call exactly comes with every
// unbacked hint the gate attached, whatever the conductor said of the pass
// (#484 round 6): a reject, or no verdict for it at all, attaches the hint
// too. It is given once, and not without a hint.
func TestUnbackedGuidanceFollowsTheHint(t *testing.T) {
	const guidance = "report it exactly as recorded"
	checks := []delivery.Check{{Command: "make test", Result: delivery.CheckPass, Evidence: "ok"},
		{Command: "make lint", Result: delivery.CheckPass, Evidence: "ok"}}
	asked := &delivery.ChecksAsked{Turn: 3, Check: checks, Closest: map[int][]string{0: {`"make  test" (exit 0)`}, 1: {}}}
	for _, c := range []struct {
		name    string
		verdict []delivery.ConductedCheck
		asked   *delivery.ChecksAsked
		want    int
	}{
		{"unbacked", []delivery.ConductedCheck{{Command: "make test", Verdict: delivery.VerdictUnbacked}, {Command: "make lint", Verdict: delivery.VerdictUnbacked}}, asked, 1},
		{"rejected", []delivery.ConductedCheck{{Command: "make test", Verdict: delivery.VerdictReject}, {Command: "make lint", Verdict: delivery.VerdictReject}}, asked, 1},
		{"one rejected, one backed", []delivery.ConductedCheck{{Command: "make test", Verdict: delivery.VerdictReject}, {Command: "make lint", Verdict: delivery.VerdictBacked}}, asked, 1},
		{"no verdict for them", nil, asked, 1},
		{"misnamed", []delivery.ConductedCheck{{Command: "make tests", Verdict: delivery.VerdictBacked}, {Command: "make lints", Verdict: delivery.VerdictBacked}}, asked, 1},
		// The ask is lost: no hint, so no guidance.
		{"unbacked, ask lost", []delivery.ConductedCheck{{Command: "make test", Verdict: delivery.VerdictUnbacked}, {Command: "make lint", Verdict: delivery.VerdictUnbacked}}, nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := verdictDecision(checksDecision{}, checks, c.asked, &delivery.ConductedChecks{Turn: 3, Checks: c.verdict, Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone}})
			if d.Verified {
				t.Fatalf("verified: %+v", d)
			}
			text := d.sendBack()
			if n := strings.Count(text, guidance); n != c.want {
				t.Fatalf("guidance %d times, want %d: %s", n, c.want, text)
			}
			if c.asked != nil && !strings.Contains(text, `dude recorded no call of "make test"`) {
				t.Fatalf("no hint: %s", text)
			}
		})
	}
}
