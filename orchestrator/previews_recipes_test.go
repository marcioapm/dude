package orchestrator_test

// A wakeable preview's lux servers follow its project's recipes: each wake
// makes them what the recipes say now, before lux starts them, on every
// path a wake takes (the first submit, a resume of the same Run, a
// replacement Run), and on declare.

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// editRecipe changes a recipe's command and env, as PUT
// /v1/projects/:id/servers/:name does.
func (w *world) editRecipe(name, command, env string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE project_servers SET command = $3, env = $4::jsonb WHERE project_id = $1 AND name = $2`,
		w.project, name, command, env)
}

// tenantCallsOf is what lux saw happen to one server, from index from on.
func (w *world) tenantCallsOf(id string, from int) []string {
	var out []string
	for _, c := range w.lux.TenantCalls()[from:] {
		if f := strings.Fields(c); len(f) > 1 && f[1] == id {
			out = append(out, c)
		}
	}
	return out
}

// patchedBeforeStart: lux saw exactly one PATCH of these fields to the
// server, and it came before the server's next start.
func patchedBeforeStart(t *testing.T, calls []string, id, fields string) {
	t.Helper()
	patch := slices.Index(calls, "patch "+id+" "+fields)
	start := slices.Index(calls, "start "+id)
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "patch ") {
			n++
		}
	}
	if patch < 0 || start < 0 || patch > start || n != 1 {
		t.Errorf("lux saw %v; want one PATCH of %s to %s, before its start", calls, fields, id)
	}
}

// The recipe edited while the preview sleeps reaches its next start, on
// each path a wake takes: lux sees the PATCH with the new command and env
// before it starts the server, and the server's process starts with them.
func TestARecipeEditReachesTheNextWakeOnEveryPath(t *testing.T) {
	for _, path := range []string{"first submit", "resume", "replacement", "started from dude"} {
		t.Run(path, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			var runID, web string
			if path == "first submit" {
				w.recipe("web", 3000, "npm run dev", "", nil, true)
				_, runID = w.declare()
				web = w.serverID(runID, "web")
			} else {
				runID, web = w.asleepPreview()
			}
			if path == "replacement" {
				w.previews.Lux = &countingLux{Client: w.previews.Lux,
					refuseResume: &lux.Error{Status: 409, Code: "no_snapshot", Message: "run cannot be resumed: its snapshot is gone"}}
			}
			mark := len(w.lux.TenantCalls())
			w.editRecipe("web", "npm run dev -- --s3", `[{"name":"PORT","value":"1"},{"name":"S3_BRIDGE","value":"versitygw"}]`)

			if path == "started from dude" {
				if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
					t.Fatalf("start = %d %v", code, out)
				}
			} else {
				w.lux.RequestServer(web, "/")
			}
			w.until("web served again", func() bool {
				sv, _ := w.lux.TenantServer(web)
				return sv.State == lux.SrvReady
			})
			patchedBeforeStart(t, w.tenantCallsOf(web, mark), web, "command,env")
			sv, _ := w.lux.TenantServer(web)
			wantEnv := map[string]string{"PORT": "1", "S3_BRIDGE": "versitygw"}
			if !slices.Equal(sv.Command, servers.ShellCommand(nil, "npm run dev -- --s3")) || !maps.Equal(sv.Env, wantEnv) {
				t.Errorf("lux's server: command %v env %v", sv.Command, sv.Env)
			}
			runs := w.luxRuns()
			serving := runs[len(runs)-1]
			if env := w.lux.ServerEnv(serving.ID, "web"); env["S3_BRIDGE"] != "versitygw" {
				t.Errorf("the server's process started with %v", env)
			}
			wantRuns := map[string]int{"first submit": 1, "resume": 1, "replacement": 2, "started from dude": 1}[path]
			if len(runs) != wantRuns {
				t.Errorf("%d lux runs; want %d", len(runs), wantRuns)
			}
			if path == "resume" && runs[0].Resumed != 1 {
				t.Errorf("resumed %d times; want the same Run resumed once", runs[0].Resumed)
			}
		})
	}
}

// A wake with the recipes as they were sends lux no PATCH, also when only
// the project's preview settings (idle timeout, egress) changed meanwhile.
// Labels are not reconciled: a server dude made carries app=dude, and one
// made before dude labelled them so (adopted here) keeps the labels it has.
func TestAnUnchangedRecipeIsNotPatched(t *testing.T) {
	for _, change := range []string{"nothing", "preview settings", "made before app=dude"} {
		t.Run(change, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			setup := "npm ci"
			w.recipe("web", 3000, "npm run dev", "apps/web", &setup, true)
			var runID string
			wantApp := lux.App
			if change == "made before app=dude" {
				var task string
				task, runID = w.startPreview()
				if _, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000,
					Command: servers.ShellCommand(&setup, "npm run dev"), Workdir: servers.Workdir("target", "apps/web"),
					Env: map[string]string{"PORT": "1"}, Hostname: servers.PreviewHostname(previewDomain, "web", w.previewOf(task), ""),
					Wake: "request", Lifetime: "owner", Labels: map[string]string{"dude.preview": runID}}); err != nil {
					t.Fatal(err)
				}
				w.untilAsleep(runID)
				wantApp = ""
			} else {
				_, runID = w.declare()
			}
			web := w.serverID(runID, "web")
			if sv, _ := w.lux.TenantServer(web); sv.Labels[lux.AppLabel] != wantApp {
				t.Fatalf("lux's server has labels %v; want %s=%q", sv.Labels, lux.AppLabel, wantApp)
			}
			w.open(web)
			w.running(runID, "web")
			w.lux.Idle(web)
			w.until("parked", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
			})
			if change == "preview settings" {
				mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":5,"egress":["registry.npmjs.org"]}'
					WHERE id = $1`, w.project)
			}
			w.open(web)
			for _, c := range w.lux.TenantCalls() {
				if strings.HasPrefix(c, "patch ") {
					t.Fatalf("lux saw %q for recipes nobody changed; all calls %v", c, w.lux.TenantCalls())
				}
			}
			if sv, _ := w.lux.TenantServer(web); sv.Labels[lux.AppLabel] != wantApp {
				t.Errorf("after the wake lux's server has labels %v; want %s=%q", sv.Labels, lux.AppLabel, wantApp)
			}
			if r := w.luxRuns()[0]; r.Resumed != 1 {
				t.Fatalf("resumed %d times", r.Resumed)
			}
		})
	}
}

