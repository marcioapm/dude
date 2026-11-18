package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// #485: a pull request fixer is told, fresh or resumed, that its replies
// and resolutions are held and posted by dude only once its published fix
// is on the pull request's head; the conductor is told, whatever its saved
// prompts, that dude posts them and wakes it, and that its own replies post
// at once.
func TestReplyPublicationContract(t *testing.T) {
	fixer := []string{
		"reply_on_pull_request with in_reply_to its id",
		"each answers held",
		"only once your published fix is on the pull request's head",
		"do not repeat it in your report or elsewhere",
		"If your fix is never published, nothing you answered is posted",
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
		if strings.Contains(text, "refused while you work") || strings.Contains(text, "The conductor answers and resolves them") {
			t.Errorf("the %s is still told its answers are refused for the conductor to relay", name)
		}
	}

	conductor := []string{
		"A pull request fixer's answers on the pull request are held until its fix is published",
		"dude posts its replies and resolves its threads itself, and wakes you",
		"only from what the published fix changed",
		"a force-push dropped the fix",
		"Your own replies post at once",
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
