package orchestrator_test

// Servers and branch previews through the orchestrator's real code: the
// internal API the backend calls, the lux client, the phase syncer's and
// the preview loop's following of lux's stream, against the fake lux's
// servers (which move as lux's do: starting, ready, stopped by a move).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// do calls the orchestrator's internal API as the backend would, as the
// person w.actor names (a key).
func (w *world) do(method, path string, body any) (int, map[string]any) {
	w.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.api+path, r)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	req.Header.Set("X-Dude-Actor", w.actor)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (w *world) recipe(name string, port int, command, workdir string, setup *string, autostart bool) {
	mustExec(w.t, w.owner, `INSERT INTO project_servers (project_id, organization_id, name, port, command, workdir, setup,
		env, autostart_in_previews) VALUES ($1, $2, $3, $4, $5, $6, $7, '[{"name":"PORT","value":"1"}]', $8)`,
		w.project, w.org, name, port, command, workdir, setup, autostart)
}

func serverNamed(out map[string]any, name string) map[string]any {
	list, _ := out["servers"].([]any)
	for _, s := range list {
		if m, _ := s.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

func (w *world) changes(runID string) []map[string]any {
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = 'servers.changed'
		ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []map[string]any
	for rows.Next() {
		var p map[string]any
		_ = rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

// stageHold is a lux Run held at a stage boundary until release is closed.
type stageHold struct {
	epoch   int
	stage   string
	release chan struct{}
}

// holdStages has every stage boundary of the fake lux wait for the test:
// next takes the next one, which must be epoch's stage. Set before any Run.
func (w *world) holdStages() (next func(epoch int, stage string) stageHold) {
	w.t.Helper()
	reached := make(chan stageHold, 32)
	done := make(chan struct{})
	w.t.Cleanup(func() { close(done) })
	w.lux.OnStage = func(epoch int, stage string) {
		h := stageHold{epoch, stage, make(chan struct{})}
		reached <- h
		select {
		case <-h.release:
		case <-done:
		}
	}
	return func(epoch int, stage string) stageHold {
		w.t.Helper()
		var h stageHold
		w.until("held at "+stage, func() bool {
			select {
			case h = <-reached:
				return true
			default:
				return false
			}
		})
		if h.epoch != epoch || h.stage != stage {
			w.t.Fatalf("held at %d %s, want %d %s", h.epoch, h.stage, epoch, stage)
		}
		return h
	}
}

// taskServers is the task's servers as the backend reads them.
func (w *world) taskServers(task string) map[string]any {
	w.t.Helper()
	code, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
	if code != 200 {
		w.t.Fatal(code, out)
	}
	return out
}

// stageChanges is how many servers.changed notifications of a lux stage
// the run has had.
func (w *world) stageChanges(runID string) int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed' AND payload->>'change' = 'stage'`, runID)
}

func TestPreviewAuthoritativeStages(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			w := newWorld(t)
			w.previews.Minute = time.Hour
			w.lux.LegacyStages = legacy
			w.lux.ServerReadyAfter = 300 * time.Millisecond
			next := w.holdStages()
			setup := "npm ci"
			w.recipe("web", 3000, "npm run dev", "", &setup, true)
			wi := w.task()
			code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
			if code != 201 {
				t.Fatal(code, out)
			}
			runID := out["run"].(map[string]any)["id"].(string)
			servers := func() map[string]any { return w.taskServers(wi) }
			view := func() map[string]any { return servers()["run"].(map[string]any) }
			// Each held boundary is exactly one stage notification more than
			// the last; a lux without stages sends none.
			notified := 0
			// held checks the view at a held boundary against lux's stage.
			held := func(h stageHold, want string) {
				t.Helper()
				if legacy {
					if n := w.stageChanges(runID); n != 0 {
						t.Fatalf("legacy lux: %d stage notifications at %s", n, h.stage)
					}
				} else {
					w.until("the "+h.stage+" notification", func() bool { return w.stageChanges(runID) > notified })
					notified++
				}
				v := view()
				lr, err := w.previews.Lux.Get(context.Background(), v["luxRunId"].(string))
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					if v["previewStageSince"] != nil {
						t.Fatalf("legacy timer at %s: %v", h.stage, v)
					}
				} else if want != "" && (v["previewStage"] != want || lr.StageSince == nil || v["previewStageSince"] != lr.StageSince.Format(time.RFC3339Nano)) {
					t.Fatalf("stage %s: %v, lux %+v", h.stage, v, lr)
				}
				if n := w.stageChanges(runID); !legacy && n != notified {
					t.Fatalf("%d stage notifications at %s, want %d", n, h.stage, notified)
				}
				close(h.release)
			}
			// running is the servers' to time (setup/starting/ready).
			shows := map[string]string{"waiting": "scheduling", "image": "image", "volumes": "volumes", "repositories": "cloning", "container": "container"}
			checkStart := func(epoch int) {
				t.Helper()
				for _, stage := range []string{"waiting", "image", "volumes", "repositories", "container", "running"} {
					held(next(epoch, stage), shows[stage])
				}
			}
			checkStart(1)
			var setupOut map[string]any
			w.until("setup", func() bool {
				setupOut = servers()
				return setupOut["run"].(map[string]any)["previewStage"] == "setup"
			})
			if since := setupOut["run"].(map[string]any)["previewStageSince"]; legacy && since != nil || !legacy && since != serverNamed(setupOut, "web")["since"] {
				t.Fatalf("setup: %v", setupOut)
			}
			w.until("ready", func() bool { return view()["previewStage"] == "ready" })
			mustExec(t, w.owner, `UPDATE runs SET started_at = now() - interval '34 minutes' WHERE id = $1`, runID)
			luxID := view()["luxRunId"].(string)
			go w.lux.Migrate(luxID)
			// A move is stopping, then waiting on the next placement: never stopped.
			held(next(1, "stopping"), "stopping")
			checkStart(2)
			w.until("ready after move", func() bool { return view()["previewStage"] == "ready" })
			w.previews.Minute = time.Millisecond
			w.until("parked", func() bool { return view()["state"] == "paused" && w.lux.State(luxID) == "stopped" })
			w.previews.Minute = time.Hour
			if v := view(); v["previewStage"] != nil || v["previewStageSince"] != nil {
				t.Fatalf("parked: %v", v)
			}
			if !legacy {
				// The park's stopping and stopped, unheld.
				notified += 2
				w.until("the park's notifications", func() bool { return w.stageChanges(runID) >= notified })
			}
			code, out = w.do("POST", "/internal/runs/"+runID+"/servers/start-all", nil)
			if code != 200 {
				t.Fatal(code, out)
			}
			checkStart(3)
			var readyOut map[string]any
			w.until("ready after warm resume", func() bool {
				readyOut = servers()
				return readyOut["run"].(map[string]any)["previewStage"] == "ready"
			})
			if since := readyOut["run"].(map[string]any)["previewStageSince"]; legacy && since != nil || !legacy && since != serverNamed(readyOut, "web")["readySince"] {
				t.Fatalf("ready timer: %v", readyOut)
			}
		})
	}
}

