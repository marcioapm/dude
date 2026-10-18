package phases

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// setupLux is a fake lux whose Runs check out real repositories (one per
// URL, each with one commit on main) and hang in their first turn.
type setupLux struct {
	t     *testing.T
	fake  *fakelux.Server
	c     *lux.HTTPClient
	repos map[string]string // URL → repository
	hooks string            // dude's hook directory
	env   map[string]string // what a Run's spec carries of git
}

func newSetupLux(t *testing.T, names ...string) *setupLux {
	t.Helper()
	l := &setupLux{t: t, repos: map[string]string{}, hooks: t.TempDir()}
	for _, name := range names {
		dir := filepath.Join(t.TempDir(), name)
		gitIn(t, "", "init", "-q", "-b", "main", dir)
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if name == "api" {
			// What a repository's README would have a person install.
			if err := os.WriteFile(filepath.Join(dir, "pre-commit"),
				[]byte("#!/bin/sh\necho repository-hook >> \"$(git rev-parse --git-path hook-log)\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", "base")
		l.repos["https://github.com/acme/"+name+".git"] = dir
	}
	hook, err := filepath.Abs("../../../images/runtime/git-hooks/dude-hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg"} {
		if err := os.Symlink(hook, filepath.Join(l.hooks, name)); err != nil {
			t.Fatal(err)
		}
	}
	include := filepath.Join(t.TempDir(), "git-config")
	if err := os.WriteFile(include, []byte("[core]\n\thooksPath = "+l.hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The Run's command-scope settings (workloadGitConfig), its include
	// naming this copy of dude's git-config, and a second entry.
	l.env = map[string]string{}
	GitConfig(l.env, "include.path", include)
	GitConfig(l.env, "user.name", "agent")
	GitConfig(l.env, "user.email", "agent@example.com")
	l.fake = fakelux.New("", "k", func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} })
	l.fake.RepoFor = func(url string) string { return l.repos[url] }
	l.fake.Workspaces = t.TempDir()
	srv := httptest.NewServer(l.fake.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(l.fake.Close)
	l.c = lux.New(srv.URL, "k")
	return l
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (l *setupLux) repo(name string) lux.Repository {
	return lux.Repository{Name: name, URL: "https://github.com/acme/" + name + ".git", Path: RepoPath(name)}
}

// submit submits a Run of the repositories with their setups, as
// buildSpec gives a Run its setup, and waits for it to run or fail.
func (l *setupLux) submit(setups map[string]string, names ...string) string {
	l.t.Helper()
	spec := lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "opencode", Prompt: "work"},
		Env: l.env, Git: &lux.Git{}}
	for _, name := range names {
		spec.Git.Repositories = append(spec.Git.Repositories, l.repo(name))
	}
	SetupSpec(&spec, l.setups(setups, names...))
	run, err := l.c.Submit(context.Background(), spec, "")
	if err != nil {
		l.t.Fatal(err)
	}
	l.settled(run.ID)
	return run.ID
}

func (l *setupLux) setups(scripts map[string]string, names ...string) []Setup {
	var out []Setup
	for _, name := range names {
		out = append(out, Setup{Name: name, Path: RepoPath(name), Script: scripts[name]})
	}
	return out
}

func (l *setupLux) settled(run string) lux.Run {
	l.t.Helper()
	var r lux.Run
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		r, _ = l.c.Get(context.Background(), run)
		if r.State == "running" || r.State == "failed" {
			return r
		}
	}
	l.t.Fatalf("the Run is %s", r.State)
	return r
}

// started is what the Run's records say of its starts: "init" for each
// init that ran, "init-failed N" for one that failed, "workload" for each
// start of its agent, and every line init wrote.
func (l *setupLux) started(run string) []string {
	var out []string
	for _, rec := range l.fake.Records(run) {
		typ, _ := rec["type"].(string)
		data, _ := rec["data"].(map[string]any)
		switch {
		case typ == "lux.init" && data["phase"] == "done" && data["exitCode"] != 0:
			out = append(out, "init-failed "+jsonOf(data["exitCode"]))
		case typ == "lux.init" && data["phase"] == "start":
			out = append(out, "init")
		case typ == "lux.session":
			out = append(out, "workload")
		}
	}
	return out
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

// initOutput is everything every init of the Run wrote, read from its
// record stream.
func (l *setupLux) initOutput(run string) string {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var b strings.Builder
	_ = l.c.Output(ctx, run, "", 0, func(f lux.Frame) error {
		if f.Kind == "record" && (f.Channel == "stdout" || f.Channel == "stderr") {
			b.WriteString(f.Data)
		}
		return nil
	})
	return b.String()
}

// A Run's setup scripts run in their checkouts before its workload, from
// the manifest secret; a resume that adds a repository gives the manifest
// again with it, and only the new checkout's script runs, before the
// workload starts again.
func TestASetupScriptRunsBeforeTheWorkloadAndOnARepositoryAResumeAdds(t *testing.T) {
	l := newSetupLux(t, "api", "web", "docs")
	scripts := map[string]string{
		"api":  "pwd >> \"$(git rev-parse --git-path setup-log)\"\n",
		"docs": "pwd >> \"$(git rev-parse --git-path setup-log)\"\n",
	}
	run := l.submit(scripts, "api", "web")
	if got := l.started(run); !slices.Equal(got, []string{"init", "workload"}) {
		t.Fatalf("starts %v", got)
	}
	api, err := l.fake.Checkout(run, "api")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(api, ".git", "setup-log")); string(b) != api+"\n" {
		t.Errorf("api's script ran %q times in, want once in %s", b, api)
	}
	web, _ := l.fake.Checkout(run, "web")
	if _, err := os.Stat(filepath.Join(web, ".git", "dude-setup-done")); err == nil {
		t.Error("web has no script, and a marker")
	}

	ctx := context.Background()
	if err := l.c.Stop(ctx, run); err != nil {
		t.Fatal(err)
	}
	for r, _ := l.c.Get(ctx, run); r.State != "stopped"; r, _ = l.c.Get(ctx, run) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := l.c.Resume(ctx, run, lux.ResumeInput{AddRepositories: []lux.Repository{l.repo("docs")},
		Secrets: []lux.Secret{SetupSecret(l.setups(scripts, "api", "web", "docs"))}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); len(l.started(run)) < 4 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := l.started(run); !slices.Equal(got, []string{"init", "workload", "init", "workload"}) {
		t.Fatalf("starts %v", got)
	}
	docs, _ := l.fake.Checkout(run, "docs")
	if b, _ := os.ReadFile(filepath.Join(docs, ".git", "setup-log")); string(b) != docs+"\n" {
		t.Errorf("docs's script ran %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(api, ".git", "setup-log")); string(b) != api+"\n" {
		t.Errorf("api's script ran again on the resume: %q", b)
	}
	out := l.initOutput(run)
	if strings.Count(out, setupRunningPrefix+"api\n") != 1 || strings.Count(out, setupRunningPrefix+"docs\n") != 1 {
		t.Errorf("init said:\n%s", out)
	}
}

func TestAnEmptySetupManifestDoesNotCloneForInit(t *testing.T) {
	l := newSetupLux(t, "api")
	run := l.submit(map[string]string{}, "api")
	if got := l.started(run); !slices.Equal(got, []string{"workload"}) {
		t.Fatalf("starts %v", got)
	}
	entries, err := os.ReadDir(l.fake.Workspaces)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty setup manifest cloned %d workspace(s)", len(entries))
	}
}

// and the workload never starts.
func TestAFailingSetupScriptFailsTheRunBeforeItsWorkload(t *testing.T) {
	l := newSetupLux(t, "api", "web")
	run := l.submit(map[string]string{"api": "echo 'npm ERR! no lockfile' >&2\nexit 3\n", "web": "touch never\n"}, "api", "web")
	if r := l.settled(run); r.State != "failed" || r.StateReason != "exit code 3" {
		t.Errorf("the Run is %s: %s", r.State, r.StateReason)
	}
	if got := l.started(run); !slices.Equal(got, []string{"init", "init-failed 3"}) {
		t.Errorf("starts %v", got)
	}
	if out := l.initOutput(run); !strings.HasSuffix(out, "npm ERR! no lockfile\n"+setupFailedPrefix+"3 api\n") {
		t.Errorf("init said:\n%s", out)
	}
	web, _ := l.fake.Checkout(run, "web")
	if _, err := os.Stat(filepath.Join(web, "never")); err == nil {
		t.Error("the next repository's script ran")
	}
}

// Inside a script the Run's command-scope git settings are gone, so a
// hook it installs is the repository's own; a later commit in the Run's
// environment runs dude's hook, which runs it.
func TestAHookASetupScriptInstallsRunsOnALaterCommit(t *testing.T) {
	l := newSetupLux(t, "api")
	run := l.submit(map[string]string{"api": `env | grep '^GIT_CONFIG' | sort > "$(git rev-parse --git-path setup-env)"
ln -sf ../../pre-commit "$(git rev-parse --git-path hooks)/pre-commit"
`}, "api")
	if got := l.started(run); !slices.Equal(got, []string{"init", "workload"}) {
		t.Fatalf("starts %v\n%s", got, l.initOutput(run))
	}
	api, _ := l.fake.Checkout(run, "api")
	if b, _ := os.ReadFile(filepath.Join(api, ".git", "setup-env")); string(b) != "GIT_CONFIG_COUNT=0\n" {
		t.Errorf("inside the script:\n%s", b)
	}
	// The agent's commit, in the Run's environment.
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", "work")
	cmd.Dir = api
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	for k, v := range l.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else if !strings.Contains(string(out), "dude: committing without co-authors") {
		t.Errorf("dude's hook did not run first:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(api, ".git", "hook-log")); string(b) != "repository-hook\n" {
		t.Errorf("the repository's hook ran %q", b)
	}
}
