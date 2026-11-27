package orchestrator_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/db"
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

	// An earlier card filed whole, a later one with items to file: the later one waits.
	again := s.session()
	done := s.proposal(again, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Filed idea", Goal: "Something proposed and filed"}})
	s.ok(s.marcio, "POST", "/internal/sessions/"+again+"/file", map[string]any{"proposalId": done, "items": []int{0}})
	next := s.proposal(again, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Next idea", Goal: "Something proposed after it"}})
	if w := s.waiting(s.marcio, again); w == nil || w["proposalId"] != next || w["count"] != float64(1) {
		t.Errorf("an earlier card filed, the later one waiting: %v, want %s", w, next)
	}
}

// relation is the card relate records: one relation between tasks.
func relation(from, to string) []delivery.ProposalItem {
	return []delivery.ProposalItem{{Kind: "relation", From: from, To: to, RelationKind: "blocks"}}
}

// statusOf is item n's status on the card, for person, in the session's detail.
func (s *sessionWorld) statusOf(person, session, card string, n int) map[string]any {
	s.t.Helper()
	for _, c := range s.ok(person, "GET", "/internal/sessions/"+session, nil)["proposals"].([]any) {
		if m := c.(map[string]any); m["id"] == card {
			return m["status"].([]any)[n].(map[string]any)
		}
	}
	s.t.Fatalf("no card %s", card)
	return nil
}

// Only a propose card replaces, and only an earlier propose card is
// replaced: a relate after a propose leaves the propose card fileable and
// the one Waiting on you names, the relation card fileable too; proposing
// again replaces the first propose card only; two relates in a row both
// stay fileable.
func TestARelateDoesNotReplaceTheCard(t *testing.T) {
	s := newSessionWorld(t)
	a, b, c := s.keyOf(s.taskIn(s.project, "Meter A", s.marcio)), s.keyOf(s.taskIn(s.project, "Meter B", s.marcio)), s.keyOf(s.taskIn(s.project, "Meter C", s.marcio))
	id := s.session()
	tasks := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"},
		{Kind: "task", Project: "BL", Title: "Dedupe runs", Goal: "The meter's 24h key is not enough for retries"},
	})
	rel := s.proposal(id, relation(a, b))

	if st := s.statusOf(s.marcio, id, tasks, 0); st["canFile"] != true {
		t.Errorf("the tasks card after a relate: %v", st)
	}
	if st := s.statusOf(s.marcio, id, rel, 0); st["canFile"] != true {
		t.Errorf("the relation card: %v", st)
	}
	if w := s.waiting(s.marcio, id); w == nil || w["proposalId"] != tasks || w["count"] != float64(2) {
		t.Errorf("Waiting on you after a relate names %v, want the tasks card %s", w, tasks)
	}
	if r := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": tasks, "items": []int{0}})["results"].([]any)[0].(map[string]any); r["status"] != "filed" {
		t.Errorf("filing from the tasks card after a relate: %v", r)
	}
	again := s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Dedupe on run id", Goal: "The meter's 24h key is not enough for retries"}})
	if st := s.statusOf(s.marcio, id, tasks, 1); st["blockedBy"] != "replaced" {
		t.Errorf("the first propose card after proposing again: %v", st)
	}
	if status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": tasks, "items": []int{1}}); status != 409 {
		t.Errorf("filing from the replaced propose card: %d %v", status, out)
	}
	if st := s.statusOf(s.marcio, id, rel, 0); st["canFile"] != true {
		t.Errorf("the relation card after proposing again: %v", st)
	}
	if w := s.waiting(s.marcio, id); w == nil || w["proposalId"] != again {
		t.Errorf("Waiting on you names %v, want the latest propose card %s", w, again)
	}
	rel2 := s.proposal(id, relation(b, c))
	for _, card := range []string{rel, rel2} {
		if st := s.statusOf(s.marcio, id, card, 0); st["canFile"] != true {
			t.Errorf("relation card %s after another relate: %v", card, st)
		}
	}
	for _, card := range []string{rel, rel2} {
		if r := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": card, "items": []int{0}})["results"].([]any)[0].(map[string]any); r["status"] != "filed" {
			t.Errorf("filing relation card %s: %v", card, r)
		}
	}
}

// Two relates and nothing proposed: both cards stay fileable, and Waiting
// on you names the latest of them.
func TestRelationCardsAloneWaitOnYou(t *testing.T) {
	s := newSessionWorld(t)
	a, b, c := s.keyOf(s.taskIn(s.project, "Meter A", s.marcio)), s.keyOf(s.taskIn(s.project, "Meter B", s.marcio)), s.keyOf(s.taskIn(s.project, "Meter C", s.marcio))
	id := s.session()
	first := s.proposal(id, relation(a, b))
	second := s.proposal(id, relation(b, c))
	for _, card := range []string{first, second} {
		if st := s.statusOf(s.marcio, id, card, 0); st["canFile"] != true {
			t.Errorf("relation card %s: %v", card, st)
		}
	}
	if w := s.waiting(s.marcio, id); w == nil || w["proposalId"] != second || w["count"] != float64(1) {
		t.Errorf("Waiting on you: %v, want the latest relation card %s", w, second)
	}
	if r := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": first, "items": []int{0}})["results"].([]any)[0].(map[string]any); r["status"] != "filed" {
		t.Errorf("filing the first relation card: %v", r)
	}
}

