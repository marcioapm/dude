package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// conductorSubmitted is the spec the fake lux recorded for the task's
// conductor, started by a message in its Chat.
func (w *world) conductorSubmitted(task string) lux.Spec {
	w.t.Helper()
	// The organisation's seeded tiers name no model until an admin sets one.
	mustExec(w.t, w.owner, `UPDATE model_tiers SET model = 'fake/scripted' WHERE organization_id = $1 AND name = 'Thinker'
		AND model IS NULL`, w.org)
	if status, out := w.chat(task, "what is this task about?"); status != 201 {
		w.t.Fatalf("chat: %d %v", status, out)
	}
	var spec lux.Spec
	_ = json.Unmarshal([]byte(w.conductorSpecOf(task)), &spec)
	return spec
}

// What a conductor Run is submitted to lux with: the organisation's Small
// machine, its Thinker tier unless the project names another, and its
// image by the chain role → project's library image → organisation's
// default → DUDE_AGENT_IMAGE.
func TestWhatAConductorIsSubmittedWith(t *testing.T) {
	small := lux.Resources{CPUs: 0.5, Memory: 1 << 30, Disk: 10 << 30}

	t.Run("Small, Thinker, the agent image", func(t *testing.T) {
		w := newWorld(t)
		// No image named anywhere: DUDE_AGENT_IMAGE.
		mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULL WHERE id = $1`, w.project)
		task := w.task()
		spec := w.conductorSubmitted(task)
		if spec.Resources == nil || *spec.Resources != small || spec.Placement != nil {
			t.Errorf("resources %+v placement %+v, want Small in the default pool", spec.Resources, spec.Placement)
		}
		id, _, _ := w.conductor(task)
		var name, from, tier, model, thinker string
		if err := w.owner.QueryRow(context.Background(), `SELECT r.machine->>'name', r.machine->>'from', r.model_tier, r.model, t.model
			FROM runs r JOIN model_tiers t ON t.organization_id = r.organization_id AND t.name = 'Thinker' WHERE r.id = $1`, id).
			Scan(&name, &from, &tier, &model, &thinker); err != nil {
			t.Fatal(err)
		}
		if name != "Small" || from != "organization" {
			t.Errorf("runs.machine is %s from %s, want Small from organization", name, from)
		}
		if tier != "Thinker" || model != thinker || spec.Labels["dude.model_tier"] != "Thinker" || spec.Labels["dude.model"] != thinker {
			t.Errorf("tier %s model %s (labels %s %s), want Thinker's %s", tier, model,
				spec.Labels["dude.model_tier"], spec.Labels["dude.model"], thinker)
		}
		if spec.Image.Ref != "default:img" {
			t.Errorf("image %s, want DUDE_AGENT_IMAGE", spec.Image.Ref)
		}
	})

	t.Run("a project's tier wins", func(t *testing.T) {
		w := newWorld(t)
		w.onModel("conductor", "claude-opus-5-5")
		spec := w.conductorSubmitted(w.task())
		if spec.Labels["dude.model_tier"] != onModelTier("claude-opus-5-5") || spec.Labels["dude.model"] != "claude-opus-5-5" {
			t.Errorf("tier %s model %s, want the project's", spec.Labels["dude.model_tier"], spec.Labels["dude.model"])
		}
		if spec.Resources == nil || *spec.Resources != small {
			t.Errorf("resources %+v, want Small", spec.Resources)
		}
	})

	t.Run("the image chain", func(t *testing.T) {
		w := newWorld(t)
		w.useLayer(imageLayer)
		// Each link's image finished to a ref of its own, so the spec says
		// which link it was submitted from.
		finals := map[string]string{}
		for i, id := range []string{"img_role", "img_proj", "img_org"} {
			version := w.libraryImage(id, strings.TrimPrefix(id, "img_")+"-image", false)
			finals[id] = fmt.Sprintf("registry.test/dude/%s@sha256:%064d", id, i+3)
			mustExec(t, w.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
				VALUES ($1, $2, $3, $4)`, w.org, version, imageLayer, finals[id])
		}
		mustExec(t, w.owner, `UPDATE organizations SET default_image_id = 'img_org' WHERE id = $1`, w.org)
		mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_proj',
			agent_models = jsonb_set(agent_models, '{conductor}', COALESCE(agent_models->'conductor', '{}') || '{"image":"img_role"}')
			WHERE id = $1`, w.project)
		ran := func(want string) {
			t.Helper()
			task := w.task()
			spec := w.conductorSubmitted(task)
			id, _, _ := w.conductor(task)
			var image string
			_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(image->>'imageId', '') FROM runs WHERE id = $1`, id).Scan(&image)
			if image != want {
				t.Errorf("ran in %q, want %q", image, want)
			}
			if spec.Image.Ref != finals[want] {
				t.Errorf("submitted image %q, want %s's %q", spec.Image.Ref, want, finals[want])
			}
		}
		ran("img_role")
		mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models #- '{conductor,image}' WHERE id = $1`, w.project)
		ran("img_proj")
		mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = NULL WHERE id = $1`, w.project)
		ran("img_org")
	})
}
