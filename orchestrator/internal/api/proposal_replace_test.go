package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Which of a session's cards a later card replaced, said once in Go
// (replacedCards) and once in SQL (replacedCardSQL), over the same
// sessions: only a propose card replaces, and only an earlier propose card
// is replaced; a card of relations only does neither.
func TestReplacedCardsSaysTheSameInGoAndSQL(t *testing.T) {
	task := delivery.ProposalItem{Kind: "task", Project: "BL", Title: "Meter runs"}
	edit := delivery.ProposalItem{Kind: "edit", Task: "BL-1"}
	rel := delivery.ProposalItem{Kind: "relation", From: "BL-1", To: "BL-2", RelationKind: "blocks"}
	P, R, M := []delivery.ProposalItem{task, task}, []delivery.ProposalItem{rel}, []delivery.ProposalItem{rel, edit}
	fixtures := []struct {
		name  string
		cards [][]delivery.ProposalItem
		want  []bool
	}{
		{"propose then relate", [][]delivery.ProposalItem{P, R}, []bool{false, false}},
		{"propose, relate, propose", [][]delivery.ProposalItem{P, R, P}, []bool{true, false, false}},
		{"two relates", [][]delivery.ProposalItem{R, R}, []bool{false, false}},
		{"relate then propose", [][]delivery.ProposalItem{R, P}, []bool{false, false}},
		{"a relation beside an edit is a propose card", [][]delivery.ProposalItem{P, M, R}, []bool{true, false, false}},
		{"three proposes", [][]delivery.ProposalItem{P, P, P}, []bool{true, true, false}},
	}
	for _, f := range fixtures {
		var cards []proposalCard
		for _, items := range f.cards {
			cards = append(cards, proposalCard{Items: items})
		}
		if got := replacedCards(cards); fmt.Sprint(got) != fmt.Sprint(f.want) {
			t.Errorf("Go, %s: %v, want %v", f.name, got, f.want)
		}
	}

	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for i, f := range fixtures {
		session := fmt.Sprintf("ses_r%d", i)
		if _, err := owner.Exec(ctx, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ($1, $2, 'S', 'person')`, session, org); err != nil {
			t.Fatal(err)
		}
		for n, items := range f.cards {
			raw, _ := json.Marshal(items)
			// One timestamp for all, so the order is the id's, as (created_at, id) reads it.
			if _, err := owner.Exec(ctx, `INSERT INTO session_proposals (id, organization_id, session_id, items, created_at)
				VALUES ($1, $2, $3, $4, '2026-01-01')`, fmt.Sprintf("%s_%d", session, n), org, session, raw); err != nil {
				t.Fatal(err)
			}
		}
		var got []bool
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT `+replacedCardSQL("sp")+` FROM session_proposals sp WHERE sp.session_id = $1 ORDER BY sp.created_at, sp.id`, session)
			if err != nil {
				return err
			}
			got, err = pgx.CollectRows(rows, pgx.RowTo[bool])
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != fmt.Sprint(f.want) {
			t.Errorf("SQL, %s: %v, want %v", f.name, got, f.want)
		}
	}
}