// A wakeable preview's view takes lux's stage too: woken, held while its
// volumes restore, it says so with lux's own time.
func TestAWakeablePreviewShowsLuxsStage(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	next := w.holdStages()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	w.lux.RequestServer(w.serverID(runID, "web"), "/")
	close(next(1, "waiting").release)
	close(next(1, "image").release)
	h := next(1, "volumes")
	// dude records the lux Run from its submit's answer, which may trail the hold.
	var v map[string]any
	w.until("the woken Run in the view", func() bool {
		v = w.taskServers(task)["run"].(map[string]any)
		return v["luxRunId"] != nil && v["luxRunId"] != ""
	})
	lr, err := w.previews.Lux.Get(context.Background(), v["luxRunId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if v["wakeable"] != true || v["previewStage"] != "volumes" || lr.StageSince == nil || v["previewStageSince"] != lr.StageSince.Format(time.RFC3339Nano) {
		t.Fatalf("wakeable view %v, lux %+v", v, lr)
	}
	close(h.release)
}

func TestAWorkingAgentsServersAreItsRunsThroughLux(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.actor = w.person("Ana")
	setup := "npm ci"
	w.recipe("web", 3000, "npm run dev -- --port 3000", "apps/web", &setup, true)
	wi := w.task()

	// Nothing runs: no run, and the project's recipes to add.
	code, out := w.do("GET", "/internal/tasks/"+wi+"/servers", nil)
	if code != 200 || out["run"] != nil || len(out["recipes"].([]any)) != 1 || len(out["servers"].([]any)) != 0 {
		t.Fatalf("before = %d %v", code, out)
	}

	w.deliver(wi)
	var runID, luxID string
	w.until("the implementer to run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1 AND status = 'running'`, wi).
			Scan(&runID, &luxID)
		return runID != ""
	})

	code, out = w.do("GET", "/internal/tasks/"+wi+"/servers", nil)
	run, _ := out["run"].(map[string]any)
	if code != 200 || run["id"] != runID || run["kind"] != "agent" || run["label"] != "Implementer run" ||
		run["luxRunId"] != luxID || run["terminalUrl"] != "https://console.lux.test/runs/"+luxID+"/terminal" ||
		run["host"] != "host-1" || run["previewStage"] != nil {
		t.Fatalf("run = %v", run)
	}
	if by, _ := run["startedBy"].(map[string]any); by != nil {
		t.Errorf("startedBy = %v; the task has no owner", by)
	}

	// A recipe, added: lux is told to run it through a shell, after its
	// setup, in the repository's checkout.
	code, added := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "web"})
	if code != 201 || added["name"] != "web" || added["state"] != "starting" {
		t.Fatalf("add = %d %v", code, added)
	}
	w.until("web ready", func() bool { return w.lux.ServerStates(luxID)["web"] == "ready" })
	var spec struct {
		Name    string            `json:"name"`
		Command []string          `json:"command"`
		Workdir string            `json:"workdir"`
		Env     map[string]string `json:"env"`
	}
	_, out = w.do("GET", "/internal/runs/"+runID+"/servers", nil)
	raw, _ := json.Marshal(serverNamed(out, "web"))
	_ = json.Unmarshal(raw, &spec)
	if !slices.Equal(spec.Command, []string{"sh", "-c", "npm ci && npm run dev -- --port 3000"}) ||
		spec.Workdir != "/workspace/repos/target/apps/web" || spec.Env["PORT"] != "1" {
		t.Errorf("lux has web as %s", raw)
	}
	// A person's own, and a name lux would refuse.
	if code, _ := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"name": "vite", "port": 5173}); code != 201 {
		t.Errorf("add a port = %d", code)
	}
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"name": "Bad-", "port": 1}); code != 400 {
		t.Errorf("a bad name = %d %v", code, body)
	}
	if code, _ := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "nope"}); code != 404 {
		t.Errorf("an unknown recipe = %d", code)
	}
	// lux's refusal is passed on as lux said it.
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "web"}); code != 409 ||
		body["error"].(map[string]any)["code"] != "name_taken" {
		t.Errorf("web twice = %d %v", code, body)
	}

	// Each change lux reported on the Run's stream is servers.changed.
	w.until("servers.changed for web ready", func() bool {
		return slices.ContainsFunc(w.changes(runID), func(p map[string]any) bool {
			return p["server"] == "web" && p["state"] == "ready" && p["taskId"] == wi
		})
	})

	// Stop all, start all: every server with a command.
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/stop-all", nil); code != 200 ||
		serverNamed(out, "web")["state"] != "stopped" {
		t.Errorf("stop-all = %d %v", code, out)
	}
	if code, _ := w.do("POST", "/internal/runs/"+runID+"/servers/start-all", nil); code != 200 {
		t.Errorf("start-all = %d", code)
	}
	w.until("web ready again", func() bool { return w.lux.ServerStates(luxID)["web"] == "ready" })
	if st := w.lux.ServerStates(luxID)["vite"]; st != "stopped" {
		t.Errorf("start-all started a port with no command: %s", st)
	}
	if code, log := w.do("GET", "/internal/runs/"+runID+"/servers/web/log?tail=1", nil); code != 200 || len(log["lines"].([]any)) != 1 {
		t.Errorf("log = %d %v", code, log)
	}

	// The Run moves host: its servers stop with it, and the task says so.
	w.lux.Migrate(luxID)
	w.until("the run to run on the new host", func() bool {
		_, out = w.do("GET", "/internal/tasks/"+wi+"/servers", nil)
		m, _ := out["moved"].(map[string]any)
		return m != nil && m["toHost"] != nil && out["run"].(map[string]any)["luxState"] == "running"
	})
	moved := out["moved"].(map[string]any)
	if moved["fromHost"] != "host-1" || moved["toHost"] != "host-2" || moved["at"] == "" {
		t.Errorf("moved = %v", moved)
	}
	if s := serverNamed(out, "web"); s["stopReason"] != "migrated" {
		t.Errorf("web after the move = %v", s)
	}
	// The move is not the agent dying.
	if s := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID); s != 1 {
		t.Errorf("the run is no longer running after a move: %s", w.describeRuns())
	}
	// Started again, it is not "moved" any more.
	if code, _ := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
		t.Errorf("start web = %d", code)
	}
	if _, out = w.do("GET", "/internal/tasks/"+wi+"/servers", nil); out["moved"] != nil {
		t.Errorf("moved after a start = %v", out["moved"])
	}
	if code, _ := w.do("DELETE", "/internal/runs/"+runID+"/servers/vite", nil); code != 204 {
		t.Errorf("remove = %d", code)
	}
	if code, _ := w.do("POST", "/internal/runs/"+runID+"/servers/web/launch", nil); code != 404 {
		t.Errorf("an unknown action = %d", code)
	}

	// Another organization has no such run.
	if code, _ := w.get("/internal/runs/"+runID+"/servers", "org_other"); code != 404 {
		t.Errorf("another organization reads the run's servers: %d", code)
	}
}

