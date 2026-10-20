package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// The instruction file names the tools the Run's capabilities grant: the
// role's defaults name what they always did, and a Run whose capabilities
// lack one is not told of it.
func TestTheInstructionFileFollowsTheRunsCapabilities(t *testing.T) {
	for _, role := range []string{"implementer", "fixer", RoleConductor, "investigator"} {
		in := InstructionsInput{Tools: true, CLI: true}
		builtin := Instructions(role, "correctness", in)
		toolRole := role
		if role == "fixer" {
			toolRole = "implementer"
		}
		defaults := capabilities.Default(toolRole)
		in.Grants = &defaults
		if got := Instructions(role, "correctness", in); got != builtin {
			t.Errorf("%s: its default capabilities, named, change its instruction file", role)
		}
		without := defaults.Without(capabilities.Ask)
		in.Grants = &without
		got := Instructions(role, "correctness", in)
		if !strings.Contains(builtin, "**Ask a person**") || strings.Contains(got, "**Ask a person**") {
			t.Errorf("%s: ask_person named with it %v, without it %v", role,
				strings.Contains(builtin, "**Ask a person**"), strings.Contains(got, "**Ask a person**"))
		}
	}
}
