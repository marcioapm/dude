package orchestrator_test

// "Can run containers" is a property of the image a Run resolves: a library
// image's version says it (recorded in runs.image), an image typed by hand
// never can, and DUDE_AGENT_IMAGE can when agent.nested_containers says so.
// Every kind of Run asks lux for sandbox.nestedContainers from that; lux
// keeps it in the Run's stored spec for every resume.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	// An agent Run keeps no engine store: its containers are the work of
	// one session, and its home is a state volume already (images never are).
	keepsEngines(t, *w.specOf("implement"), false)
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
		spec := submitted(t, w.luxRuns()[0])
		if !nested(&spec) {
			t.Errorf("the preview did not ask for nested containers")
		}
		keepsEngines(t, spec, true)
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
		spec := submitted(t, w.luxRuns()[0])
		if !nested(&spec) {
			t.Errorf("the woken preview did not ask for nested containers")
		}
		keepsEngines(t, spec, true)
	})
	t.Run("on the fallback, as the operator says", func(t *testing.T) {
		for _, flag := range []bool{false, true} {
			w := newWorld(t)
			w.previews.DefaultImageContainers = flag
			mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULL WHERE id = $1`, w.project)
			w.startPreview()
			w.until("the preview to reach lux", func() bool { return len(w.luxRuns()) == 1 })
			spec := submitted(t, w.luxRuns()[0])
			if got := nested(&spec); got != flag {
				t.Errorf("flag %v: nested = %v", flag, got)
			}
			keepsEngines(t, spec, flag)
		}
	})
}

// keepsEngines: a preview that can run containers keeps the engines' store
// on a state volume over $XDG_DATA_HOME, set in its env, so its containers
// survive sleep; any other Run has neither.
func keepsEngines(t *testing.T, spec lux.Spec, want bool) {
	t.Helper()
	var has bool
	for _, v := range spec.Volumes {
		if v.Path == "/home/agent/.local/share" {
			has = v.Kind == "state"
		}
	}
	env := spec.Env["XDG_DATA_HOME"] == "/home/agent/.local/share"
	if has != want || env != want {
		t.Errorf("engine store volume %v, XDG_DATA_HOME %v; want %v (volumes %+v)", has, env, want, spec.Volumes)
	}
}

// A Run lux cannot place says why on its servers' view, which the task's
// Servers tab and the Run page read: lux's stateReason while it waits.
func TestARunWaitingForAHostThatCanRunContainersSaysWhy(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	podman := w.libraryImage("img_podman", "abs-preview", true)
	w.canRunContainers(podman, true)
	mustExec(t, w.owner, `UPDATE projects SET preview_image_id = 'img_podman' WHERE id = $1`, w.project)
	w.lux.NoNestedHost = true
	task, runID := w.startPreview()
	w.until("the preview to reach lux", func() bool { return len(w.luxRuns()) == 1 })
	var view struct {
		Run struct {
			LuxState      string  `json:"luxState"`
			PreviewStage  *string `json:"previewStage"`
			WaitingReason *string `json:"waitingReason"`
		} `json:"run"`
	}
	for _, path := range []string{"/internal/tasks/" + task + "/servers", "/internal/runs/" + runID + "/servers"} {
		code, body := w.get(path, w.org)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		if err := json.Unmarshal([]byte(body), &view); err != nil {
			t.Fatal(err)
		}
		if view.Run.WaitingReason == nil || *view.Run.WaitingReason != "waiting for capacity: 1 host in its pool does not support nested containers" {
			t.Errorf("%s: waitingReason = %v (lux %s)", path, view.Run.WaitingReason, view.Run.LuxState)
		}
		if view.Run.PreviewStage == nil || *view.Run.PreviewStage != "scheduling" {
			t.Errorf("%s: stage = %v", path, view.Run.PreviewStage)
		}
	}
}

// Once it has a host, or has ended with a reason of lux's, there is no
// wait to show.
func TestARunNotWaitingForAHostHasNoWaitingReason(t *testing.T) {
	w := newWorld(t)
	task, runID := w.startPreview()
	w.until("the preview to run", func() bool { return len(w.luxRuns()) == 1 && w.lux.State(w.luxRuns()[0].ID) == "running" })
	_, body := w.get("/internal/tasks/"+task+"/servers", w.org)
	if !strings.Contains(body, `"waitingReason":null`) {
		t.Errorf("running: servers = %s", body)
	}
	// lux's reason for a Run lost with its host is no wait.
	w.lux.Lose(w.luxRuns()[0].ID)
	_, body = w.get("/internal/runs/"+runID+"/servers", w.org)
	if !strings.Contains(body, `"luxState":"lost"`) || !strings.Contains(body, `"waitingReason":null`) {
		t.Errorf("lost: servers = %s", body)
	}
}

// A session's agent and a task's conductor ask for nested containers as
// their image says, through the same submit as every agent: on a library
// image whose version can, not on one that cannot, and on DUDE_AGENT_IMAGE
// as the operator says. Neither keeps an engine store.
func TestASessionAndAConductorAskForContainersAsTheirImageSays(t *testing.T) {
	for _, c := range []struct {
		name     string
		image    string // "" for DUDE_AGENT_IMAGE
		can      bool
		operator bool
		want     bool
	}{
		{"a library image that can", "img_podman", true, false, true},
		{"a library image that cannot", "img_base", false, true, false},
		{"the fallback, operator on", "", false, true, true},
		{"the fallback, operator off", "", false, false, false},
	} {
		t.Run("session, "+c.name, func(t *testing.T) {
			s := newSessionWorld(t)
			s.syncer.Agent.NestedContainers = c.operator
			if c.image != "" {
				s.useLayer(imageLayer)
				s.canRunContainers(s.libraryImage(c.image, strings.TrimPrefix(c.image, "img_"), true), c.can)
				mustExec(t, s.owner, `UPDATE organizations SET default_image_id = $2 WHERE id = $1`, s.org, c.image)
			}
			id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Containers"})["id"].(string)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
			run := s.started(id)
			spec := s.luxRun(run).spec
			if got := nested(&spec); got != c.want {
				t.Errorf("nested = %v, want %v", got, c.want)
			}
			keepsEngines(t, spec, false)
			if got := s.str(`SELECT coalesce(image->>'canRunContainers', 'false') FROM runs WHERE id = $1`, run); got != fmt.Sprint(c.image != "" && c.can) {
				t.Errorf("runs.image canRunContainers = %s", got)
			}
		})
		t.Run("conductor, "+c.name, func(t *testing.T) {
			w := newWorld(t)
			w.syncer.Agent.NestedContainers = c.operator
			if c.image != "" {
				w.useLayer(imageLayer)
				w.canRunContainers(w.libraryImage(c.image, strings.TrimPrefix(c.image, "img_"), true), c.can)
				mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = $2 WHERE id = $1`, w.project, c.image)
			} else {
				mustExec(t, w.owner, `UPDATE projects SET runtime_image = NULL WHERE id = $1`, w.project)
			}
			task := w.task()
			spec := w.conductorSubmitted(task)
			if got := nested(&spec); got != c.want {
				t.Errorf("nested = %v, want %v", got, c.want)
			}
			keepsEngines(t, spec, false)
		})
	}
}

