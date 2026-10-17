package orchestrator_test

// Commenting on a preview: every wakeable preview server carries one
// script tag loading dude's overlay from auth.public_url, on create and on
// the recipe sync, only when lux says its servers take inject
// (serverInject) and public_url is set. Asserted on what lux was sent.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

const dudeOrigin = "https://dude.example.test"

const overlayTag = `<script src="https://dude.example.test/feedback/overlay.js" data-dude-origin="https://dude.example.test" async></script>`

// commenting sets the two facts commenting needs: lux's serverInject (on
// the fake and as startup read it) and dude's public_url.
func (w *world) commenting(serverInject bool, publicURL string) {
	w.lux.ServerInject = serverInject
	w.previews.ServerInject = serverInject
	w.previews.PublicURL = publicURL
}

// injectsSent is, for each create and patch of a tenant server lux was
// sent from index from on, whether its body named inject and with what.
func (w *world) injectsSent(from int) (calls []string, sent []*string) {
	for _, b := range w.lux.TenantBodies()[from:] {
		calls = append(calls, b.Call)
		raw, ok := b.Fields["inject"]
		if !ok {
			sent = append(sent, nil)
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			w.t.Fatalf("%s: inject %s", b.Call, raw)
		}
		sent = append(sent, &s)
	}
	return calls, sent
}

func creates(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "POST ") {
			n++
		}
	}
	return n
}

// runningPreview declares a wakeable preview of one server, web, made
// under origin ("" none) by a lux whose servers take inject, and wakes it
// until web serves.
func (w *world) runningPreview(origin string) (task, runID, web string) {
	w.t.Helper()
	w.wakeable()
	w.luxsWakeTimeout()
	w.commenting(true, origin)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID = w.declare()
	web = w.serverID(runID, "web")
	w.running(runID, "web")
	w.until("web ready", func() bool { sv, _ := w.lux.TenantServer(web); return sv.State == lux.SrvReady })
	return task, runID, web
}

