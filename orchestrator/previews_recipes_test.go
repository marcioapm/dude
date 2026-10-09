package orchestrator_test

// A wakeable preview's lux servers follow its project's recipes: each wake
// makes them what the recipes say now, before lux starts them, on every
// path a wake takes (the first submit, a resume of the same Run, a
// replacement Run), and on declare.

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
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
		t.Fatalf("lux saw %v; want one PATCH of %s to %s, before its start", calls, fields, id)
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
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			web := w.serverID(runID, "web")
			if path != "first submit" {
				w.open(web)
				w.running(runID, "web")
				w.lux.Idle(web)
				w.until("parked", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
				})
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

// A wake with the recipes as they were sends lux no PATCH.
func TestAnUnchangedRecipeIsNotPatched(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	setup := "npm ci"
	w.recipe("web", 3000, "npm run dev", "apps/web", &setup, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.open(web)
	for _, c := range w.lux.TenantCalls() {
		if strings.HasPrefix(c, "patch ") {
			t.Fatalf("lux saw %q for recipes nobody changed; all calls %v", c, w.lux.TenantCalls())
		}
	}
	if r := w.luxRuns()[0]; r.Resumed != 1 {
		t.Fatalf("resumed %d times", r.Resumed)
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
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID); n != 1 {
				t.Errorf("the preview did not live on:\n%s", w.describeRuns())
			}
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

// A server of the preview already in lux when it is declared (a create
// whose answer was lost) is adopted at the recipe as it is now.
func TestAnAdoptedServerTakesTheCurrentRecipe(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev -- --new", "", nil, true)
	task, runID := w.startPreview()
	old, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000,
		Command: servers.ShellCommand(nil, "npm run dev"), Workdir: servers.Workdir("target", ""), Env: map[string]string{"PORT": "1"},
		Hostname: servers.PreviewHostname(previewDomain, "web", w.previewOf(task), ""), Wake: "request", Lifetime: "owner",
		Labels: map[string]string{"dude.preview": runID}})
	if err != nil {
		t.Fatal(err)
	}
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if w.serverID(runID, "web") != old.ID {
		t.Fatal("the server in lux was not adopted")
	}
	sv, _ := w.lux.TenantServer(old.ID)
	if want := servers.ShellCommand(nil, "npm run dev -- --new"); !slices.Equal(sv.Command, want) {
		t.Fatalf("adopted with command %v; want %v (calls %v)", sv.Command, want, w.lux.TenantCalls())
	}
	if !slices.Contains(w.lux.TenantCalls(), fmt.Sprintf("patch %s command", old.ID)) {
		t.Errorf("lux saw %v; want only its command patched", w.lux.TenantCalls())
	}
}
