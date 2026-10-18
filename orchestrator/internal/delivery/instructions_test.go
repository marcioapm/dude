package delivery

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func TestGoldenInstructions(t *testing.T) {
	for _, role := range PromptRoles {
		for _, layer := range goldenInstructions {
			in := goldenTask()
			in.Tools, in.CLI, in.BlockingSeverities = true, true, []string{"blocking", "high"}
			in.Repositories, in.Anywhere = readOnlyRepos(), role == RoleBrainstorm
			in.OrgPrompt, in.ProjectPrompt, in.ProjectPromptMode = layer.org, layer.project, layer.projectMod
			golden(t, fmt.Sprintf("instructions-%s-%s", role, layer.name), Instructions(role, "correctness", in.InstructionsInput()))
		}

	}
}

func TestGoldenReviewerInstructions(t *testing.T) {
	for _, category := range reviewerOrder {
		in := goldenTask()
		in.Tools, in.CLI = true, true
		golden(t, "instructions-aspect-"+category, Instructions("reviewer", category, in.InstructionsInput()))
	}
}

func TestInstructionsKeepFixedSectionsUnderSavedPrompts(t *testing.T) {
	org := "Organisation's stance: {{task.title}}."
	for _, role := range PromptRoles {
		in := InstructionsInput{Tools: true, CLI: true, Vars: PromptVars{Title: "A task"}}
		builtin := Instructions(role, "correctness", in)
		in.OrgPrompt = &org
		custom := Instructions(role, "correctness", in)
		in.ProjectPrompt, in.ProjectPromptMode = "Project's stance: {{task.title}}.", "replace"
		replaced := Instructions(role, "correctness", in)
		parts := roleParts[role]
		for mode, text := range map[string]string{"builtin": builtin, "org": custom, "project replace": replaced} {
			for _, want := range []string{render(charterTmpl, in.data(role)), render(dudeTmpl, in.data(role)), render(parts[0], in.data(role)), render(parts[2], in.data(role))} {
				if want != "" && strings.Count(text, want) != 1 {
					t.Errorf("%s/%s: fixed section occurs %d times", role, mode, strings.Count(text, want))
				}
			}
		}
		if strings.Contains(custom, BuiltinPrompt(role)) || strings.Contains(replaced, BuiltinPrompt(role)) {
			t.Errorf("%s: replace did not replace the stance", role)
		}
		if !strings.Contains(custom, "Organisation's stance: A task.") || strings.Contains(replaced, "Organisation's stance") || !strings.Contains(replaced, "Project's stance: A task.") {
			t.Errorf("%s: saved prompt layering or variables", role)
		}
		in.ProjectPromptMode = "add"
		added := Instructions(role, "correctness", in)
		if !strings.Contains(added, "Organisation's stance: A task.") || !strings.Contains(added, "Project's stance: A task.") {
			t.Errorf("%s: project add lost a saved prompt", role)
		}
		in.OrgPrompt = nil
		added = Instructions(role, "correctness", in)
		if strings.Count(added, BuiltinPrompt(role)) != 1 {
			t.Errorf("%s: project add removed the built-in stance", role)
		}
	}
}

func TestRoleSplitRejectsMissingAndDuplicateStances(t *testing.T) {
	for _, text := range []string{"# Role\n\nNo stance.\n", "# Role\n\n## How you work as role\nA\n\n## How you work as other\nB\n"} {
		_, err := parseAgentTemplates(fstest.MapFS{"agents/instructions/roles/role.md": {Data: []byte(text)}})
		if err == nil || !strings.Contains(err.Error(), "agents/instructions/roles/role.md") {
			t.Errorf("malformed role loads: %v", err)
		}
	}
}

func TestInstructionIncludesAreCheckedAtStartup(t *testing.T) {
	for name, text := range map[string]string{
		"instructions/charter":    "{{template \"gone\"}}",
		"instructions/roles/role": "# Role\n\n## How you work as role\n{{template \"gone\"}}\n\n## Mechanics\nA",
	} {
		_, err := parseAgentTemplates(fstest.MapFS{"agents/" + name + ".md": {Data: []byte(text)}})
		if err == nil || !strings.Contains(err.Error(), "gone") {
			t.Errorf("%s: missing include loads: %v", name, err)
		}
	}
}