// Each field of a recipe edited on its own reaches lux as a PATCH of the
// field lux holds it in, before the server starts; what lux stores and
// what the process starts with are the new value. The log names the
// fields, never an env value.
func TestEachRecipeFieldReachesLux(t *testing.T) {
	const sentinel = "s3cr3t-7c1f9a"
	for _, tc := range []struct {
		field, edit, fields string
		check               func(t *testing.T, w *world, sv lux.TenantServer, luxRun string)
	}{
		{"setup", `UPDATE project_servers SET setup = 'npm ci' WHERE project_id = $1 AND name = 'web'`, "command",
			func(t *testing.T, w *world, sv lux.TenantServer, luxRun string) {
				setup := "npm ci"
				if want := servers.ShellCommand(&setup, "npm run dev"); !slices.Equal(sv.Command, want) {
					t.Errorf("lux's command %v; want %v", sv.Command, want)
				}
				if started := strings.Join(w.lux.ServerCommand(luxRun, "web"), " "); !strings.Contains(started, "npm ci && npm run dev") {
					t.Errorf("the process started as %q", started)
				}
			}},
		{"port", `UPDATE project_servers SET port = 3001 WHERE project_id = $1 AND name = 'web'`, "port",
			func(t *testing.T, w *world, sv lux.TenantServer, luxRun string) {
				if sv.Port != 3001 {
					t.Errorf("lux's port %d", sv.Port)
				}
			}},
		{"workdir", `UPDATE project_servers SET workdir = 'apps/web' WHERE project_id = $1 AND name = 'web'`, "workdir",
			func(t *testing.T, w *world, sv lux.TenantServer, luxRun string) {
				if want := servers.Workdir("target", "apps/web"); sv.Workdir != want {
					t.Errorf("lux's workdir %q; want %q", sv.Workdir, want)
				}
			}},
		{"env", `UPDATE project_servers SET env = '[{"name":"PORT","value":"1"},{"name":"S3_SECRET","value":"` + sentinel + `"}]'
			WHERE project_id = $1 AND name = 'web'`, "env",
			func(t *testing.T, w *world, sv lux.TenantServer, luxRun string) {
				if sv.Env["S3_SECRET"] != sentinel {
					t.Errorf("lux's env %v", sv.Env)
				}
				if env := w.lux.ServerEnv(luxRun, "web"); env["S3_SECRET"] != sentinel {
					t.Errorf("the process started with %v", env)
				}
			}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			logs := &lockedBuffer{}
			w.previews.Log = slog.New(slog.NewTextHandler(logs, nil))
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			web := w.serverID(runID, "web")
			mark := len(w.lux.TenantCalls())
			mustExec(t, w.owner, tc.edit, w.project)
			w.open(web)
			patchedBeforeStart(t, w.tenantCallsOf(web, mark), web, tc.fields)
			sv, _ := w.lux.TenantServer(web)
			tc.check(t, w, sv, w.luxRuns()[0].ID)
			if !strings.Contains(logs.String(), "fields=["+tc.fields+"]") {
				t.Errorf("the log does not name the field changed:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), sentinel) {
				t.Errorf("an env value reached the log:\n%s", logs.String())
			}
		})
	}
}

