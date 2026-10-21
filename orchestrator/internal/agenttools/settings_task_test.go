package agenttools_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A session with two linked projects reads both; with none, the
// organisation alone. A project or a task of another organisation is
// refused in the words of a project or task that is not there.
func TestABrainstormReadsItsLinkedProjectsAndNothingOfAnotherOrganisation(t *testing.T) {
	f := setup(t)
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_two', $1, 'Two', 'two', 'TWO')`, f.org)
	linked := f.session(t, "ssn_two", "", "")
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES
		('ssn_two', $1, $2), ('ssn_two', 'prj_two', $2)`, f.project, f.org)
	none := f.session(t, "ssn_none", "", "")
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"maxReviewIterations":9}' WHERE id = 'prj_two'`)

	out := f.settingsOf(t, linked, `{"section":"delivery"}`)
	scopes, _ := out["settings"].([]any)
	if len(scopes) != 2 || field(scopes, 0, "project", "key") != "TEXT" || field(scopes, 1, "project", "key") != "TWO" ||
		field(scopes, 1, "delivery", "maxReviewIterations", "source") != "project" ||
		field(scopes, 0, "delivery", "maxReviewIterations", "source") != "dude" {
		t.Errorf("two linked projects: %v", scopes)
	}
	out = f.settingsOf(t, none, `{"section":"delivery"}`)
	if scopes, _ := out["settings"].([]any); len(scopes) != 1 || field(scopes, 0, "project") != nil ||
		field(scopes, 0, "delivery", "maxReviewIterations", "value") != 5.0 {
		t.Errorf("no linked project: %v", scopes)
	}

	other := dbtest.Org(t, f.owner)
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, next_task_number)
		VALUES ('prj_other', $1, 'Theirs', 'theirs', 'OTH', 2)`, other)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_other', $1, 'prj_other', 1, 'Theirs', 'running')`, other)
	for args, want := range map[string]string{
		`{"project":"OTH"}`:       `no project "OTH" in this organisation`,
		`{"project":"prj_other"}`: `no project "prj_other" in this organisation`,
		`{"task":"OTH-1"}`:        `no task "OTH-1" in this organisation`,
		`{"task":"wi_other"}`:     `no task "wi_other" in this organisation`,
	} {
		status, got := f.post(t, linked, "settings", args)
		if status != 422 || !strings.HasPrefix(got["error"].(string), want) {
			t.Errorf("%s: %d %v", args, status, got)
		}
	}
	// The same task named in its own organisation is read, with its project.
	status, got := f.post(t, linked, "settings", `{"task":"TEXT-1","section":"delivery"}`)
	if status != 200 || field(got, "task", "key") != "TEXT-1" || field(got, "settings", 0, "project", "key") != "TEXT" {
		t.Errorf("a task of the organisation: %d %v", status, got)
	}
}

// A live delivery whose frozen maxReviewIterations and maxPrFixIterations
// differ from today's shows both, today's with where it is set, and a raise
// granted with request_limit; an ended delivery and none say what limits
// says, with no frozen values.
func TestTheTaskViewShowsTheFrozenPolicyBesideTodaysAndTheRaises(t *testing.T) {
	f, token := limitsWorld(t, `{}`)
	mustExec(t, f.owner, `UPDATE organizations SET delivery_policy = '{"maxReviewIterations":8}' WHERE id = $1`, f.org)
	mustExec(t, f.owner, `UPDATE projects SET delivery_policy = '{"maxPrFixIterations":6}' WHERE id = $1`, f.project)

	out := f.settingsOf(t, token, `{"section":"delivery"}`)
	if d, _ := field(out, "task", "delivery").(string); !strings.Contains(d, "no delivery yet") || field(out, "task", "differsFromToday") != nil ||
		field(out, "task", "policy", "maxReviewIterations", "value") != 8.0 {
		t.Errorf("no delivery: %v", out["task"])
	}

	frozen := delivery.DefaultPolicy()
	frozen.MaxReviewIterations, frozen.MaxPRFixIterations = 5, 3
	policy, _ := json.Marshal(frozen)
	f.deliver(t, "running", string(policy), 1, 1)
	mustExec(t, f.owner, `UPDATE workflow_runs SET state = jsonb_set(jsonb_set(state, '{limitGrants}', $2::jsonb), '{changedPaths}',
		'["target/migrations/1.sql"]') WHERE task_id = $1`, f.item,
		`[{"limit":"perReviewRound","extra":2,"by":"psn_bo","byName":"Bo","questionId":"q1","at":"2026-03-04T05:06:07Z"}]`)
	task, _ := f.settingsOf(t, token, `{}`)["task"].(map[string]any)
	if d, _ := task["delivery"].(string); !strings.Contains(d, "held to") {
		t.Errorf("live: %v", task["delivery"])
	}
	for key, want := range map[string][2]any{"maxReviewIterations": {5.0, "organization"}, "maxPrFixIterations": {3.0, "project"}} {
		if field(task, "differsFromToday", key, "frozen") != want[0] || field(task, "differsFromToday", key, "today", "source") != want[1] ||
			field(task, "policy", key, "source") != "frozen" {
			t.Errorf("%s: %v / %v", key, field(task, "differsFromToday", key), field(task, "policy", key))
		}
	}
	if field(task, "differsFromToday", "maxReviewIterations", "today", "value") != 8.0 ||
		field(task, "differsFromToday", "maxPrFixIterations", "today", "value") != 6.0 {
		t.Errorf("today's: %v", task["differsFromToday"])
	}
	if !strings.Contains(task["note"].(string), "next delivery") {
		t.Errorf("note: %v", task["note"])
	}
	if field(task, "raisedForThisTask", 0, "extra") != 2.0 || !strings.Contains(field(task, "raisedForThisTask", 0, "by").(string), "Bo") {
		t.Errorf("raises: %v", task["raisedForThisTask"])
	}
	if r, _ := task["reviewers"].([]any); len(r) != 4 || r[2] != "database" {
		t.Errorf("reviewers for a migration: %v", task["reviewers"])
	}
	if d, _ := task["differsFromToday"].(map[string]any); len(d) != 2 {
		t.Errorf("differs in %d keys, want the two changed: %v", len(d), d)
	}

	mustExec(t, f.owner, `UPDATE workflow_runs SET status = 'completed' WHERE task_id = $1`, f.item)
	task, _ = f.settingsOf(t, token, `{}`)["task"].(map[string]any)
	if d, _ := task["delivery"].(string); !strings.Contains(d, "has ended") || task["differsFromToday"] != nil ||
		field(task, "policy", "maxReviewIterations", "value") != 8.0 {
		t.Errorf("ended: %v", task)
	}
}
