package orchestrator_test

// A GitHub App's credential through the orchestrator's real code: the
// installation's tokens reach lux on the submit, every push and sync, and
// every resume, each minted then; a PAT goes the same way; a lux from
// before credentials on push and sync is pushed without them.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	pemfile "encoding/pem"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// asApp connects the world's organisation to GitHub as an App installed on
// acme. Its tokens live five minutes, inside dude's refresh margin, so
// every use mints one: a token reused where a fresh one is due shows.
func (w *world) asApp() *fakegithub.App {
	w.t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		w.t.Fatal(err)
	}
	app := fakegithub.NewApp(7001, "dude-bot-test", &key.PublicKey)
	app.Set(func(a *fakegithub.App) {
		a.Installations = map[string]int64{"acme": 71}
		a.TokenLifetime = 5 * time.Minute
	})
	w.ghApp.Store(app)
	block := pemfile.EncodeToMemory(&pemfile.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	mustExec(w.t, w.owner, `UPDATE forge_credentials SET auth = 'github_app', secret = $2, app_id = '7001' WHERE organization_id = $1`,
		w.org, string(block))
	return app
}

func runFor(w *world, luxID string) *fakelux.Run {
	for _, r := range w.lux.Runs() {
		if r.ID == luxID {
			return r
		}
	}
	return nil
}

func secretValue(spec lux.Spec, name string) string {
	for _, s := range spec.Secrets {
		if s.Name == name {
			return s.Value
		}
	}
	return ""
}

// Every push of a delivery carries a token minted for it, by lux's name
// for the repository: an installation token of the App, none of them the
// submit's.
func TestEveryPushCarriesAFreshAppToken(t *testing.T) {
	w := newWorld(t)
	app := w.asApp()
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	pushes := 0
	for _, r := range w.lux.Runs() {
		spec := submitted(t, r)
		if spec.Env["GIT_AUTHOR_NAME"] != "dude-bot-test[bot]" || spec.Env["GIT_COMMITTER_EMAIL"] != "4242+dude-bot-test[bot]@users.noreply.github.com" {
			t.Errorf("run %s commits as %q <%s>, want the App's bot", r.ID, spec.Env["GIT_AUTHOR_NAME"], spec.Env["GIT_COMMITTER_EMAIL"])
		}
		submitted := secretValue(spec, "GIT_TOKEN")
		if app.Installation(submitted) != 71 {
			t.Errorf("run %s submitted with GIT_TOKEN %q, not an installation token", r.ID, submitted)
		}
		for _, creds := range r.PushCredentials {
			pushes++
			token := creds["target"]
			if len(creds) != 1 || app.Installation(token) != 71 {
				t.Errorf("push credentials %v, want target's installation token", creds)
			}
			if token == submitted {
				t.Errorf("run %s pushed with the token it was submitted with", r.ID)
			}
		}
	}
	// implement, fix and simplify publish.
	if pushes != 3 {
		t.Errorf("%d pushes with credentials, want 3", pushes)
	}
}

// A PAT goes the same way: its token on every push.
func TestEveryPushCarriesThePAT(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	pushes := 0
	for _, r := range w.lux.Runs() {
		if _, ok := submitted(t, r).Env["GIT_AUTHOR_NAME"]; ok {
			t.Errorf("a PAT's run %s sets a commit identity", r.ID)
		}
		for _, creds := range r.PushCredentials {
			pushes++
			if len(creds) != 1 || creds["target"] != "ghp_test" {
				t.Errorf("push credentials %v, want target's PAT", creds)
			}
		}
	}
	if pushes != 3 {
		t.Errorf("%d pushes with credentials, want 3", pushes)
	}
}

// A runner that refuses credentials on a push (409
// push_credentials_unsupported) is asked again without them, and the
// delivery goes on.
func TestALuxWithoutPushCredentialsIsPushedWithout(t *testing.T) {
	w := newWorld(t)
	w.lux.NoOperationCredentials = true
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	pushes := 0
	for _, r := range w.lux.Runs() {
		for _, creds := range r.PushCredentials {
			pushes++
			if creds != nil {
				t.Errorf("an old runner took credentials %v", creds)
			}
		}
	}
	if pushes != 3 {
		t.Errorf("%d pushes, want 3", pushes)
	}
}

