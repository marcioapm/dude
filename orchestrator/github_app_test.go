package orchestrator_test

// A GitHub App's credential through the orchestrator's real code: the
// installation's tokens reach lux on the submit, every push and sync, and
// every resume, each minted then; a PAT goes the same way; a lux from
// before credentials on push and sync is pushed without them.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	pemfile "encoding/pem"
	"testing"
	"time"

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
