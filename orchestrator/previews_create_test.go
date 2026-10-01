package orchestrator_test

// Creating a wakeable preview's lux servers: which previews get them, a
// create whose answer was lost, and two orchestrators creating at once.

import (
	"context"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// labelled is how many servers lux has for a preview.
func (w *world) labelled(runID string) int {
	w.t.Helper()
	list, err := w.previews.Lux.ListServers(context.Background(), "", "dude.preview="+runID)
	if err != nil {
		w.t.Fatal(err)
	}
	return len(list)
}

// startPreview asks for a preview of a new task, as declare does, without
// sweeping. Returns the task and run ids.
func (w *world) startPreview() (task, runID string) {
	w.t.Helper()
	task = w.task()
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		w.t.Fatalf("start = %d %v", code, out)
	}
	return task, out["run"].(map[string]any)["id"].(string)
}

// A project with no server to start in previews keeps the old path: there
// is nothing to wake by URL.
func TestAPreviewOfNoAutostartServerIsNotWakeable(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, false)
	_, runID := w.startPreview()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND NOT wakeable`, runID); n != 1 {
		t.Fatal("a preview with no autostart server is wakeable")
	}
}

// A create lux took whose answer dude lost (no row) is adopted, not made
// twice.
func TestAServerWhoseCreateWasLostIsAdopted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	before := w.serverID(runID, "web")
	mustExec(t, w.owner, `DELETE FROM preview_servers WHERE run_id = $1`, runID)
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending' WHERE id = $1`, runID)
	w.until("asleep again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if got := w.serverID(runID, "web"); got != before || len(w.lux.TenantServers()) != 1 {
		t.Fatalf("server %s (was %s), lux has %v", got, before, w.lux.TenantServers())
	}
}

// hiddenOnce is another orchestrator's create not yet visible: the first
// list by hostname answers empty.
type hiddenOnce struct {
	lux.Client
	mu     sync.Mutex
	hidden bool
}

func (h *hiddenOnce) ListServers(ctx context.Context, hostname string, labels ...string) ([]lux.TenantServer, error) {
	h.mu.Lock()
	hide := hostname != "" && !h.hidden
	h.hidden = h.hidden || hide
	h.mu.Unlock()
	if hide {
		return nil, nil
	}
	return h.Client.ListServers(ctx, hostname, labels...)
}

// Another orchestrator created this preview's server at its hostname
// between this one's look and its create (409 hostname_taken): it is
// adopted, not made again under a salted hostname.
func TestAServerAnotherOrchestratorCreatedIsAdopted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.startPreview()
	host := servers.PreviewHostname(previewDomain, "web", task, w.project, "")
	theirs, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000, Hostname: host,
		Wake: "request", Lifetime: "owner", Labels: map[string]string{"dude.preview": runID}})
	if err != nil {
		t.Fatal(err)
	}
	w.previews.Lux = &hiddenOnce{Client: w.previews.Lux}
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if n := w.labelled(runID); n != 1 || w.serverID(runID, "web") != theirs.ID {
		t.Fatalf("lux has %d servers for the preview; dude holds %s, want the other's %s", n, w.serverID(runID, "web"), theirs.ID)
	}
}

// recordedFirst is another orchestrator recording its own server for the
// preview just after this one's create.
type recordedFirst struct {
	lux.Client
	record func()
}

func (r *recordedFirst) CreateServer(ctx context.Context, in lux.CreateServer) (lux.TenantServer, error) {
	sv, err := r.Client.CreateServer(ctx, in)
	if err == nil && r.record != nil {
		r.record()
		r.record = nil
	}
	return sv, err
}

// Two orchestrators each created a server for the same preview; the one
// whose row was recorded second deletes its own: no orphan in lux.
func TestASecondServerForThePreviewIsDeleted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.startPreview()
	theirs, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000,
		Hostname: "theirs." + previewDomain, Wake: "request", Lifetime: "owner", Labels: map[string]string{"dude.preview": runID}})
	if err != nil {
		t.Fatal(err)
	}
	w.previews.Lux = &recordedFirst{Client: w.previews.Lux, record: func() {
		mustExec(t, w.owner, `INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname)
			VALUES ($1, $2, 'web', $3, $4)`, runID, w.org, theirs.ID, "theirs."+previewDomain)
	}}
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if n := w.labelled(runID); n != 1 || w.serverID(runID, "web") != theirs.ID {
		t.Fatalf("lux has %d servers for the preview; want only the recorded %s", n, theirs.ID)
	}
}