// A resume of a Run started with an App's token carries one minted for
// the resume.
func TestAResumeCarriesAFreshAppToken(t *testing.T) {
	w := newWorld(t)
	app := w.asApp()
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	w.pauseAndResume(wi)
	r := w.lux.Runs()[0]
	submittedWith := secretValue(submitted(t, r), "GIT_TOKEN")
	var resumedWith string
	for _, s := range r.ResumeSecrets[0] {
		if s.Name == "GIT_TOKEN" {
			resumedWith = s.Value
		}
	}
	if app.Installation(resumedWith) != 71 || resumedWith == submittedWith {
		t.Errorf("resumed with GIT_TOKEN %q (submitted with %q), want a fresh installation token", resumedWith, submittedWith)
	}
}

// A preview's own secret named like an owner's token (stored before the
// name was reserved) is the project's: a resume sends its value, once, and
// mints nothing for an owner "deploy".
func TestAPreviewSecretNamedLikeAnOwnersTokenKeepsTheProjectsValue(t *testing.T) {
	w := newWorld(t)
	app := w.asApp()
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.secret("GIT_TOKEN_DEPLOY", "deploy-key-project")
	task := w.task()
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("web ready", func() bool {
		runs := w.luxRuns()
		return len(runs) == 1 && w.lux.ServerStates(runs[0].ID)["web"] == "ready"
	})
	r := w.luxRuns()[0]
	mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
	w.previews.Minute = time.Millisecond
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code >= 300 {
		t.Fatalf("start web on the parked preview = %d %v", code, body)
	}
	w.until("web ready again", func() bool { return w.luxCalls(r.ID, "resume") == 1 && w.lux.ServerStates(r.ID)["web"] == "ready" })
	var values []string
	for _, s := range r.ResumeSecrets[0] {
		if s.Name == "GIT_TOKEN_DEPLOY" {
			values = append(values, s.Value)
		}
	}
	if len(values) != 1 || values[0] != "deploy-key-project" {
		t.Errorf("the resume sent GIT_TOKEN_DEPLOY as %v, want the project's value once", values)
	}
	if env := w.lux.ServerEnv(r.ID, "web"); env["GIT_TOKEN_DEPLOY"] != "deploy-key-project" {
		t.Errorf("web's GIT_TOKEN_DEPLOY = %q", env["GIT_TOKEN_DEPLOY"])
	}
	app.Set(func(a *fakegithub.App) {
		if n := a.Lookups["/users/deploy/installation"]; n != 0 {
			t.Errorf("dude looked up an installation for %q %d times", "deploy", n)
		}
	})
}