func TestABranchPreviewServesTheTasksBranchAndIsParkedWhenUnused(t *testing.T) {
	w := newWorld(t)
	w.previews.Minute = time.Hour
	w.actor = w.person("Ana")
	w.lux.ServerReadyAfter = 150 * time.Millisecond
	setup := "npm ci"
	w.recipe("web", 3000, "npm run dev", "", &setup, true)
	w.recipe("docs", 4000, "npm run docs", "docs", nil, false)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"image":"node:22","egress":["registry.npmjs.org"],"idleTimeoutMinutes":5}'
		WHERE id = $1`, w.project)
	wi := w.task()

	code, out := w.do("POST", "/internal/tasks/"+wi+"/preview", nil)
	run, _ := out["run"].(map[string]any)
	if code != 201 || run["kind"] != "preview" || run["label"] != "Branch preview" || run["previewStage"] != "scheduling" ||
		run["parksAfterMinutes"] != float64(5) || run["startedBy"].(map[string]any)["name"] != "Ana" {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := run["id"].(string)
	if code, body := w.do("POST", "/internal/tasks/"+wi+"/preview", nil); code != 409 ||
		body["error"].(map[string]any)["code"] != "preview_running" {
		t.Errorf("a second preview = %d %v", code, body)
	}
	// A preview has no agent: not to be steered, paused or aborted.
	if code, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{}); code != 409 {
		t.Errorf("abort a preview = %d %v", code, body)
	}

	var stages []string
	w.until("the preview to serve", func() bool {
		_, out = w.do("GET", "/internal/tasks/"+wi+"/servers", nil)
		stage, _ := out["run"].(map[string]any)["previewStage"].(string)
		if len(stages) == 0 || stages[len(stages)-1] != stage {
			stages = append(stages, stage)
		}
		return stage == "ready"
	})
	// On its way it set up (web has a setup step) before it was ready.
	if !slices.Contains(stages, "setup") || stages[len(stages)-1] != "ready" {
		t.Errorf("stages = %v", stages)
	}
	luxID := out["run"].(map[string]any)["luxRunId"].(string)

	// What lux was asked to run: the project's code at its default branch
	// (nothing was published yet), not pushed; a workload that waits; the
	// servers that start in previews; its egress and image.
	var spec lux.Spec
	_ = json.Unmarshal(w.lux.Runs()[0].Spec, &spec)
	if spec.Image.Ref != "node:22" || spec.Workload.Adapter != "generic" || !slices.Equal(spec.Workload.Command, []string{"sleep", "infinity"}) ||
		spec.Workload.Prompt != "" || spec.Network == nil || len(spec.Network.Egress) != 1 || spec.Network.Egress[0].Host != "registry.npmjs.org" {
		t.Errorf("spec = %+v", spec)
	}
	if len(spec.Workload.Servers) != 1 || spec.Workload.Servers[0].Name != "web" ||
		spec.Workload.Servers[0].Workdir != "/workspace/repos/target" {
		t.Errorf("servers = %+v", spec.Workload.Servers)
	}
	if spec.Git == nil || len(spec.Git.Repositories) != 1 || spec.Git.Repositories[0].Ref != "main" ||
		spec.Git.Repositories[0].Push == nil || *spec.Git.Repositories[0].Push || spec.Git.Push != nil {
		t.Errorf("git = %+v", spec.Git)
	}
	if run := out["run"].(map[string]any); run["branch"] != "main" || run["commit"] == nil || run["state"] != "running" {
		t.Errorf("run = %v", run)
	}

	// Opened by someone a moment ago: in use, not parked.
	w.previews.Minute = 40 * time.Millisecond // 5 "minutes" = 200ms
	time.Sleep(250 * time.Millisecond)
	w.lux.Request(luxID, "web", time.Now())
	w.pump()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID); n != 1 {
		t.Fatalf("parked while in use: %s", w.describeRuns())
	}
	// Unused for the limit: parked — lux stopped, the task told.
	w.until("the preview to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'unused' AND lux_state = 'stopped'`, runID) == 1
	})
	_, out = w.do("GET", "/internal/tasks/"+wi+"/servers", nil)
	if run := out["run"].(map[string]any); run["id"] != runID || run["previewStage"] != nil || run["state"] != "paused" {
		t.Errorf("parked = %v", run)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked' AND payload->>'reason' = 'unused'`, runID); n != 1 {
		t.Errorf("%d run.parked", n)
	}

	// Starting a server wakes it: resumed, and that server started too.
	w.previews.Minute = time.Hour
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers/docs/start", nil); code != 404 {
		t.Errorf("starting a server the run does not have = %d %v", code, body)
	}
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{"recipe": "docs"}); code != 201 {
		t.Fatalf("add docs while parked = %d %v", code, body)
	}
	w.until("docs and web to serve after the resume", func() bool {
		st := w.lux.ServerStates(luxID)
		return st["docs"] == "ready" && st["web"] == "ready"
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND dude_pause IS NULL AND pending_starts = '{}'`, runID); n != 1 {
		t.Errorf("after the resume: %s", w.describeRuns())
	}
	if !slices.ContainsFunc(w.changes(runID), func(p map[string]any) bool { return p["change"] == "resumed" }) {
		t.Error("no servers.changed for the resume")
	}

	// Stopped for good: lux cancels it, and the task has none live.
	if code, _ := w.do("DELETE", "/internal/tasks/"+wi+"/preview", nil); code != 200 {
		t.Errorf("stop = %d", code)
	}
	w.until("lux to cancel it", func() bool { return slices.Contains(w.lux.CallsOf(luxID), "cancel") })
	if _, out = w.do("GET", "/internal/tasks/"+wi+"/servers", nil); out["run"] != nil {
		t.Errorf("after the stop = %v", out["run"])
	}
	if code, _ := w.do("DELETE", "/internal/tasks/"+wi+"/preview", nil); code != 404 {
		t.Errorf("stop again = %d", code)
	}
	if code, _ := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 409 {
		t.Errorf("start on a finished preview = %d", code)
	}
	// A new preview may start now.
	if code, _ := w.do("POST", "/internal/tasks/"+wi+"/preview", nil); code != 201 {
		t.Errorf("a new preview = %d", code)
	}
}

func TestKeylessPeopleOwnAgentServersAndStartPreviews(t *testing.T) {
	w := newWorld(t)
	ownerID, starterID := "per_owner_"+w.org, "per_starter_"+w.org
	mustExec(t, w.owner, `INSERT INTO people (id, organization_id, name)
		VALUES ($1, $3, 'Keyless owner'), ($2, $3, 'Keyless starter')`, ownerID, starterID, w.org)
	wi := w.task()
	mustExec(t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		VALUES ($1, $2, $3, 5)`, wi, ownerID, w.org)
	call := func(person, method, path string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, w.api+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer svc")
		req.Header.Set("X-Dude-Organization", w.org)
		req.Header.Set("X-Dude-Credential-Kind", "person")
		req.Header.Set("X-Dude-Actor", person)
		req.Header.Set("X-Dude-Person", person)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, out
	}
	code, out := call(starterID, "POST", "/internal/tasks/"+wi+"/preview")
	if code != 201 {
		t.Fatalf("start: %d %v", code, out)
	}
	run := out["run"].(map[string]any)
	previewID := run["id"].(string)
	by := run["startedBy"].(map[string]any)
	if by["id"] != starterID || by["name"] != "Keyless starter" {
		t.Fatalf("preview starter: %v", by)
	}
	var persisted string
	if err := w.owner.QueryRow(context.Background(), `SELECT started_by FROM runs WHERE id = $1`, previewID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != starterID {
		t.Fatalf("persisted starter: %s", persisted)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created'
		AND actor_type = 'person' AND actor_id = $2`, previewID, starterID) != 1 {
		t.Fatal("preview creation lacks person attribution")
	}
	if code, out = call(starterID, "DELETE", "/internal/tasks/"+wi+"/preview"); code != 200 {
		t.Fatalf("stop: %d %v", code, out)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.completed'
		AND actor_type = 'person' AND actor_id = $2`, previewID, starterID) != 1 {
		t.Fatal("preview stop lacks person attribution")
	}
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.deliver(wi)
	w.until("the keyless owner's agent to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND status = 'running'`, wi) == 1
	})
	code, out = call(starterID, "GET", "/internal/tasks/"+wi+"/servers")
	if code != 200 {
		t.Fatalf("servers: %d %v", code, out)
	}
	run = out["run"].(map[string]any)
	by = run["startedBy"].(map[string]any)
	if run["kind"] != "agent" || by["id"] != ownerID || by["name"] != "Keyless owner" {
		t.Fatalf("agent owner: %v", run)
	}
	if n := w.count(`SELECT count(*) FROM api_keys WHERE person_id IN ($1, $2)`, ownerID, starterID); n != 0 {
		t.Fatalf("keyless people have %d keys", n)
	}
}

