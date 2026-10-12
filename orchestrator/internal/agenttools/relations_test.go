package agenttools_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// relFixture adds TEXT-2 and TEXT-3 to the fixture's project, and an owner
// (Ana) on TEXT-1.
func (f *fixture) relFixture(t *testing.T) {
	t.Helper()
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_ana_'||$1, $1, 'Ana Nunes')`, f.org)
	mustExec(t, f.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ($2, 'per_ana_'||$1, $1, 0)`, f.org, f.item)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ($1||'_2', $1, $2, 2, 'Meter', 'review'), ($1||'_3', $1, $2, 3, 'Sync', 'received')`, f.org, f.project)
	mustExec(t, f.owner, `UPDATE projects SET next_task_number = 4 WHERE id = $1`, f.project)
}

func (f *fixture) edgeCount(t *testing.T, where string) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM task_relations WHERE organization_id = $1 AND `+where, f.org).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The conductor relates its task at any status and removes only what its
// task's agents added; a refused relation names why.
func TestTheConductorRelatesItsTaskAndRemovesOnlyItsOwn(t *testing.T) {
	f := setup(t)
	f.relFixture(t)
	token := f.conductor(t)
	var out map[string]any
	if status := f.postAs(t, token, "update_task", `{"addRelations":[{"kind":"blocked_by","task":"TEXT-2","why":"needs the meter"}]}`, &out); status != 200 {
		t.Fatalf("addRelations on a running task: %d %v", status, out)
	}
	if f.edgeCount(t, `from_task_id = $1||'_2' AND created_by_run_id = 'run_cond' AND why = 'needs the meter'`) != 1 {
		t.Fatal("the edge was not written as the conductor's")
	}
	// A loop is refused naming it: TEXT-3 blocks TEXT-2, which blocks TEXT-1.
	mustExec(t, f.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $1||'_3', $1||'_2', 'blocks', 'per_ana_'||$1)`, f.org)
	status := f.postAs(t, token, "update_task", `{"addRelations":[{"kind":"blocks","task":"TEXT-3"}]}`, &out)
	if status != 422 || out["error"] != "that would make a loop: TEXT-3 → TEXT-2 → TEXT-1 → TEXT-3. A task can't wait on itself" {
		t.Fatalf("a loop: %d %v", status, out)
	}
	// A person's edge is not the conductor's to take back.
	mustExec(t, f.owner, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $1||'_3', $2, 'related', 'per_ana_'||$1)`, f.org, f.item)
	status = f.postAs(t, token, "update_task", `{"removeRelations":[{"kind":"related","task":"TEXT-3"}]}`, &out)
	if status != 422 || out["error"] != "you can remove only a relation this task's agents added; ask Ana, its owner" {
		t.Fatalf("removing a person's edge: %d %v", status, out)
	}
	if status := f.postAs(t, token, "update_task", `{"removeRelations":[{"kind":"blocked_by","task":"TEXT-2"}]}`, &out); status != 200 {
		t.Fatalf("removing its own edge: %d %v", status, out)
	}
	if f.edgeCount(t, `from_task_id = $1||'_2'`) != 0 {
		t.Fatal("its own edge is still there")
	}
}

// create_task writes relations with the task for a conductor or an
// investigator; an implementer may not relate; a refused edge refuses the
// whole create. list_tasks gives each task's relations, and startable.
func TestCreateTaskRelatesAndListTasksSaysWhatCanStart(t *testing.T) {
	f := setup(t)
	f.relFixture(t)
	token := f.run(t, "run_inv", "investigator", "running")
	var out map[string]any
	if status := f.postAs(t, token, "create_task", `{"title":"Invoice page","goal":"A page with the month's invoice on it","blockedBy":["TEXT-2"],"related":["TEXT-3"]}`, &out); status != 200 {
		t.Fatalf("create_task with relations: %d %v", status, out)
	}
	if out["key"] != "TEXT-4" || f.edgeCount(t, `created_by_run_id = 'run_inv'`) != 2 {
		t.Fatalf("created %v with %d edges", out, f.edgeCount(t, `created_by_run_id = 'run_inv'`))
	}
	if status := f.postAs(t, token, "create_task", `{"title":"Nope","goal":"Blocked by a task that is not there","blockedBy":["TEXT-99"]}`, &out); status != 422 {
		t.Fatalf("an unknown key: %d %v", status, out)
	}
	var tasks int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE organization_id = $1`, f.org).Scan(&tasks)
	if tasks != 4 {
		t.Fatalf("a refused create left %d tasks", tasks)
	}
	impl := f.run(t, "run_impl", "implementer", "running")
	if status := f.postAs(t, impl, "create_task", `{"title":"x","goal":"An implementer relating its follow-up","blocks":["TEXT-1"]}`, &out); status != 422 {
		t.Fatalf("an implementer's relation: %d %v", status, out)
	}

	var list struct {
		Tasks []struct {
			Key       string
			Startable bool
			BlockedBy []struct {
				Key    string
				Merged bool
			}
			Line string
		}
	}
	raw := map[string]any{}
	f.postAs(t, token, "list_tasks", `{}`, &raw)
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &list)
	var four string
	for _, tk := range list.Tasks {
		if tk.Key == "TEXT-4" {
			four = tk.Line
			if tk.Startable || len(tk.BlockedBy) != 1 || tk.BlockedBy[0].Key != "TEXT-2" {
				t.Errorf("TEXT-4: %+v", tk)
			}
		}
	}
	if four != "TEXT-4 · received · blocked by TEXT-2 (review) · related TEXT-3" {
		t.Errorf("TEXT-4's line: %q", four)
	}
	f.postAs(t, token, "list_tasks", `{"startable":true}`, &raw)
	b, _ = json.Marshal(raw)
	if strings.Contains(string(b), `"key":"TEXT-4"`) || !strings.Contains(string(b), `"key":"TEXT-3"`) {
		t.Errorf("startable lists: %s", b)
	}
}