// A task whose repository's owner has not installed the App fails at
// submit, saying so, rather than being asked again every few seconds.
func TestARunOnAnOwnerWithoutTheAppFailsSayingSo(t *testing.T) {
	w := newWorld(t)
	app := w.asApp()
	app.Set(func(a *fakegithub.App) { a.Installations = map[string]int64{"marcio": 72} })
	wi := w.task()
	w.deliver(wi)
	var why string
	w.until("the Run to fail", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM runs WHERE task_id = $1 AND status = 'failed'`, wi).Scan(&why)
		return why != ""
	})
	if !strings.Contains(why, "the GitHub App is not installed on acme") {
		t.Errorf("the Run failed with %q, want the App not installed on acme", why)
	}
	if len(w.lux.Runs()) != 0 {
		t.Errorf("%d lux Runs, want none submitted", len(w.lux.Runs()))
	}
	app.Set(func(a *fakegithub.App) {
		if n := a.Lookups["/repos/acme/target/installation"]; n != 1 {
			t.Errorf("acme/target's installation looked up %d times, want once", n)
		}
	})
}

// A repository of another owner added mid-task, whose name sorts before the
// one the Run was submitted with: the resume keeps GIT_TOKEN the submitted
// repository's owner's, as lux holds it for that repository, and declares
// the added one's owner's token for it.
func TestAResumeKeepsGitTokenTheSubmittedRepositorysOwners(t *testing.T) {
	w := newWorld(t)
	app := w.asApp()
	app.Set(func(a *fakegithub.App) { a.Installations = map[string]int64{"acme": 71, "marcio": 72} })
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'api', 'git://127.0.0.1/marcio/web.git', 'main')`, "repo_api_"+w.org, w.org, w.project)
	w.lux.Decide = hang
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	r := w.lux.Runs()[0]
	if got := app.Installation(secretValue(submitted(t, r), "GIT_TOKEN")); got != 71 {
		t.Fatalf("submitted with GIT_TOKEN of installation %d, want acme's 71", got)
	}
	status, body := w.callTool(tools.URL, string(r.Spec), "request_repository", `{"repository":"api","reason":"read the client"}`)
	if status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(body), &req)
	if status, out := w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("approve: %d %v", status, out)
	}
	w.until("api cloned", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, req.RequestID) == 1
	})
	if len(r.ResumeSecrets) != 1 {
		t.Fatalf("%d resumes, want 1", len(r.ResumeSecrets))
	}
	sent := secretsByName(r.ResumeSecrets[0])
	if got := app.Installation(sent["GIT_TOKEN"]); got != 71 {
		t.Errorf("resumed with GIT_TOKEN of installation %d, want acme's 71: %v", got, sent)
	}
	if got := app.Installation(sent["GIT_TOKEN_MARCIO"]); got != 72 {
		t.Errorf("resumed with GIT_TOKEN_MARCIO of installation %d, want marcio's 72", got)
	}
	if _, ok := sent["GIT_TOKEN_ACME"]; ok {
		t.Errorf("the resume sent GIT_TOKEN_ACME, which the Run does not declare")
	}
	var spec lux.Spec
	_ = json.Unmarshal(r.Spec, &spec)
	for _, repo := range spec.Git.Repositories {
		if want := map[string]string{"target": "GIT_TOKEN", "api": "GIT_TOKEN_MARCIO"}[repo.Name]; repo.Credential != want {
			t.Errorf("%s clones with %q, want %q", repo.Name, repo.Credential, want)
		}
	}
}

// One App installed on two owners, a Run on a repository of each: every
// push and sync carries each repository's own owner's token, the spec
// declares GIT_TOKEN and GIT_TOKEN_MARCIO, and a resume mints marcio's again.
func TestARunAcrossTwoOwnersUsesEachOwnersInstallation(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = map[string]string{"target:API.md": "api\n", "web:PAGE.md": "page\n"}
		}
		return b
	}
	wi := w.task()
	app := w.twoOwners(wi)
	w.start(wi, delivery.DefaultPolicy())
	w.until("a pull request in each repository", func() bool { return len(w.gh.Pulls()) == 1 && len(w.marcioWeb.Pulls()) == 1 })
	pushes := 0
	for _, r := range w.lux.Runs() {
		spec := submitted(t, r)
		if app.Installation(secretValue(spec, "GIT_TOKEN")) != 71 || app.Installation(secretValue(spec, "GIT_TOKEN_MARCIO")) != 72 {
			t.Errorf("run %s declares %+v, want GIT_TOKEN acme's and GIT_TOKEN_MARCIO marcio's", r.ID, spec.Secrets)
		}
		for _, repo := range spec.Git.Repositories {
			if want := map[string]string{"target": "GIT_TOKEN", "web": "GIT_TOKEN_MARCIO"}[repo.Name]; repo.Credential != want {
				t.Errorf("run %s: %s clones with %q, want %q", r.ID, repo.Name, repo.Credential, want)
			}
		}
		for _, creds := range r.PushCredentials {
			pushes++
			if len(creds) != 2 || app.Installation(creds["target"]) != 71 || app.Installation(creds["web"]) != 72 {
				t.Errorf("push credentials %v, want target 71 and web 72", creds)
			}
		}
	}
	if pushes == 0 {
		t.Error("no push carried credentials")
	}
}

// twoOwners connects the world as an App installed on acme (71) and marcio
// (72), and names on task both target and web, which is marcio's.
func (w *world) twoOwners(task string) *fakegithub.App {
	w.t.Helper()
	app := w.asApp()
	app.Set(func(a *fakegithub.App) { a.Installations = map[string]int64{"acme": 71, "marcio": 72} })
	webID := w.addWeb(task, "write")
	mustExec(w.t, w.owner, `UPDATE repositories SET url = 'git://127.0.0.1/marcio/web.git' WHERE id = $1`, webID)
	return app
}

