package orchestrator_test

// Registry logins: what dude sends lux when agent images come from a
// private registry, on every start of a Run.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

const (
	ecrRegistry = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	ecrImage    = ecrRegistry + "/dude/agent:1"
)

// fakeECR is ECR's GetAuthorizationToken with no network: each call mints
// a new token, valid 12 hours from the fake clock, whose passwords are
// recorded so a test can look for them anywhere.
type fakeECR struct {
	mu     sync.Mutex
	clock  time.Time
	minted []string
	fail   error
}

func (f *fakeECR) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *fakeECR) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(d)
}

func (f *fakeECR) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func (f *fakeECR) tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.minted...)
}

func (f *fakeECR) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	password := fmt.Sprintf("ecr-password-%d-%d", len(f.minted)+1, f.clock.UnixNano())
	f.minted = append(f.minted, password)
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []types.AuthorizationData{{
		AuthorizationToken: aws.String(base64.StdEncoding.EncodeToString([]byte("AWS:" + password))),
		ExpiresAt:          aws.Time(f.clock.Add(12 * time.Hour)),
	}}}, nil
}

// withECR logs the world's syncer in to an ECR registry that agent images
// come from, through a fake ECR.
func (w *world) withECR() *fakeECR {
	api := &fakeECR{clock: time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)}
	w.syncer.Registry = registry.NewECR(ecrRegistry, api, api.now)
	mustExec(w.t, w.owner, `UPDATE projects SET runtime_image = $2 WHERE id = $1`, w.project, ecrImage)
	return api
}

func secretValue(secrets []lux.Secret, name string) (string, bool) {
	for _, s := range secrets {
		if s.Name == name {
			return s.Value, true
		}
	}
	return "", false
}

func submitted(t *testing.T, r *fakelux.Run) lux.Spec {
	t.Helper()
	var spec lux.Spec
	if err := json.Unmarshal(r.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// With no login configured (DUDE_REGISTRY_AUTH unset or none), an image in
// ECR is submitted and resumed as before: no registryAuth, no secret.
// buildSpec's own output is pinned by phases' golden files.
func TestWithoutALoginNothingIsAdded(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = $2 WHERE id = $1`, w.project, ecrImage)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.pauseAndResume(wi)
	r := w.lux.Runs()[0]
	spec := submitted(t, r)
	if len(spec.Image.RegistryAuth) != 0 {
		t.Errorf("registryAuth = %+v", spec.Image.RegistryAuth)
	}
	for _, secrets := range append([][]lux.Secret{spec.Secrets}, r.ResumeSecrets...) {
		if _, ok := secretValue(secrets, "DUDE_REGISTRY_AUTH"); ok {
			t.Errorf("a login was sent with none configured")
		}
	}
}

func TestAnAgentImageInTheFactorysRegistryIsPulledWithItsLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.deliver(w.task())
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })

	spec := submitted(t, w.lux.Runs()[0])
	if spec.Image.Ref != ecrImage || len(spec.Image.RegistryAuth) != 1 ||
		spec.Image.RegistryAuth[0] != (lux.RegistryAuth{Registry: ecrRegistry, Secret: "DUDE_REGISTRY_AUTH"}) {
		t.Fatalf("image = %+v", spec.Image)
	}
	if v, _ := secretValue(spec.Secrets, "DUDE_REGISTRY_AUTH"); v != "AWS:"+api.tokens()[0] {
		t.Errorf("DUDE_REGISTRY_AUTH = %q, want AWS:<ECR's password>", v)
	}
}

func TestAStaticLoginIsSentForItsRegistry(t *testing.T) {
	w := newWorld(t)
	p, err := registry.NewStatic("ghcr.io", "acme-bot:ghp_registry")
	if err != nil {
		t.Fatal(err)
	}
	w.syncer.Registry = p
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULL WHERE id = $1`, w.project)
	w.syncer.Agent.DefaultImage = "ghcr.io/acme/agent:2"
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.deliver(w.task())
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })

	spec := submitted(t, w.lux.Runs()[0])
	if spec.Image.Ref != "ghcr.io/acme/agent:2" || len(spec.Image.RegistryAuth) != 1 || spec.Image.RegistryAuth[0].Registry != "ghcr.io" {
		t.Fatalf("image = %+v", spec.Image)
	}
	if v, _ := secretValue(spec.Secrets, "DUDE_REGISTRY_AUTH"); v != "acme-bot:ghp_registry" {
		t.Errorf("DUDE_REGISTRY_AUTH = %q", v)
	}
}

