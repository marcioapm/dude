package orchestrator_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// instructionsOf is the instruction file a Run's spec writes.
func (w *world) instructionsOf(runID string) string {
	w.t.Helper()
	return specInstructions(w.t, w.specOfRun(runID))
}

// specInstructions is the instruction file spec's init script writes, run
// with /bin/sh in a directory standing in for /workspace; "" with no init.
func specInstructions(t *testing.T, spec lux.Spec) string {
	t.Helper()
	if spec.Init == nil {
		return ""
	}
	dir := t.TempDir()
	script := strings.ReplaceAll(spec.Init.Script, "/workspace/AGENTS.md", dir+"/AGENTS.md")
	script = strings.ReplaceAll(script, "/workspace/CLAUDE.md", dir+"/CLAUDE.md")
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("the init script failed: %v: %s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A Run's instruction file is written into its /workspace before its agent
// starts, from the saved prompts recorded on the Run. lux keeps the spec, so
// a move lux makes on its own writes the same file again, even after the
// organisation saved another prompt: that one is for the next Run.
func TestARunsInstructionFileIsPinnedAcrossPlacements(t *testing.T) {
	w := newWorld(t)
	w.lux.Workspaces = t.TempDir()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true, Reply: "Working."} }
	mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, role, body, created_by)
		VALUES ('pv_first', $1, 'implementer', 'FIRST ORG PROMPT', 'key_x')`, w.org)
	wi := w.task()
	w.deliver(wi)
	runID := w.working(wi)
	luxID := w.luxRunOf(runID)
	path := filepath.Join(w.lux.Workspace(luxID), "AGENTS.md")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# How you work", "# How dude works", "# Your role: implementer", "FIRST ORG PROMPT"} {
		if !strings.Contains(string(first), want) {
			t.Errorf("the instruction file lacks %q:\n%s", want, first)
		}
	}
	if claude, err := os.ReadFile(filepath.Join(w.lux.Workspace(luxID), "CLAUDE.md")); err != nil || string(claude) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md is %q: %v", claude, err)
	}

	mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, role, body, created_by, created_at)
		VALUES ('pv_second', $1, 'implementer', 'SECOND ORG PROMPT', 'key_x', now() + interval '1 second')`, w.org)
	w.lux.Migrate(luxID)
	w.until("the moved Run's init to run", func() bool {
		for _, r := range w.lux.Runs() {
			if r.ID == luxID {
				return w.lux.InitsOf(luxID) == 2 && r.Epoch == 2
			}
		}
		return false
	})
	second, err := os.ReadFile(path)
	if err != nil || string(second) != string(first) {
		t.Errorf("the moved Run's instruction file differs (%v):\n%s", err, second)
	}
}
