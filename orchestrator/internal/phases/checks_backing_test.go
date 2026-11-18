package phases

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A pass the agent really ran is backed on its first report, in the
// shapes agents report it (#484); one whose recorded exit status does not
// prove it passed, or that no call ran, is still not.
func TestRecordedCallsBackWhatTheAgentRan(t *testing.T) {
	for _, c := range []struct {
		name, reported string
		calls          [][]any // command, exit[, output]
		want           string
	}{
		// The environment is not the command.
		{"env recorded, assignment reported", "NODE_OPTIONS=x npx jest a b", [][]any{{"env NODE_OPTIONS=x npx jest a b", 0}}, "exit 0"},
		{"assignment recorded, env reported", "env NODE_OPTIONS=x npx jest a b", [][]any{{"NODE_OPTIONS=x npx jest a b", 0}}, "exit 0"},
		{"assignment recorded, none reported", "npx jest a b", [][]any{{"NODE_OPTIONS=x npx jest a b", 0}}, "exit 0"},
		{"assignment reported, none recorded", "NODE_OPTIONS=x npx jest a b", [][]any{{"npx jest a b", 0}}, "exit 0"},
		{"other assignments on both sides", "NODE_OPTIONS=y npx jest a b", [][]any{{"env NODE_OPTIONS=x npx jest a b", 0}}, "none recorded"},
		// A report's annotation naming a directory is that directory.
		{"annotated directory", "npm run typecheck (office/x)", [][]any{{"cd office/x && npm run typecheck", 0}}, "exit 0"},
		{"annotated directory, run in another", "npm run typecheck (office/x)", [][]any{{"cd office/y && npm run typecheck", 0}}, "none recorded"},
		{"annotated directory, run at the root", "npm run typecheck (office/x)", [][]any{{"npm run typecheck", 0}}, "none recorded"},
		{"an annotation naming no directory", "python run_tests.py (run by subagent)", [][]any{{"python run_tests.py", 0}}, "none recorded"},
		// Output plumbing that leaves the exit the command's own.
		{"redirected to a log", "make test", [][]any{{"make test > /tmp/log 2>&1", 0}}, "exit 0"},
		{"appended to a log", "make test", [][]any{{"make test &>> /tmp/log", 2}}, "exit 2"},
		{"teed with pipefail", "make test", [][]any{{"set -o pipefail && make test 2>&1 | tee /tmp/log", 0}}, "exit 0"},
		{"tailed with set -euo pipefail", "cd api && make test", [][]any{{"set -euo pipefail; cd api && make test | tail -5", 1}}, "exit 1"},
		{"its exit echoed", "make test", [][]any{{"make test > log 2>&1; echo exit=$?", 0, "exit=0\n"}}, "exit 0"},
		{"its failing exit echoed", "make test", [][]any{{"make test > log 2>&1; echo exit=$?", 0, "exit=2\n"}}, "exit 2"},
		{"its exit echoed, not shown", "make test", [][]any{{"make test; echo exit=$?", 0, "ok\n"}}, "unclear: its echoed exit status is not the end of its recorded output"},
		{"its exit echoed elsewhere", "make test", [][]any{{"make test; echo exit=$? >&2", 0, "exit=0\n"}}, "none recorded"},
		// Plumbing that makes the exit another command's.
		{"tailed with no pipefail", "make test", [][]any{{"make test | tail -5", 0}}, "none recorded"},
		{"tailed, reported tailed", "make test | tail -5", [][]any{{"make test | tail -5", 0}}, "none recorded"},
		{"tailed, reported tailed, run plain", "make test | tail -5", [][]any{{"make test", 0}}, "exit 0"},
		{"pipefail after the pipe", "make test", [][]any{{"make test | tail -5; set -o pipefail", 0}}, "none recorded"},
		{"pipefail set off", "make test", [][]any{{"set +o pipefail && make test | tail -5", 0}}, "none recorded"},
		{"its exit echoed after a pipe", "make test", [][]any{{"make test | tail; echo exit=$?", 0, "exit=0"}}, "none recorded"},
		{"then another step", "make test", [][]any{{"make test; true", 0}}, "none recorded"},
		{"ORed after a redirect", "make test", [][]any{{"make test > log || true", 0}}, "none recorded"},
		// Arguments, directory and words must be the command's.
		{"fewer arguments", "npx jest a b", [][]any{{"npx jest a", 0}}, "none recorded"},
		{"other arguments", "npx jest a b", [][]any{{"npx jest a c", 0}}, "none recorded"},
		{"more arguments", "npx jest a b", [][]any{{"npx jest a b --coverage=false", 0}}, "none recorded"},
		{"in another directory", "cd api && make test", [][]any{{"cd web && make test > log 2>&1", 0}}, "none recorded"},
		{"prose", "eslint on the touched files", [][]any{{"npx eslint src/a.ts src/b.ts", 0}}, "none recorded"},
		{"a placeholder", "flake8 <python files changed under tests/>", [][]any{{"flake8 tests/a.py", 0}}, "none recorded"},
		{"quoted operators are words", "grep -c 'a | b' f", [][]any{{"grep -c 'a | b' f", 0}}, "exit 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			got := w.recordedAfter(c.calls, c.reported, delivery.CheckPass, nil)
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("read %q, want %q", got, c.want)
			}
		})
	}
}

