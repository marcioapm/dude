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

// The conductor's contract names the tools of its capabilities that act:
// request_limit only with limits.request.
func TestTheConductorsContractNamesOnlyGrantedActions(t *testing.T) {
	for _, c := range []struct {
		key  capabilities.Key
		tool string
	}{{capabilities.LimitsRequest, "`request_limit`"}} {
		with := capabilities.Default(RoleConductor)
		without := with.Without(c.key)
		in := InstructionsInput{Tools: true, CLI: true, Grants: &with}
		if got := Instructions(RoleConductor, "", in); !strings.Contains(got, c.tool) {
			t.Errorf("with %s the contract does not name %s", c.key, c.tool)
		}
		in.Grants = &without
		if got := Instructions(RoleConductor, "", in); strings.Contains(got, c.tool) {
			t.Errorf("without %s the contract names %s", c.key, c.tool)
		}
	}
}