// fileable is how many of the card's items person can file, by the session's detail: what its band counts.
func (s *sessionWorld) fileable(person, session, card string) int {
	s.t.Helper()
	n := 0
	for _, c := range s.ok(person, "GET", "/internal/sessions/"+session, nil)["proposals"].([]any) {
		if m := c.(map[string]any); m["id"] == card {
			for _, st := range m["status"].([]any) {
				if st.(map[string]any)["canFile"] == true {
					n++
				}
			}
		}
	}
	return n
}

// A session whose owner left is its admins': an admin, who is not in it,
// is waited on by its card as its page says (the same count as the band),
// as an unaddressed question there waits on them; a member who is not in
// it is not.
func TestAnAdminOwnedSessionsCardWaitsOnTheAdmin(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	card := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"},
		{Kind: "task", Project: "BL", Title: "Dedupe runs", Goal: "The meter's 24h key is not enough for retries"},
	})
	s.ownerLeaves(id)
	band := s.fileable(s.admin, id, card)
	if band != 2 {
		t.Fatalf("the admin's band counts %d, want 2", band)
	}
	if w := s.waiting(s.admin, id); w == nil || w["proposalId"] != card || w["count"] != float64(band) {
		t.Errorf("the admin's entry: %v, want card %s with %d", w, card, band)
	}
	if w := s.waiting(s.joao, id); w != nil {
		t.Errorf("a member not in it, no admin: %v", w)
	}
}

// The sessions list says which cards wait on you in a number of queries
// that does not grow with the sessions they are in: Ana, waited on in 1,
// and Márcio, in 20 (each with an earlier card replaced, a relation card,
// a filed item and an edit, a comment and a link to decide on), cost the
// same. No image is read for it.
func TestTheListReadsWaitingCardsInBoundedQueries(t *testing.T) {
	s := newSessionWorld(t)
	counter := &queryCount{}
	cfg, err := pgxpool.ParseConfig(s.app.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t0(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	srv := httptest.NewServer((&api.Server{DB: &db.DB{Pool: pool}, Lux: s.syncer.Lux, Token: "svc", Log: s.log, Kick: func() {}}).Handler())
	t.Cleanup(srv.Close)
	list := func(person string) ([]any, int64) {
		req, _ := http.NewRequest("GET", srv.URL+"/internal/sessions", nil)
		for k, v := range map[string]string{"Authorization": "Bearer svc", "X-Dude-Organization": s.org,
			"X-Dude-Credential-Kind": "person", "X-Dude-Person": person, "X-Dude-Actor": person} {
			req.Header.Set(k, v)
		}
		before := counter.n.Load()
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode != 200 {
			t.Fatalf("list: %d %v", res.StatusCode, out)
		}
		proposals, _ := out["proposals"].([]any)
		return proposals, counter.n.Load() - before
	}
	mine, other := s.keyOf(s.taskIn(s.project, "Daily rollup", s.marcio)), s.keyOf(s.taskIn(s.project, "Weekly rollup", s.marcio))
	for i := 0; i < 20; i++ {
		id := s.session()
		if i == 0 {
			s.join(id, s.ana, "chat")
		}
		s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Old idea", Goal: "Something proposed before"}})
		card := s.proposal(id, []delivery.ProposalItem{
			{Kind: "task", Project: "BL", Title: fmt.Sprintf("Meter %d", i), Goal: "Count experiment runs per org per day"},
			{Kind: "task", Project: "BL", Title: fmt.Sprintf("Dedupe %d", i), Goal: "The meter's 24h key is not enough for retries"},
			{Kind: "edit", Task: mine, After: &delivery.TaskText{Goal: ptr("A goal long enough to keep, changed")}},
			{Kind: "comment", Task: mine, Text: "a note"},
			{Kind: "link_project", Project: "BL", Why: "it reads it"},
		})
		s.proposal(id, relation(mine, other))
		s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": card, "items": []int{0}})
	}
	few, one := list(s.ana)
	many, twenty := list(s.marcio)
	t.Logf("queries: %d for 1 waiting session, %d for 20", one, twenty)
	if len(few) != 1 || len(many) != 20 {
		t.Fatalf("%d waiting for Ana, %d for Márcio", len(few), len(many))
	}
	if twenty != one {
		t.Errorf("the list ran %d queries with 20 waiting sessions, %d with 1: it must not grow with them", twenty, one)
	}
	// Márcio: the second task, his edit and the comment (BL is linked already).
	if w := many[0].(map[string]any); w["count"] != float64(3) {
		t.Errorf("Márcio's entry: %v", w)
	}
	// Ana: the task and the comment; the edit is Márcio's.
	if w := few[0].(map[string]any); w["count"] != float64(2) {
		t.Errorf("Ana's entry: %v", w)
	}
}
