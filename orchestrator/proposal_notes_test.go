package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// propose takes an epic's notes and a note item, and refuses what could
// never be filed; a brainstorm has no note tool of its own.
func TestABrainstormProposesNotesAndNeverPinsThem(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	mustExec(t, s.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_x', $1, $2, 'Existing')`, s.org, s.project)
	if status, out := s.tool(run, "propose", `{"items":[{"kind":"epic","project":"BL","title":"New one","notes":["a fact"]},
		{"kind":"note","project":"BL","epic":"New one","text":"another"},{"kind":"note","project":"BL","epic":"Existing","text":"x","replaces":1}]}`); status != 200 {
		t.Fatalf("propose with notes: %d %v", status, out)
	}
	for body, want := range map[string]string{
		`{"items":[{"kind":"note","project":"BL","epic":"Nowhere","text":"x"}]}`:                                                   "no epic",
		`{"items":[{"kind":"note","project":"BL","epic":"Existing","text":""}]}`:                                                   "a note of 1 to",
		`{"items":[{"kind":"epic","project":"BL","title":"E","notes":["` + strings.Repeat("x", 2001) + `"]}]}`:                     "note 1 is 1 to",
		`{"items":[{"kind":"epic","project":"BL","title":"E"},{"kind":"note","project":"BL","epic":"E","text":"x","replaces":1}]}`: "a new epic has no note",
	} {
		if status, out := s.tool(run, "propose", body); status != 422 || !strings.Contains(out["error"].(string), want) {
			t.Errorf("%s: %d %v, want %q", body, status, out, want)
		}
	}
	if status, _ := s.tool(run, "note", `{"text":"x"}`); status != 404 {
		t.Errorf("a brainstorm pinned a note: %d", status)
	}
}

// A filed epic's notes are pinned in order as the filer's, from the
// session; a filed note item pins or replaces on an existing epic, as its
// own; each is announced as any note; and the 10-note limit holds at
// filing, refusing the item and filing the rest.
func TestFilingAProposalPinsItsNotesFromTheSession(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	ctx := context.Background()
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "epic", Project: "BL", Title: "Metric preview", Notes: []string{"ABS-1 serves the engine endpoint.", "Read .ai/INDEX.md."}},
		{Kind: "note", Project: "BL", Epic: "Metric preview", Text: "Body is {metric, from, period}."},
		{Kind: "note", Project: "BL", Epic: "Metric preview", Text: "Read .ai/personal/INDEX.md.", Replaces: 2},
	})
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1, 2}})
	for _, r := range out["results"].([]any) {
		if m := r.(map[string]any); m["status"] != "filed" {
			t.Fatalf("item %v: %v", m["item"], m)
		}
	}
	rows, err := s.owner.Query(ctx, `SELECT n.n, n.text, n.by_person, n.from_session_id, n.by_label, n.replaced_at IS NOT NULL
		FROM epic_notes n JOIN epics e ON e.id = n.epic_id WHERE e.title = 'Metric preview' ORDER BY n.n`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var n int
		var text, by, session, label string
		var replaced bool
		_ = rows.Scan(&n, &text, &by, &session, &label, &replaced)
		if by != s.marcio || session != id || label != "Márcio Martins" {
			t.Errorf("note %d pinned by %s from %s as %q", n, by, session, label)
		}
		state := "current"
		if replaced {
			state = "replaced"
		}
		got = append(got, text+" "+state)
	}
	want := []string{"ABS-1 serves the engine endpoint. current", "Read .ai/INDEX.md. replaced", "Body is {metric, from, period}. current",
		"Read .ai/personal/INDEX.md. current"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("notes %v, want %v", got, want)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'epic.note.pinned' AND payload->>'from_session' = $1
		AND actor_id = $2`, id, s.marcio); n != 4 {
		t.Errorf("%d epic.note.pinned events from the session", n)
	}

	// At 10, a note item is refused at filing, saying why; a replace files.
	for i := range 7 {
		s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": s.proposal(id, []delivery.ProposalItem{
			{Kind: "note", Project: "BL", Epic: "Metric preview", Text: "fact " + string(rune('a'+i))}}), "items": []int{0}})
	}
	if n := s.count(`SELECT count(*) FROM epic_notes WHERE replaced_at IS NULL AND retired_at IS NULL`); n != 10 {
		t.Fatalf("%d current notes", n)
	}
	full := s.proposal(id, []delivery.ProposalItem{
		{Kind: "note", Project: "BL", Epic: "Metric preview", Text: "the eleventh"},
		{Kind: "note", Project: "BL", Epic: "Metric preview", Text: "fact a, corrected", Replaces: 5},
		{Kind: "note", Project: "BL", Epic: "Metric preview", Text: "against a replaced one", Replaces: 2},
	})
	out = s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": full, "items": []int{0, 1, 2}})
	results := out["results"].([]any)
	if m := results[0].(map[string]any); m["status"] != "refused" || m["why"] != delivery.NotesLimitText {
		t.Errorf("the 11th at filing: %v", m)
	}
	if m := results[1].(map[string]any); m["status"] != "filed" {
		t.Errorf("a replace at 10: %v", m)
	}
	if m := results[2].(map[string]any); m["status"] != "refused" || !strings.Contains(m["why"].(string), "note 2 was replaced by note 4") {
		t.Errorf("a replace of a replaced note: %v", m)
	}
	if n := s.count(`SELECT count(*) FROM epic_notes WHERE text = 'the eleventh'`); n != 0 {
		t.Errorf("the 11th was pinned")
	}
}
