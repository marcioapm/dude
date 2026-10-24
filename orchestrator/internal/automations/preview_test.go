package automations

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func (h *harness) cookieHeld(luxRunID string) bool {
	h.runner.mu.Lock()
	defer h.runner.mu.Unlock()
	_, ok := h.runner.previewCookies[luxRunID]
	return ok
}

// A Server run's preview sign-in is let go of however its lux Run ends:
// ended by dude (stopOrCancel, which the cancel and cancelled-while-starting
// paths take), Start fresh, and a kept session's expiry.
func TestAutomationPreviewSignInIsForgottenOnEveryEnd(t *testing.T) {
	h := newHarness(t)
	h.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	posted := func(id string, n int) func() bool {
		return func() bool {
			var got int
			_ = h.owner.QueryRow(context.Background(), `SELECT COALESCE(sum(posted), 0) FROM automation_runs WHERE automation_id = $1`, id).Scan(&got)
			return got == n
		}
	}

	h.automation("aut_cancel", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": nil}})
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first event is POSTed", posted("aut_cancel", 1))
	h.runner.Wait()
	first := h.runs("aut_cancel")[0]
	if !h.cookieHeld(first.LuxRunID) {
		t.Fatal("no sign-in held for the running server")
	}
	exec(t, h.owner, `UPDATE automation_runs SET status = 'cancelled', ended_at = now() WHERE id = $1`, first.ID)
	h.runner.EndCancelled(context.Background(), []Cancelled{{LuxRunID: first.LuxRunID}})
	if h.cookieHeld(first.LuxRunID) {
		t.Error("a cancelled run's preview sign-in is still held")
	}
	exec(t, h.owner, `UPDATE automations SET enabled = false WHERE id = 'aut_cancel'`)

	for _, end := range []string{"start fresh", "expiry"} {
		id := "aut_" + strings.ReplaceAll(end, " ", "_")
		h.automation(id, "server", map[string]any{"kind": "github", "event": "push"},
			map[string]any{"session_mode": "resumable", "expiry_days": 1, "server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": nil}})
		h.deliver("p-"+id, "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
		h.until(id+"'s event is POSTed", posted(id, 1))
		h.runner.Wait()
		run := h.runs(id)[0]
		if !h.cookieHeld(run.LuxRunID) {
			t.Fatalf("%s: no sign-in held", id)
		}
		// Kept stopped but signed-in, as a run cancelled with its session kept is.
		exec(t, h.owner, `UPDATE automation_runs SET status = 'cancelled', ended_at = now() WHERE id = $1`, run.ID)
		h.runner.mu.Lock()
		h.runner.previewCookies[run.LuxRunID] = "lux_preview=kept"
		h.runner.mu.Unlock()
		if end == "start fresh" {
			if err := h.runner.StartFresh(context.Background(), h.org, id); err != nil {
				t.Fatal(err)
			}
		} else {
			exec(t, h.owner, `UPDATE automation_sessions SET last_run_at = now() - interval '2 days' WHERE automation_id = $1`, id)
			h.until(id+"'s session expires", func() bool {
				var n int
				_ = h.owner.QueryRow(context.Background(), `SELECT count(*) FROM automation_sessions WHERE automation_id = $1`, id).Scan(&n)
				return n == 0
			})
		}
		if h.cookieHeld(run.LuxRunID) {
			t.Errorf("%s: the ended lux Run's preview sign-in is still held", end)
		}
		exec(t, h.owner, `UPDATE automations SET enabled = false WHERE id = $1`, id)
	}
}

// A sign-in that cannot reach the preview says so without its ticket: the
// error is logged, and the ticket is a credential.
func TestAutomationPreviewSignInErrorHoldsNoTicket(t *testing.T) {
	h := newHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	ln.Close()
	h.automation("aut_dial", "command", map[string]any{"kind": "github", "event": "push"}, map[string]any{"command": "sleep 5"})
	h.deliver("d-1", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("its container starts", func() bool { r := h.runs("aut_dial"); return len(r) == 1 && r[0].LuxRunID != "" })
	luxRun := h.runs("aut_dial")[0].LuxRunID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = h.runner.previewSignIn(ctx, previewAuth{luxRunID: luxRun, base: base})
	if err == nil || strings.Contains(err.Error(), "ticket") || strings.Contains(err.Error(), "tkt_") {
		t.Errorf("sign-in error %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), base+"/.lux/auth") {
		t.Errorf("sign-in error %v does not say where it went", err)
	}
	h.runner.Wait()
}