// A pause and resume of a Run across two owners mints both owners' tokens
// again, each under the name the Run declared.
func TestAResumeAcrossTwoOwnersMintsEachOwnersToken(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = hang
	wi := w.task()
	app := w.twoOwners(wi)
	w.start(wi, delivery.DefaultPolicy())
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	w.pauseAndResume(wi)
	r := w.lux.Runs()[0]
	spec := submitted(t, r)
	sent := secretsByName(r.ResumeSecrets[0])
	if app.Installation(sent["GIT_TOKEN"]) != 71 || sent["GIT_TOKEN"] == secretValue(spec, "GIT_TOKEN") {
		t.Errorf("resumed with GIT_TOKEN %q, want a fresh token of acme's 71", sent["GIT_TOKEN"])
	}
	if app.Installation(sent["GIT_TOKEN_MARCIO"]) != 72 || sent["GIT_TOKEN_MARCIO"] == secretValue(spec, "GIT_TOKEN_MARCIO") {
		t.Errorf("resumed with GIT_TOKEN_MARCIO %q, want a fresh token of marcio's 72", sent["GIT_TOKEN_MARCIO"])
	}
}

// A conductor's sync across two owners carries each repository's own
// owner's token.
func TestAConductorsSyncAcrossTwoOwnersCarriesEachOwnersToken(t *testing.T) {
	w := conducting(t)
	task := w.task()
	app := w.twoOwners(task)
	cond := w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.wokenWith(task, "after implement")
	r := runFor(w, w.luxRunOf(cond))
	if r == nil || len(r.SyncCredentials) == 0 {
		t.Fatal("the conductor's checkout was never synced")
	}
	for _, creds := range r.SyncCredentials {
		if len(creds) != 2 || app.Installation(creds["target"]) != 71 || app.Installation(creds["web"]) != 72 {
			t.Errorf("sync credentials %v, want target 71 and web 72", creds)
		}
	}
}

// Verify mints a token for one linked repository and reads the bot, or
// says GitHub's reason it could not.
func TestVerifyingAnAppMintsATokenForALinkedRepository(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE repositories SET url = 'https://github.com/acme/target.git' WHERE id = $1`, w.repoID)
	app := w.asApp()
	status, out := w.call("/internal/github/verify-app", map[string]any{})
	if status != 200 || out["ok"] != true || out["repository"] != "acme/target" || out["bot"] != "dude-bot-test[bot]" {
		t.Errorf("verify: %d %v", status, out)
	}
	// asApp's tokens are always due for refresh: the bot's lookup mints one too.
	if app.ExchangesFor(71) == 0 {
		t.Error("verify minted no token")
	}
	app.Set(func(a *fakegithub.App) { a.Installations = map[string]int64{} })
	mustExec(t, w.owner, `UPDATE repositories SET url = 'https://github.com/elsewhere/target.git' WHERE id = $1`, w.repoID)
	status, out = w.call("/internal/github/verify-app", map[string]any{})
	if reason, _ := out["reason"].(string); status != 200 || out["ok"] != false || !strings.Contains(reason, "not installed on elsewhere: install it there, with access to elsewhere/target") {
		t.Errorf("verify, not installed: %d %v", status, out)
	}
}

// The conductor's checkout is synced with fresh credentials: an App's
// installation token, minted for the sync.
func TestAConductorsSyncCarriesAFreshAppToken(t *testing.T) {
	e := newEditing(t)
	app := e.asApp()
	e.wokenWith(e.task, "after implement")
	r := runFor(e.world, e.luxRunOf(e.cond))
	if r == nil || len(r.SyncCredentials) == 0 {
		t.Fatal("the conductor's checkout was never synced")
	}
	for _, creds := range r.SyncCredentials {
		if len(creds) != 1 || app.Installation(creds["target"]) != 71 {
			t.Errorf("sync credentials %v, want target's installation token", creds)
		}
	}
}
