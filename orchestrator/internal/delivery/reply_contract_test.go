package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// #485: a pull request fixer is told, fresh or resumed, that it does not
// answer or resolve on the pull request itself but reports its intended
// answers; the conductor is told, whatever its saved prompts, to post and
// resolve only after the published fix is verified on the current head,
// from the publication rather than the report.
func TestReplyPublicationContract(t *testing.T) {
	fixer := []string{
		"reply_on_pull_request and resolve_thread are refused while you work",
		"put in your final report its id",
		"never as posted",
		"after it confirms your published fix is on the pull request",
	}
	in := goldenTask()
	in.Tools = true
	in.PRFeedback = []forge.ActionableFeedback{{Source: "review", Repo: "api", Path: "main.go", Author: "ana", Body: "Rename it.", ID: "line-comment-1"}}
	from, to := map[string]string{"api": "aaa111"}, map[string]string{"api": "bbb222"}
	for name, text := range map[string]string{
		"fresh fixer":   Prompt(PhaseFix, in),
		"resumed fixer": FixRoundMessage(from, to, true, in),
	} {
		for _, want := range fixer {
			if !strings.Contains(text, want) {
				t.Errorf("the %s is not told %q", name, want)
			}
		}
		if strings.Contains(text, "you may answer it on the pull request") {
			t.Errorf("the %s is still told to answer on the pull request itself", name)
		}
	}

	conductor := []string{
		"A fixer of pull request feedback cannot answer on the pull request or resolve its threads",
		"only after its fix is published and its checks and review are done",
		"the published commit, not the fixer's words",
		"a force-push that dropped it does not",
		"you post at once",
	}
	cin := goldenTask()
	cin.Tools, cin.CLI = true, true
	cin.Repositories = readOnlyRepos()
	for _, ins := range goldenInstructions {
		cin.OrgPrompt, cin.ProjectPrompt, cin.ProjectPromptMode = ins.org, ins.project, ins.projectMod
		text := Instructions(RoleConductor, "", cin.InstructionsInput())
		for _, want := range conductor {
			if !strings.Contains(text, want) {
				t.Errorf("the conductor with the %s saved prompts is not told %q", ins.name, want)
			}
		}
	}
}
