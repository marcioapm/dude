package delivery

import (
	"fmt"
	"maps"
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

func TestGoldenChildInstructions(t *testing.T) {
	for _, role := range []string{"investigator", "reviewer", "qa_browser", "implementer", "fixer", "simplifier"} {
		golden(t, "instructions-child-"+role, Instructions(role, "correctness", InstructionsInput{Child: true, Tools: true, CLI: true}))
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

func TestChildContractFollowsPublishes(t *testing.T) {
	in := InstructionsInput{Child: true, Tools: true, CLI: true}
	contractOf := func(role string) string {
		text := Instructions(role, "correctness", in)
		writer, report := render(childWriterTmpl, in.data(role)), render(childToolsTmpl, in.data(role))
		switch {
		case strings.HasSuffix(text, writer+"\n") && !strings.Contains(text, report):
			return "writer"
		case strings.HasSuffix(text, report+"\n") && !strings.Contains(text, writer):
			return "report"
		}
		return "neither"
	}
	for _, role := range []string{"investigator", "reviewer", "qa_browser", "implementer", "fixer", "simplifier"} {
		phase := PhaseForPromptRole(role)
		want := map[bool]string{true: "writer", false: "report"}
		if got := contractOf(role); got != want[Publishes[phase]] {
			t.Errorf("%s child: %s contract, publishes %v", role, got, Publishes[phase])
		}
		// The contract is the phase's publishing, not the role's name.
		saved := Publishes
		Publishes = maps.Clone(saved)
		Publishes[phase] = !saved[phase]
		got := contractOf(role)
		Publishes = saved
		if got != want[!Publishes[phase]] {
			t.Errorf("%s child with publishing flipped: %s contract", role, got)
		}
	}
}

func TestInstructionsAspectsUseSharedSeverity(t *testing.T) {
	const definition = "Severity: `blocking` is wrong behaviour on a supported path, data loss, a security boundary crossed, or an acceptance criterion not met; `high` will cause a real defect or outage under realistic use; `medium` is a real but bounded problem worth fixing; `low` is minor; `note` is an observation, not a request."
	const reference = "Use the shared reviewer severity contract above. This aspect does not redefine severity; in particular, an unmet acceptance criterion or wrong behaviour on a supported path is `blocking`, not `high`."
	for _, aspect := range []string{"api", "correctness", "database", "frontend", "performance", "security", "tests"} {
		for mode, text := range underEveryMode("reviewer", aspect, InstructionsInput{Tools: true}) {
			t.Run(aspect+"/"+mode, func(t *testing.T) {
				for _, want := range []string{definition, reference, "# Your aspect: " + aspect} {
					if n := strings.Count(text, want); n != 1 {
						t.Errorf("%q occurs %d times, want 1", want, n)
					}
				}
				for _, local := range []string{"**High:**", "**Medium:**", "**Low:**"} {
					if strings.Contains(text, local) {
						t.Errorf("aspect redefines severity with %q", local)
					}
				}
			})
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
