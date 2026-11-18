package phases

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// unprovenPasses are recorded calls that exit 0 although the reported
// command, run as reported, fails or never ran (#484 review): each must
// back nothing.
var unprovenPasses = []struct{ name, reported, recorded string }{
	// set forms that end with pipefail off, set positional arguments, or
	// stop the shell executing anything.
	{"pipefail set then unset", "false", "set -o pipefail +o pipefail; false | true"},
	{"pipefail as positional arguments", "false", "set -- -o pipefail; false | true"},
	{"no-exec", "false", "set -no pipefail; false"},
	{"pipefail unset in a later step", "set +o pipefail && false", "set -o pipefail && set +o pipefail && false | true"},
	// A comment hides everything after it.
	{"a comment hiding the check", "false", "set -o pipefail # ignored; false | true"},
	{"a comment after the command", "false", "true # false"},
	// The environment is part of what ran.
	{"another PATH", "npx jest", "PATH=/tmp/fake npx jest"},
	{"an assignment the call lacks", "NODE_ENV=production npx jest", "npx jest"},
	{"an env assignment the report lacks", "npx jest", "env NODE_ENV=test npx jest"},
	{"assignments in another order", "A=1 A=2 npx jest", "A=2 A=1 npx jest"},
	{"an env expansion read before its assignment", "A=1 B=$A npx jest", "env A=1 B=$A npx jest"},
	// Whitespace the shell reads.
	{"a newline between commands", "false true", "false\ntrue"},
	{"a carriage return", "false true", "false\r\ntrue"},
	{"quoted whitespace", "test 'a  b' = 'a b'", "test 'a b' = 'a b'"},
	{"quoted whitespace reported collapsed", "test 'a b' = 'a  b'", "test 'a b' = 'a b'"},
	{"a tab inside quotes", "test 'a\tb' = 'a b'", "test 'a b' = 'a b'"},
	// A background job's exit is not waited for.
	{"the command in the background", "make test", "make test &"},
	{"a failing background job", "false", "false & true"},
}

// The matcher backs none of them.
func TestUnprovenPassesMatchNoReportedCommand(t *testing.T) {
	for _, c := range unprovenPasses {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := sameCommand(c.reported, c.recorded); ok {
				t.Fatalf("%q backs %q", c.recorded, c.reported)
			}
		})
	}
}

// Nor does dude's reading of the ledger: the recorded exit 0 is not the
// reported command's.
func TestUnprovenPassesAreNotRecordedAsTheCommandsExit(t *testing.T) {
	for _, c := range unprovenPasses {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			got := w.recordedAfter([][]any{{c.recorded, 0}}, c.reported, delivery.CheckPass, nil)
			if len(got) != 1 || got[0] != "none recorded" {
				t.Fatalf("read %q for %q after %q, want none recorded", got, c.reported, c.recorded)
			}
		})
	}
}

