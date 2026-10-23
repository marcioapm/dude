package phases

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// hostileLog is a job log's tail holding what a shell would read and lines
// a here-document could mistake for its end.
func hostileLog(n int) string {
	var b strings.Builder
	b.WriteString("$HOME ${PATH} $(id) `whoami` 'single' \"double\" back\\slash\nEOF\n'EOF'\n<<EOF\nDUDE_CHECKLOG_\n\ttab\n")
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "tests/test_backup.py::test_%d PASSED\n", i)
	}
	return b.String()[:n]
}

func fixFeedback(checks ...forge.FailingCheck) []forge.ActionableFeedback {
	return []forge.ActionableFeedback{{Source: "checks", Repo: "api", Body: "CI is failing.", Checks: checks}}
}

// fixSpec is a fix Run's spec as Syncer.spec builds it: the instruction
// file, then the check logs in the room it leaves.
func fixSpec(t *testing.T, instructions string, feedback []forge.ActionableFeedback) (specInput, []forge.ActionableFeedback, string) {
	t.Helper()
	c, in := goldenInput("claude-opus-5-5")
	in.Phase, in.Role, in.Instructions = delivery.PhaseFix, "implementer", instructions
	out, logs := checkLogsInit(workspaceDir, feedback, initRoom(c, in))
	in.CheckLogs = logs
	spec, err := runSpec(c, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Init.Script) > MaxInitScript {
		t.Fatalf("init is %d bytes, over %d", len(spec.Init.Script), MaxInitScript)
	}
	return in, out, spec.Init.Script
}

// instructionsLeaving is an instruction file that leaves room bytes for a
// fix Run's check logs.
func instructionsLeaving(t *testing.T, room int) string {
	t.Helper()
	c, in := goldenInput("claude-opus-5-5")
	in.Phase, in.Role, in.Instructions = delivery.PhaseFix, "implementer", "x\n"
	text := strings.Repeat("r", initRoom(c, in)-room) + "x\n"
	if in.Instructions = text; initRoom(c, in) != room {
		t.Fatalf("room %d, want %d", initRoom(c, in), room)
	}
	return text
}

// runInWorkspace runs a spec's init with dir standing in for /workspace.
func runInWorkspace(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	runInit(t, dir, strings.ReplaceAll(script, workspaceDir+"/", dir+"/"))
	return dir
}

// (e) A fix Run's init writes each failing check's job log tail under
// /workspace/.dude/checks, byte for byte whatever it holds, after the
// instruction file; the prompt names the file. A second placement writes
// the same bytes.
func TestAFixRunsInitWritesItsCheckLogs(t *testing.T) {
	unit, lint := hostileLog(20<<10), "app.py:3:1: F401 `os` imported but unused"
	feedback := fixFeedback(
		forge.FailingCheck{Name: "Python (lint, tests)", Log: "excerpt", Full: unit},
		forge.FailingCheck{Name: "lint", Log: "excerpt", Full: lint},
		forge.FailingCheck{Name: "e2e", Log: "no log", Unread: "the log has expired"},
	)
	_, out, script := fixSpec(t, "rules\n", feedback)
	dir := runInWorkspace(t, script)
	for _, want := range []struct{ name, body string }{{"api-python-lint-tests.log", unit}, {"api-lint.log", lint}} {
		got, err := os.ReadFile(filepath.Join(dir, ".dude", "checks", want.name))
		if err != nil || string(got) != strings.TrimSuffix(want.body, "\n")+"\n" {
			t.Errorf("%s: %v, %d bytes, want %d", want.name, err, len(got), len(want.body))
		}
	}
	if agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); string(agents) != "rules\n" {
		t.Errorf("AGENTS.md is %q", agents)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".dude", "checks"))
	if len(entries) != 2 {
		t.Errorf("the init left %d files in .dude/checks", len(entries))
	}
	checks := out[0].Checks
	if checks[0].Path != "/workspace/.dude/checks/api-python-lint-tests.log" || checks[0].FileKiB != 20 || checks[0].FileCut ||
		checks[1].Path != "/workspace/.dude/checks/api-lint.log" || checks[2].Path != "" || checks[2].NoFile {
		t.Errorf("checks %+v", checks)
	}
	if feedback[0].Checks[0].Path != "" {
		t.Error("the caller's feedback was changed")
	}
	prompt := delivery.Prompt(delivery.PhaseFix, delivery.PromptInput{PRFeedback: out})
	if !strings.Contains(prompt, "The last 20 KiB of this job's log is at `/workspace/.dude/checks/api-python-lint-tests.log`: read it") {
		t.Errorf("the prompt does not name the file:\n%s", prompt)
	}
	runInit(t, dir, strings.ReplaceAll(script, workspaceDir+"/", dir+"/"))
	if again, _ := os.ReadFile(filepath.Join(dir, ".dude", "checks", "api-lint.log")); string(again) != lint+"\n" {
		t.Errorf("a second placement wrote %q", again)
	}
}