// A recipe removed, or no longer started in previews, takes its server
// away at the next wake; one added gets a server, which the wake starts.
// The preview lives on and serves.
func TestRecipesRemovedAndAddedReachTheNextWake(t *testing.T) {
	for _, gone := range []string{"removed", "not in previews"} {
		t.Run(gone, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.recipe("api", 4000, "go run .", "api", nil, true)
			_, runID := w.declare()
			web, api := w.serverID(runID, "web"), w.serverID(runID, "api")
			if gone == "removed" {
				mustExec(t, w.owner, `DELETE FROM project_servers WHERE project_id = $1 AND name = 'api'`, w.project)
			} else {
				mustExec(t, w.owner, `UPDATE project_servers SET autostart_in_previews = false WHERE project_id = $1 AND name = 'api'`, w.project)
			}
			w.recipe("docs", 5000, "npm run docs", "docs", nil, true)
			mark := len(w.lux.TenantCalls())
			c := w.refusing()

			w.open(web)
			if _, ok := w.lux.TenantServer(api); ok {
				t.Fatalf("api's server is still in lux; calls %v", w.lux.TenantCalls()[mark:])
			}
			if n := w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND name = 'api'`, runID); n != 0 {
				t.Errorf("dude still holds api's server")
			}
			docs := w.serverID(runID, "docs")
			w.until("docs served", func() bool { sv, _ := w.lux.TenantServer(docs); return sv.State == lux.SrvReady })
			calls := w.lux.TenantCalls()[mark:]
			created, started := slices.Index(calls, "create "+docs+" docs"), slices.Index(calls, "start "+docs)
			if created < 0 || started < created {
				t.Errorf("lux saw %v; want docs created, then started", calls)
			}
			if !slices.Contains(calls, "delete "+api) {
				t.Errorf("lux saw %v; want api deleted", calls)
			}
			if n := c.deletesOf(api); n != 1 {
				t.Errorf("api's DELETE was sent %d times; want once", n)
			}
			// docs served means lux's Run is running; dude records that when
			// its event follower applies lux's state event, on its own
			// goroutine, so the row is waited for rather than read once.
			w.until("the preview to live on", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
			})
			hosts := w.lux.TenantServers()
			if len(hosts) != 2 {
				t.Errorf("lux has %v; want web and docs", hosts)
			}
		})
	}
}

// Every recipe removed: the preview's last server is deleted at the next
// wake, and with nothing left to open the preview ends.
func TestAPreviewWhoseLastRecipeIsRemovedEnds(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	mustExec(t, w.owner, `DELETE FROM project_servers WHERE project_id = $1`, w.project)
	w.lux.RequestServer(web, "/")
	w.until("the preview to end", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if _, ok := w.lux.TenantServer(web); ok {
		t.Error("its server is still in lux")
	}
	if n := len(w.luxRuns()); n != 0 {
		t.Errorf("%d lux runs submitted for a preview with nothing to serve", n)
	}
}

// staleWebServer starts a preview of the recipe web "npm run dev -- --new"
// and makes its web server in lux first, of the older "npm run dev", as a
// create whose answer was lost.
func (w *world) staleWebServer() (runID string, old lux.TenantServer) {
	w.t.Helper()
	w.recipe("web", 3000, "npm run dev -- --new", "", nil, true)
	task, runID := w.startPreview()
	old, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000,
		Command: servers.ShellCommand(nil, "npm run dev"), Workdir: servers.Workdir("target", ""), Env: map[string]string{"PORT": "1"},
		Hostname: servers.PreviewHostname(previewDomain, "web", w.previewOf(task), ""), Wake: "request", Lifetime: "owner",
		Labels: map[string]string{"dude.preview": runID}})
	if err != nil {
		w.t.Fatal(err)
	}
	return runID, old
}

// adoptedAtTheNewRecipe: dude holds old, and lux has it at the new command.
func (w *world) adoptedAtTheNewRecipe(runID string, old lux.TenantServer) {
	w.t.Helper()
	if w.serverID(runID, "web") != old.ID {
		w.t.Fatal("the server in lux was not adopted")
	}
	sv, _ := w.lux.TenantServer(old.ID)
	if want := servers.ShellCommand(nil, "npm run dev -- --new"); !slices.Equal(sv.Command, want) {
		w.t.Fatalf("adopted with command %v; want %v (calls %v)", sv.Command, want, w.lux.TenantCalls())
	}
}

// A server of the preview already in lux when it is declared (a create
// whose answer was lost) is adopted at the recipe as it is now.
func TestAnAdoptedServerTakesTheCurrentRecipe(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	runID, old := w.staleWebServer()
	w.untilAsleep(runID)
	w.adoptedAtTheNewRecipe(runID, old)
	if !slices.Contains(w.lux.TenantCalls(), fmt.Sprintf("patch %s command", old.ID)) {
		t.Errorf("lux saw %v; want only its command patched", w.lux.TenantCalls())
	}
}

// refusingLux is the world's lux client, answering in lux's stead: each
// PATCH the next of patches (nil: lux answers), and a DELETE of a server
// in deletes once. deleted counts the DELETEs asked, by id.
type refusingLux struct {
	lux.Client
	mu      sync.Mutex
	patches []error
	deletes map[string]error
	deleted map[string]int
	refused int
}

func (c *refusingLux) PatchServer(ctx context.Context, id string, in lux.PatchServer) (lux.TenantServer, error) {
	c.mu.Lock()
	var err error
	if len(c.patches) > 0 {
		err, c.patches = c.patches[0], c.patches[1:]
	}
	if err != nil {
		c.refused++
	}
	c.mu.Unlock()
	if err != nil {
		return lux.TenantServer{}, err
	}
	return c.Client.PatchServer(ctx, id, in)
}

func (c *refusingLux) DeleteServer(ctx context.Context, id string) error {
	c.mu.Lock()
	err := c.deletes[id]
	delete(c.deletes, id)
	c.deleted[id]++
	if err != nil {
		c.refused++
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.Client.DeleteServer(ctx, id)
}

func (c *refusingLux) deletesOf(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleted[id]
}

func (c *refusingLux) refusals() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused
}

func (w *world) refusing() *refusingLux {
	c := &refusingLux{Client: w.previews.Lux, deletes: map[string]error{}, deleted: map[string]int{}}
	w.previews.Lux = c
	return c
}

// letGo: what lux answered let the preview's wake (or declare) go, to
// be tried again later, without failing the preview; it is made due now.
func (w *world) letGo(runID string) {
	w.t.Helper()
	w.until("the attempt let go", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at > now() AND wake_claimed_at IS NULL`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID); n != 0 {
		w.t.Fatalf("the preview failed:\n%s", w.describeRuns())
	}
	mustExec(w.t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
}

// What lux answers a PATCH decides the wake: a refusal no retry changes
// (any 4xx but 409) fails the preview before anything starts; a 409 (the
// server attached or detached meanwhile) lets the wake go and the next
// one PATCHes what is still different, before the servers start.
func TestWhatLuxAnswersAPatchDecidesTheWake(t *testing.T) {
	conflict := &lux.Error{Status: 409, Code: "conflict", Message: "the server changed meanwhile"}
	for _, tc := range []struct {
		name    string
		patches []error
		edit    string // web's env
		reason  string // the preview fails with; "": it is retried
	}{
		{"422", []error{&lux.Error{Status: 422, Code: "invalid_server", Message: "server.port: need 1-65535"}}, `[{"name":"A","value":"1"}]`,
			"lux refused preview server web: server.port: need 1-65535"},
		{"403", []error{&lux.Error{Status: 403, Code: "forbidden", Message: "not this key's server"}}, `[{"name":"A","value":"1"}]`,
			"lux refused preview server web: not this key's server"},
		{"422 from lux's own check", nil, `[{"name":"NOT-A-NAME","value":"1"}]`,
			`lux refused preview server web: server.env: invalid name "NOT-A-NAME"`},
		{"409 on the first of two", []error{conflict}, `[{"name":"A","value":"1"}]`, ""},
		{"409 on the second of two", []error{nil, conflict}, `[{"name":"A","value":"1"}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("api", 4000, "go run .", "", nil, true)
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			api, web := w.serverID(runID, "api"), w.serverID(runID, "web")
			c := w.refusing()
			c.patches = tc.patches
			mark := len(w.lux.TenantCalls())
			w.editRecipe("web", "npm run dev -- --new", tc.edit)
			if tc.reason == "" {
				w.editRecipe("api", "go run ./cmd/api", `[{"name":"B","value":"2"}]`)
			}
			w.lux.RequestServer(web, "/")

			if tc.reason != "" {
				w.until("the preview to fail", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1
				})
				if got := w.str(`SELECT error FROM runs WHERE id = $1`, runID); got != tc.reason {
					t.Errorf("error %q; want %q", got, tc.reason)
				}
				if n := len(w.luxRuns()); n != 0 {
					t.Errorf("%d lux runs submitted for a preview lux refused", n)
				}
				for _, call := range w.lux.TenantCalls()[mark:] {
					if strings.HasPrefix(call, "start ") || strings.HasPrefix(call, "patch ") {
						t.Errorf("lux saw %v", w.lux.TenantCalls()[mark:])
						break
					}
				}
				return
			}
			w.until("lux's 409", func() bool { return c.refusals() == 1 })
			w.letGo(runID)
			w.until("both served", func() bool {
				a, _ := w.lux.TenantServer(api)
				b, _ := w.lux.TenantServer(web)
				return a.State == lux.SrvReady && b.State == lux.SrvReady
			})
			patchedBeforeStart(t, w.tenantCallsOf(api, mark), api, "command,env")
			patchedBeforeStart(t, w.tenantCallsOf(web, mark), web, "command,env")
			luxRun := w.luxRuns()[0].ID
			if env := w.lux.ServerEnv(luxRun, "web"); env["A"] != "1" {
				t.Errorf("web started with %v", env)
			}
			if env := w.lux.ServerEnv(luxRun, "api"); env["B"] != "2" {
				t.Errorf("api started with %v", env)
			}
		})
	}
}

// A 409 on the PATCH of a server adopted on declare is tried again, not a
// refusal: the next pass PATCHes it to the recipe.
func TestAConflictPatchingAnAdoptedServerIsTriedAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	c := w.refusing()
	c.patches = []error{&lux.Error{Status: 409, Code: "conflict", Message: "the server changed meanwhile"}}
	runID, old := w.staleWebServer()
	w.until("lux's 409", func() bool { return c.refusals() == 1 })
	w.letGo(runID)
	w.untilAsleep(runID)
	w.adoptedAtTheNewRecipe(runID, old)
}

// A server whose recipe was removed, and whose delete in lux failed, is
// deleted all the same: by the next wake, or when the preview ends.
func TestADroppedServerLuxDidNotDeleteIsDeletedLater(t *testing.T) {
	for _, then := range []string{"next wake", "preview ends"} {
		t.Run(then, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.recipe("api", 4000, "go run .", "", nil, true)
			task, runID := w.declare()
			web, api := w.serverID(runID, "web"), w.serverID(runID, "api")
			c := w.refusing()
			c.deletes[api] = &lux.Error{Status: 503, Code: "unavailable", Message: "try again"}
			mustExec(t, w.owner, `DELETE FROM project_servers WHERE project_id = $1 AND name = 'api'`, w.project)
			w.lux.RequestServer(web, "/")
			w.until("lux's 503", func() bool { return c.refusals() == 1 })
			if _, ok := w.lux.TenantServer(api); !ok {
				t.Fatal("api was deleted in lux all the same")
			}
			if then == "next wake" {
				w.letGo(runID)
				w.until("web served", func() bool { sv, _ := w.lux.TenantServer(web); return sv.State == lux.SrvReady })
			} else {
				if code, _ := w.do("DELETE", "/internal/tasks/"+task+"/preview", nil); code != 200 {
					t.Fatal(code)
				}
				mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
				w.until("its servers deleted", func() bool { _, ok := w.lux.TenantServer(web); return !ok })
			}
			if _, ok := w.lux.TenantServer(api); ok {
				t.Errorf("api is still in lux; calls %v", w.lux.TenantCalls())
			}
		})
	}
}

// listedAfter is lux whose first label list of a preview's servers first
// runs then: what another orchestrator did between this one's read of the
// preview's rows and its list.
type listedAfter struct {
	lux.Client
	mu   sync.Mutex
	then func()
}

func (c *listedAfter) ListServers(ctx context.Context, hostname string, labels ...string) ([]lux.TenantServer, error) {
	c.mu.Lock()
	then := c.then
	if len(labels) > 0 {
		c.then = nil
	}
	c.mu.Unlock()
	if len(labels) > 0 && then != nil {
		then()
	}
	return c.Client.ListServers(ctx, hostname, labels...)
}

// A declare of a pending preview deletes no server it has no row of:
// another orchestrator may be creating one. The next wake, which is
// claimed, deletes it once it is a real leftover (no row, no recipe).
func TestADeclareLeavesLeftoversToTheNextWake(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	inner := w.previews.Lux
	var theirs lux.TenantServer
	w.previews.Lux = &listedAfter{Client: inner, then: func() {
		var err error
		theirs, err = inner.CreateServer(context.Background(), lux.CreateServer{Name: "api", Port: 4000,
			Hostname: "api-theirs." + previewDomain, Wake: "request", Lifetime: "owner", Labels: map[string]string{"dude.preview": runID}})
		if err != nil {
			t.Error(err)
			return
		}
		mustExec(t, w.owner, `INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname)
			VALUES ($1, $2, 'api', $3, $4)`, runID, w.org, theirs.ID, "api-theirs."+previewDomain)
	}}
	// A declare retried after a partial create: pending, with web's row live.
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending' WHERE id = $1`, runID)
	w.until("the declare done", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status <> 'pending'`, runID) == 1
	})
	if theirs.ID == "" {
		t.Fatal("the other orchestrator's server was never made")
	}
	if _, ok := w.lux.TenantServer(theirs.ID); !ok {
		t.Fatalf("the declare deleted the other orchestrator's server; calls %v", w.lux.TenantCalls())
	}

	mustExec(t, w.owner, `DELETE FROM preview_servers WHERE lux_server_id = $1`, theirs.ID)
	w.open(web)
	if _, ok := w.lux.TenantServer(theirs.ID); ok {
		t.Errorf("the next wake left the leftover in lux; calls %v", w.lux.TenantCalls())
	}
}
