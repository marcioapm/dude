package agenttools_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// list_tasks leaves archived tasks out unless asked (archived: true), says
// each task's resolution, and with closed lists only what a person closed
// (tracking-work.md, "Agents and CLI"); so does dude task list.
func TestListTasksLeavesArchivedOutAndSaysResolutions(t *testing.T) {
	f := setup(t)
	f.relFixture(t)
	// TEXT-2 closed as Won't do and archived; TEXT-3 closed as a duplicate of TEXT-1.
	mustExec(t, f.owner, `UPDATE tasks SET status = 'aborted', resolution = 'wont_do', archived_at = now() WHERE id = $1||'_2'`, f.org)
	mustExec(t, f.owner, `UPDATE tasks SET status = 'aborted', resolution = 'duplicate' WHERE id = $1||'_3'`, f.org)
	mustExec(t, f.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $1||'_3', $2, 'duplicates', 'per_ana_'||$1)`, f.org, f.item)
	token := f.conductor(t)
	type task struct {
		Key, Resolution, Line string
		Archived              bool
		Related               []string
		DuplicateOf           string
	}
	list := func(args string) map[string]task {
		t.Helper()
		raw := map[string]any{}
		if status := f.postAs(t, token, "list_tasks", args, &raw); status != 200 {
			t.Fatalf("list_tasks %s: %d %v", args, status, raw)
		}
		var out struct{ Tasks []task }
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &out)
		byKey := map[string]task{}
		for _, tk := range out.Tasks {
			byKey[tk.Key] = tk
		}
		return byKey
	}
	everyday := list(`{}`)
	if _, ok := everyday["TEXT-2"]; ok || len(everyday) != 2 {
		t.Fatalf("list_tasks lists the archived TEXT-2: %v", everyday)
	}
	three := everyday["TEXT-3"]
	if three.Resolution != "duplicate" || three.DuplicateOf != "TEXT-1" || len(three.Related) != 0 ||
		three.Line != "TEXT-3 · aborted (duplicate) · duplicate of TEXT-1" {
		t.Fatalf("TEXT-3: %+v", three)
	}
	all := list(`{"archived":true}`)
	if two := all["TEXT-2"]; !two.Archived || two.Resolution != "wont_do" || two.Line != "TEXT-2 · aborted (won't do) · archived" {
		t.Fatalf("archived: true: %+v", all)
	}
	closed := list(`{"closed":true,"archived":true}`)
	if len(closed) != 2 || closed["TEXT-1"].Key != "" {
		t.Fatalf("closed: %v", closed)
	}

	bin := cli(t)
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := dude("task", "list"); err != nil || strings.Contains(out, `"key": "TEXT-2"`) {
		t.Errorf("dude task list: %v\n%s", err, out)
	}
	if out, err := dude("task", "list", "--archived"); err != nil || !strings.Contains(out, `"key": "TEXT-2"`) {
		t.Errorf("dude task list --archived: %v\n%s", err, out)
	}
	if out, err := dude("task", "list", "--closed"); err != nil || strings.Contains(out, `"key": "TEXT-1"`) || !strings.Contains(out, `"key": "TEXT-3"`) {
		t.Errorf("dude task list --closed: %v\n%s", err, out)
	}
	// Close and Archive are a person's: the commands say so and change nothing.
	for _, args := range [][]string{{"task", "close", "--resolution", "wont_do", "--remove-relations"}, {"task", "archive"}, {"task", "reopen"}, {"task", "unarchive"}} {
		out, err := dude(args...)
		if err == nil || !strings.Contains(out, "a person's decision, not an agent's") {
			t.Errorf("dude %v: %v\n%s", args, err, out)
		}
	}
	if out, err := dude("task", "close"); err == nil || !strings.Contains(out, "usage: dude task close --resolution") {
		t.Errorf("dude task close with no resolution: %v\n%s", err, out)
	}
}
