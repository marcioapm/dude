package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// A branch preview runs the agent image when its project names no preview
// image: its submit, and the resume that wakes it after a park, carry the
// registry login as an agent Run's do, minted for each start.
func TestABranchPreviewPullsTheAgentImageWithTheLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.previews.Registry = w.syncer.Registry
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("docs", 4000, "npm run docs", "docs", nil, false)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":5}' WHERE id = $1`, w.project)
	wi := w.task()

	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the preview to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	r := w.lux.Runs()[0]
	spec := submitted(t, r)
	if spec.Image.Ref != ecrImage || len(spec.Image.RegistryAuth) != 1 ||
		spec.Image.RegistryAuth[0] != (lux.RegistryAuth{Registry: ecrRegistry, Secret: "DUDE_REGISTRY_AUTH"}) {
		t.Fatalf("preview image = %+v", spec.Image)
	}
	if v, _ := loginIn(spec.Secrets); v != "AWS:"+api.tokens()[0] {
		t.Errorf("preview submitted with DUDE_REGISTRY_AUTH = %q, want ECR's first token", v)
	}
	var sent struct {
		Secrets json.RawMessage `json:"secrets"`
	}
	if err := json.Unmarshal(r.Spec, &sent); err != nil {
		t.Fatal(err)
	}
	checkPreviewSecrets(t, "submit", sent.Secrets, "AWS:"+api.tokens()[0])

	// Parked, then woken past the first token's refresh point.
	w.previews.Minute = time.Millisecond
	w.until("the preview to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	api.advance(11 * time.Hour)
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "docs"}); code != 201 {
		t.Fatalf("add docs while parked = %d %v", code, body)
	}
	w.until("the preview to be resumed", func() bool { return r.Resumed == 1 })
	fresh, ok := loginIn(r.ResumeSecrets[0])
	if tokens := api.tokens(); !ok || len(tokens) != 2 || fresh != "AWS:"+tokens[1] {
		t.Errorf("preview resumed with %q (sent: %v), ECR minted %d; want the second token", fresh, ok, len(tokens))
	}
	checkPreviewSecrets(t, "resume", r.ResumeSecretsRaw[0], "AWS:"+api.tokens()[1])
}

var updatePreview = flag.Bool("update", false, "rewrite testdata/preview-*.golden from what the preview submits")

// What dude submits to lux for a branch preview of the agent image in the
// factory's ECR registry, byte for byte as the fake lux received it, with
// the test's generated ids replaced: image.registryAuth, the runner-only
// login beside GIT_TOKEN, the generic workload and its autostart server.
// The phase golden (internal/phases/testdata) does not cover it: a preview
// is built by servers.Previews, not buildSpec.
func TestThePreviewSpecIsTheGoldenOne(t *testing.T) {
	w := newWorld(t)
	w.withECR()
	w.previews.Registry = w.syncer.Registry
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("docs", 4000, "npm run docs", "docs", nil, false)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":5,"egress":["registry.npmjs.org"]}' WHERE id = $1`, w.project)
	wi := w.task()
	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the preview to be submitted", func() bool { return len(w.lux.Runs()) == 1 })

	var indented bytes.Buffer
	if err := json.Indent(&indented, w.lux.Runs()[0].Spec, "", "  "); err != nil {
		t.Fatal(err)
	}
	got := strings.NewReplacer(runID, "<run>", wi, "<task>", w.org, "<org>").Replace(indented.String()) + "\n"
	path := filepath.Join("testdata", "preview-registry.golden")
	if *updatePreview {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("preview spec differs from %s:\n got: %s\nwant: %s", path, got, want)
	}
}

// checkPreviewSecrets checks a preview's submit or resume secrets as sent,
// every field of each: the registry login, with no way into the container
// (no as, no path, no runnerOnly: lux makes a secret image.registryAuth
// names runner-only), and the forge token, separate, as the repositories'
// credential.
func checkPreviewSecrets(t *testing.T, what string, raw json.RawMessage, login string) {
	t.Helper()
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s secrets: %v in %s", what, err, raw)
	}
	want := []map[string]any{
		{"name": "GIT_TOKEN", "value": "ghp_test"},
		{"name": "DUDE_REGISTRY_AUTH", "value": login},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s secrets = %s\nwant the forge token and the registry login, names and values only", what, raw)
	}
}

// A preview of an image from another registry (node:22 from Docker Hub) is
// not handed the factory's registry credential, and ECR is not asked.
func TestAPreviewOfAnotherRegistrysImageGetsNoLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.previews.Registry = w.syncer.Registry
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"image":"node:22"}' WHERE id = $1`, w.project)
	wi := w.task()
	if code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil); code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	w.until("the preview to be submitted", func() bool { return len(w.lux.Runs()) == 1 })
	spec := submitted(t, w.lux.Runs()[0])
	if _, ok := loginIn(spec.Secrets); ok || len(spec.Image.RegistryAuth) != 0 || len(api.tokens()) != 0 {
		t.Errorf("image %s sent with a login: %+v", spec.Image.Ref, spec.Image.RegistryAuth)
	}
}

