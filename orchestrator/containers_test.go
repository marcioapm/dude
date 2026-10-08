package orchestrator_test

// "Can run containers" is a property of the image a Run resolves: a library
// image's version says it (recorded in runs.image), an image typed by hand
// never can, and DUDE_AGENT_IMAGE can when agent.nested_containers says so.
// Every kind of Run asks lux for sandbox.nestedContainers from that; lux
// keeps it in the Run's stored spec for every resume.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func nested(s *lux.Spec) bool { return s != nil && s.Sandbox != nil && s.Sandbox.NestedContainers }

// canRunContainers marks the published version of a library image.
func (w *world) canRunContainers(version string, can bool) {
	mustExec(w.t, w.owner, `UPDATE image_versions SET can_run_containers = $2 WHERE id = $1`, version, can)
}

func TestAnAgentRunAsksForContainersWhenItsLibraryImageCan(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	w.libraryImage("img_base", "acme-base", true)
	podman := w.libraryImage("img_podman", "agents-podman", true)
	w.canRunContainers(podman, true)
	// The implementer's role names the image that can; the reviewer runs
	// on the project's, which cannot. The operator's flag is on, and
	// changes neither: it is about DUDE_AGENT_IMAGE.
	w.syncer.Agent.NestedContainers = true
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_base',
		agent_models = jsonb_set(agent_models, '{implementer,image}', '"img_podman"') WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("the reviewer to reach lux", func() bool { return w.specOf("review") != nil })
	if !nested(w.specOf("implement")) {
		t.Errorf("the implementer on agents-podman did not ask for nested containers")
	}
	if nested(w.specOf("review")) {
		t.Errorf("the reviewer on acme-base asked for nested containers")
	}
	if got := w.runImage(wi, "implement"); !got.CanRunContainers {
		t.Errorf("runs.image = %+v, want canRunContainers", got)
	}
	if got := w.runImage(wi, "review"); got.CanRunContainers {
		t.Errorf("runs.image = %+v, want no canRunContainers", got)
	}
}

// An image typed by hand never runs containers, whatever the operator's
// flag says; DUDE_AGENT_IMAGE does when the flag is on.
func TestATypedImageNeverRunsContainersAndTheFallbackDoesWhenTheOperatorSays(t *testing.T) {
	for _, c := range []struct {
		name  string
		typed string
		flag  bool
		want  bool
	}{
		{"typed, flag on", "agent:test", true, false},
		{"fallback, flag on", "", true, true},
		{"fallback, flag off", "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.Agent.NestedContainers = c.flag
			mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULLIF($2, '') WHERE id = $1`, w.project, c.typed)
			wi := w.task()
			w.deliver(wi)
			w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
			if got := nested(w.specOf("implement")); got != c.want {
				t.Errorf("nested = %v, want %v", got, c.want)
			}
		})
	}
}

// A branch preview asks for nested containers on an image that can, both
// ways it reaches lux: submitted (an old-style preview) and woken.
func TestAPreviewAsksForContainersWhenItsImageCan(t *testing.T) {
	t.Run("submitted", func(t *testing.T) {
		w := newWorld(t)
		w.useLayer(imageLayer)
		podman := w.libraryImage("img_podman", "abs-preview", true)
		w.canRunContainers(podman, true)
		mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_podman' WHERE id = $1`, w.project)
		_, runID := w.startPreview()
		w.until("the preview to reach lux", func() bool { return len(w.luxRuns()) == 1 })
		if !nested(ptr(submitted(t, w.luxRuns()[0]))) {
			t.Errorf("the preview did not ask for nested containers")
		}
		if got := w.str(`SELECT image->>'canRunContainers' FROM runs WHERE id = $1`, runID); got != "true" {
			t.Errorf("runs.image canRunContainers = %s", got)
		}
	})
	t.Run("woken", func(t *testing.T) {
		w := newWorld(t)
		w.wakeable()
		w.useLayer(imageLayer)
		podman := w.libraryImage("img_podman", "abs-preview", true)
		w.canRunContainers(podman, true)
		mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_podman' WHERE id = $1`, w.project)
		w.recipe("web", 3000, "npm run dev", "", nil, true)
		_, runID := w.declare()
		w.lux.RequestServer(w.serverID(runID, "web"), "/")
		w.until("the preview to reach lux", func() bool { return len(w.luxRuns()) == 1 })
		if !nested(ptr(submitted(t, w.luxRuns()[0]))) {
			t.Errorf("the woken preview did not ask for nested containers")
		}
	})
	t.Run("on the fallback, as the operator says", func(t *testing.T) {
		for _, flag := range []bool{false, true} {
			w := newWorld(t)
			w.previews.DefaultImageContainers = flag
			mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULL WHERE id = $1`, w.project)
			w.startPreview()
			w.until("the preview to reach lux", func() bool { return len(w.luxRuns()) == 1 })
			if got := nested(ptr(submitted(t, w.luxRuns()[0]))); got != flag {
				t.Errorf("flag %v: nested = %v", flag, got)
			}
		}
	})
}
