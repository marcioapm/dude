package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/images"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

const (
	imageLayer = "registry.test/dude/layer@sha256:aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000"
	nextLayer  = "registry.test/dude/layer@sha256:bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000"
	userRef    = "registry.test/dude/custom@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	finalRef   = "registry.test/dude/custom@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// libraryImage adds an image to w's organization with version 1 published
// and, when final is set, finished with imageLayer.
func (w *world) libraryImage(id, name string, final bool) string {
	w.t.Helper()
	version := "imv_" + id
	mustExec(w.t, w.owner, `INSERT INTO images (id, organization_id, name) VALUES ($1, $2, $3)`, id, w.org, name)
	mustExec(w.t, w.owner, `INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state, user_ref)
		VALUES ($1, $2, $3, 1, 'FROM debian', 'published', $4)`, version, w.org, id, userRef)
	mustExec(w.t, w.owner, `UPDATE images SET published_version_id = $2 WHERE id = $1`, id, version)
	if final {
		mustExec(w.t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
			VALUES ($1, $2, $3, $4)`, w.org, version, imageLayer, finalRef)
	}
	return version
}

func (w *world) useLayer(layer string) {
	w.syncer.Agent.Layer = layer
	w.previews.Layer = layer
}

// finishJobs are the organization's finish jobs, as "state layer".
func (w *world) finishJobs() []string {
	rows, err := w.owner.Query(context.Background(), `SELECT state || ' ' || layer_ref FROM image_builds
		WHERE organization_id = $1 AND kind = 'finish' ORDER BY requested_at`, w.org)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (w *world) runImage(wi, phase string) images.RunImage {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT image FROM runs WHERE task_id = $1 AND phase::text = $2`, wi, phase).Scan(&raw); err != nil {
		w.t.Fatal(err)
	}
	var got images.RunImage
	_ = json.Unmarshal(raw, &got)
	return got
}

// The organization's default base, published and finished with the
// current layer: every Run gets its final image by digest and records it.
func TestARunGetsItsLibraryImagesFinalAndRecordsIt(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	version := w.libraryImage("img_base", "acme-base", true)
	mustExec(t, w.owner, `UPDATE organizations SET default_image_id = 'img_base' WHERE id = $1`, w.org)
	// The typed image from before the library loses to any library image.
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	if got := w.specOf("implement").Image.Ref; got != finalRef {
		t.Errorf("image = %s, want the final %s", got, finalRef)
	}
	want := images.RunImage{ImageID: "img_base", Name: "acme-base", VersionID: version, Version: 1, Ref: finalRef, Layer: imageLayer}
	if got := w.runImage(wi, "implement"); got != want {
		t.Errorf("runs.image = %+v, want %+v", got, want)
	}
}

// A role's image wins over the project's; the fixer follows the implementer.
func TestARolesImageWinsOverTheProjects(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	w.libraryImage("img_base", "acme-base", true)
	w.libraryImage("img_qa", "playwright", true)
	mustExec(t, w.owner, `UPDATE images SET published_version_id = NULL WHERE id = 'img_qa'`)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base',
		agent_models = jsonb_set(agent_models, '{reviewer,image}', '"img_gone"') WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("a reviewer to reach lux", func() bool { return w.specOf("review") != nil })
	// The reviewer names an image that is gone: skipped, the project's.
	if got := w.runImage(wi, "review").ImageID; got != "img_base" {
		t.Errorf("reviewer ran in %s", got)
	}
}

// With no final for the current layer, the Run waits — pending, no lux Run —
// on a finish job two Runs share, then submits once the builder made it.
func TestARunWaitsForItsDudeLayerThenStarts(t *testing.T) {
	w := newWorld(t)
	w.useLayer(nextLayer)
	version := w.libraryImage("img_base", "acme-base", true)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base' WHERE id = $1`, w.project)
	a, b := w.task(), w.task()
	w.deliver(a)
	w.deliver(b)
	w.until("both implementers to wait on a finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE organization_id = $1 AND phase = 'implement' AND image_build_id IS NOT NULL
			AND status = 'pending' AND lux_run_id IS NULL`, w.org) == 2
	})
	if got := w.finishJobs(); len(got) != 1 || got[0] != "queued "+nextLayer {
		t.Fatalf("finish jobs = %v, want one for the new layer", got)
	}
	if n := len(w.lux.Runs()); n != 0 {
		t.Fatalf("%d Runs reached lux before their image", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = $2`, w.org, phases.EvImagePreparing); n != 2 {
		t.Errorf("%d preparing events, want one per Run", n)
	}
	// The builder finishes it.
	mustExec(t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, $2, $3, $4)`,
		w.org, version, nextLayer, "registry.test/dude/custom@sha256:3333333333333333333333333333333333333333333333333333333333333333")
	mustExec(t, w.owner, `UPDATE image_builds SET state = 'succeeded' WHERE organization_id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE organization_id = $1`, w.org)
	w.until("both to reach lux", func() bool { return len(w.lux.Runs()) >= 2 })
	for _, r := range w.lux.Runs() {
		if got := submitted(t, r).Image.Ref; !strings.HasSuffix(got, "@sha256:3333333333333333333333333333333333333333333333333333333333333333") {
			t.Errorf("submitted on %s", got)
		}
	}
}

// A failed finish fails the Run before lux, with the build's sentence.
func TestAFailedFinishFailsTheRunBeforeLux(t *testing.T) {
	w := newWorld(t)
	w.useLayer(nextLayer)
	w.libraryImage("img_base", "acme-base", false)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base' WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to wait", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND image_build_id IS NOT NULL`, wi) == 1
	})
	mustExec(t, w.owner, `UPDATE image_builds SET state = 'failed', error = 'the image needs git: agents commit with it' WHERE organization_id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE task_id = $1`, wi)
	w.until("the implementer to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, wi) == 1
	})
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&reason)
	if reason != "cannot start: its image acme-base v1 could not get the dude layer: the image needs git: agents commit with it" {
		t.Errorf("error = %q", reason)
	}
	if n := len(w.lux.Runs()); n != 0 {
		t.Errorf("%d lux Runs", n)
	}
}

