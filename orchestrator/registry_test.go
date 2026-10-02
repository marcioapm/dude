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
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
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
	// calls counts every GetAuthorizationToken, failed ones included.
	calls int
	// signers is the access key of the credentials each call was given
	// (options' Credentials), "" for the client's own.
	signers []string
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

func (f *fakeECR) signedBy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.signers...)
}

func (f *fakeECR) GetAuthorizationToken(ctx context.Context, _ *ecr.GetAuthorizationTokenInput, optFns ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	var o ecr.Options
	for _, fn := range optFns {
		fn(&o)
	}
	var signer string
	if o.Credentials != nil {
		c, err := o.Credentials.Retrieve(ctx)
		if err != nil {
			return nil, err
		}
		signer = c.AccessKeyID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	f.signers = append(f.signers, signer)
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
	w.syncer.Registry = registry.NewECR(ecrRegistry, api, api.now, registry.WithLog(quiet))
	mustExec(w.t, w.owner, `UPDATE projects SET runtime_image = $2 WHERE id = $1`, w.project, ecrImage)
	return api
}

// hang is an agent that never finishes its turn.
func hang(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }

// loginIn is the registry login among secrets, if one was sent.
func loginIn(secrets []lux.Secret) (string, bool) {
	for _, s := range secrets {
		if s.Name == "DUDE_REGISTRY_AUTH" {
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
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.pauseAndResume(wi)
	r := w.lux.Runs()[0]
	spec := submitted(t, r)
	if len(spec.Image.RegistryAuth) != 0 {
		t.Errorf("registryAuth = %+v", spec.Image.RegistryAuth)
	}
	for _, secrets := range append([][]lux.Secret{spec.Secrets}, r.ResumeSecrets...) {
		if _, ok := loginIn(secrets); ok {
			t.Errorf("a login was sent with none configured")
		}
	}
}

func TestAnAgentImageInTheFactorysRegistryIsPulledWithItsLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.lux.Decide = hang
	w.deliver(w.task())
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })

	spec := submitted(t, w.lux.Runs()[0])
	if spec.Image.Ref != ecrImage || len(spec.Image.RegistryAuth) != 1 ||
		spec.Image.RegistryAuth[0] != (lux.RegistryAuth{Registry: ecrRegistry, Secret: "DUDE_REGISTRY_AUTH"}) {
		t.Fatalf("image = %+v", spec.Image)
	}
	if v, _ := loginIn(spec.Secrets); v != "AWS:"+api.tokens()[0] {
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
	w.lux.Decide = hang
	w.deliver(w.task())
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })

	spec := submitted(t, w.lux.Runs()[0])
	if spec.Image.Ref != "ghcr.io/acme/agent:2" || len(spec.Image.RegistryAuth) != 1 || spec.Image.RegistryAuth[0].Registry != "ghcr.io" {
		t.Fatalf("image = %+v", spec.Image)
	}
	if v, _ := loginIn(spec.Secrets); v != "acme-bot:ghp_registry" {
		t.Errorf("DUDE_REGISTRY_AUTH = %q", v)
	}
}