// childBashCall records a shell call a subagent (a Task or spawned agent
// under the Run's call parent) made, as the translators do.
func childBashCall(w *harnessWorld, parent, id, command, output string, exit int) {
	w.t.Helper()
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
		if err := w.s.event(context.Background(), tx, w.run, evChildToolCalled, "agent", map[string]any{"tool": "bash", "callId": id,
			"childId": "child_" + parent, "parentCallId": parent, "input": map[string]any{"command": command}}); err != nil {
			return err
		}
		return w.s.event(context.Background(), tx, w.run, evChildToolCompleted, "agent", map[string]any{"tool": "bash", "callId": id,
			"childId": "child_" + parent, "parentCallId": parent, "status": "completed", "exitCode": exit, "output": map[string]any{"head": output}})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// A subagent's recorded shell call backs a pass as the Run's own does,
// under the same rules.
func TestASubagentsRecordedCallBacksAPass(t *testing.T) {
	for _, c := range []struct {
		name, command string
		exit          int
		want          string
	}{
		{"run by the subagent", "python run_tests.py -k chart", 0, "exit 0"},
		{"failing in the subagent", "python run_tests.py -k chart", 1, "exit 1"},
		{"tailed in the subagent", "python run_tests.py -k chart | tail -3", 0, "none recorded"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			childBashCall(w, "task_1", "sub_1", c.command, "ok", c.exit)
			got := w.recordedAfter(nil, "python run_tests.py -k chart", delivery.CheckPass, nil)
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("read %q, want %q", got, c.want)
			}
		})
	}
}

// An unbacked pass comes back naming the recorded calls nearest it (up to
// three, one-lined, with how each ended), so the agent can report one
// exactly; the conductor's wake names them too.
func TestAnUnbackedPassNamesTheClosestRecordedCalls(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	for i, call := range []string{
		"npx jest src/a.test.ts\n  --coverage=false",
		"npx jest src/b.test.ts",
		"git status",
		"npx jest src/a.test.ts | tail -5",
		"env NODE_OPTIONS=--max-old-space-size=6000 npx jest src/a.test.ts --coverage=false",
		"ls",
	} {
		bashCall(w, "call_"+string(rune('a'+i)), call, "ok", 0)
	}
	checksReply(w, "```yaml\nchecks:\n  - command: 'npx jest src/a.test.ts'\n    result: pass\n    evidence: 'Tests: 4 passed'\n```")
	endTurn(w)
	if pass, _ := w.gate(); pass {
		t.Fatal("passed before the conductor decided")
	}
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE key = $1`, delivery.ChecksKey(w.run.ID, w.turn())).Scan(&line); err != nil {
		t.Fatal(err)
	}
	near := []string{
		`"env NODE_OPTIONS=--max-old-space-size=6000 npx jest src/a.test.ts --coverage=false" (exit 0)`,
		`"npx jest src/a.test.ts | tail -5" (exit 0, backing nothing: its exit status is the last command's in its pipe (no set -o pipefail))`,
		`"npx jest src/a.test.ts --coverage=false" (exit 0)`,
	}
	for _, n := range near {
		if !strings.Contains(line, n) {
			t.Errorf("the wake does not name %s: %s", n, line)
		}
	}
	if _, err := w.decide(cond, delivery.DecideChecks{RunID: w.run.ID, Turn: w.turn(),
		Checks:  []delivery.ConductedCheck{{Command: "npx jest src/a.test.ts", Verdict: delivery.VerdictUnbacked, Reason: "no call of it"}},
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone, Reason: "fine"}}); err != nil {
		t.Fatal(err)
	}
	if pass, acted := w.gate(); pass || !acted {
		t.Fatalf("pass %v acted %v", pass, acted)
	}
	var text string
	if err := w.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id = $1 AND text LIKE 'Your checks report was not verified%' ORDER BY created_at DESC LIMIT 1`,
		w.run.ID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, n := range near {
		if !strings.Contains(text, n) {
			t.Errorf("the send-back does not name %s: %s", n, text)
		}
	}
	for _, far := range []string{`"git status"`, `"ls"`, `"npx jest src/b.test.ts"`} {
		if strings.Contains(text, far) {
			t.Errorf("the send-back names %s, not among the 3 nearest: %s", far, text)
		}
	}
	if !strings.Contains(text, "Report one of them exactly as recorded") {
		t.Errorf("the send-back does not say what to report: %s", text)
	}
}
