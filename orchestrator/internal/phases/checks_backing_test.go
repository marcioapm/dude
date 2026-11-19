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
		// #484 round 5: a recorded exit proves only the full command run.
		{"reported grep pipeline, recorded plain", "cat coverage.txt | grep -q 100%", [][]any{{"cat coverage.txt", 0}}, "none recorded"},
		{"reported pipefail grep, recorded plain", "set -o pipefail && cat coverage.txt | grep -q PASS", [][]any{{"cat coverage.txt", 0}}, "none recorded"},
		{"reported grep, recorded tail", "set -o pipefail && make test 2>&1 | grep -q PASS", [][]any{{"set -o pipefail && make test 2>&1 | tail -5", 0}}, "none recorded"},
		{"plain report, self-overwriting recorded", "bash t.sh", [][]any{{"bash t.sh > t.sh", 0}}, "none recorded"},
		{"self-overwriting report, plain recorded", "bash t.sh > t.sh", [][]any{{"bash t.sh", 0}}, "none recorded"},
		{"redirect order differs", "make test > log 2>&1", [][]any{{"make test 2>&1 > log", 0}}, "none recorded"},
		{"redirect target quoting differs", "make test > '$LOG'", [][]any{{"make test > \"$LOG\"", 0}}, "none recorded"},
		// The environment is part of the command: the same assignments
		// back it only with the same env/assignment partition; missing or other ones do
		// not (#484 review: PATH or NODE_ENV change what runs).
		{"env recorded, assignment reported", "NODE_OPTIONS=x npx jest a b", [][]any{{"env NODE_OPTIONS=x npx jest a b", 0}}, "none recorded"},
		{"assignment recorded, env reported", "env NODE_OPTIONS=x npx jest a b", [][]any{{"NODE_OPTIONS=x npx jest a b", 0}}, "none recorded"},
		{"assignment recorded, none reported", "npx jest a b", [][]any{{"NODE_OPTIONS=x npx jest a b", 0}}, "none recorded"},
		{"assignment reported, none recorded", "NODE_OPTIONS=x npx jest a b", [][]any{{"npx jest a b", 0}}, "none recorded"},
		{"other assignments on both sides", "NODE_OPTIONS=y npx jest a b", [][]any{{"env NODE_OPTIONS=x npx jest a b", 0}}, "none recorded"},
		// A report's annotation naming a directory is that directory.
		{"annotated directory", "npm run typecheck (office/x)", [][]any{{"cd office/x && npm run typecheck", 0}}, "exit 0"},
		{"annotated directory, run in another", "npm run typecheck (office/x)", [][]any{{"cd office/y && npm run typecheck", 0}}, "none recorded"},
		{"annotated directory, run at the root", "npm run typecheck (office/x)", [][]any{{"npm run typecheck", 0}}, "none recorded"},
		{"an annotation naming no directory", "python run_tests.py (run by subagent)", [][]any{{"python run_tests.py", 0}}, "none recorded"},
		// Output plumbing that leaves the exit the command's own.
		{"redirected to a log", "make test > /tmp/log 2>&1", [][]any{{"make test > /tmp/log 2>&1", 0}}, "exit 0"},
		// Appended in the portable form (#484 round 4: it was bash's &>>,
		// which dash reads as a background job; that form is the next case).
		{"appended to a log", "make test >> /tmp/log 2>&1", [][]any{{"make test >> /tmp/log 2>&1", 2}}, "exit 2"},
		{"appended to a log with bash's &>>", "make test", [][]any{{"make test &>> /tmp/log", 2}}, "none recorded"},
		{"teed with pipefail", "make test 2>&1 | tee /tmp/log", [][]any{{"set -o pipefail && make test 2>&1 | tee /tmp/log", 0}}, "exit 0"},
		{"tailed with set -euo pipefail", "cd api && make test | tail -5", [][]any{{"set -euo pipefail; cd api && make test | tail -5", 1}}, "its pipe failed (exit 1), not proof of which command failed"},
		{"its exit echoed", "make test > log 2>&1", [][]any{{"make test > log 2>&1; echo exit=$?", 0, "exit=0\n"}}, "exit 0"},
		{"its failing exit echoed", "make test > log 2>&1", [][]any{{"make test > log 2>&1; echo exit=$?", 0, "exit=2\n"}}, "exit 2"},
		{"its exit echoed, not shown", "make test", [][]any{{"make test; echo exit=$?", 0, "ok\n"}}, "unclear: its echoed exit status is not the end of its recorded output"},
		{"its exit echoed elsewhere", "make test", [][]any{{"make test; echo exit=$? >&2", 0, "exit=0\n"}}, "none recorded"},
		// Plumbing that makes the exit another command's.
		{"tailed with no pipefail", "make test", [][]any{{"make test | tail -5", 0}}, "none recorded"},
		{"tailed, reported tailed", "make test | tail -5", [][]any{{"make test | tail -5", 0}}, "none recorded"},
		{"tailed, reported tailed, run plain", "make test | tail -5", [][]any{{"make test", 0}}, "none recorded"},
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