// The conductor is asked about them as unbacked: its wake names no call of
// the reported command, and offers none of them to report instead.
func TestTheWakeNamesNoUnprovenCallAsTheChecks(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	for i, c := range []string{
		"set -o pipefail +o pipefail; false | true",
		"set -- -o pipefail; false | true",
		"set -no pipefail; false",
		"set -o pipefail # ignored; false | true",
		"false\ntrue",
		"false &",
	} {
		bashCall(w, fmt.Sprintf("call_%d", i), c, "", 0)
	}
	checksReply(w, "```yaml\nchecks:\n  - command: 'false'\n    result: pass\n    evidence: 'exit 0'\n```")
	endTurn(w)
	if pass, _ := w.gate(); pass {
		t.Fatal("passed before the conductor decided")
	}
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE key = $1`, delivery.ChecksKey(w.run.ID, w.turn())).Scan(&line); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "(dude recorded its latest call of it: none recorded)") || strings.Contains(line, "exit 0)") {
		t.Fatalf("the wake reads a call of false: %s", line)
	}
	if strings.Contains(line, "nearest recorded calls") {
		t.Fatalf("the wake offers a call backing nothing: %s", line)
	}
}

// Equivalent forms still back a pass: the same assignments in env or
// assignment form, plain output redirects, a named directory, and a pipe
// after an exact set of pipefail.
func TestEquivalentFormsStillBack(t *testing.T) {
	for _, c := range []struct{ reported, recorded string }{
		{"NODE_ENV=test npx jest", "env NODE_ENV=test npx jest"},
		{"env A=1 B=2 npx jest", "A=1 B=2 npx jest"},
		{"make test", "make test > /tmp/log 2>&1"},
		{"make test (api)", "cd api && make test"},
		{"make test", "set -o pipefail && make test 2>&1 | tee log"},
		{"make test", "set -euo pipefail; make test | tail -5"},
		{"make test", "  make\ttest  "},
		{"grep -c 'a # b' f", "grep -c 'a # b' f"},
		{"test 'a  b' = 'a  b'", "test 'a  b' = 'a  b'"},
	} {
		if _, ok := sameCommand(c.reported, c.recorded); !ok {
			t.Errorf("%q does not back %q", c.recorded, c.reported)
		}
	}
}

// A pipe's non-zero exit under pipefail is some stage's: dude does not
// call it the command's own.
func TestAPipesFailureIsNotTheCommandsExit(t *testing.T) {
	w := newHarnessWorld(t)
	got := w.recordedAfter([][]any{{"set -o pipefail && make test | tail -5", 1}}, "make test", delivery.CheckPass, nil)
	if len(got) != 1 || got[0] == "exit 1" || !strings.Contains(got[0], "not proof") {
		t.Fatalf("read %q", got)
	}
}

// An echoed status is read only from the output's last line: an earlier
// line's is not the call's.
func TestAnEchoedStatusOnAnEarlierLineIsNotTheExit(t *testing.T) {
	w := newHarnessWorld(t)
	got := w.recordedAfter([][]any{{"make test; echo exit=$?", 0, "exit=0\nmake: *** [test] Error 2\n"}}, "make test", delivery.CheckPass, nil)
	if len(got) != 1 || got[0] == "exit 0" || !strings.HasPrefix(got[0], "unclear") {
		t.Fatalf("read %q", got)
	}
}

// otherRunEvents records a finished shell call of run r directly in the
// ledger: the Run's own (childID "") or a subagent's.
func otherRunEvents(w *harnessWorld, r phaseRun, childID, id, command string, exit int) {
	w.t.Helper()
	called, completed := evToolCalled, evToolCompleted
	in := map[string]any{"tool": "bash", "callId": id, "input": map[string]any{"command": command}}
	out := map[string]any{"tool": "bash", "callId": id, "status": "completed", "exitCode": exit, "output": map[string]any{"head": "ok"}}
	if childID != "" {
		called, completed = evChildToolCalled, evChildToolCompleted
		in["childId"], out["childId"] = childID, childID
	}
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
		if err := w.s.event(context.Background(), tx, r, called, "agent", in); err != nil {
			return err
		}
		return w.s.event(context.Background(), tx, r, completed, "agent", out)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// Another Run's calls, its own or its subagents', neither back this Run's
// pass nor are offered as its nearest calls.
func TestAnotherRunsCallsNeitherBackNorHint(t *testing.T) {
	w := newHarnessWorld(t)
	other := w.otherRun(t, "run_other", w.run.TaskID, 1)
	otherRunEvents(w, other, "child_x", "sub_1", "python run_tests.py -k chart", 0)
	otherRunEvents(w, other, "", "own_1", "python run_tests.py -k chart --fast", 0)
	bashCall(w, "call_1", "ls", "", 0)
	var got []string
	var closest map[int][]string
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		got, closest, err = recordedCalls(context.Background(), tx, w.run.ID,
			[]delivery.Check{{Command: "python run_tests.py -k chart", Result: delivery.CheckPass}}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "none recorded" {
		t.Fatalf("read %q from another Run's call", got)
	}
	if c, ok := closest[0]; !ok || len(c) != 0 {
		t.Fatalf("closest %q: another Run's calls are not this Run's", c)
	}
}

// A long nearest call is clipped, on a character boundary.
func TestALongClosestCallIsClipped(t *testing.T) {
	w := newHarnessWorld(t)
	long := "npx jest src/a.test.ts --name " + strings.Repeat("é", 200)
	bashCall(w, "call_1", long, "", 0)
	var closest map[int][]string
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		_, closest, err = recordedCalls(context.Background(), tx, w.run.ID,
			[]delivery.Check{{Command: "npx jest src/a.test.ts", Result: delivery.CheckPass}}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(closest[0]) != 1 {
		t.Fatalf("closest %q", closest[0])
	}
	c := closest[0][0]
	if !utf8.ValidString(c) || !strings.Contains(c, "…") || len(c) > closestCallMax+len(`"…" (exit 0)`) {
		t.Fatalf("not clipped to %d bytes: %d %q", closestCallMax, len(c), c)
	}
}

// Many unbacked passes with long nearest calls still leave every check's
// question in the conductor's wake: the hints share a bound.
func TestLongHintsLeaveEveryQuestionInTheWake(t *testing.T) {
	w := newHarnessWorld(t)
	conducted(w)
	report := "```yaml\nchecks:\n"
	for i := range 6 {
		for j := range 3 {
			bashCall(w, fmt.Sprintf("call_%d_%d", i, j), fmt.Sprintf("npx jest suite%d.test.ts --opt%d %s", i, j, strings.Repeat("x", 150)), "", 0)
		}
		report += fmt.Sprintf("  - command: 'npx jest suite%d.test.ts'\n    result: pass\n    evidence: 'ok'\n", i)
	}
	checksReply(w, report+"```")
	endTurn(w)
	w.gate()
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE key = $1`, delivery.ChecksKey(w.run.ID, w.turn())).Scan(&line); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		if !strings.Contains(line, fmt.Sprintf(`"npx jest suite%d.test.ts" reported pass`, i)) {
			t.Errorf("the wake lost check %d's question: %s", i+1, line)
		}
	}
	if !strings.Contains(line, "Then missing:") {
		t.Errorf("the wake lost its missing question: %s", line)
	}
}
