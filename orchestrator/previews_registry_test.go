package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the submit", func() bool { return len(w.lux.Runs()) == 1 })
	if v, _ := loginIn(submitted(t, w.lux.Runs()[0]).Secrets); v != "AWS:"+api.tokens()[0] {
		t.Errorf("DUDE_REGISTRY_AUTH = %q", v)
	}
}

// A preview started with a login, resumed by an orchestrator that no longer
// logs in there, is left parked rather than resumed without it (lux would
// refuse the resume, which fails the preview for good).
func TestAParkedPreviewWaitsForTheLoginItStartedWith(t *testing.T) {
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
	other, err := registry.NewStatic("ghcr.io", "bot:tok")
	if err != nil {
		t.Fatal(err)
	}
	w.previews.Registry = other
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "docs"}); code != 201 {
		t.Fatalf("add docs while parked = %d %v", code, body)
	}
	w.pump()
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := w.lux.Runs()[0]; r.Resumed != 0 {
		t.Errorf("resumed without the login it was started with: %+v", r.ResumeSecrets)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID); n != 1 {
		t.Errorf("the preview is no longer parked: %s", w.describeRuns())
	}
}
