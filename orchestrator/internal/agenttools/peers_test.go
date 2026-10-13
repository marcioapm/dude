package agenttools_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
)

// onEpic puts the fixture's task TEXT-1 on its epic with TEXT-2 and TEXT-3,
// TEXT-4 on no epic and TEXT-5 on another epic. Returns the tasks' ids by
// key.
func (f *fixture) onEpic(t *testing.T) map[string]string {
	t.Helper()
	ids := map[string]string{"TEXT-1": f.item}
	mustExec(t, f.owner, `UPDATE tasks SET epic_id = $2 WHERE id = $1`, f.item, "epc_"+f.org)
	mustExec(t, f.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Elsewhere')`,
		"epc_o"+f.org, f.org, f.project)
	for n, epic := range []string{"", "epc_" + f.org, "epc_" + f.org, "", "epc_o" + f.org} {
		if n < 1 {
			continue
		}
		key := "TEXT-" + string(rune('1'+n))
		id := "wi_" + string(rune('1'+n)) + f.org
		ids[key] = id
		mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, 'running')`, id, f.org, f.project, n+1, epic, "Task "+key)
	}
	mustExec(t, f.owner, `UPDATE projects SET next_task_number = 6 WHERE id = $1`, f.project)
	return ids
}

// runOn makes a phase Run of task with a token.
func (f *fixture) runOn(t *testing.T, id, task, phase, role, status string) string {
	t.Helper()
	token, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, mcp_token_hash)
		VALUES ($1, $2, $3, $4, 1, $5::run_status, $6::run_phase, $7::agent_role, 'agent', $8)`, id, f.org, f.project, task, status, phase, role, hash)
	return token
}

type peerRow struct {
	Key, Title, Status, Phase, Role, Run, RunID, Via, LastSaid, WaitingOn string
	Self                                                                  bool
	Heads                                                                 []struct{ Repo, Branch, Head string }
	Files                                                                 []struct {
		Path                 string
		Additions, Deletions int
	}
	FileCount        int
	MessagesForYou   int
	WaitsOnYourReply bool
}

func (f *fixture) peers(t *testing.T, token string) map[string]peerRow {
	t.Helper()
	var out struct{ Tasks []peerRow }
	if status := f.postAs(t, token, "list_tasks", `{"peers":true}`, &out); status != 200 {
		t.Fatalf("list_tasks peers: %d", status)
	}
	byKey := map[string]peerRow{}
	for _, p := range out.Tasks {
		byKey[p.Key] = p
	}
	return byKey
}

// list_tasks with peers lists the caller's task (self) and the other tasks
// on its epic, each with its phase and role, live or parked or no Run, its
// files from the stored snapshot by churn, its agent's last message cut to
// one bounded paragraph, and how many of its messages wait for the caller.
func TestListTasksWithPeersDescribesEachPeer(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	f.runOn(t, "run_two", ids["TEXT-2"], "implement", "implementer", "running")
	f.runOn(t, "run_three", ids["TEXT-3"], "fix", "implementer", "paused")
	// TEXT-2's code: b.go churns 7, a.go 7, c.go 3.
	text := ""
	for _, file := range []struct {
		path       string
		adds, dels int
	}{{"c.go", 2, 1}, {"b.go", 4, 3}, {"a.go", 7, 0}} {
		full, _ := gitDiff(file.path, file.adds, file.dels)
		text += full
	}
	f.storeDiff(t, "run_two", text)
	long := "First paragraph " + strings.Repeat("word ", 300) + "\n\nSecond paragraph, never shown."
	payload, _ := json.Marshal(map[string]any{"text": long})
	mustExec(t, f.owner, `INSERT INTO events (id, organization_id, event_type, task_id, run_id, actor_type, actor_id, source, payload)
		VALUES ('ev_said', $1, 'agent.message', $2, 'run_two', 'agent', 'run_two', 'runner', $3)`, f.org, ids["TEXT-2"], payload)
	mustExec(t, f.owner, `INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text)
		VALUES ('pm_a', $1, $2, 'run_two', $3, 'which body?'), ('pm_b', $1, $2, 'run_two', $3, 'and the response?'),
		       ('pm_read', $1, $2, 'run_two', $3, 'read already')`, f.org, ids["TEXT-2"], f.item)
	mustExec(t, f.owner, `UPDATE peer_messages SET read_at = now() WHERE id = 'pm_read'`)

	got := f.peers(t, token)
	if len(got) != 3 || !got["TEXT-1"].Self || got["TEXT-2"].Self || got["TEXT-4"].Key != "" || got["TEXT-5"].Key != "" {
		t.Fatalf("peers %+v: want TEXT-1 (self), TEXT-2 and TEXT-3", got)
	}
	two := got["TEXT-2"]
	if two.Phase != "implement" || two.Role != "implementer" || two.Run != "live" || two.RunID != "run_two" || two.Via != "epic" {
		t.Errorf("TEXT-2: %+v", two)
	}
	if two.FileCount != 3 || len(two.Files) != 3 || two.Files[0].Path != "a.go" || two.Files[1].Path != "b.go" || two.Files[2].Path != "c.go" ||
		two.Files[1].Additions != 4 || two.Files[1].Deletions != 3 {
		t.Errorf("TEXT-2's files by churn: %+v", two.Files)
	}
	if !strings.HasPrefix(two.LastSaid, "First paragraph word") || len([]rune(two.LastSaid)) != 600 ||
		!strings.HasSuffix(two.LastSaid, "…") || strings.Contains(two.LastSaid, "Second") {
		t.Errorf("TEXT-2's last words, %d characters: %q", len([]rune(two.LastSaid)), two.LastSaid)
	}
	if two.MessagesForYou != 2 {
		t.Errorf("TEXT-2 has %d messages waiting for the caller, want 2", two.MessagesForYou)
	}
	if three := got["TEXT-3"]; three.Run != "parked" || three.Phase != "fix" || three.MessagesForYou != 0 || len(three.Files) != 0 {
		t.Errorf("TEXT-3: %+v", three)
	}
	// Without peers, unchanged: the project's tasks, all five.
	var plain struct{ Tasks []map[string]any }
	if status := f.postAs(t, token, "list_tasks", `{}`, &plain); status != 200 || len(plain.Tasks) != 5 {
		t.Errorf("list_tasks without peers: %d %d tasks", status, len(plain.Tasks))
	}
}

