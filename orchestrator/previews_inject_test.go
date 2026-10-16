package orchestrator_test

// Commenting on a preview: every wakeable preview server carries one
// script tag loading dude's overlay from auth.public_url, on create and on
// the recipe sync, only when lux says its servers take inject
// (serverInject) and public_url is set. Asserted on what lux was sent.

import (
	"encoding/json"
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
				case tc.want != "" && (s == nil || *s != tc.want):
					t.Errorf("%s: inject %v, want %s", calls[i], s, tc.want)
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
			if !tc.serverInject {
				// What the server holds is lux's; an older lux reports none.
				w.lux.ServerInject = false
			}
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