// At most delivery.CheckLogFiles checks get a file, the first in feedback
// order; two checks of the same name get different files.
func TestAtMostFourCheckLogsAreWritten(t *testing.T) {
	var checks []forge.FailingCheck
	for i := range 6 {
		checks = append(checks, forge.FailingCheck{Name: "test", Log: "x", Full: fmt.Sprintf("log %d", i)})
	}
	_, out, script := fixSpec(t, "rules\n", fixFeedback(checks...))
	dir := runInWorkspace(t, script)
	for i, c := range out[0].Checks {
		if i < delivery.CheckLogFiles {
			got, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(c.Path, "/workspace/")))
			if err != nil || string(got) != fmt.Sprintf("log %d\n", i) || c.NoFile {
				t.Errorf("check %d: %+v: %q, %v", i, c, got, err)
			}
		} else if c.Path != "" || !c.NoFile {
			t.Errorf("check %d past the bound: %+v", i, c)
		}
	}
	if out[0].Checks[1].Path != "/workspace/.dude/checks/api-test-2.log" {
		t.Errorf("a second check of the same name: %s", out[0].Checks[1].Path)
	}
}

// (f) Four 64 KiB logs beside a 20 KiB instruction file are over what one
// init may carry: each log is shortened to its end, the init stays within
// checkLogInitMax (and so runs as one `sh -c` argument, as runInit runs it)
// and writes what the checks say, and the prompt says the file was
// shortened. An instruction file that leaves too little room writes fewer
// logs, and the prompt says so. The Run is never refused.
func TestCheckLogsAreCutToFitTheInit(t *testing.T) {
	var checks []forge.FailingCheck
	for i := range 4 {
		checks = append(checks, forge.FailingCheck{Name: fmt.Sprintf("job %d", i), Log: "x", Full: hostileLog(delivery.CheckLogFileMax)})
	}
	_, out, script := fixSpec(t, strings.Repeat("rule\n", 4<<10), fixFeedback(checks...))
	if len(script) > checkLogInitMax || len(script) < checkLogInitMax-(1<<10) {
		t.Errorf("init is %d bytes: want the room up to %d used", len(script), checkLogInitMax)
	}
	dir := runInWorkspace(t, script)
	for i, c := range out[0].Checks {
		got, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(c.Path, "/workspace/")))
		if err != nil || !c.FileCut || len(got) < 16<<10 || len(got) >= delivery.CheckLogFileMax ||
			!strings.HasSuffix(checks[i].Full, string(got[:len(got)-1])) || c.FileKiB != (len(got)+512)>>10 {
			t.Errorf("check %d: %+v, %d bytes: %v", i, c, len(got), err)
		}
	}
	prompt := delivery.Prompt(delivery.PhaseFix, delivery.PromptInput{PRFeedback: out})
	if !strings.Contains(prompt, "shortened to fit beside the other files dude writes into your workspace") {
		t.Errorf("the prompt does not say the log was shortened:\n%s", prompt)
	}

	// About 10 KiB of room: two logs of at least checkLogMin, two left out.
	_, out, script = fixSpec(t, instructionsLeaving(t, 10<<10), fixFeedback(checks...))
	written, cut := 0, 0
	for _, c := range out[0].Checks {
		switch {
		case c.NoFile && c.Path == "":
			cut++
		case c.Path != "" && !c.NoFile:
			written++
		default:
			t.Errorf("neither written nor said: %+v", c)
		}
	}
	if written == 0 || cut == 0 || len(script) > checkLogInitMax {
		t.Errorf("%d written, %d left out, init %d bytes", written, cut, len(script))
	}
	runInWorkspace(t, script)
	prompt = delivery.Prompt(delivery.PhaseFix, delivery.PromptInput{PRFeedback: out})
	if !strings.Contains(prompt, "This job's log did not fit beside the other files dude writes into your workspace") {
		t.Errorf("the prompt does not say a log was left out:\n%s", prompt)
	}

	// No room at all: no log, no failure.
	_, out, script = fixSpec(t, strings.Repeat("r", checkLogInitMax)+"\n", fixFeedback(checks...))
	for _, c := range out[0].Checks {
		if !c.NoFile || c.Path != "" {
			t.Errorf("a log with no room: %+v", c)
		}
	}
	if strings.Contains(script, checkLogDir) {
		t.Error("an init with no room writes a log")
	}
}

// A check's file is named for its repository and check, in a slug.
func TestACheckLogsFileNameIsASlug(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"api", "Python (lint, tests)"}: "/w/.dude/checks/api-python-lint-tests.log",
		{"", "../../etc/passwd"}:        "/w/.dude/checks/etc-passwd.log",
		{"Web_App", "☃"}:                "/w/.dude/checks/web-app-check.log",
	} {
		if got := checkLogPath("/w", in[0], in[1], map[string]bool{}); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}