// A project that names an image of its own elsewhere is not handed the
// factory's registry credential, at its start or at a resume.
func TestAnImageFromAnotherRegistryGetsNoLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = 'ghcr.io/someone/agent:1' WHERE id = $1`, w.project)
	w.lux.Decide = hang
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
		if _, ok := loginIn(secrets); ok {
			t.Errorf("a login was sent for an image in another registry")
		}
	}
	if len(api.tokens()) != 0 {
		t.Errorf("ECR was asked for %d tokens nobody needed", len(api.tokens()))
	}
}

// pauseAndResume pauses the task's running Run as a person would, and
// resumes it once lux has stopped it.
func (w *world) pauseAndResume(wi string) {
	w.t.Helper()
	runID := w.parked(wi)
	resumed := w.lux.Runs()[0].Resumed
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		w.t.Fatalf("resume: %d %v", status, out)
	}
	w.until("the resume", func() bool { return w.lux.Runs()[0].Resumed == resumed+1 })
}

// Every way a Run is resumed carries a login minted for it: the one it
// started with has passed its refresh point by then, as a token from
// yesterday would have expired.
func TestEveryResumeCarriesAFreshlyMintedLogin(t *testing.T) {
	for name, park := range map[string]func(w *world) (resume func()){
		"a person's pause": func(w *world) func() {
			w.lux.Decide = hang
			wi := w.task()
			w.deliver(wi)
			runID := w.parked(wi)
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
			w.lux.Decide = hang
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
				VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
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
			started, _ := loginIn(submitted(t, r).Secrets)
			if started != "AWS:"+api.tokens()[0] {
				t.Fatalf("started with %q, want ECR's first token", started)
			}
			// Parked past the cached token's refresh point.
			api.advance(11 * time.Hour)
			resume()
			w.until("the resume", func() bool { return r.Resumed == 1 })

			fresh, ok := loginIn(r.ResumeSecrets[0])
			if tokens := api.tokens(); !ok || len(tokens) != 2 || fresh != "AWS:"+tokens[1] {
				t.Errorf("resumed with %q (sent: %v), ECR minted %d; want the second token", fresh, ok, len(tokens))
			}
		})
	}
}

// A Run started before the factory logged in to a registry, resumed by an
// orchestrator restarted with an ECR login, is resumed as it was started:
// lux's spec names no login, so none is sent (lux would take an unnamed
// secret as the workload's) and ECR is not asked.
func TestARunStartedWithoutALoginResumesWithout(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = $2 WHERE id = $1`, w.project, ecrImage)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	api := &fakeECR{clock: time.Now()}
	w.restart(registry.NewECR(ecrRegistry, api, api.now))
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	r := w.lux.Runs()[0]
	w.until("the resume", func() bool { return r.Resumed == 1 })
	if _, ok := loginIn(r.ResumeSecrets[0]); ok || len(api.tokens()) != 0 {
		t.Errorf("a login was sent to resume a Run started without one (ECR minted %d)", len(api.tokens()))
	}
}

