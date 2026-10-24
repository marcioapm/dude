package orchestrator_test

// Relations from proposals and agents (docs/design/task-relations.md,
// "Propose work", "Agents"): filing writes a proposal's relations as real
// edges, files an item's in-proposal blockers with it, and refuses an item
// whose blocker can't be filed; a brainstorm's relate is filed like any item.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// edges are the world's relations, as "FROM kind TO (by)".
func (w *world) edges() []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT fp.key_prefix || '-' || f.number || ' ' || r.kind || ' ' ||
			tp.key_prefix || '-' || t.number || ' (' || COALESCE(r.created_by_person, '') || '/' || COALESCE(r.proposed_by_run_id, '') || ')'
		FROM task_relations r JOIN tasks f ON f.id = r.from_task_id JOIN projects fp ON fp.id = f.project_id
		JOIN tasks t ON t.id = r.to_task_id JOIN projects tp ON tp.id = t.project_id
		WHERE r.organization_id = $1 ORDER BY 1`, w.org)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// Ticking item 3 files item 1 (what it waits on) with it, blockers first,
// and the edges are written as the person filing, proposed by the card.
func TestFilingABlockedItemFilesItsBlockerWithItsRelations(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	existing := s.taskIn(s.project, "Hourly meter", s.marcio)
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Line items", Goal: "Line items for the invoice, from the meter's rows"},
		{Kind: "task", Project: "BL", Title: "Invoice page", Goal: "A page showing the month's invoice, line by line",
			BlockedBy: []delivery.ProposalRef{{Item: 1}, {Key: s.keyOf(existing)}}},
		{Kind: "task", Project: "BL", Title: "Invoice email", Goal: "Email the invoice when the month closes",
			BlockedBy: []delivery.ProposalRef{{Item: 1}}, Related: []delivery.ProposalRef{{Item: 2}}},
	})
	// Item 3 (index 2) says it is filed with item 1.
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)
	raw, _ := json.Marshal(card["proposals"])
	if !strings.Contains(string(raw), `"filesWith":[0]`) {
		t.Fatalf("the card does not say item 3 files item 1 with it: %s", raw)
	}
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{2}})
	results, _ := out["results"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["item"] != 0.0 || results[1].(map[string]any)["item"] != 2.0 {
		t.Fatalf("filing item 3 filed %v, want item 1 first, then 3", results)
	}
	edges := s.edges()
	if len(edges) != 1 || !strings.Contains(edges[0], " blocks ") || !strings.Contains(edges[0], "("+s.marcio+"/") {
		t.Fatalf("edges after filing 1 and 3: %v", edges)
	}
	// Then item 2: its edges to item 1 (filed), the existing task, and item
	// 3's related edge to it, written now from item 3's side.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{1}})
	if edges := s.edges(); len(edges) != 4 {
		t.Fatalf("edges after filing all: %v", edges)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'task.relation_added' AND organization_id = $1`, s.org); n != 8 {
		t.Fatalf("%d relation_added events, want one on each end of 4 edges", n)
	}
}

// An item whose in-proposal blocker can't be filed can't be filed either,
// saying which; the blocker says its own reason.
func TestAnItemWaitingOnAnUnfileableItemCantBeFiled(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "WC", Title: "Pipeline", Goal: "A pipeline in a project no longer linked"},
		{Kind: "task", Project: "BL", Title: "Uses it", Goal: "Uses the pipeline once it is merged", BlockedBy: []delivery.ProposalRef{{Item: 1}}},
	})
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)
	raw, _ := json.Marshal(card["proposals"])
	for _, want := range []string{`"blockedBy":"unlinked"`, `"blockedBy":"blocker_unfiled"`, `Waits on item 1, which can't be filed`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the card lacks %s: %s", want, raw)
		}
	}
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{1}})
	for _, r := range out["results"].([]any) {
		if r.(map[string]any)["status"] != "refused" {
			t.Errorf("filed past an unfileable blocker: %v", out)
		}
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE organization_id = $1 AND title = 'Uses it'`, s.org); n != 0 {
		t.Fatal("the waiting item was filed")
	}
}

// A brainstorm's relate is a card item: filed by a member, the edge is
// theirs, proposed by the brainstorm's Run.
func TestARelateItemIsFiledAsAnEdge(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	a := s.taskIn(s.project, "Meter", s.marcio)
	b := s.taskIn(s.project, "Billing page", s.marcio)
	proposal := s.proposal(id, []delivery.ProposalItem{{Kind: "relation", From: s.keyOf(a), To: s.keyOf(b), RelationKind: "blocks", Why: "the page reads it"}})
	// The card's brainstorm Run, which the edge names as its proposer.
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role)
		VALUES ('run_bs_'||$1, $2, $1, 1, 'running', 'agent', 'brainstorm')`, id, s.org)
	mustExec(t, s.owner, `UPDATE session_proposals SET run_id = 'run_bs_'||$2 WHERE id = $1`, proposal, id)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}})
	edges := s.edges()
	if want := s.keyOf(a) + " blocks " + s.keyOf(b) + " (" + s.marcio + "/run_bs_" + id + ")"; len(edges) != 1 || edges[0] != want {
		t.Fatalf("edges: %v, want %q", edges, want)
	}
	status, body := s.call("/internal/tasks/"+b+"/deliver", map[string]any{})
	if status != 409 {
		t.Fatalf("the filed edge does not block: %d %v", status, body)
	}
}

// #308: Filing a card that names an existing task and makes a new one,
// while an agent's create_task in the same project relates its new task to
// that existing one. The agent holds the project's row (its next number)
// and its relation's insert checks the foreign key to the existing task
// (KEY SHARE). Filing holds the named task and, making its new task, waits
// on the project's row. Filing's task lock must not exclude that KEY
// SHARE: both commit, and the file answers 200, not a deadlock's 500.
func TestFilingBesideAnAgentRelatingToTheNamedTaskDoesNotDeadlock(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	named := s.taskIn(s.project, "Meter", s.marcio)
	other := s.taskIn(s.project, "Rates", s.marcio)
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "comment", Task: s.keyOf(named), Text: "Count by the hour"},
		{Kind: "task", Project: "BL", Title: "Line items", Goal: "Line items for the invoice, from the meter's rows"},
	})
	ctx := context.Background()
	agent, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Rollback(ctx) }()
	// create_task's first write: the project's next number.
	if _, err := agent.Exec(ctx, `UPDATE projects SET next_task_number = next_task_number + 1 WHERE id = $1`, s.project); err != nil {
		t.Fatal(err)
	}
	filed := make(chan int, 1)
	go func() {
		status, body := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1}})
		if status != 200 {
			t.Logf("file: %d %v", status, body)
		}
		filed <- status
	}()
	s.blockedBehind(agent)
	if _, err := agent.Exec(ctx, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $2, $3, 'blocks', $4)`, s.org, other, named, s.marcio); err != nil {
		t.Fatalf("the agent's relation: %v", err)
	}
	if err := agent.Commit(ctx); err != nil {
		t.Fatalf("the agent's commit: %v", err)
	}
	select {
	case status := <-filed:
		if status != 200 {
			t.Fatalf("file: %d", status)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the file did not finish after the agent committed")
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE organization_id = $1 AND title = 'Line items'`, s.org); n != 1 {
		t.Fatalf("%d filed tasks, want 1", n)
	}
}