// ECR unreachable holds a preview's submit back, and it goes ahead once ECR
// answers: never submitted without its login, never failed for it.
func TestAnECROutageDelaysAPreview(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.previews.Registry = w.syncer.Registry
	api.setFail(errors.New("no EC2 IMDS role found"))
	wi := w.task()
	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the submit to be held back", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'pending' AND next_attempt_at IS NOT NULL`, runID) == 1
	})
	if len(w.lux.Runs()) != 0 {
		t.Fatalf("a preview was submitted without its login")
	}
	api.setFail(nil)
	api.advance(registry.FirstRetry) // past the provider's back-off
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the submit", func() bool { return len(w.lux.Runs()) == 1 })
	if v, _ := loginIn(submitted(t, w.lux.Runs()[0]).Secrets); v != "AWS:"+api.tokens()[0] {
		t.Errorf("DUDE_REGISTRY_AUTH = %q", v)
	}
}

// countingLux is the world's lux client, counting Gets and Resumes; a
// set refuseResume answers the next Resume instead of lux.
type countingLux struct {
	lux.Client
	mu            sync.Mutex
	gets, resumes int
	refuseResume  error
}

func (c *countingLux) Get(ctx context.Context, id string) (lux.Run, error) {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
	return c.Client.Get(ctx, id)
}

func (c *countingLux) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	c.mu.Lock()
	c.resumes++
	refuse := c.refuseResume
	c.refuseResume = nil
	c.mu.Unlock()
	if refuse != nil {
		return lux.Run{}, refuse
	}
	return c.Client.Resume(ctx, id, in)
}

func (c *countingLux) counts() (gets, resumes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.resumes
}

// countingForges counts forge credential lookups.
type countingForges struct {
	delivery.Forges
	n atomic.Int64
}

func (c *countingForges) For(ctx context.Context, org string) (*forge.GitHub, error) {
	c.n.Add(1)
	return c.Forges.For(ctx, org)
}

// countPreviewCalls routes the previews loop's lux calls and forge lookups
// through counters.
func (w *world) countPreviewCalls() (*countingLux, *countingForges) {
	l := &countingLux{Client: w.previews.Lux}
	f := &countingForges{Forges: w.previews.Forges}
	w.previews.Lux, w.previews.Forges = l, f
	return l, f
}

// parkedPreview starts a preview of the ECR agent image, waits for it to
// be parked, and asks for its docs server, which wakes it. Returns the
// preview's Run id.
func (w *world) parkedPreview() string {
	w.t.Helper()
	w.previews.Minute = time.Millisecond
	w.recipe("docs", 4000, "npm run docs", "docs", nil, false)
	mustExec(w.t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":5}' WHERE id = $1`, w.project)
	wi := w.task()
	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	if code != 201 {
		w.t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the preview to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	return runID
}

// A preview started with a login, woken on an orchestrator that no longer
// logs in there, stays parked: its resume is not sent (lux would refuse it,
// which fails the preview for good), and it is checked again every
// phases.LoginRetry, not every sweep — no lux Get, no forge lookup between.
// Once the login is configured again, it resumes with a fresh one.
func TestAParkedPreviewWaitsForTheLoginItStartedWith(t *testing.T) {
	ghcr, err := registry.NewStatic("ghcr.io", "bot:tok")
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
			ecrLogin := w.syncer.Registry
			w.previews.Registry = ecrLogin
			runID := w.parkedPreview()
			calls, forges := w.countPreviewCalls()
			w.previews.Registry = restarted
			if code, body := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "docs"}); code != 201 {
				t.Fatalf("add docs while parked = %d %v", code, body)
			}

			// Ten sweeps five seconds apart: under a minute in all.
			for range 10 {
				if _, err := w.previews.Sweep(context.Background()); err != nil {
					t.Fatal(err)
				}
				mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = next_attempt_at - interval '5 seconds' WHERE id = $1`, runID)
			}
			if gets, resumes := calls.counts(); gets != 1 || resumes != 0 || forges.n.Load() != 0 {
				t.Errorf("in under a minute of sweeps: %d lux Gets, %d Resumes, %d forge lookups; want 1, 0, 0",
					gets, resumes, forges.n.Load())
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND pending_starts = '{docs}'
				AND next_attempt_at > now()`, runID); n != 1 {
				t.Fatalf("the preview is no longer parked awaiting its login: %s", w.describeRuns())
			}

			// Restored, and past the retry.
			api.advance(11 * time.Hour)
			w.previews.Registry = ecrLogin
			mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = next_attempt_at - interval '1 minute' WHERE id = $1`, runID)
			r := w.lux.Runs()[0]
			w.until("the preview to be resumed", func() bool { return r.Resumed == 1 })
			fresh, _ := loginIn(r.ResumeSecrets[0])
			if tokens := api.tokens(); len(tokens) != 2 || fresh != "AWS:"+tokens[1] {
				t.Errorf("resumed with %q, ECR minted %d; want the second token", fresh, len(tokens))
			}
			w.until("the docs server to start", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND pending_starts = '{}'`, runID) == 1
			})
		})
	}
}

// A parked preview whose lux Run lux no longer has fails when a person
// wakes it, as a refused resume does; it is not retried for good.
func TestAPreviewLuxLostFailsWhenWoken(t *testing.T) {
	w := newWorld(t)
	w.withECR()
	w.previews.Registry = w.syncer.Registry
	w.previews.Minute = time.Millisecond
	w.recipe("docs", 4000, "npm run docs", "docs", nil, false)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":5}' WHERE id = $1`, w.project)
	wi := w.task()
	_, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the preview to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	mustExec(t, w.owner, `UPDATE runs SET pending_starts = '{docs}' WHERE id = $1`, runID)
	w.lux.Forget()
	w.until("the preview to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1
	})
}
