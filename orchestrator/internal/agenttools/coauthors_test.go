package agenttools_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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

// hookRepo is a repository whose hooks are dude's image's (core.hooksPath
// at a copy of its hook directory), committing with the dude CLI on PATH
// reaching the tools as lux's socket would, or reaching nothing.
type hookRepo struct {
	t   *testing.T
	dir string
	env []string
}

func newHookRepo(t *testing.T, service string, bin string) *hookRepo {
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
	r := &hookRepo{t: t, dir: t.TempDir()}
	path := filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH")
	r.env = append(os.Environ(), "PATH="+path, "LUX_SERVICE_DUDE="+service, "DUDE_TOOLS_URL=", "DUDE_TOOLS_TOKEN=",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_EDITOR=true",
		"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0="+hooks,
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
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "hooks", name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		r.t.Fatal(err)
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
	r.git("commit", "-q", "--allow-empty", "-m", "Base")
	r.git("commit", "-q", "--allow-empty", "-m", "Under Ana")
	before := r.git("rev-parse", "HEAD")

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
	r.repoHook("commit-msg", `cp "$1" "`+seen+`"`+"\n")
	r.repoHook("prepare-commit-msg", `printf '\nRefs: TEXT-1\n' >> "$1"`+"\n")
	r.git("commit", "-q", "--allow-empty", "-m", "Chained")
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
