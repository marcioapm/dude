package delivery

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A decision recorded as an answered question names its one item itself:
// it does not lean on the trigger that fills items for a writer naming none.
func TestARecordedDecisionNamesItsItem(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_d', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_d', $1, 'prj_d', 1, 'T', 'g')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE questions DISABLE TRIGGER questions_default_items`); err != nil {
		t.Fatal(err)
	}
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		return RecordDecisionTx(ctx, tx, org, "wi_d", "Stuck on review: what now?", "Retry")
	}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := owner.QueryRow(ctx, `SELECT items FROM questions WHERE task_id = 'wi_d'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	got, err := ReadItems(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := []QuestionItem{SingleItem("Stuck on review: what now?", nil)}; !reflect.DeepEqual(got, want) {
		t.Errorf("items %+v, want %+v", got, want)
	}
}

// The migration's one-item expression and SingleItem are the same item.
func TestQuestionItemsOfIsSingleItem(t *testing.T) {
	_, owner := dbtest.Open(t)
	options, _ := json.Marshal([]string{"Yes", "No — later"})
	var raw []byte
	if err := owner.QueryRow(context.Background(), `SELECT question_items_of($1, $2::jsonb)`, "Proceed?", options).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	got, err := ReadItems(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := []QuestionItem{SingleItem("Proceed?", []string{"Yes", "No — later"})}; !reflect.DeepEqual(got, want) {
		t.Errorf("question_items_of %+v, want %+v", got, want)
	}
}
