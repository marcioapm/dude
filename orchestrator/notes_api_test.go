package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"
)

// People pin, replace and retire an epic's notes through the
// orchestrator's API, as themselves; agents on the epic are told; the
// limit holds and its refusal is recorded; another organisation's epic is
// not found; and a request with no person may do none of it.
func TestPeoplePinReplaceAndRetireNotes(t *testing.T) {
	s := newSessionWorld(t)
	wi := s.task()
	epic := s.onAnEpic(wi)
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ('run_live', $1, $2, $3, 1, 'running', 'implement', 'implementer', 'agent')`, s.org, s.project, wi)
	base := "/internal/epics/" + epic + "/notes"
	out := s.ok(s.marcio, "POST", base, map[string]any{"text": "ABS-1 serves the engine endpoint."})
	if out["n"].(float64) != 1 || out["told"].(float64) != 1 {
		t.Fatalf("pin: %v", out)
	}
	out = s.ok(s.ana, "POST", base, map[string]any{"text": "ABS-1 serves POST /v1/query/estimate.", "replaces": 1})
	if out["n"].(float64) != 2 || out["replaces"].(float64) != 1 {
		t.Fatalf("replace: %v", out)
	}
	out = s.ok(s.marcio, "POST", base+"/2/retire", nil)
	if out["retiredBy"] != "Márcio Martins" || out["retiredAt"] == nil {
		t.Fatalf("retire: %v", out)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = 'run_live'`); n != 3 {
		t.Errorf("the live agent was told %d changes, want 3", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type IN ('epic.note.pinned', 'epic.note.retired') AND actor_type = 'person'`); n != 3 {
		t.Errorf("%d note events as people", n)
	}
	if status, out := s.as(s.marcio, "POST", base+"/2/retire", nil); status != 409 {
		t.Errorf("retiring twice: %d %v", status, out)
	}
	if status, out := s.as(s.marcio, "POST", base+"/9/retire", nil); status != 404 {
		t.Errorf("retiring no note: %d %v", status, out)
	}
	for i := range 10 {
		s.ok(s.marcio, "POST", base, map[string]any{"text": "fact " + string(rune('a'+i))})
	}
	status, refused := s.as(s.marcio, "POST", base, map[string]any{"text": "the eleventh"})
	if status != 409 || !strings.Contains(fmt.Sprint(refused["error"]), "This epic has 10 notes.") {
		t.Errorf("the 11th: %d %v", status, refused)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'epic.note.refused' AND actor_id = $1`, s.marcio); n != 1 {
		t.Errorf("%d refusals recorded", n)
	}
	// Another organisation's epic does not exist here.
	other := "org_other_" + s.org
	mustExec(t, s.owner, `INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, other)
	mustExec(t, s.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_o', $1, 'O', 'o', 'O')`, other)
	mustExec(t, s.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_o', $1, 'prj_o', 'Theirs')`, other)
	if status, out := s.as(s.marcio, "POST", "/internal/epics/epc_o/notes", map[string]any{"text": "x"}); status != 404 {
		t.Errorf("another organisation's epic: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM epic_notes WHERE epic_id = 'epc_o'`); n != 0 {
		t.Errorf("a note pinned in another organisation")
	}
	// The service alone is no person.
	if status, out := s.call(base, map[string]any{"text": "x"}); status != 403 {
		t.Errorf("no person: %d %v", status, out)
	}
}