// servers.changed is what has a watching Run page read why its Run waits
// again: lux's state events while it waits for a host, and the one that
// ends the wait, each bring one, with the reason they leave.
func TestAnAgentRunsWaitForAHostReachesItsPage(t *testing.T) {
	w := newWorld(t)
	w.useLayer(imageLayer)
	w.canRunContainers(w.libraryImage("img_podman", "agents-podman", true), true)
	mustExec(t, w.owner, `UPDATE projects SET runtime_image_id = 'img_podman' WHERE id = $1`, w.project)
	w.lux.NoNestedHost = true
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	runID := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi)
	luxID := w.luxRunOf(runID)
	changes := func() int {
		n, _ := strconv.Atoi(w.str(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed'`, runID))
		return n
	}
	reason := func() *string {
		code, body := w.get("/internal/runs/"+runID+"/servers", w.org)
		if code != 200 {
			t.Fatalf("servers: %d %s", code, body)
		}
		var view struct {
			Run struct {
				WaitingReason *string `json:"waitingReason"`
			} `json:"run"`
		}
		_ = json.Unmarshal([]byte(body), &view)
		return view.Run.WaitingReason
	}
	for _, step := range []struct {
		name string
		do   func()
		want string
	}{
		{"none to a reason", func() {
			w.lux.Wait(luxID, "waiting for capacity: 1 host in its pool does not support nested containers")
		},
			"waiting for capacity: 1 host in its pool does not support nested containers"},
		{"a reason to another", func() {
			w.lux.Wait(luxID, "waiting for capacity: 2 hosts in its pool do not support nested containers")
		},
			"waiting for capacity: 2 hosts in its pool do not support nested containers"},
		{"a reason to none", func() { w.lux.Place(luxID) }, ""},
	} {
		before := changes()
		step.do()
		w.until(step.name+": servers.changed", func() bool { return changes() > before })
		got := reason()
		if (step.want == "" && got != nil) || (step.want != "" && (got == nil || *got != step.want)) {
			t.Errorf("%s: waitingReason = %v, want %q", step.name, got, step.want)
		}
	}
}