// parked waits for the task's Run to be working, pauses it as a person
// would, and waits for lux to stop it. Returns the Run's id.
func (w *world) parked(wi string) string {
	w.t.Helper()
	var runID string
	w.until("the agent to be working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi).Scan(&runID)
		return runID != ""
	})
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		w.t.Fatalf("pause: %d %v", status, out)
	}
	w.until("lux to stop it", func() bool {
		return w.lux.Runs()[0].State == "stopped" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	return runID
}

// A Run started with a host-minted ECR token, resumed after a restart that
// mints as a pull role, and again after one back to the host's
// credentials: each resume carries a token freshly minted by the identity
// configured at that moment.
func TestAResumeAfterARestartIsMintedByTheNewIdentity(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	r := w.lux.Runs
	for i, next := range []struct {
		provider registry.Provider
		signer   string
	}{
		{registry.NewECRWithRole(ecrRegistry, pullRole, api, &fakeSTS{}, api.now), "ASIAPULL1"},
		{registry.NewECR(ecrRegistry, api, api.now), ""},
	} {
		runID := w.parked(wi)
		w.restart(next.provider)
		w.call("/internal/runs/"+runID+"/resume", map[string]any{})
		w.until("the resume", func() bool { return r()[0].Resumed == i+1 })
		sent, _ := loginIn(r()[0].ResumeSecrets[i])
		tokens, signers := api.tokens(), api.signedBy()
		if len(tokens) != i+2 || sent != "AWS:"+tokens[i+1] {
			t.Fatalf("resume %d sent %q, ECR minted %d; want a token minted for it", i+1, sent, len(tokens))
		}
		if signers[i+1] != next.signer {
			t.Errorf("resume %d's token minted by %q, want %q", i+1, signers[i+1], next.signer)
		}
	}
	if s := api.signedBy(); s[0] != "" {
		t.Errorf("the submit's token minted by %q, want the host's credentials", s[0])
	}
}

// restart replaces the world's syncer with a new one, as an orchestrator
// restarted with another DUDE_REGISTRY_AUTH would build: same database and
// lux, provider p (nil for none), logging to the returned buffer.
func (w *world) restart(p registry.Provider) *bytes.Buffer {
	w.t.Helper()
	old := w.syncer
	old.Stop()
	var logs bytes.Buffer
	w.syncer = &phases.Syncer{DB: old.DB, Lux: old.Lux, Forges: old.Forges, Agent: old.Agent, Registry: p,
		Log: slog.New(slog.NewTextHandler(&logs, nil)), ParkAfter: old.ParkAfter, IdleAfter: old.IdleAfter,
		RetryAhead: old.RetryAhead}
	w.t.Cleanup(w.syncer.Stop)
	return &logs
}

// retried checks that Run runID is backed off by about want, that the
// sweep leaves it alone until then, and then lets the ordinary sweep take
// it up when it comes due — with the sweep's clock moved ahead
// (Syncer.RetryAhead) rather than the wait sat through or the deadline
// cleared.
func (w *world) retried(runID string, want time.Duration, taken func() bool) {
	w.t.Helper()
	var left float64
	if err := w.owner.QueryRow(context.Background(), `SELECT EXTRACT(EPOCH FROM next_attempt_at - now())::float8
		FROM runs WHERE id = $1 AND next_attempt_at IS NOT NULL`, runID).Scan(&left); err != nil {
		w.t.Fatalf("no back-off: %v", err)
	}
	if got := time.Duration(left * float64(time.Second)); got > want || got < want-3*time.Second {
		w.t.Fatalf("backed off %v, want about %v", got, want)
	}
	w.syncer.RetryAhead = want / 2
	for range 3 {
		w.pump()
	}
	if taken() {
		w.t.Fatalf("taken up before its back-off of %v was over", want)
	}
	w.syncer.RetryAhead = want + time.Second
	w.until("the retry once due", taken)
	w.syncer.RetryAhead = 0
}

// runOf is the id of the task's one Run.
func (w *world) runOf(taskID string) string {
	w.t.Helper()
	var id string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, taskID).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// A Run started with a login, parked, and resumed by an orchestrator that
// cannot supply that login (logins turned off, or another registry's) is
// not resumed and not failed: it stays paused, says which login it waits
// for, and resumes with a fresh one once that login is configured again.
func TestARunWaitsForTheLoginItWasStartedWith(t *testing.T) {
	ghcr, err := registry.NewStatic("ghcr.io", "acme-bot:ghp_registry")
	if err != nil {
		t.Fatal(err)
	}
	for name, restarted := range map[string]registry.Provider{
		"logins turned off":  nil,
		"another registry's": ghcr,
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			api := w.withECR()
			w.lux.Decide = hang
			wi := w.task()
			w.deliver(wi)
			runID := w.parked(wi)

			logs := w.restart(restarted)
			w.call("/internal/runs/"+runID+"/resume", map[string]any{})
			w.until("the resume to be held back", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at IS NOT NULL`, runID) == 1
			})
			r := w.lux.Runs()[0]
			if r.Resumed != 0 {
				t.Fatalf("lux was asked to resume a Run without its login (%d resumes)", r.Resumed)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND control = 'resume'`, runID); n != 1 {
				t.Fatalf("the Run is no longer paused awaiting its resume")
			}
			if got := logs.String(); !strings.Contains(got, ecrRegistry) || !strings.Contains(got, "DUDE_REGISTRY_AUTH") {
				t.Errorf("the log does not say which login to restore:\n%s", got)
			}

			api.advance(11 * time.Hour)
			w.restart(registry.NewECR(ecrRegistry, api, api.now))
			w.retried(runID, time.Minute, func() bool { return r.Resumed == 1 })
			fresh, _ := loginIn(r.ResumeSecrets[0])
			if tokens := api.tokens(); len(tokens) != 2 || fresh != "AWS:"+tokens[1] {
				t.Errorf("resumed with %q, ECR minted %d; want the second token", fresh, len(tokens))
			}
		})
	}
}

// A Run waiting a minute for its login and then aborted has its lux Run
// kept on the next sweep, not after the login retry comes due.
func TestAnAbortDoesNotWaitForTheLoginRetry(t *testing.T) {
	w := newWorld(t)
	w.withECR()
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	w.restart(nil)
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the login retry", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at > now() + interval '30 seconds'`, runID) == 1
	})
	var due time.Time
	_ = w.owner.QueryRow(context.Background(), `SELECT next_attempt_at FROM runs WHERE id = $1`, runID).Scan(&due)

	if status, out := w.call("/internal/runs/"+runID+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, out)
	}
	w.pump()
	// lux's Run is stopped, which the syncer treats as over (ask skips the
	// call): the sweep's keep shows as the recorded stop reason.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept' AND control = 'none'`, runID); n != 1 {
		t.Fatalf("the abort was not acted on by the sweep after it")
	}
	var still time.Time
	_ = w.owner.QueryRow(context.Background(), `SELECT next_attempt_at FROM runs WHERE id = $1`, runID).Scan(&still)
	if !still.Equal(due) || !time.Now().Before(due) {
		t.Errorf("next_attempt_at = %v, was %v: the retry deadline moved or had passed", still, due)
	}
}