// A project that names an image of its own elsewhere is not handed the
// factory's registry credential, at its start or at a resume.
func TestAnImageFromAnotherRegistryGetsNoLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = 'ghcr.io/someone/agent:1' WHERE id = $1`, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	w.pauseAndResume(wi)

	r := w.lux.Runs()[0]
	spec := submitted(t, r)
	if len(spec.Image.RegistryAuth) != 0 {
		t.Errorf("registryAuth = %+v, want none for another registry", spec.Image.RegistryAuth)
	}
	for _, secrets := range append([][]lux.Secret{spec.Secrets}, r.ResumeSecrets...) {
		if _, ok := secretValue(secrets, "DUDE_REGISTRY_AUTH"); ok {
			t.Errorf("a login was sent for an image in another registry")
		}
	}
	if len(api.tokens()) != 0 {
		t.Errorf("ECR was asked for %d tokens nobody needed", len(api.tokens()))
	}
}

// pauseAndResume pauses the task's running Run as a person would, and
// resumes it once lux has stopped it. Returns the Run's id.
func (w *world) pauseAndResume(wi string) string {
	w.t.Helper()
	var runID string
	w.until("the agent to be working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND status = 'running'`, wi).Scan(&runID)
		return runID != ""
	})
	resumed := w.lux.Runs()[0].Resumed
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		w.t.Fatalf("pause: %d %v", status, out)
	}
	w.until("lux to stop it", func() bool { return w.lux.Runs()[0].State == "stopped" })
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		w.t.Fatalf("resume: %d %v", status, out)
	}
	w.until("the resume", func() bool { return w.lux.Runs()[0].Resumed == resumed+1 })
	return runID
}