// A task on no epic has no peers, and lists none.
func TestATaskOnNoEpicHasNoPeers(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	token := f.runOn(t, "run_alone", ids["TEXT-4"], "implement", "implementer", "running")
	if got := f.peers(t, token); len(got) != 0 {
		t.Errorf("peers of a task on no epic: %+v", got)
	}
	if status, out := f.post(t, token, "run_diff", `{"task":"TEXT-2"}`); status != 422 || !strings.Contains(out["error"].(string), "not one of your peers") {
		t.Errorf("run_diff --task from a task on no epic: %d %v", status, out)
	}
}

// run_diff with task reads a peer's stored snapshot, files then lines;
// another epic's task, or one on no epic, is refused as another task's Run
// is.
func TestRunDiffReadsAPeersStoredSnapshotAndRefusesAnyOtherTask(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "review", "reviewer", "running")
	f.runOn(t, "run_two_old", ids["TEXT-2"], "implement", "implementer", "completed")
	f.runOn(t, "run_five", ids["TEXT-5"], "implement", "implementer", "running")
	full, hunks := gitDiff("api.ts", 3, 1)
	f.storeDiff(t, "run_two_old", full)
	f.storeDiff(t, "run_five", full)

	got := f.diff(t, token, `{"task":"text-2"}`)
	if got.Run != "run_two_old" || got.TotalFiles != 1 || paths(got.Files)[0] != "api.ts" {
		t.Errorf("TEXT-2's ended Run's diff: %+v", got)
	}
	lines := f.diff(t, token, `{"task":"TEXT-2","paths":["api.ts"]}`)
	if len(lines.Files) != 1 || lines.Files[0]["patch"] != hunks {
		t.Errorf("TEXT-2's lines: %+v", lines.Files)
	}
	for _, key := range []string{"TEXT-5", "TEXT-4", "TEXT-9"} {
		if status, raw := f.diffCall(t, token, `{"task":"`+key+`"}`); status != 422 || !strings.Contains(raw, "not one of your peers") {
			t.Errorf("run_diff --task %s: %d %s", key, status, raw)
		}
	}
	if status, raw := f.diffCall(t, token, `{"task":"TEXT-3"}`); status != 422 || !strings.Contains(raw, "no Run yet") {
		t.Errorf("a peer with no Run: %d %s", status, raw)
	}
	// A live Run of a peer is read before its ended one.
	f.runOn(t, "run_two_new", ids["TEXT-2"], "fix", "implementer", "running")
	f.storeDiff(t, "run_two_new", full)
	if got := f.diff(t, token, `{"task":"TEXT-2"}`); got.Run != "run_two_new" {
		t.Errorf("a peer's current Run: %s", got.Run)
	}
}