// ECR that cannot be reached holds a start back rather than failing it,
// and the Run goes ahead when it answers.
func TestAnECROutageDelaysARunAndDoesNotFailIt(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	api.setFail(errors.New("no EC2 IMDS role found"))
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the submit to be held back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND next_attempt_at IS NOT NULL`, wi) == 1
	})
	if len(w.lux.Runs()) != 0 || w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi) != 0 {
		t.Fatalf("a Run went ahead or failed without its login")
	}
	api.setFail(nil)
	api.advance(registry.FirstRetry) // past the provider's back-off
	w.retried(w.runOf(wi), 5*time.Second, func() bool { return len(w.lux.Runs()) == 1 })
}

func (f *fakeECR) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Runs waiting on a failing login — three phase Runs and a preview to be
// submitted, and a parked Run to be resumed — share the provider's one
// back-off: sweeps every five seconds for ten minutes ask ECR once per
// back-off step, not once per Run per sweep. None is submitted, resumed
// or failed meanwhile, and all go ahead once ECR answers.
func TestRunsWaitingOnAFailingLoginShareOneBackOff(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	// The top of each delay's range: steps of 5, 10, 20… seconds exactly.
	w.syncer.Registry = registry.NewECR(ecrRegistry, api, api.now, registry.WithLog(quiet),
		registry.WithJitter(func() float64 { return 0.999999999 }))
	w.previews.Registry = w.syncer.Registry
	w.lux.Decide = hang

	parked := w.task()
	w.deliver(parked)
	parkedRun := w.parked(parked)
	api.advance(11 * time.Hour) // its token past the refresh point
	api.setFail(errors.New("dial tcp: lookup api.ecr.eu-west-1.amazonaws.com: i/o timeout"))
	w.call("/internal/runs/"+parkedRun+"/resume", map[string]any{})
	var pending []string
	for range 3 {
		wi := w.task()
		w.deliver(wi)
		pending = append(pending, wi)
	}
	previewTask := w.task()
	if code, out := w.do("POST", "/internal/tasks/"+previewTask+"/preview", nil); code != 201 {
		t.Fatalf("start preview = %d %v", code, out)
	}
	w.until("every Run to wait on its login", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE organization_id = $1 AND next_attempt_at IS NOT NULL`, w.org) == 5
	})

	// Ten minutes of sweeps five seconds apart, each Run due every time.
	before := api.attempts()
	for range 120 {
		mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE organization_id = $1`, w.org)
		w.pump()
		api.advance(5 * time.Second)
	}
	// 5, 15, 35, 75, 155, 315 and 615 seconds from the first failure.
	if n := api.attempts() - before; n > 7 {
		t.Fatalf("ECR asked %d times in ten minutes by 5 waiting Runs, want at most 7", n)
	}
	if len(w.lux.Runs()) != 1 || w.lux.Runs()[0].Resumed != 0 {
		t.Fatalf("a Run went ahead without its login: %d lux Runs", len(w.lux.Runs()))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE organization_id = $1 AND status = 'failed'`, w.org); n != 0 {
		t.Fatalf("%d Runs failed while waiting on their login:\n%s", n, w.describeRuns())
	}

	api.setFail(nil)
	api.advance(registry.MaxRetry)
	w.until("every Run to go ahead", func() bool {
		mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE organization_id = $1`, w.org)
		return len(w.lux.Runs()) == 5 && w.lux.Runs()[0].Resumed == 1
	})
	tokens := api.tokens()
	if len(tokens) != 2 {
		t.Fatalf("ECR minted %d tokens, want the first submit's and one after the outage", len(tokens))
	}
	for _, r := range w.lux.Runs()[1:] {
		if v, _ := loginIn(submitted(t, r).Secrets); v != "AWS:"+tokens[1] {
			t.Errorf("a Run submitted with %q, want the token minted after the outage", v)
		}
	}
	if v, _ := loginIn(w.lux.Runs()[0].ResumeSecrets[0]); v != "AWS:"+tokens[1] {
		t.Errorf("the parked Run resumed with %q, want the token minted after the outage", v)
	}
}

const pullRole = "arn:aws:iam::123456789012:role/dude-ecr-pull"

// fakeSTS is STS's AssumeRole for pullRole: credentials numbered by call,
// valid an hour of wall-clock time (the SDK's cache reads the real clock).
type fakeSTS struct {
	mu    sync.Mutex
	calls int
	fail  error
}

func (f *fakeSTS) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	if aws.ToString(in.RoleArn) != pullRole {
		return nil, fmt.Errorf("AccessDenied: no role %s", aws.ToString(in.RoleArn))
	}
	f.calls++
	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId:     aws.String(fmt.Sprintf("ASIAPULL%d", f.calls)),
		SecretAccessKey: aws.String("pull-secret"), SessionToken: aws.String("pull-session"),
		Expiration: aws.Time(time.Now().Add(time.Hour)),
	}}, nil
}

// An AssumeRole that fails at submit holds the Run back: no lux Run, not
// failed, and once STS answers it is submitted with a token minted as the
// role.
func TestAFailedAssumeRoleDelaysASubmitAndDoesNotFailIt(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	roles := &fakeSTS{}
	roles.setFail(errors.New("AccessDenied: not authorized to perform sts:AssumeRole"))
	w.syncer.Registry = registry.NewECRWithRole(ecrRegistry, pullRole, api, roles, api.now)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the submit to be held back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND next_attempt_at IS NOT NULL`, wi) == 1
	})
	if len(w.lux.Runs()) != 0 || len(api.tokens()) != 0 {
		t.Fatalf("%d lux Runs, %d ECR tokens while AssumeRole failed; want none", len(w.lux.Runs()), len(api.tokens()))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'pending'`, wi); n != 1 {
		t.Fatalf("the Run is no longer pending")
	}

	roles.setFail(nil)
	api.advance(registry.FirstRetry) // past the provider's back-off
	w.retried(w.runOf(wi), 5*time.Second, func() bool { return len(w.lux.Runs()) == 1 })
	sent, _ := loginIn(submitted(t, w.lux.Runs()[0]).Secrets)
	if tokens, signers := api.tokens(), api.signedBy(); len(tokens) != 1 || sent != "AWS:"+tokens[0] || signers[0] != "ASIAPULL1" {
		t.Errorf("submitted with %q; ECR minted %d, signed by %q; want one token minted as the role", sent, len(tokens), signers)
	}
}

// An AssumeRole that fails is an ECR outage: a paused Run's resume waits,
// still paused, and goes ahead with a token minted as the role once STS
// answers. No token is minted with the host's credentials meanwhile.
func TestAFailedAssumeRoleDelaysAResumeAndDoesNotFailIt(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	roles := &fakeSTS{}
	w.syncer.Registry = registry.NewECRWithRole(ecrRegistry, pullRole, api, roles, api.now)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)

	// Past the token's refresh point, with the role's credentials gone
	// (as if expired) and STS refusing.
	api.advance(11 * time.Hour)
	w.syncer.Registry = registry.NewECRWithRole(ecrRegistry, pullRole, api, roles, api.now)
	roles.setFail(errors.New("AccessDenied: not authorized to perform sts:AssumeRole"))
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the resume to be held back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at IS NOT NULL`, runID) == 1
	})
	r := w.lux.Runs()[0]
	if r.Resumed != 0 {
		t.Fatalf("lux was asked to resume without a login (%d resumes)", r.Resumed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND control = 'resume'`, runID); n != 1 {
		t.Fatalf("the Run is no longer paused awaiting its resume")
	}
	if len(api.tokens()) != 1 {
		t.Fatalf("ECR minted %d tokens while AssumeRole failed; want only the submit's", len(api.tokens()))
	}

	roles.setFail(nil)
	api.advance(registry.FirstRetry) // past the provider's back-off
	w.retried(runID, 5*time.Second, func() bool { return r.Resumed == 1 })
	fresh, _ := loginIn(r.ResumeSecrets[0])
	tokens := api.tokens()
	if len(tokens) != 2 || fresh != "AWS:"+tokens[1] {
		t.Fatalf("resumed with %q, ECR minted %d; want the second token", fresh, len(tokens))
	}
	if got := api.signedBy(); len(got) != 2 || got[0] != "ASIAPULL1" || got[1] != "ASIAPULL2" {
		t.Errorf("tokens minted with %q; want each with the assumed role's credentials", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID); n != 0 {
		t.Errorf("the Run failed")
	}
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
	api.advance(registry.FirstRetry) // past the provider's back-off
	w.retried(w.runOf(wi), 5*time.Second, func() bool { return len(w.lux.Runs()) == 1 })

	runID := w.parked(wi)
	api.advance(11 * time.Hour)
	api.setFail(errors.New("ExpiredTokenException"))
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the failed login to hold the resume back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at IS NOT NULL`, runID) == 1
	})
	api.setFail(nil)
	api.advance(registry.FirstRetry) // past the provider's back-off
	w.retried(runID, 5*time.Second, func() bool { return w.lux.Runs()[0].Resumed == 1 })
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
	if v, _ := loginIn(r.ResumeSecrets[0]); v != "AWS:"+tokens[1] {
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