// A server is created with the snippet only when both hold; otherwise its
// create names no inject at all (an older lux refuses the property).
func TestAPreviewServerIsCreatedWithTheOverlayOnlyWhenCommentingIsAvailable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		serverInject bool
		publicURL    string
		want         string
	}{
		{"lux without serverInject", false, dudeOrigin, ""},
		{"public_url unset", true, "", ""},
		{"both", true, dudeOrigin, overlayTag},
		{"both, public_url with a trailing slash", true, dudeOrigin + "/", overlayTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.commenting(tc.serverInject, tc.publicURL)
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.recipe("api", 4000, "go run .", "api", nil, true)
			task, runID := w.declare()
			calls, sent := w.injectsSent(0)
			if creates(calls) != 2 {
				t.Fatalf("lux was sent %v; want two creates", calls)
			}
			for i, s := range sent {
				switch {
				case tc.want == "" && s != nil:
					t.Errorf("%s named inject %q", calls[i], *s)
				case tc.want != "" && s == nil:
					t.Errorf("%s: no inject, want %q", calls[i], tc.want)
				case tc.want != "" && *s != tc.want:
					t.Errorf("%s: inject %q, want %q", calls[i], *s, tc.want)
				}
			}
			if tc.want == "" {
				return
			}
			// The snippet carries dude's origin and nothing of the preview:
			// no task, run, project, organisation, key or person.
			var key string
			if err := w.owner.QueryRow(t.Context(), `SELECT p.key_prefix || '-' || t.number FROM tasks t
				JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, task).Scan(&key); err != nil {
				t.Fatal(err)
			}
			for _, s := range sent {
				for _, id := range []string{task, runID, key, strings.ToLower(key), w.project, w.org, w.actor, "lux-key", "svc"} {
					if id != "" && strings.Contains(*s, id) {
						t.Errorf("the snippet %s holds %q", *s, id)
					}
				}
			}
			sv, _ := w.lux.TenantServer(w.serverID(runID, "web"))
			if sv.Inject != overlayTag {
				t.Errorf("lux holds inject %q", sv.Inject)
			}
		})
	}
}

// The recipe sync (each wake, before lux starts the servers) puts the
// current snippet on a server made without it or with another origin's,
// and sends no inject while commenting is unavailable.
func TestTheRecipeSyncPutsTheCurrentOverlayOnAServer(t *testing.T) {
	for _, tc := range []struct {
		name                string
		before              string // the origin the server was made with; "" none
		serverInject        bool
		publicURL, wantSent string
	}{
		{"made without, lux without serverInject", "", false, dudeOrigin, ""},
		{"made without, public_url unset", "", true, "", ""},
		{"made without", "", true, dudeOrigin, overlayTag},
		{"made with another origin", "https://old.example.test", true, dudeOrigin + "/", overlayTag},
		{"made with the current one", dudeOrigin, true, dudeOrigin, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.luxsWakeTimeout()
			w.commenting(tc.before != "", tc.before)
			_, web := w.asleepPreview()
			w.commenting(tc.serverInject, tc.publicURL)
			mark, marks := len(w.lux.TenantCalls()), len(w.lux.TenantBodies())
			w.lux.RequestServer(web, "/")
			w.until("web served again", func() bool {
				sv, _ := w.lux.TenantServer(web)
				return sv.State == lux.SrvReady
			})
			_, sent := w.injectsSent(marks)
			var got []string
			for _, s := range sent {
				if s != nil {
					got = append(got, *s)
				}
			}
			if tc.wantSent == "" {
				if len(got) != 0 {
					t.Errorf("the sync sent inject %q", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tc.wantSent {
				t.Fatalf("the sync sent inject %q; want once %s", got, tc.wantSent)
			}
			patchedBeforeStart(t, w.tenantCallsOf(web, mark), web, "inject")
			if sv, _ := w.lux.TenantServer(web); sv.Inject != overlayTag {
				t.Errorf("lux holds inject %q", sv.Inject)
			}
		})
	}
}

// serverFacts is what a task's servers read (the web's) says of one
// server about commenting, as JSON had it.
func (w *world) serverFacts(task, name string) (commenting map[string]any, needsRestart any) {
	w.t.Helper()
	code, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
	if code != 200 {
		w.t.Fatalf("servers = %d %v", code, out)
	}
	sv := serverNamed(out, name)
	if sv == nil {
		w.t.Fatalf("no server %s in %v", name, out)
	}
	commenting, _ = sv["commenting"].(map[string]any)
	return commenting, sv["needsRestart"]
}

// The servers read says, per wakeable preview server, whether commenting
// is available (and if not, what is missing), and whether a running
// server serves its pages without the overlay wanted now.
func TestTheServersReadSaysWhetherCommentingIsAvailable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		serverInject bool
		publicURL    string
		want         map[string]any
	}{
		{"public_url unset", true, "", map[string]any{"available": false, "reason": "public_url"}},
		{"neither", false, "", map[string]any{"available": false, "reason": "public_url"}},
		{"lux without serverInject", false, dudeOrigin, map[string]any{"available": false, "reason": "lux"}},
		{"both", true, dudeOrigin, map[string]any{"available": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.commenting(tc.serverInject, tc.publicURL)
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			task, _ := w.declare()
			commenting, needsRestart := w.serverFacts(task, "web")
			if !reflect.DeepEqual(commenting, tc.want) || needsRestart != false {
				t.Errorf("commenting %v needsRestart %v; want %v false", commenting, needsRestart, tc.want)
			}
		})
	}
}

// A running server made without the overlay, or with another origin's,
// needs a restart; one with the current one, or one asleep, does not. The
// restart people already have puts it on, and nothing else does.
func TestARunningServerWithoutTheCurrentOverlayNeedsARestart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string // the origin the running server was made with; "" none
		want   bool
	}{
		{"made without", "", true},
		{"made with another origin", "https://old.example.test", true},
		{"made with the current one", dudeOrigin, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			task, runID, web := w.runningPreview(tc.before)
			w.commenting(true, dudeOrigin)

			commenting, needsRestart := w.serverFacts(task, "web")
			if commenting["available"] != true || needsRestart != tc.want {
				t.Fatalf("commenting %v needsRestart %v; want available, %v", commenting, needsRestart, tc.want)
			}
			// Reading changes nothing in lux, and a sweep restarts nothing.
			marks := len(w.lux.TenantBodies())
			for range 3 {
				w.pump()
			}
			if _, sent := w.injectsSent(marks); len(sent) != 0 {
				t.Fatalf("lux was sent %d changes no one asked for", len(sent))
			}
			if !tc.want {
				return
			}
			if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/restart", nil); code != 200 {
				t.Fatalf("restart = %d %v", code, out)
			}
			if sv, _ := w.lux.TenantServer(web); sv.Inject != overlayTag {
				t.Errorf("after the restart lux holds inject %q", sv.Inject)
			}
			if _, needsRestart := w.serverFacts(task, "web"); needsRestart != false {
				t.Errorf("after the restart needsRestart %v", needsRestart)
			}
		})
	}
}

// Start all puts the current overlay on each server it starts, as a
// person's start of one does.
func TestStartAllPutsTheCurrentOverlayOnAServer(t *testing.T) {
	w := newWorld(t)
	_, runID, web := w.runningPreview("")
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/stop", nil); code != 200 {
		t.Fatalf("stop = %d %v", code, out)
	}
	w.commenting(true, dudeOrigin)
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/start-all", nil); code != 200 {
		t.Fatalf("start-all = %d %v", code, out)
	}
	sv, _ := w.lux.TenantServer(web)
	if sv.Inject != overlayTag {
		t.Errorf("after Start all lux holds inject %q", sv.Inject)
	}
	if sv.State == lux.SrvStopped {
		t.Errorf("Start all left web stopped")
	}
}

// An asleep server without the current overlay is not flagged: its next
// wake syncs it before lux starts it.
func TestAnAsleepServerNeverNeedsARestart(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.luxsWakeTimeout()
	w.commenting(true, "")
	runID, _ := w.asleepPreview()
	w.commenting(true, dudeOrigin)
	var task string
	if err := w.owner.QueryRow(t.Context(), `SELECT task_id FROM runs WHERE id = $1`, runID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if commenting, needsRestart := w.serverFacts(task, "web"); commenting["available"] != true || needsRestart != false {
		t.Errorf("asleep: commenting %v needsRestart %v", commenting, needsRestart)
	}
}
