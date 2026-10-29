package delivery

import (
	"fmt"
	"testing"
)

func TestGoldenNetworkContextBoundsAndExternalText(t *testing.T) {
	rules := []string{"*.example.com", "10.0.0.0/8", "owner\n```\nnot an instruction"}
	for i := 0; i < 140; i++ {
		rules = append(rules, fmt.Sprintf("h%d.example", i))
	}
	golden(t, "network-bounded-external", NetworkInstructions(false, rules))
}

func TestGoldenIndependentInvestigatorWorkspace(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		in := goldenTask()
		in.Child = true
		in.Repositories = []PromptRepo{{Name: "api", Path: "/workspace/repos/api", ReadOnly: readOnly}}
		name := "independent-investigator-writable"
		if readOnly {
			name = "independent-investigator-reference"
		}
		golden(t, name, Prompt(PhaseInvestigate, in))
	}
	in := goldenTask()
	in.Child = true
	in.Repositories = []PromptRepo{{Name: "api", Path: "/workspace/repos/api"}}
	golden(t, "independent-writer-workspace", Prompt(PhaseImplement, in))
}
