package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Proposing again replaces the card: an earlier card's unfiled items are
// nobody's to file, each saying so, and filing one through the API is
// refused (proposal_replaced). What was filed from it stays filed; the
// latest card files as before.
func TestALaterProposalReplacesTheCard(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	first := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"},
		{Kind: "task", Project: "BL", Title: "Dedupe runs", Goal: "The meter's 24h key is not enough for retries"},
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": first, "items": []int{0}})
	latest := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Dedupe runs on run id", Goal: "The meter's 24h key is not enough for retries"},
	})

	cards := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["proposals"].([]any)
	if len(cards) != 2 {
		t.Fatalf("%d cards, want 2", len(cards))
	}
	old := cards[0].(map[string]any)["status"].([]any)
	if st := old[0].(map[string]any); st["filed"] != true {
		t.Errorf("the earlier card's filed item: %v", st)
	}
	if st := old[1].(map[string]any); st["canFile"] != false || st["blockedBy"] != "replaced" || st["why"] != "Replaced by a later proposal" {
		t.Errorf("the earlier card's unfiled item: %v", st)
	}
	if st := cards[1].(map[string]any)["status"].([]any)[0].(map[string]any); st["canFile"] != true {
		t.Errorf("the latest card's item: %v", st)
	}

	status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": first, "items": []int{1}})
	if e, _ := out["error"].(map[string]any); status != 409 || e["code"] != "proposal_replaced" {
		t.Errorf("filing from a replaced card: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE title = 'Dedupe runs'`); n != 0 {
		t.Errorf("a replaced card's item was filed")
	}
	results := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": latest, "items": []int{0}})["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "filed" {
		t.Errorf("filing from the latest card: %v", r)
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE title IN ('Meter runs', 'Dedupe runs on run id')`); n != 2 {
		t.Errorf("%d tasks filed, want the earlier card's and the latest's", n)
	}
}

// waiting is the caller's proposals entry in GET /internal/sessions for the session, nil without one.
func (s *sessionWorld) waiting(person, session string) map[string]any {
	s.t.Helper()
	list, _ := s.ok(person, "GET", "/internal/sessions", nil)["proposals"].([]any)
	for _, p := range list {
		if m := p.(map[string]any); m["sessionId"] == session {
			return m
		}
	}
	return nil
}

// The sessions list says, for the caller, which sessions' latest card has
// items they can file, and how many, by the card's own rule: the owner's
// tasks count and Márcio's edit does not for Ana; a reader has none; a
// filed item no longer counts; an earlier card a later one replaced is
// never counted.
func TestTheListSaysWhichCardsWaitOnYou(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.join(id, s.joao, "read")
	mine := s.taskIn(s.project, "Daily rollup", s.marcio)
	early := s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Old idea", Goal: "Something proposed before"}})
	latest := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"},
		{Kind: "task", Project: "BL", Title: "Dedupe runs", Goal: "The meter's 24h key is not enough for retries"},
		{Kind: "edit", Task: s.keyOf(mine), After: &delivery.TaskText{Goal: ptr("Count each run id once")}},
	})

	w := s.waiting(s.marcio, id)
	if w == nil || w["proposalId"] != latest || w["count"] != float64(3) || w["title"] != "Usage-based billing" || w["proposedAt"] == nil {
		t.Fatalf("Márcio's entry: %v", w)
	}
	if kinds, _ := w["kinds"].([]any); len(kinds) != 3 || kinds[0] != "task" || kinds[2] != "edit" {
		t.Errorf("kinds %v", w["kinds"])
	}
	if w := s.waiting(s.ana, id); w == nil || w["count"] != float64(2) {
		t.Errorf("Ana's entry (Márcio's edit is his): %v", w)
	}
	if w := s.waiting(s.joao, id); w != nil {
		t.Errorf("a reader's entry: %v", w)
	}

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": latest, "items": []int{0, 1}})
	if w := s.waiting(s.marcio, id); w == nil || w["count"] != float64(1) {
		t.Errorf("after filing two: %v", w)
	}
	if w := s.waiting(s.ana, id); w != nil {
		t.Errorf("Ana, with only Márcio's edit left: %v", w)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": latest, "items": []int{2}})
	if w := s.waiting(s.marcio, id); w != nil {
		t.Errorf("all filed, the earlier card replaced (%s): %v", early, w)
	}
}
