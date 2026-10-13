package agenttools_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The trailer's address: commit email, else the GitHub noreply address
// (login and id both), else the sign-in email; nobody without one, nobody
// twice, never the committing bot.
func TestCoauthorsAreChosenInOrderOnceEachAndNeverTheBot(t *testing.T) {
	bot := "99+dude-app[bot]@users.noreply.github.com"
	got := agenttools.Coauthors([]agenttools.CoauthorPerson{
		{Name: "Ana", CommitEmail: "ana@commits.example", GitHubLogin: "ana", GitHubUserID: 1, Email: "ana@corp.example"},
		{Name: "Bo", GitHubLogin: "bo-dev", GitHubUserID: 2, Email: "bo@corp.example"},
		{Name: "Cy", GitHubLogin: "cy", Email: "cy@corp.example"},
		{Name: "Di"},
		{Name: "Ana again", Email: "ANA@commits.example"},
		{Name: "The bot", CommitEmail: "99+DUDE-APP[bot]@users.noreply.github.com"},
		{Name: "Eve <x>\n", Email: "eve@corp.example"},
	}, bot)
	want := []string{
		"Co-authored-by: Ana <ana@commits.example>",
		"Co-authored-by: Bo <2+bo-dev@users.noreply.github.com>",
		"Co-authored-by: Cy <cy@corp.example>",
		"Co-authored-by: Eve x <eve@corp.example>",
	}
	var lines []string
	for _, c := range got {
		lines = append(lines, c.Trailer())
	}
	if !slices.Equal(lines, want) {
		t.Errorf("trailers\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// coauthorTask puts people on the fixture's task, in order.
func (f *fixture) coauthorTask(t *testing.T) {
	t.Helper()
	for i, p := range [][3]string{{"per_a_" + f.org, "Ana Lima", "ana@corp.example"}, {"per_b_" + f.org, "Bo Chen", "bo@corp.example"}} {
		mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name, email) VALUES ($1, $2, $3, $4)`, p[0], f.org, p[1], p[2])
		mustExec(t, f.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ($1, $2, $3, $4)`,
			f.item, p[0], f.org, i)
	}
	mustExec(t, f.owner, `UPDATE people SET github_login = 'bo-dev', github_user_id = 2 WHERE id = $1`, "per_b_"+f.org)
}

// A Run reads its own task's people, as they are when it asks: one of
// another task never sees them.
func TestARunReadsOnlyItsOwnTasksCoauthors(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	mine := f.run(t, "run_co_mine", "implementer", "running")
	other := "wi_other_" + f.org
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 9, 'Other')`,
		other, f.org, f.project)
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ('run_co_other', $1, $2, $3, 1, 'running', 'implement', 'implementer', $4)`, f.org, f.project, other, hash)

	get := func(token string) (int, string) {
		req, _ := http.NewRequest("GET", f.url+"/git/coauthors", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if status, body := get(mine); status != 200 ||
		body != "Co-authored-by: Ana Lima <ana@corp.example>\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>\n" {
		t.Errorf("its own task: %d %q", status, body)
	}
	if status, body := get(token); status != 200 || body != "" {
		t.Errorf("another task's Run: %d %q, want no one", status, body)
	}
	if status, _ := get(""); status != http.StatusUnauthorized {
		t.Errorf("no token: %d", status)
	}
	// Read now: the owner changed hands, and the next commit says so.
	mustExec(t, f.owner, `DELETE FROM task_people WHERE person_id = $1`, "per_a_"+f.org)
	if _, body := get(mine); body != "Co-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>\n" {
		t.Errorf("after the owner left: %q", body)
	}
}

// appForges is an organisation connected to GitHub as an App, served by a
// fake GitHub whose GET /app waits appDelay first.
type appForges struct{ gh *forge.GitHub }

func (a appForges) For(context.Context, string) (*forge.GitHub, error) { return a.gh, nil }

func newAppForges(t *testing.T, appDelay time.Duration) appForges {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app := fakegithub.NewApp(9101, "dude-co", &key.PublicKey)
	app.Set(func(a *fakegithub.App) { a.Installations = map[string]int64{"acme": 11} })
	h := app.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app" {
			select {
			case <-time.After(appDelay):
			case <-r.Context().Done():
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return appForges{forge.NewGitHub(forge.Credential{Auth: "github_app", Secret: string(block), AppID: "9101", APIBaseURL: srv.URL})}
}

// getCoauthors is GET /git/coauthors as a Run with token.
func (f *fixture) getCoauthors(t *testing.T, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", f.url+"/git/coauthors", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// With a GitHub App, the endpoint reads the App's bot and leaves out a
// person whose address is the bot's.
func TestARunsCoauthorsLeaveOutTheAppsBot(t *testing.T) {
	f := setupWith(t, &agenttools.Server{Forges: newAppForges(t, 0)})
	f.coauthorTask(t)
	mustExec(t, f.owner, `UPDATE people SET commit_email = '4242+dude-co[bot]@users.noreply.github.com' WHERE id = $1`, "per_a_"+f.org)
	token := f.run(t, "run_co_app", "implementer", "running")
	if status, body := f.getCoauthors(t, token); status != 200 || body != "Co-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>\n" {
		t.Errorf("with the App's bot on the task: %d %q, want Bo alone", status, body)
	}
}

// GitHub slow to say who the App's bot is: the people's trailers come
// within the endpoint's bound, the bot left in only if a person is it.
func TestASlowGitHubDoesNotHoldACommitsCoauthors(t *testing.T) {
	f := setupWith(t, &agenttools.Server{Forges: newAppForges(t, 10*time.Second)})
	f.coauthorTask(t)
	token := f.run(t, "run_co_slow", "implementer", "running")
	start := time.Now()
	status, body := f.getCoauthors(t, token)
	if took := time.Since(start); took >= 3*time.Second {
		t.Errorf("the endpoint took %s, want under 3s", took)
	}
	if status != 200 || body != "Co-authored-by: Ana Lima <ana@corp.example>\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>\n" {
		t.Errorf("with GitHub slow: %d %q, want both people", status, body)
	}
}

// Another caller with no bound (a submit's spec build) already looking up
// the bot on a slow GitHub holds it: a commit's co-authors still come
// within the endpoint's bound.
func TestACommitsCoauthorsDoNotWaitBehindAnotherBotLookup(t *testing.T) {
	forges := newAppForges(t, 6*time.Second)
	f := setupWith(t, &agenttools.Server{Forges: forges})
	f.coauthorTask(t)
	token := f.run(t, "run_co_behind", "implementer", "running")
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = forges.gh.Bot(context.Background())
	}()
	t.Cleanup(func() { <-first })
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	status, body := f.getCoauthors(t, token)
	if took := time.Since(start); took >= 3*time.Second {
		t.Errorf("the endpoint took %s behind another lookup, want under 3s", took)
	}
	if status != 200 || body != "Co-authored-by: Ana Lima <ana@corp.example>\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>\n" {
		t.Errorf("behind another lookup: %d %q, want both people", status, body)
	}
}

// hookRepo is a repository whose hooks are dude's image's (its git-config,
// setting core.hooksPath at a copy of its hook directory, included in
// command scope as a Run includes it), committing with the dude CLI on
// PATH reaching the tools as lux's socket would, or reaching nothing. HOME
// and the global config are its own; the system config is none, or, under
// asImage, the runtime image's (its core.hooksPath naming dude's directory
// too).
type hookRepo struct {
	t       *testing.T
	dir     string
	env     []string
	hooks   string // dude's hook directory
	include string // the image's git-config
	home    string
}

// asImage gives a hookRepo the runtime image's system config.
const asImage = true

func newHookRepo(t *testing.T, service string, bin string, image ...bool) *hookRepo {
	t.Helper()
	hooks := t.TempDir()
	script, err := filepath.Abs("../../../images/runtime/git-hooks/dude-hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit"} {
		if err := os.Symlink(script, filepath.Join(hooks, name)); err != nil {
			t.Fatal(err)
		}
	}
	r := &hookRepo{t: t, dir: t.TempDir(), hooks: hooks, home: t.TempDir()}
	global := filepath.Join(r.home, ".gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	system := "GIT_CONFIG_NOSYSTEM=1"
	if len(image) > 0 && image[0] {
		file := filepath.Join(t.TempDir(), "gitconfig")
		if err := os.WriteFile(file, []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		system = "GIT_CONFIG_SYSTEM=" + file
	}
	// The image's git-config, which the Run includes in command scope.
	r.include = filepath.Join(t.TempDir(), "git-config")
	if err := os.WriteFile(r.include, []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH")
	r.env = append(os.Environ(), "PATH="+path, "LUX_SERVICE_DUDE="+service, "DUDE_TOOLS_URL=", "DUDE_TOOLS_TOKEN=",
		"HOME="+r.home, "GIT_CONFIG_NOSYSTEM=", system, "GIT_CONFIG_GLOBAL="+global, "GIT_EDITOR=true",
		"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=include.path", "GIT_CONFIG_VALUE_0="+r.include,
		"GIT_CONFIG_KEY_1=user.name", "GIT_CONFIG_VALUE_1=dude", "GIT_CONFIG_KEY_2=user.email", "GIT_CONFIG_VALUE_2=dude@localhost")
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *hookRepo) run(args ...string) (string, string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (r *hookRepo) git(args ...string) string {
	r.t.Helper()
	out, errOut, err := r.run(args...)
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s%s", args, err, out, errOut)
	}
	return strings.TrimSpace(out)
}

func (r *hookRepo) message() string { return r.git("log", "-1", "--format=%B") }

// repoHook writes the repository's own hook of that name.
func (r *hookRepo) repoHook(name, body string) {
	r.t.Helper()
	writeHook(r.t, filepath.Join(r.dir, ".git", "hooks"), name, body)
}

// writeHook writes an executable shell hook name in dir, made if missing.
func writeHook(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// ran is a hook body that appends its name to log, for a test to read.
func ran(log, name string) string { return "echo " + name + " >> '" + log + "'\n" }

func readLog(t *testing.T, log string) []string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Fields(string(b))
}

// A repository managing its hooks with husky (core.hooksPath=.husky/_ in
// its config, a relative path): dude's hook still adds the co-authors and
// then runs husky's hooks, from a subdirectory too; husky's commit-msg sees
// the trailers, and its pre-commit's refusal stops the commit with its own
// status. The same with the image's system config, and husky's path wins
// over .git/hooks.
func TestARepositorysHookManagerRunsAfterDudesHook(t *testing.T) {
	for _, image := range []bool{false, asImage} {
		t.Run(fmt.Sprintf("image=%v", image), func(t *testing.T) { huskyRunsAfterDude(t, image) })
	}
}

func huskyRunsAfterDude(t *testing.T, image bool) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, fmt.Sprintf("run_hook_husky_%v", image), "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t), image)
	log := filepath.Join(t.TempDir(), "log")
	r.repoHook("pre-commit", ran(log, "dot-git"))
	husky := filepath.Join(r.dir, ".husky", "_")
	writeHook(t, husky, "pre-commit", ran(log, "pre-commit")+"[ -f \"$(git rev-parse --show-toplevel)/REFUSE\" ] && { echo 'husky: lint failed' >&2; exit 7; }\nexit 0\n")
	writeHook(t, husky, "commit-msg", "grep -q '^Co-authored-by: Ana Lima' \"$1\" || exit 9\n"+ran(log, "commit-msg"))
	r.git("config", "core.hooksPath", ".husky/_")
	if err := os.MkdirAll(filepath.Join(r.dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	r.git("-C", "src", "commit", "-q", "--allow-empty", "-m", "Through husky")
	if got := r.message(); !strings.Contains(got, "Co-authored-by: Ana Lima") || !strings.Contains(got, "Co-authored-by: Bo Chen") {
		t.Errorf("message:\n%s", got)
	}
	if got := readLog(t, log); !slices.Equal(got, []string{"pre-commit", "commit-msg"}) {
		t.Errorf("husky's hooks ran %v, want pre-commit then commit-msg", got)
	}

	head := r.git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(r.dir, "REFUSE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := r.run("commit", "-q", "--allow-empty", "-m", "Refused"); err == nil || !strings.Contains(errOut, "husky: lint failed") {
		t.Errorf("husky's pre-commit refusing: %v %q", err, errOut)
	}
	if r.git("rev-parse", "HEAD") != head {
		t.Error("a commit husky refused was made")
	}
	// The status git sees is husky's own: dude's hook exits as it does.
	cmd := exec.Command(filepath.Join(r.hooks, "pre-commit"))
	cmd.Dir, cmd.Env = r.dir, r.env
	if err := cmd.Run(); cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 7 {
		t.Errorf("dude's pre-commit exited %v, want husky's 7", err)
	}
}

// The Run's command-scope include.path of the image's git-config outranks
// a repository's husky core.hooksPath: dude's hook runs, then husky's, and
// git reports the included value under command scope, which dude's hook
// passes over. Without the file, as in an image without dude's layer, git
// skips the include and runs the repository's hooks itself, with no
// co-authors.
func TestTheRunsIncludeOfDudesGitConfig(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, "run_hook_include", "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t))
	log := filepath.Join(t.TempDir(), "log")
	writeHook(t, filepath.Join(r.dir, ".husky", "_"), "commit-msg", ran(log, "husky"))
	r.git("config", "core.hooksPath", ".husky/_")

	if got, want := r.git("config", "--show-scope", "--get-all", "core.hooksPath"),
		"local\t.husky/_\ncommand\t"+r.hooks; got != want {
		t.Errorf("core.hooksPath by scope:\n%s\nwant\n%s", got, want)
	}
	r.git("commit", "-q", "--allow-empty", "-m", "Included")
	if got := readLog(t, log); !slices.Equal(got, []string{"husky"}) || !strings.Contains(r.message(), "Co-authored-by: Ana Lima") {
		t.Errorf("with dude's git-config: ran %v, message\n%s", got, r.message())
	}

	if err := os.Remove(r.include); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(log)
	r.git("commit", "-q", "--allow-empty", "-m", "No layer")
	if got := readLog(t, log); !slices.Equal(got, []string{"husky"}) || r.message() != "No layer" {
		t.Errorf("without dude's git-config: ran %v, message\n%s", got, r.message())
	}
	r.git("config", "--unset", "core.hooksPath")
	r.repoHook("commit-msg", ran(log, "dot-git"))
	_ = os.Remove(log)
	r.git("commit", "-q", "--allow-empty", "-m", "No layer, no husky")
	if got := readLog(t, log); !slices.Equal(got, []string{"dot-git"}) || r.message() != "No layer, no husky" {
		t.Errorf("without dude's git-config or husky: ran %v, message\n%s", got, r.message())
	}
}

// A commit in a linked worktree (git worktree add) runs the main
// repository's .git/hooks, where git looks for them, and gets the
// co-authors, under the image's system config.
func TestACommitInALinkedWorktreeRunsTheRepositorysHooks(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, "run_hook_worktree", "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t), asImage)
	log := filepath.Join(t.TempDir(), "log")
	r.repoHook("pre-commit", ran(log, "pre-commit"))
	r.git("commit", "-q", "--allow-empty", "-m", "Base")
	_ = os.Remove(log)
	wt := filepath.Join(t.TempDir(), "side")
	r.git("worktree", "add", "-q", "-b", "side", wt)
	r.git("-C", wt, "commit", "-q", "--allow-empty", "-m", "In the worktree")
	if got := readLog(t, log); !slices.Equal(got, []string{"pre-commit"}) {
		t.Errorf("the main repository's .git/hooks/pre-commit ran %v, want once", got)
	}
	if got := r.git("-C", wt, "log", "-1", "--format=%B"); got != "In the worktree\n\nCo-authored-by: Ana Lima <ana@corp.example>\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>" {
		t.Errorf("message:\n%s", got)
	}
}

// In the runtime image, whose system config names dude's directory as
// core.hooksPath, a repository with no setting of its own still runs its
// .git/hooks, and its commits get the co-authors.
func TestInTheImageARepositorysGitHooksRun(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, "run_hook_image", "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t), asImage)
	log := filepath.Join(t.TempDir(), "log")
	r.repoHook("pre-commit", ran(log, "pre-commit"))
	r.git("commit", "-q", "--allow-empty", "-m", "In the image")
	if got := readLog(t, log); !slices.Equal(got, []string{"pre-commit"}) {
		t.Errorf("the repository's .git/hooks/pre-commit ran %v, want once", got)
	}
	if got := r.message(); got != "In the image\n\nCo-authored-by: Ana Lima <ana@corp.example>\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>" {
		t.Errorf("message:\n%s", got)
	}
}

// Where git would look for hooks without dude's setting, dude's hook looks
// too: an absolute path (lefthook), the global config's ~/ path, a
// worktree's own setting over the repository's, and .git/hooks when none
// is set; none when the last is empty, as git. A setting naming dude's own directory, directly or through a
// symlink, is passed over for .git/hooks and does not loop. All of it
// with no system config and with the image's.
func TestDudesHookRunsTheHooksGitWouldHaveRun(t *testing.T) {
	for _, image := range []bool{false, asImage} {
		t.Run(fmt.Sprintf("image=%v", image), func(t *testing.T) { hooksGitWouldHaveRun(t, image) })
	}
}

func hooksGitWouldHaveRun(t *testing.T, image bool) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, fmt.Sprintf("run_hook_where_%v", image), "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t), image)
	log := filepath.Join(t.TempDir(), "log")
	commit := func(what string) []string {
		t.Helper()
		_ = os.Remove(log)
		r.git("commit", "-q", "--allow-empty", "-m", what)
		if !strings.Contains(r.message(), "Co-authored-by: Ana Lima") {
			t.Errorf("%s: no co-authors:\n%s", what, r.message())
		}
		return readLog(t, log)
	}

	r.repoHook("commit-msg", ran(log, "dot-git"))
	if got := commit("none set"); !slices.Equal(got, []string{"dot-git"}) {
		t.Errorf("no core.hooksPath: ran %v, want .git/hooks", got)
	}

	lefthook := t.TempDir()
	writeHook(t, lefthook, "commit-msg", ran(log, "absolute"))
	r.git("config", "core.hooksPath", lefthook)
	if got := commit("absolute"); !slices.Equal(got, []string{"absolute"}) {
		t.Errorf("an absolute path: ran %v", got)
	}

	r.git("config", "--unset", "core.hooksPath")
	writeHook(t, filepath.Join(r.home, "my-hooks"), "commit-msg", ran(log, "global"))
	r.git("config", "--global", "core.hooksPath", "~/my-hooks")
	if got := commit("global"); !slices.Equal(got, []string{"global"}) {
		t.Errorf("a global ~/ path: ran %v", got)
	}

	// A worktree's setting comes after the repository's, as git reads them.
	r.git("config", "core.hooksPath", lefthook)
	r.git("config", "extensions.worktreeConfig", "true")
	worktree := t.TempDir()
	writeHook(t, worktree, "commit-msg", ran(log, "worktree"))
	r.git("config", "--worktree", "core.hooksPath", worktree)
	if got := commit("worktree"); !slices.Equal(got, []string{"worktree"}) {
		t.Errorf("a worktree's setting: ran %v", got)
	}
	r.git("config", "--worktree", "--unset", "core.hooksPath")

	// An empty value, last, makes git run no repository hook (it looks for
	// /commit-msg), as git itself shows without dude: dude's hook runs none
	// after adding the trailers.
	r.git("config", "core.hooksPath", "")
	gitAlone := append(slices.Clone(r.env), "GIT_CONFIG_COUNT=0", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@localhost",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@localhost")
	plain := exec.Command("git", "-C", r.dir, "rev-parse", "--git-path", "hooks/commit-msg")
	plain.Env = gitAlone
	if out, err := plain.Output(); err != nil || strings.TrimSpace(string(out)) != "/commit-msg" {
		t.Errorf("git's own hook path under an empty core.hooksPath: %q %v", out, err)
	}
	_ = os.Remove(log)
	plain = exec.Command("git", "-C", r.dir, "commit", "-q", "--allow-empty", "-m", "git alone")
	plain.Env = gitAlone
	if out, err := plain.CombinedOutput(); err != nil {
		t.Fatalf("git alone: %v\n%s", err, out)
	}
	if got := readLog(t, log); len(got) != 0 {
		t.Errorf("git alone under an empty core.hooksPath ran %v", got)
	}
	if got := commit("empty"); len(got) != 0 {
		t.Errorf("an empty setting: ran %v, want none, as git", got)
	}
	r.git("config", "--unset", "core.hooksPath")

	// A setting naming dude's own directory is passed over: the global one
	// before it, else .git/hooks.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(r.hooks, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		own, unsetGlobal string
		want             string
	}{{r.hooks, "", "global"}, {link, "", "global"}, {r.hooks, "unset", "dot-git"}, {link, "", "dot-git"}} {
		if c.unsetGlobal != "" {
			r.git("config", "--global", "--unset", "core.hooksPath")
		}
		r.git("config", "core.hooksPath", c.own)
		done := make(chan []string, 1)
		go func() { done <- commit("own " + c.own) }()
		select {
		case got := <-done:
			if !slices.Equal(got, []string{c.want}) || strings.Count(r.message(), "Ana Lima") != 1 {
				t.Errorf("dude's own directory %s: ran %v, want %s; message\n%s", c.own, got, c.want, r.message())
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("dude's own directory %s: the commit loops", c.own)
		}
	}
}

// An agent's explicit `git -c core.hooksPath=…` is read after the Run's
// command-scope setting, and git takes the last: that one command runs its
// own hooks and gets no co-authors, a bypass it asked for by name.
func TestAnExplicitCommandLineHooksPathBypassesDude(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, "run_hook_cli", "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t))
	log := filepath.Join(t.TempDir(), "log")
	mine := t.TempDir()
	writeHook(t, mine, "commit-msg", ran(log, "mine"))
	r.git("-c", "core.hooksPath="+mine, "commit", "-q", "--allow-empty", "-m", "Mine")
	if got := readLog(t, log); !slices.Equal(got, []string{"mine"}) || strings.Contains(r.message(), "Co-authored-by") {
		t.Errorf("git -c core.hooksPath: ran %v, message\n%s", got, r.message())
	}
}

func TestTheCommitHookAddsTheTasksPeopleAsCoauthors(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, "run_hook", "implementer", "running")
	bin := cli(t)
	r := newHookRepo(t, luxService(t, f.url, token), bin)
	want := "Co-authored-by: Ana Lima <ana@corp.example>\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>"

	r.git("commit", "-q", "--allow-empty", "-m", "Add the thing")
	if got := r.message(); got != "Add the thing\n\n"+want {
		t.Errorf("message after commit -m:\n%s", got)
	}
	// An amend keeps one of each.
	r.git("commit", "-q", "--amend", "--allow-empty", "--no-edit")
	r.git("commit", "-q", "--amend", "--allow-empty", "-m", "Add the thing, better")
	if got := r.message(); got != "Add the thing, better\n\n"+want {
		t.Errorf("message after amends:\n%s", got)
	}
	// One the agent wrote itself is not doubled.
	r.git("commit", "-q", "--allow-empty", "-m", "Mine\n\nCo-authored-by: Ana Lima <ana@corp.example>")
	if got := r.message(); strings.Count(got, "Ana Lima") != 1 || !strings.Contains(got, "Bo Chen") {
		t.Errorf("a trailer given twice:\n%s", got)
	}
	// --no-verify skips commit-msg, never prepare-commit-msg.
	r.git("commit", "-q", "--allow-empty", "--no-verify", "-m", "Quick")
	if got := r.message(); !strings.HasSuffix(got, want) {
		t.Errorf("message with --no-verify:\n%s", got)
	}
}

// A commit git replays keeps the people it was written with: a rebase
// after the task changed hands adds nobody to it, and the next new commit
// names the new owner.
func TestARebasedCommitKeepsItsCoauthors(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	mustExec(t, f.owner, `DELETE FROM task_people WHERE person_id = $1`, "per_b_"+f.org)
	token := f.run(t, "run_hook_rebase", "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t))
	old := "Co-authored-by: Ana Lima <ana@corp.example>"
	// The repository's own hook keeps running while git replays commits.
	log := filepath.Join(t.TempDir(), "log")
	writeHook(t, filepath.Join(r.dir, ".husky"), "prepare-commit-msg", ran(log, "replayed"))
	r.git("config", "core.hooksPath", ".husky")
	r.git("commit", "-q", "--allow-empty", "-m", "Base")
	r.git("commit", "-q", "--allow-empty", "-m", "Under Ana")
	before := r.git("rev-parse", "HEAD")
	_ = os.Remove(log)

	// The task passes from Ana to Bo.
	mustExec(t, f.owner, `DELETE FROM task_people WHERE person_id = $1`, "per_a_"+f.org)
	mustExec(t, f.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ($1, $2, $3, 0)`,
		f.item, "per_b_"+f.org, f.org)
	// Committed at another time, so the replayed commit is a new one.
	env := r.env
	r.env = append(slices.Clone(env), "GIT_COMMITTER_DATE=2001-01-01T00:00:00Z")
	r.git("rebase", "-q", "--force-rebase", "--keep-empty", "HEAD~1")
	r.env = env
	if r.git("rev-parse", "HEAD") == before {
		t.Fatal("the rebase did not rewrite the commit")
	}
	if got := r.message(); got != "Under Ana\n\n"+old {
		t.Errorf("the rebased commit:\n%s\nwant Ana alone", got)
	}
	// A cherry-pick of it, the same.
	r.git("switch", "-q", "-c", "side", "HEAD~1")
	r.git("cherry-pick", "--allow-empty", "--keep-redundant-commits", "main")
	if got := r.message(); got != "Under Ana\n\n"+old {
		t.Errorf("the cherry-picked commit:\n%s\nwant Ana alone", got)
	}
	if got := readLog(t, log); !slices.Equal(got, []string{"replayed", "replayed"}) {
		t.Errorf("the repository's prepare-commit-msg during the rebase and cherry-pick ran %v, want twice", got)
	}
	r.git("commit", "-q", "--allow-empty", "-m", "Under Bo")
	if got := r.message(); got != "Under Bo\n\nCo-authored-by: Bo Chen <2+bo-dev@users.noreply.github.com>" {
		t.Errorf("a new commit after the rebase:\n%s\nwant Bo", got)
	}
}

// The repository's own hooks still run, through dude's, with the outcome
// they choose: a commit-msg sees the trailers, a pre-commit can refuse.
func TestTheCommitHookRunsTheRepositorysOwnHooks(t *testing.T) {
	f := setup(t)
	f.coauthorTask(t)
	token := f.run(t, "run_hook_chain", "implementer", "running")
	r := newHookRepo(t, luxService(t, f.url, token), cli(t))
	seen := filepath.Join(t.TempDir(), "seen")
	pre := filepath.Join(t.TempDir(), "pre")
	r.repoHook("commit-msg", `cp "$1" "`+seen+`"`+"\n")
	// Copied before it appends: the trailers are in the message already.
	r.repoHook("prepare-commit-msg", `cp "$1" "`+pre+`"`+"\n"+`printf '\nRefs: TEXT-1\n' >> "$1"`+"\n")
	r.git("commit", "-q", "--allow-empty", "-m", "Chained")
	if b, err := os.ReadFile(pre); err != nil || !strings.Contains(string(b), "Co-authored-by: Ana Lima") {
		t.Errorf("the repository's prepare-commit-msg saw %q (%v), want the trailers added before it ran", b, err)
	}
	b, err := os.ReadFile(seen)
	if err != nil || !strings.Contains(string(b), "Co-authored-by: Bo Chen") {
		t.Errorf("the repository's commit-msg saw %q (%v), want the trailers", b, err)
	}
	if got := r.message(); !strings.Contains(got, "Refs: TEXT-1") || !strings.Contains(got, "Co-authored-by: Ana Lima") {
		t.Errorf("message:\n%s", got)
	}
	r.repoHook("pre-commit", "echo 'lint failed' >&2\nexit 1\n")
	if _, errOut, err := r.run("commit", "-q", "--allow-empty", "-m", "Refused"); err == nil || !strings.Contains(errOut, "lint failed") {
		t.Errorf("the repository's pre-commit refusing: %v %q", err, errOut)
	}
}

// With dude's tools out of reach the commit goes ahead, without trailers,
// and says so once.
func TestACommitWithoutDudesToolsGoesAheadWithoutCoauthors(t *testing.T) {
	bin := cli(t)
	r := newHookRepo(t, "unix:"+filepath.Join(t.TempDir(), "gone.sock"), bin)
	_, errOut, err := r.run("commit", "-q", "--allow-empty", "-m", "Alone")
	if err != nil {
		t.Fatalf("the commit failed: %v\n%s", err, errOut)
	}
	if got := r.message(); got != "Alone" {
		t.Errorf("message %q", got)
	}
	if lines := strings.Split(strings.TrimSpace(errOut), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "dude: committing without co-authors") {
		t.Errorf("stderr %q, want one line from dude", errOut)
	}
	// No dude CLI at all: the same.
	r.env = append(r.env, "PATH=/usr/bin:/bin")
	if _, errOut, err := r.run("commit", "-q", "--allow-empty", "-m", "No CLI"); err != nil ||
		errOut != "dude: committing without co-authors: no dude CLI on PATH\n" {
		t.Errorf("without the CLI: %v %q", err, errOut)
	}
}
