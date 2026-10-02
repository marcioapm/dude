package orchestrator_test

// A wakeable preview's hostname: its server's name, its task's key and its
// project's slug. Written against the world's helpers and lux alone, so it
// reads the same whatever names the hostname in dude.

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// jervasion makes the world's project "jervasion", key prefix JERV, and
// gives it a second task, JERV-2, returned.
func (w *world) jervasion() string {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE projects SET slug = 'jervasion', key_prefix = 'JERV' WHERE id = $1`, w.project)
	w.task()
	task := w.task()
	if key := w.str(`SELECT p.key_prefix || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`,
		task); key != "JERV-2" {
		w.t.Fatalf("task key %s", key)
	}
	return task
}

// startPreviewOf asks for a preview of a task without sweeping.
func (w *world) startPreviewOf(task string) string {
	w.t.Helper()
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		w.t.Fatalf("start = %d %v", code, out)
	}
	return out["run"].(map[string]any)["id"].(string)
}

func (w *world) untilAsleep(runID string) {
	w.t.Helper()
	w.until("the preview to be asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wakeable`, runID) == 1
	})
}

func TestAPreviewIsNamedByItsTaskKeyAndProjectSlug(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.jervasion()
	runID := w.startPreviewOf(task)
	w.untilAsleep(runID)

	host := "web-jerv-2-jervasion." + previewDomain
	if got := w.str(`SELECT hostname FROM preview_servers WHERE run_id = $1`, runID); got != host {
		t.Fatalf("hostname %s, want %s", got, host)
	}
	sv, ok := w.lux.TenantServer(w.serverID(runID, "web"))
	if !ok || *sv.Hostname != host || *sv.URL != "https://"+host {
		t.Fatalf("lux has %+v", sv)
	}
	// dude stores and shows lux's URL.
	if got := w.str(`SELECT url FROM preview_servers WHERE run_id = $1`, runID); got != "https://"+host {
		t.Errorf("stored url %s", got)
	}
	code, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
	if web := serverNamed(out, "web"); code != 200 || web == nil || web["url"] != "https://"+host {
		t.Errorf("task's servers = %d %v", code, out)
	}
}

// Another org's project may have the same slug and a task of the same key:
// its preview holds the hostname, and this one takes the salted second
// choice.
func TestAnotherOrgsPreviewOfTheSameKeyAndSlugGetsTheSaltedName(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.jervasion()
	plain := "web-jerv-2-jervasion." + previewDomain
	theirs, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000, Hostname: plain,
		Wake: "request", Lifetime: "owner", Labels: map[string]string{"dude.org": "org_other", "dude.preview": "run_other"}})
	if err != nil {
		t.Fatal(err)
	}
	runID := w.startPreviewOf(task)
	w.untilAsleep(runID)
	got := w.str(`SELECT hostname FROM preview_servers WHERE run_id = $1`, runID)
	salted := regexp.MustCompile(`^web-jerv-2-jervasion-[0-9a-f]{8}\.` + regexp.QuoteMeta(previewDomain) + `$`)
	if !salted.MatchString(got) {
		t.Fatalf("hostname %s, want the salted web-jerv-2-jervasion-<hash>", got)
	}
	if sv, ok := w.lux.TenantServer(theirs.ID); !ok || *sv.Hostname != plain || w.labelled(runID) != 1 {
		t.Errorf("the other org's server %+v; %d of this preview", sv, w.labelled(runID))
	}
}

// A server lux made for this preview under the id-based hostname (before
// key and slug named them) whose create's answer was lost is adopted, not
// made again under the new name.
func TestAnOldSchemeServerIsAdopted(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.task()
	runID := w.startPreviewOf(task)
	idPart := func(id string) string { return strings.ReplaceAll(strings.ToLower(id), "_", "-") }
	old := "web-" + idPart(task) + "-" + idPart(w.project) + "." + previewDomain
	theirs, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000, Hostname: old,
		Wake: "request", Lifetime: "owner", Labels: map[string]string{"dude.org": w.org, "dude.project": w.project,
			"dude.task": task, "dude.preview": runID, "dude.kind": "preview"}})
	if err != nil {
		t.Fatal(err)
	}
	w.untilAsleep(runID)
	if got := w.serverID(runID, "web"); got != theirs.ID || w.labelled(runID) != 1 || len(w.lux.TenantServers()) != 1 {
		t.Fatalf("dude holds %s, want the old %s; lux has %v", got, theirs.ID, w.lux.TenantServers())
	}
	if got := w.str(`SELECT hostname FROM preview_servers WHERE run_id = $1`, runID); got != old {
		t.Errorf("hostname %s, want the old %s", got, old)
	}
}

// A preview's servers keep their hostname when its project's key prefix is
// renamed, and a server whose create's answer was lost is still found.
func TestAServerIsAdoptedAfterTheKeyPrefixIsRenamed(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.jervasion()
	runID := w.startPreviewOf(task)
	w.untilAsleep(runID)
	before := w.serverID(runID, "web")
	mustExec(t, w.owner, `UPDATE projects SET key_prefix = 'JV' WHERE id = $1`, w.project)
	mustExec(t, w.owner, `DELETE FROM preview_servers WHERE run_id = $1`, runID)
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending' WHERE id = $1`, runID)
	w.untilAsleep(runID)
	if got := w.serverID(runID, "web"); got != before || w.labelled(runID) != 1 {
		t.Fatalf("dude holds %s (was %s); %d servers of the preview in lux", got, before, w.labelled(runID))
	}
	if got := w.str(`SELECT hostname FROM preview_servers WHERE run_id = $1`, runID); got != "web-jerv-2-jervasion."+previewDomain {
		t.Errorf("hostname %s", got)
	}
}
