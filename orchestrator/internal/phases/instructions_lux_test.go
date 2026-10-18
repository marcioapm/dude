package phases

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// instructedSpec is the spec runSpec makes for a Run of the setup lux's
// repositories with an instruction file and their setup scripts, with the
// lux's git settings and repositories as the fake checks them out.
func (l *setupLux) instructedSpec(scripts map[string]string, names ...string) lux.Spec {
	l.t.Helper()
	c, in := goldenInput("claude-opus-5-5")
	in.Repos = nil
	for _, name := range names {
		in.Repos = append(in.Repos, specRepo{Name: name, URL: "https://github.com/acme/" + name + ".git", Ref: "main", Setup: scripts[name]})
	}
	in.Instructions = "# How you work\n\nRules.\n"
	spec, err := runSpec(c, in)
	if err != nil {
		l.t.Fatal(err)
	}
	spec.Env, spec.Git = l.env, &lux.Git{}
	for _, name := range names {
		spec.Git.Repositories = append(spec.Git.Repositories, l.repo(name))
	}
	spec.Network, spec.Workload.BeforeStop = nil, nil
	return spec
}

// A Run with an instruction file and a setup script: its one init writes
// /workspace/AGENTS.md, then runs the setup in the checkout, which reads
// the file already written; a failing setup still fails the Run
// init-failed, read as that repository's setup failure.
func TestARunsInitWritesItsInstructionsThenRunsItsSetup(t *testing.T) {
	l := newSetupLux(t, "api")
	spec := l.instructedSpec(map[string]string{"api": "cat ../../AGENTS.md > \"$(git rev-parse --git-path setup-saw)\"\n"}, "api")
	run, err := l.c.Submit(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := l.settled(run.ID); r.State != "running" {
		t.Fatalf("the Run is %s: %s\n%s", r.State, r.StateReason, l.initOutput(run.ID))
	}
	if got := l.started(run.ID); !slices.Equal(got, []string{"init", "workload"}) {
		t.Fatalf("starts %v", got)
	}
	api, err := l.fake.Checkout(run.ID, "api")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Dir(filepath.Dir(api))
	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil || string(agents) != "# How you work\n\nRules.\n" {
		t.Fatalf("/workspace/AGENTS.md is %q: %v", agents, err)
	}
	if saw, _ := os.ReadFile(filepath.Join(api, ".git", "setup-saw")); string(saw) != string(agents) {
		t.Errorf("the setup script ran %q, want it to run after the instruction file was written", saw)
	}
	if _, err := os.Stat(filepath.Join(api, ".git", "dude-setup-done")); err != nil {
		t.Errorf("the setup did not finish: %v", err)
	}

	l = newSetupLux(t, "api")
	spec = l.instructedSpec(map[string]string{"api": "echo 'npm ERR! no lockfile' >&2\nexit 3\n"}, "api")
	run, err = l.c.Submit(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := l.settled(run.ID); r.State != "failed" || r.StateReason != "exit code 3" {
		t.Fatalf("the Run is %s: %s", r.State, r.StateReason)
	}
	var o InitOutput
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = l.c.Output(ctx, run.ID, "", 0, func(f lux.Frame) error {
		if f.Kind == "record" {
			o.Record(f)
		}
		return nil
	})
	if o.Failed == nil || o.Failed.Repository != "api" || o.Failed.ExitCode != 3 {
		t.Fatalf("init's failure read as %+v", o.Failed)
	}
	if why := o.Failed.Reason(); why != "The setup script of api failed with exit code 3: npm ERR! no lockfile" {
		t.Errorf("the failure says %q", why)
	}
}
