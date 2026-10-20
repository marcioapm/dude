package fakeagent_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

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