// Without DUDE_LAYER_IMAGE the library is off: a Run on a library image
// fails before lux saying so, and one on a typed image runs as before.
func TestWithNoLayerALibraryImageFailsAndATypedOneRuns(t *testing.T) {
	w := newWorld(t)
	w.libraryImage("img_base", "acme-base", true)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	if got := w.specOf("implement").Image.Ref; got != "agent:test" {
		t.Errorf("typed image: ran %s", got)
	}
	if got := w.runImage(wi, "implement"); got != (images.RunImage{}) {
		t.Errorf("a typed image recorded %+v", got)
	}

	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base' WHERE id = $1`, w.project)
	other := w.task()
	w.deliver(other)
	w.until("its implementer to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, other) == 1
	})
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1`, other).Scan(&reason)
	if reason != "cannot start: "+images.ErrNotConfigured {
		t.Errorf("error = %q", reason)
	}
}

// Nothing set anywhere: DUDE_AGENT_IMAGE, unchanged.
func TestNothingNamedRunsTheAgentImage(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULL WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	if got := w.specOf("implement").Image.Ref; got != "default:img" {
		t.Errorf("ran %s", got)
	}
}

// A resumed Run keeps the image it started with, whatever was published since.
func TestAResumedRunKeepsItsImage(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	w.libraryImage("img_base", "acme-base", true)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base' WHERE id = $1`, w.project)
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	runID := w.parked(wi)
	// v2 published and finished: a new Run would get it; this one does not.
	mustExec(t, w.owner, `INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state, user_ref)
		VALUES ('imv_v2', $1, 'img_base', 2, 'FROM debian', 'published', $2)`, w.org, userRef)
	mustExec(t, w.owner, `UPDATE image_versions SET state = 'superseded' WHERE id = 'imv_img_base'`)
	mustExec(t, w.owner, `UPDATE images SET published_version_id = 'imv_v2' WHERE id = 'img_base'`)
	mustExec(t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, 'imv_v2', $2, 'x@sha256:9')`,
		w.org, imageLayer)
	r := w.lux.Runs()[0]
	if status, out := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, out)
	}
	for i := 0; r.Resumed == 0 && i < 200; i++ {
		w.pump()
		time.Sleep(10 * time.Millisecond)
	}
	if r.Resumed != 1 {
		t.Fatalf("not resumed\n%s", w.describeRuns())
	}
	if got := submitted(t, r).Image.Ref; got != finalRef {
		t.Errorf("lux's spec = %s", got)
	}
	if got := w.runImage(wi, "implement"); got.Version != 1 || got.Ref != finalRef {
		t.Errorf("runs.image = %+v", got)
	}
}

// A branch preview resolves its image the same way: its own library
// image, waiting for the layer and then running on its final.
func TestABranchPreviewWaitsForItsImageThenRuns(t *testing.T) {
	w := newWorld(t)
	w.useLayer(nextLayer)
	version := w.libraryImage("img_web", "web", true)
	mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_web', preview_settings = '{"image":"typed:old"}' WHERE id = $1`, w.project)
	wi := w.task()
	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("the preview to wait", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND image_build_id IS NOT NULL AND lux_run_id IS NULL`, runID) == 1
	})
	mustExec(t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, $2, $3, $4)`,
		w.org, version, nextLayer, finalRef)
	mustExec(t, w.owner, `UPDATE image_builds SET state = 'succeeded' WHERE organization_id = $1`, w.org)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the preview to reach lux", func() bool { return len(w.lux.Runs()) == 1 })
	if got := submitted(t, w.lux.Runs()[0]).Image.Ref; got != finalRef {
		t.Errorf("preview image = %s", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND image->>'name' = 'web'`, runID); n != 1 {
		t.Error("the preview did not record its image")
	}
}

// Another organization's image is no image here: the database refuses
// the reference, and an id that names none fails the Run.
func TestAnotherOrganizationsImageCannotBeUsed(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	other := "org_other_" + w.org
	mustExec(t, w.owner, `INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, other)
	mustExec(t, w.owner, `INSERT INTO images (id, organization_id, name) VALUES ('img_theirs', $1, 'theirs')`, other)
	if _, err := w.owner.Exec(context.Background(), `UPDATE projects SET runtime_image_id = 'img_theirs' WHERE id = $1`, w.project); err == nil {
		t.Fatal("a project named another organization's image")
	}
	// A role's image is JSON, unchecked by the database: RLS hides theirs.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,image}', '"img_theirs"') WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	if got := w.specOf("implement").Image.Ref; got != "agent:test" {
		t.Errorf("ran %s, want the project's own typed image", got)
	}
}

// A library image lives in the same ECR registry as DUDE_AGENT_IMAGE
// (dude/custom beside dude/agents): the Run's pull carries the same
// login, so the pull-only role covers it with no other setting.
func TestALibraryImageInTheAgentImagesRegistryIsPulledWithItsLogin(t *testing.T) {
	w := newWorld(t)
	api := w.withECR()
	w.useLayer(imageLayer)
	version := w.libraryImage("img_base", "acme-base", false)
	custom := ecrRegistry + "/dude/custom@sha256:4444444444444444444444444444444444444444444444444444444444444444"
	mustExec(t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref) VALUES ($1, $2, $3, $4)`,
		w.org, version, imageLayer, custom)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base' WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	spec := submitted(t, w.lux.Runs()[0])
	if spec.Image.Ref != custom || len(spec.Image.RegistryAuth) != 1 || spec.Image.RegistryAuth[0].Registry != ecrRegistry {
		t.Fatalf("image = %+v", spec.Image)
	}
	if v, _ := loginIn(spec.Secrets); v != "AWS:"+api.tokens()[0] {
		t.Errorf("login = %q", v)
	}
}