// The relations from the conductor's shell: dude task update --block-on and
// --unblock, dude task list --startable with each task's line.
func TestTheConductorsRelationsFromTheShell(t *testing.T) {
	f := setup(t)
	f.relFixture(t)
	token := f.conductor(t)
	bin := cli(t)
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dude %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	dude("task", "update", "--block-on", "TEXT-2", "--relate", "TEXT-3", "--why", "needs the meter")
	if f.edgeCount(t, `created_by_run_id = 'run_cond'`) != 2 {
		t.Fatal("the shell's relations were not written")
	}
	if out := dude("task", "list"); !strings.Contains(out, `"line": "TEXT-1 · running · blocked by TEXT-2 (review) · related TEXT-3"`) {
		t.Errorf("dude task list:\n%s", out)
	}
	if out := dude("task", "list", "--startable"); strings.Contains(out, `"key": "TEXT-1"`) || !strings.Contains(out, `"key": "TEXT-3"`) {
		t.Errorf("dude task list --startable:\n%s", out)
	}
	dude("task", "update", "--unblock", "TEXT-2", "--unrelate", "TEXT-3")
	if f.edgeCount(t, `true`) != 0 {
		t.Fatal("the shell's removals left edges")
	}
}

// autoStartEndedByRemoval checks TEXT-1's start when unblocked, set by Ana,
// was ended by removing its last blocker, as the conductor's act.
func (f *fixture) autoStartEndedByRemoval(t *testing.T) {
	t.Helper()
	var rows, events int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, f.item).Scan(&rows)
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled'
		AND payload->>'why' = 'unblocked_by_removal' AND actor_id = 'run_cond'`, f.item).Scan(&events)
	if rows != 0 || events != 1 {
		t.Fatalf("removing the last blocker: %d requests left, %d task.auto_start_cancelled {why: unblocked_by_removal}", rows, events)
	}
}

func (f *fixture) setAutoStart(t *testing.T) {
	t.Helper()
	mustExec(t, f.owner, `INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person) VALUES ($1, $2, 'deliver', 'per_ana_'||$2)`,
		f.item, f.org)
}

// update_task and --unblock removing the task's last blocker end its start
// when unblocked: nothing is left in front of it to wait for.
func TestRemovingTheLastBlockerEndsStartWhenUnblocked(t *testing.T) {
	f := setup(t)
	f.relFixture(t)
	token := f.conductor(t)
	var out map[string]any
	if status := f.postAs(t, token, "update_task", `{"addRelations":[{"kind":"blocked_by","task":"TEXT-2"},{"kind":"blocked_by","task":"TEXT-3"}]}`, &out); status != 200 {
		t.Fatalf("addRelations: %d %v", status, out)
	}
	f.setAutoStart(t)
	if status := f.postAs(t, token, "update_task", `{"removeRelations":[{"kind":"blocked_by","task":"TEXT-2"}]}`, &out); status != 200 {
		t.Fatalf("removing one blocker: %d %v", status, out)
	}
	var rows int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, f.item).Scan(&rows)
	if rows != 1 {
		t.Fatal("removing one of two blockers ended the request")
	}
	if status := f.postAs(t, token, "update_task", `{"removeRelations":[{"kind":"blocked_by","task":"TEXT-3"}]}`, &out); status != 200 {
		t.Fatalf("removing the last blocker: %d %v", status, out)
	}
	f.autoStartEndedByRemoval(t)

	// From the shell: --unblock.
	g := setup(t)
	g.relFixture(t)
	gt := g.conductor(t)
	if status := g.postAs(t, gt, "update_task", `{"addRelations":[{"kind":"blocked_by","task":"TEXT-2"}]}`, &out); status != 200 {
		t.Fatalf("addRelations: %d %v", status, out)
	}
	g.setAutoStart(t)
	cmd := exec.Command(cli(t), "task", "update", "--unblock", "TEXT-2")
	cmd.Env = append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, g.url, gt), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dude task update --unblock: %v\n%s", err, b)
	}
	g.autoStartEndedByRemoval(t)
}

// relate is a brainstorm's: a card item, never an edge.
func TestABrainstormRelatesByProposingOnly(t *testing.T) {
	f := setup(t)
	f.relFixture(t)
	token := f.session(t, "ssn_rel", "", "")
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_rel', $1, $2)`, f.project, f.org)
	var out map[string]any
	if status := f.postAs(t, token, "relate", `{"from":"TEXT-2","to":"TEXT-3","kind":"blocks","why":"the sync reads the meter"}`, &out); status != 200 {
		t.Fatalf("relate: %d %v", status, out)
	}
	if f.edgeCount(t, `true`) != 0 {
		t.Fatal("relate wrote an edge")
	}
	var items string
	_ = f.owner.QueryRow(context.Background(), `SELECT items::text FROM session_proposals WHERE session_id = 'ssn_rel'`).Scan(&items)
	if !strings.Contains(items, `"kind": "relation"`) || !strings.Contains(items, `"relationKind": "blocks"`) {
		t.Fatalf("the card item: %s", items)
	}
	// propose refuses a loop among its own items.
	status := f.postAs(t, token, "propose", `{"items":[
		{"kind":"task","project":"TEXT","title":"A","goal":"First of a loop of two tasks","blockedBy":[{"item":2}]},
		{"kind":"task","project":"TEXT","title":"B","goal":"Second of a loop of two tasks","blockedBy":[{"item":1}]}]}`, &out)
	if status != 422 || !strings.Contains(out["error"].(string), "loop") {
		t.Fatalf("a loop among items: %d %v", status, out)
	}
	// A task's agents have no relate.
	if status := f.postAs(t, f.conductor(t), "relate", `{"from":"TEXT-2","to":"TEXT-3","kind":"blocks"}`, &out); status != 404 {
		t.Fatalf("a conductor's relate: %d", status)
	}
}
