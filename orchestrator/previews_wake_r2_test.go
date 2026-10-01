package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// resumedThen409 resumes in lux, then answers 409 as if refused: lux moved
// the Run up between dude's Get and its Resume.
type resumedThen409 struct {
	lux.Client
	done bool
}

func (c *resumedThen409) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	if c.done {
		return c.Client.Resume(ctx, id, in)
	}
	c.done = true
	if _, err := c.Client.Resume(ctx, id, in); err != nil {
		return lux.Run{}, err
	}
	return lux.Run{}, &lux.Error{Status: 409, Code: "not_resumable", Message: "run is resuming: stop it first"}
}

func TestA409OnAResumeAlreadyComingUpKeepsTheRun(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	_, web := w.asleepPreview()
	w.previews.Lux = &resumedThen409{Client: w.previews.Lux}
	w.open(web)
	if n := len(w.luxRuns()); n != 1 {
		t.Fatalf("%d lux runs; a Run lux was resuming was abandoned for a new one", n)
	}
}

func TestASaltedServerWhoseCreateWasLostIsAdopted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.task()
	taken := servers.PreviewHostname(previewDomain, "web", task, w.project, "")
	if _, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 1, Hostname: taken}); err != nil {
		t.Fatal(err)
	}
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		t.Fatal(code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	before := w.serverID(runID, "web")
	mustExec(t, w.owner, `DELETE FROM preview_servers WHERE run_id = $1`, runID)
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending' WHERE id = $1`, runID)
	w.until("asleep again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if got := w.serverID(runID, "web"); got != before || w.labelled(runID) != 1 {
		t.Fatalf("server %s (was %s), %d labelled", got, before, w.labelled(runID))
	}
}

func TestACrashedPreviewWithADetachedServerWakes(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.running(runID, "web")
	w.lux.Crash(w.luxRuns()[0].ID)
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if err := w.previews.Lux.DetachServer(context.Background(), web); err != nil {
		t.Fatal(err)
	}
	w.open(web)
	if n := len(w.luxRuns()); n != 1 {
		t.Fatalf("%d lux runs; want the failed one resumed", n)
	}
}