func TestAPreviewChecksOutTheBranchTheTaskPublished(t *testing.T) {
	w := newWorld(t)
	w.actor = w.person("Ana")
	wi := w.task()
	branch := "dude/" + wi + "/attempt-1"
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'write')`,
		w.org, wi, w.repoID)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, branch, heads)
		VALUES ('run_done_'||$1, $1, $2, $3, 1, 'completed', 'implement', $4, '{"target":{"sha":"abc"}}')`, w.org, w.project, wi, branch)
	if code, _ := w.do("POST", "/internal/tasks/"+wi+"/preview", nil); code != 201 {
		t.Fatal(code)
	}
	w.until("the preview to be submitted", func() bool { return len(w.lux.Runs()) == 1 })
	var spec lux.Spec
	_ = json.Unmarshal(w.lux.Runs()[0].Spec, &spec)
	if spec.Git.Repositories[0].Ref != branch || spec.Image.Ref != "agent:test" {
		t.Errorf("ref = %s, image = %s", spec.Git.Repositories[0].Ref, spec.Image.Ref)
	}
	// No autostart recipes: nothing to wait for once it runs.
	if len(spec.Workload.Servers) != 0 || !strings.HasPrefix(spec.Name, "preview ") || len(spec.Network.Egress) != 0 || spec.Network.Unrestricted {
		t.Errorf("spec = %+v", spec)
	}
}
