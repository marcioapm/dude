package fakeagent_test

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

func TestScriptedConductorAnswersChecksUnlessExplicit(t *testing.T) {
	wake := `decide_checks (runId run_1, turn 2) "make test" reported pass (evidence ran): is the pass backed (dude recorded its latest call of it: exit 0)?`
	for _, model := range []string{"fake/scripted", fakeagent.DefaultsModel} {
		script := fakeagent.ConductorScriptFor("## The task\n\nT-1\n\n## Owner's message\n\n"+wake, model)
		if strings.Count(script, "http dude POST /tools/decide_checks ") != 1 || !strings.Contains(script, `"verdict":"backed"`) {
			t.Errorf("%s did not decide checks once: %s", model, script)
		}
	}
	if calls := fakeagent.ChecksAnswer(wake, [][2]string{{"decide_checks", `{}`}}); len(calls) != 0 {
		t.Fatal("overrode an explicit decision")
	}
	if calls := fakeagent.ChecksAnswer(fakeagent.ManualChecks+"\n"+wake, nil); len(calls) != 0 {
		t.Fatal("overrode a manual script")
	}
	if calls := fakeagent.ChecksAnswer("Decision waiting: after implement", nil); len(calls) != 0 {
		t.Fatal("took an ordinary delivery decision")
	}
}

// Every scripted writer ends with a checks report the gate can read; a
// reviewer and a tester hand back none.
func TestScriptedWritersEndWithAChecksReport(t *testing.T) {
	for _, phase := range []string{delivery.PhaseImplement, delivery.PhaseFix, delivery.PhaseSimplify} {
		step := fakeagent.For(phase, "fake/scripted", "run_1", false)
		if checks, ok := delivery.ParseChecks(step.Reply + "\n\n" + step.Checks); !ok || len(checks) != 0 {
			t.Errorf("%s: report %q read as %v, %v", phase, step.Checks, checks, ok)
		}
	}
	for _, phase := range []string{delivery.PhaseReview, delivery.PhaseTest} {
		if step := fakeagent.For(phase, "fake/scripted", "run_1", false); step.Checks != "" {
			t.Errorf("%s hands back a checks report: %q", phase, step.Checks)
		}
	}
}

// The checks models claim a pass for the command they ran: honestly, or
// over a failing run until a send-back's turn runs it passing.
func TestTheChecksModelsRunTheCommandTheyReport(t *testing.T) {
	for _, tc := range []struct {
		model string
		turn  int
		exit  string
	}{
		{fakeagent.ChecksHonestModel, 0, "0"},
		{fakeagent.ChecksUnbackedModel, 0, "1"},
		{fakeagent.ChecksUnbackedModel, 2, "1"},
		{fakeagent.ChecksFixedModel, 0, "1"},
		{fakeagent.ChecksFixedModel, 1, "0"},
	} {
		commands, report, ok := fakeagent.ChecksTurn(tc.model, tc.turn)
		checks, parsed := delivery.ParseChecks(report)
		if !ok || !parsed || len(commands) != 1 || commands[0][2] != tc.exit || len(checks) != 1 ||
			checks[0].Command != commands[0][0] || checks[0].Result != delivery.CheckPass {
			t.Errorf("%s turn %d: commands %v report %v", tc.model, tc.turn, commands, checks)
		}
	}
	if _, _, ok := fakeagent.ChecksTurn("fake/scripted", 0); ok {
		t.Error("the plain scripted model plays a checks turn")
	}
}