// Every way a Run is resumed carries a login minted for it: the one it
// started with has passed its refresh point by then, as a token from
// yesterday would have expired.
func TestEveryResumeCarriesAFreshlyMintedLogin(t *testing.T) {
	for name, park := range map[string]func(w *world) (resume func()){
		"a person's pause": func(w *world) func() {
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
			wi := w.task()
			w.deliver(wi)
			var runID string
			w.until("the agent to be working", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND status = 'running'`, wi).Scan(&runID)
				return runID != ""
			})
			w.call("/internal/runs/"+runID+"/pause", map[string]any{})
			w.until("lux to stop it", func() bool { return w.lux.Runs()[0].State == "stopped" })
			return func() { w.call("/internal/runs/"+runID+"/resume", map[string]any{}) }
		},
		"parked on a question": func(w *world) func() {
			w.syncer.ParkAfter = 300 * time.Millisecond
			wi, _ := w.asking()
			w.until("lux to stop it", func() bool { return w.lux.Runs()[0].State == "stopped" })
			return func() { w.call("/internal/questions/"+w.questionID(wi)+"/answer", map[string]any{"text": "yes"}) }
		},
		"parked idle": func(w *world) func() {
			w.syncer.IdleAfter = 300 * time.Millisecond
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
			wi := w.task()
			w.deliver(wi)
			var runID string
			w.until("the idle park", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND dude_pause = 'idle' AND status = 'paused'`, wi).Scan(&runID)
				return runID != "" && w.lux.Runs()[0].State == "stopped"
			})
			return func() { w.call("/internal/runs/"+runID+"/resume", map[string]any{}) }
		},
		"a repository added (addedRepositories)": func(w *world) func() {
			w.withTools()
			w.syncer.ParkAfter = 300 * time.Millisecond
			mustExec(w.t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
				VALUES ($1, $2, $3, 'web', 'https://github.com/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
			w.lux.Decide = func(map[string]any) fakelux.Behaviour {
				return fakelux.Behaviour{CallTools: [][2]string{{"request_repository", `{"repository":"web","reason":"r","wait":true}`}},
					Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
			}
			wi := w.task()
			w.names(wi, w.repoID)
			w.deliver(wi)
			var runID, reqID string
			w.until("the run to be parked", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND dude_pause = 'person'`, wi).Scan(&runID)
				return runID != "" && w.lux.Runs()[0].State == "stopped"
			})
			_ = w.owner.QueryRow(context.Background(), `SELECT id FROM repository_requests WHERE run_id = $1`, runID).Scan(&reqID)
			return func() {
				w.call("/internal/repository-requests/"+reqID+"/decide", map[string]any{"approve": true})
				w.until("the resume to bring the repository", func() bool {
					return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, reqID) == 1
				})
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			api := w.withECR()
			resume := park(w)
			r := w.lux.Runs()[0]
			started, _ := secretValue(submitted(t, r).Secrets, "DUDE_REGISTRY_AUTH")
			if started != "AWS:"+api.tokens()[0] {
				t.Fatalf("started with %q, want ECR's first token", started)
			}
			// Parked past the cached token's refresh point.
			api.advance(11 * time.Hour)
			resume()
			w.until("the resume", func() bool { return r.Resumed == 1 })

			fresh, ok := secretValue(r.ResumeSecrets[0], "DUDE_REGISTRY_AUTH")
			if tokens := api.tokens(); !ok || len(tokens) != 2 || fresh != "AWS:"+tokens[1] {
				t.Errorf("resumed with %q (sent: %v), ECR minted %d; want the second token", fresh, ok, len(tokens))
			}
		})
	}
}

// A Run started before the factory logged in to a registry is resumed as
// it was started: lux's spec names no login, so none is sent (lux would
// take an unnamed secret as the workload's).
func TestARunStartedWithoutALoginResumesWithout(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = $2 WHERE id = $1`, w.project, ecrImage)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	api := &fakeECR{clock: time.Now()}
	w.syncer.Registry = registry.NewECR(ecrRegistry, api, api.now)
	w.pauseAndResume(wi)
	if _, ok := secretValue(w.lux.Runs()[0].ResumeSecrets[0], "DUDE_REGISTRY_AUTH"); ok || len(api.tokens()) != 0 {
		t.Errorf("a login was sent to resume a Run started without one")
	}
}

// ECR that cannot be reached holds a start back rather than failing it,
// and the Run goes ahead when it answers.
func TestAnECROutageDelaysARunAndDoesNotFailIt(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	api.setFail(errors.New("no EC2 IMDS role found"))
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the submit to be held back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND next_attempt_at IS NOT NULL`, wi) == 1
	})
	if len(w.lux.Runs()) != 0 || w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi) != 0 {
		t.Fatalf("a Run went ahead or failed without its login")
	}
	api.setFail(nil)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE task_id = $1`, wi)
	w.until("the submit", func() bool { return len(w.lux.Runs()) == 1 })
}

// The login's value reaches lux and nothing else: not the syncer's log
// (the only code that holds it), not a row of the database, through a
// failed login at submit, a start, a failed login at resume, a resume and
// the Run's end.
func TestTheRegistryLoginIsNeverLoggedOrStored(t *testing.T) {
	w := newWorld(t)
	var logs bytes.Buffer
	w.syncer.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	api := w.withECR()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done.", Commit: map[string]string{"A.md": "a\n"}}
	}
	api.setFail(errors.New("ExpiredTokenException"))
	wi := w.task()
	w.deliver(wi)
	w.until("the failed login to hold the submit back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND next_attempt_at IS NOT NULL`, wi) == 1
	})
	api.setFail(nil)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE task_id = $1`, wi)

	var runID string
	w.until("the agent to be working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND status = 'running'`, wi).Scan(&runID)
		return runID != ""
	})
	w.call("/internal/runs/"+runID+"/pause", map[string]any{})
	w.until("lux to stop it", func() bool { return w.lux.Runs()[0].State == "stopped" })
	api.advance(11 * time.Hour)
	api.setFail(errors.New("ExpiredTokenException"))
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the failed login to hold the resume back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at IS NOT NULL`, runID) == 1
	})
	api.setFail(nil)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})

	tokens := api.tokens()
	if len(tokens) != 2 {
		t.Fatalf("ECR minted %d tokens, want one per start (2)", len(tokens))
	}
	// Each in every form a leak would take it: the password, the secret
	// lux gets, and the base64 ECR returns.
	var forms []string
	for _, p := range tokens {
		forms = append(forms, p, "AWS:"+p, base64.StdEncoding.EncodeToString([]byte("AWS:"+p)))
	}
	if !strings.Contains(logs.String(), "ExpiredTokenException") {
		t.Fatalf("the failed logins were not logged: the check would prove nothing\n%s", logs.String())
	}
	stored := w.everyRow()
	for _, f := range forms {
		if strings.Contains(logs.String(), f) {
			t.Errorf("the log carries a registry login")
		}
		if strings.Contains(stored, f) {
			t.Errorf("the database holds a registry login")
		}
	}
	// It did reach lux, so the search above looked for the right thing.
	r := w.lux.Runs()[0]
	if v, _ := secretValue(r.ResumeSecrets[0], "DUDE_REGISTRY_AUTH"); v != "AWS:"+tokens[1] {
		t.Errorf("the resume sent %q", v)
	}
}

// everyRow is every row of every table in the public schema, as JSON.
func (w *world) everyRow() string {
	ctx := context.Background()
	rows, err := w.owner.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		w.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		tables = append(tables, t)
	}
	rows.Close()
	var all strings.Builder
	for _, table := range tables {
		// A table name from the catalogue, quoted: no query builder takes an
		// identifier as a parameter.
		var dump *string
		if err := w.owner.QueryRow(ctx, fmt.Sprintf(`SELECT json_agg(t)::text FROM public.%q t`, table)).Scan(&dump); err != nil {
			w.t.Fatal(err)
		}
		if dump != nil {
			all.WriteString(*dump)
		}
	}
	return all.String()
}
