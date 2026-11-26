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
