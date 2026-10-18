package phases

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// runInit runs an init script with /bin/sh in dir, standing in for
// /workspace, as lux's shim runs it.
func runInit(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/home/agent-must-not-appear"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v: %s\n%s", err, out, script)
	}
}

// readBack is what an init wrote: AGENTS.md and CLAUDE.md.
func readBack(t *testing.T, dir string) (agents, claude string) {
	t.Helper()
	a, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(a), string(c)
}

// The init writes the instruction file byte for byte as Instructions makes
// it, whatever the text holds a shell would read, and CLAUDE.md importing
// it; nothing is left beside them.
func TestTheInstructionsInitWritesTheFileExactly(t *testing.T) {
	hostile := "# Title\n\n$HOME ${PATH} $(id) `whoami` 'single' \"double\" back\\slash \\n\\\nEOF\n'EOF'\n<<EOF\nDUDE_AGENTS_\n\ttab\n\n"
	texts := map[string]string{"hostile": hostile}
	for _, role := range delivery.PromptRoles {
		org := "ORG: `$HOME` and 'quotes' \\ and\nEOF\nfor {{task.title}}"
		in := delivery.InstructionsInput{Tools: true, CLI: true, OrgPrompt: &org, Context: "Notes $USER\nEOF",
			Vars: delivery.PromptVars{Title: "T `x` $y"}}
		texts[role] = delivery.Instructions(role, "security", in)
	}
	for name, text := range texts {
		dir := t.TempDir()
		script, err := instructionsInit(dir, text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		runInit(t, dir, script)
		agents, claude := readBack(t, dir)
		if agents != text {
			t.Errorf("%s: AGENTS.md differs:\n got %q\nwant %q", name, agents, text)
		}
		if claude != "@AGENTS.md\n" {
			t.Errorf("%s: CLAUDE.md is %q", name, claude)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 2 {
			t.Errorf("%s: init left %d files", name, len(entries))
		}
		if strings.Contains(agents, "agent-must-not-appear") {
			t.Errorf("%s: the shell expanded the text", name)
		}
		// Placed again over the files it wrote: the same bytes.
		runInit(t, dir, script)
		if again, _ := readBack(t, dir); again != text {
			t.Errorf("%s: a second placement wrote another file", name)
		}
	}
}

// The init writes into /workspace itself, each file through a temporary
// beside it; a text without a final newline is refused rather than
// written with one added.
func TestTheInstructionsInitWritesIntoTheWorkspace(t *testing.T) {
	script, err := instructionsInit(workspaceDir, "x\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"> /workspace/AGENTS.md.tmp", "mv -f /workspace/AGENTS.md.tmp /workspace/AGENTS.md",
		"> /workspace/CLAUDE.md.tmp", "mv -f /workspace/CLAUDE.md.tmp /workspace/CLAUDE.md"} {
		if !strings.Contains(script, want) {
			t.Errorf("the init does not %q:\n%s", want, script)
		}
	}
	if _, err := instructionsInit(workspaceDir, "no newline"); err == nil {
		t.Error("a text without a final newline is written")
	}
}

// A text holding the delimiter it would be written with is refused: the
// here-document would end there and the rest run as commands.
func TestATextHoldingItsOwnDelimiterIsRefused(t *testing.T) {
	text := "a\n"
	s, _ := instructionsInit(workspaceDir, text)
	_, end, _ := strings.Cut(strings.SplitN(s, "\n", 3)[1], "<<'")
	end, _, _ = strings.Cut(end, "'")
	if err := refuseDelimiter(text+end+"\n", end); err == nil {
		t.Fatal("a text with its delimiter as a line is accepted")
	}
	if err := refuseDelimiter(text+" "+end+"\n", end); err != nil {
		t.Fatalf("a line merely containing the delimiter is refused: %v", err)
	}
}

// Every agent Run gets its instruction file through its init script, and
// never as a secret: lux redacts every secret's value from what the Run
// prints, and would redact the instructions wherever the agent quotes them.
func TestEveryAgentRunsInstructionsAreItsInitNotASecret(t *testing.T) {
	type run struct{ phase, role, session string }
	runs := map[string]run{"conductor": {role: delivery.RoleConductor}, "brainstorm": {role: delivery.RoleBrainstorm, session: "ssn_1"}}
	for phase, role := range delivery.PromptRoleForPhase {
		runs[role] = run{phase: phase, role: delivery.RoleForPhase[phase]}
	}
	for _, harness := range []string{delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex} {
		for _, model := range []string{"claude-opus-5-5", "fake/scripted"} {
			for name, r := range runs {
				c, in := goldenInput(model)
				in.Phase, in.Role, in.Harness = r.phase, r.role, harness
				if r.session != "" {
					in.TaskID, in.SessionID = "", r.session
				}
				in.Instructions = delivery.Instructions(name, "correctness", delivery.InstructionsInput{Tools: true, CLI: true})
				spec, err := runSpec(c, in)
				if err != nil {
					t.Fatalf("%s/%s/%s: %v", harness, model, name, err)
				}
				for _, s := range spec.Secrets {
					if strings.Contains(s.Value, "# How you work") || strings.Contains(s.Value, "@AGENTS.md") {
						t.Errorf("%s/%s/%s: the instructions are in secret %s", harness, model, name, s.Name)
					}
				}
				if spec.Init == nil {
					t.Fatalf("%s/%s/%s: no init", harness, model, name)
				}
				// What this script writes, TestTheInstructionsInitWritesTheFileExactly
				// runs; the repositories' setup runs after it.
				write, _ := instructionsInit(workspaceDir, in.Instructions)
				want := strings.TrimSuffix(write, "\n") + " && (\nset +eu\n" + SetupInit(SetupManifestPath, workspaceDir, SetupTimeout) + "\n)\n"
				if spec.Init.Script != want {
					t.Errorf("%s/%s/%s: init is not the instructions' write, then the setup:\n%s", harness, model, name, spec.Init.Script)
				}
				if spec.Workload.Workdir != workspaceDir && !(model == "fake/scripted" && len(in.Repos) == 1) {
					t.Errorf("%s/%s/%s: starts in %s, not beside its instruction file", harness, model, name, spec.Workload.Workdir)
				}
			}
		}
	}
}

// An agent starts in /workspace, where its instruction file is, with one
// repository or several. The scripted agent, which reads no instructions,
// starts in its one repository, where its scripts name files.
func TestAnAgentStartsBesideItsInstructionFile(t *testing.T) {
	for _, harness := range []string{delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex} {
		c, in := goldenInput("claude-opus-5-5")
		in.Harness, in.Repos = harness, in.Repos[:1]
		if got := buildSpec(c, in).Workload.Workdir; got != "/workspace" {
			t.Errorf("%s with one repository starts in %s", harness, got)
		}
	}
	c, in := goldenInput("fake/scripted")
	in.Repos = in.Repos[:1]
	if got := buildSpec(c, in).Workload.Workdir; got != RepoPath("api") {
		t.Errorf("the scripted agent with one repository starts in %s", got)
	}
}

// An init script over MaxInitScript is refused before lux is asked, saying
// how large it is. The limit is on the whole init, the repositories' setup
// it runs after the instruction file included.
func TestAnInitOverTheLimitIsRefused(t *testing.T) {
	c, in := goldenInput("claude-opus-5-5")
	in.Instructions = "x\n"
	small, err := runSpec(c, in)
	if err != nil {
		t.Fatal(err)
	}
	// Everything but the text: the delimiter's length does not depend on it.
	fits := MaxInitScript - (len(small.Init.Script) - len(in.Instructions))
	in.Instructions = strings.Repeat("x", fits-1) + "\n"
	if spec, err := runSpec(c, in); err != nil || len(spec.Init.Script) != MaxInitScript {
		t.Fatalf("an init of exactly the limit: %v", err)
	}
	in.Instructions = strings.Repeat("x", fits) + "\n"
	if _, err := runSpec(c, in); err == nil || !strings.Contains(err.Error(), "over the 262144-byte limit") {
		t.Fatalf("an init one byte over: %v", err)
	}
}

// The instruction file is written first; an init the spec already has
// runs after it, only if it succeeded, and with the shell's own defaults.
func TestTheInstructionsInitRunsBeforeAnExistingInit(t *testing.T) {
	dir := t.TempDir()
	script, err := instructionsInit(dir, "rules\n")
	if err != nil {
		t.Fatal(err)
	}
	spec := lux.Spec{Init: &lux.Init{Script: "cat AGENTS.md > seen; echo $UNSET_VAR >> seen; false; echo after >> seen"}}
	addInit(&spec, script)
	runInit(t, dir, spec.Init.Script)
	if seen, _ := os.ReadFile(filepath.Join(dir, "seen")); string(seen) != "rules\n\nafter\n" {
		t.Errorf("the existing init saw %q", seen)
	}

	// The instructions' write fails: the existing init does not run.
	blocked := t.TempDir()
	if err := os.Mkdir(filepath.Join(blocked, "AGENTS.md.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	script, _ = instructionsInit(blocked, "rules\n")
	spec = lux.Spec{Init: &lux.Init{Script: "touch ran"}}
	addInit(&spec, script)
	cmd := exec.Command("/bin/sh", "-c", spec.Init.Script)
	cmd.Dir = blocked
	if err := cmd.Run(); err == nil {
		t.Error("an init that could not write the file exited 0")
	}
	if _, err := os.Stat(filepath.Join(blocked, "ran")); err == nil {
		t.Error("the existing init ran after the instructions failed")
	}

	none := lux.Spec{}
	addInit(&none, script)
	if none.Init == nil || none.Init.Script != script {
		t.Errorf("alone, the init is %+v", none.Init)
	}
}