func TestUnbackedGuidanceIsOncePerReport(t *testing.T) {
	var checks []delivery.Check
	var verdicts []delivery.ConductedCheck
	for _, command := range []string{"make test", "make lint"} {
		checks = append(checks, delivery.Check{Command: command, Result: delivery.CheckPass, Evidence: "no call"})
		verdicts = append(verdicts, delivery.ConductedCheck{Command: command, Verdict: delivery.VerdictUnbacked})
	}
	asked := &delivery.ChecksAsked{Turn: 3, Check: checks, Closest: map[int][]string{0: {}, 1: {}}}
	d := verdictDecision(checksDecision{}, checks, asked, &delivery.ConductedChecks{Turn: 3, Checks: verdicts,
		Missing: delivery.ConductedMissing{Verdict: delivery.MissingNone}})
	text := d.sendBack()
	if strings.Count(text, "as one bare command on one line") != 1 {
		t.Fatalf("guidance must appear exactly once: %s", text)
	}
	for _, command := range []string{"make test", "make lint"} {
		if !strings.Contains(text, `dude recorded no call of "`+command+`"`) {
			t.Fatalf("missing specific evidence for %s: %s", command, text)
		}
	}
}

func TestChildCallIDCollisionKeepsItsOwnCompletion(t *testing.T) {
	w := newHarnessWorld(t)
	childBashCall(w, "a", "reused", "make test", "failed", 2)
	childBashCall(w, "b", "reused", "ls", "ok", 0)
	got := w.recordedAfter(nil, "make test", delivery.CheckPass, nil)
	if len(got) != 1 || got[0] != "exit 2" {
		t.Fatalf("read %q, want exit 2 from child_a", got)
	}
}

// Even a malformed child event without childId must not complete a parent
// call: event kind, independently of childId, is part of the join identity.
func TestParentChildCompletionKindsStayIsolated(t *testing.T) {
	for _, parentCall := range []bool{true, false} {
		t.Run(map[bool]string{true: "parent call", false: "child call"}[parentCall], func(t *testing.T) {
			w := newHarnessWorld(t)
			called, completed, foreign := evToolCalled, evToolCompleted, evChildToolCompleted
			if !parentCall {
				called, completed, foreign = evChildToolCalled, evChildToolCompleted, evToolCompleted
			}
			if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
				if err := w.s.event(context.Background(), tx, w.run, called, "agent", map[string]any{"tool": "bash", "callId": "collision", "input": map[string]any{"command": "make test"}}); err != nil {
					return err
				}
				for _, e := range []struct {
					kind string
					exit int
				}{{completed, 2}, {foreign, 0}} {
					if err := w.s.event(context.Background(), tx, w.run, e.kind, "agent", map[string]any{"tool": "bash", "callId": "collision", "status": "completed", "exitCode": e.exit}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			got := w.recordedAfter(nil, "make test", delivery.CheckPass, nil)
			if len(got) != 1 || got[0] != "exit 2" {
				t.Fatalf("read %q, want exit 2 from matching event kind", got)
			}
		})
	}
}

// An unbacked pass comes back naming the recorded calls nearest it (up to
// three, with how each ended) whose exit status may be their own, so the
// agent can report one exactly; the conductor's wake names them too.
func TestAnUnbackedPassNamesTheClosestRecordedCalls(t *testing.T) {
	w := newHarnessWorld(t)
	cond := conducted(w)
	for i, call := range []string{
		"npx jest src/a.test.ts\n  --coverage=false",
		"npx jest src/a.test.ts --watch=false",
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
		`"npx jest src/a.test.ts --watch=false" (exit 0)`,
		`"npx jest src/b.test.ts" (exit 0)`,
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
	// Calls that back nothing (piped without pipefail, spanning lines)
	// are no calls to report: never named (#484 review).
	for _, far := range []string{`"git status"`, `"ls"`, `tail -5`, `\n  --coverage`, `backing nothing`} {
		if strings.Contains(text, far) || strings.Contains(line, far) {
			t.Errorf("names %s, not among the 3 nearest calls that may back it: %s\n%s", far, line, text)
		}
	}
	if !strings.Contains(text, "If one of the nearest calls is the check you meant, report it exactly as recorded") {
		t.Errorf("the send-back does not say what to report: %s", text)
	}
}
