package orchestrator_test

// A project's running servers (the project Servers page's one read):
// task Runs and branch previews alike, everyone's, and nothing to an
// organisation the project is not in.

import (
	"encoding/json"
	"testing"
)

func TestAProjectsRunningServersListEveryLiveRunAndRefuseOtherOrganisations(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/list", "main", "list")
	task, taskRun := w.declare()
	branchRun := w.startBranch("feature/list")
	// An ended branch preview is not running.
	w.gh.Branch("feature/old", "main", "old")
	ended := w.startBranch("feature/old")
	if code, out := w.do("DELETE", "/internal/branch-previews/"+ended, nil); code != 200 {
		t.Fatalf("stop: %d %v", code, out)
	}

	path := "/internal/projects/" + w.project + "/servers/running"
	code, body := w.get(path, w.org)
	if code != 200 {
		t.Fatalf("list: %d %s", code, body)
	}
	var out struct {
		Runs []struct {
			ID     string  `json:"id"`
			Kind   string  `json:"kind"`
			Branch *string `json:"branch"`
			Task   *struct {
				ID, Key, Title string
			} `json:"task"`
			Servers []map[string]any `json:"servers"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, r := range out.Runs {
		byID[r.ID] = i
	}
	if len(out.Runs) != 2 {
		t.Fatalf("listed %s, want the task's preview and the live branch preview", body)
	}
	tr, br := out.Runs[byID[taskRun]], out.Runs[byID[branchRun]]
	if tr.ID != taskRun || tr.Kind != "task" || tr.Task == nil || tr.Task.ID != task || tr.Task.Key == "" || tr.Task.Title != "Greet people" {
		t.Errorf("the task's run: %+v", tr)
	}
	if br.ID != branchRun || br.Kind != "preview" || br.Task != nil || br.Branch == nil || *br.Branch != "feature/list" {
		t.Errorf("the branch preview: %+v", br)
	}
	for _, r := range out.Runs {
		if len(r.Servers) != 1 || r.Servers[0]["name"] != "web" || r.Servers[0]["url"] == nil || r.Servers[0]["port"] == nil {
			t.Errorf("%s servers: %v", r.ID, r.Servers)
		}
		// A projection: lux's env and command never reach the page.
		if _, ok := r.Servers[0]["env"]; ok {
			t.Errorf("%s server carries env: %v", r.ID, r.Servers[0])
		}
	}

	other := "org_other_" + w.org
	mustExec(t, w.owner, `INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, other)
	if code, body := w.get(path, other); code != 404 {
		t.Errorf("another organisation reads the project's servers: %d %s", code, body)
	}
	if code, body := w.get("/internal/projects/prj_nope/servers/running", w.org); code != 404 {
		t.Errorf("an unknown project: %d %s", code, body)
	}
}
