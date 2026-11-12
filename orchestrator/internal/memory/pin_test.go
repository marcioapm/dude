package memory_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// A fact read, then made about a task by a write committed before the pin
// takes its row lock, is refused the pin: Pin checks the memory as it is
// under the lock, not the copy its caller read.
func TestAFactMadeAboutATaskAfterItWasReadIsNotPinned(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, task := seed(t, owner)
	remember(t, owner, org, "mem_race", project, "The mirror goes down", "Retry once.")
	ctx := context.Background()

	err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		m, err := memory.Get(ctx, tx, "mem_race", memory.Viewer{})
		if err != nil {
			return err
		}
		if err := memory.Pinnable(m); err != nil {
			t.Fatalf("the fact as read is not pinnable: %v", err)
		}
		// A PATCH elsewhere, committed: now it is about a task.
		exec(t, owner, `INSERT INTO memory_refs (memory_id, organization_id, ref_type, ref_id) VALUES ('mem_race', $1, 'task', $2)`, org, task)
		_, err = memory.Pin(ctx, tx, org, m, true, memory.Pinner{RunID: ""}, memory.Actor{Type: "system", ID: "test"})
		return err
	})
	var bad *memory.Invalid
	if !errors.As(err, &bad) {
		t.Fatalf("pinned a fact about a task, or failed otherwise: %v", err)
	}
	var pinned bool
	if err := owner.QueryRow(ctx, `SELECT pinned_at IS NOT NULL FROM memories WHERE id = 'mem_race'`).Scan(&pinned); err != nil || pinned {
		t.Errorf("the row is pinned: %v %v", pinned, err)
	}
}

// A fact read unpinned, then pinned by a pin committed before the edit
// takes its row lock, keeps Update's pinned guard: making it a note is
// refused with the reason, not failed by the database's CHECK.
func TestAnEditOfAFactPinnedAfterItWasReadKeepsThePinnedGuard(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, _ := seed(t, owner)
	remember(t, owner, org, "mem_race", project, "The mirror goes down", "Retry once.")
	ctx := context.Background()

	note := "note"
	err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		m, err := memory.Get(ctx, tx, "mem_race", memory.Viewer{})
		if err != nil {
			return err
		}
		exec(t, owner, `UPDATE memories SET pinned_at = now() WHERE id = 'mem_race'`)
		_, err = memory.Update(ctx, tx, org, m, memory.Patch{Kind: &note}, memory.Actor{Type: "system", ID: "test"})
		return err
	})
	var bad *memory.Invalid
	if !errors.As(err, &bad) || !strings.Contains(bad.Reason, "unpin it first") {
		t.Fatalf("a pinned fact made a note, or failed otherwise: %v", err)
	}
	var kind string
	if err := owner.QueryRow(ctx, `SELECT kind FROM memories WHERE id = 'mem_race'`).Scan(&kind); err != nil || kind != "fact" {
		t.Errorf("kind %q (%v)", kind, err)
	}
}

// The database's own refusal of a pin, its CHECK (23514), is a refusal
// the caller is told, as Pinnable's are: never a failure.
func TestTheDatabasesRefusalOfAPinIsARefusal(t *testing.T) {
	_, owner := dbtest.Open(t)
	org, _, _, _ := seed(t, owner)
	_, err := owner.Exec(context.Background(), `INSERT INTO memories (id, organization_id, title, content, author_kind, system_reason, pinned_at)
		VALUES ('mem_org', $1, 't', 'x', 'system', 'test', now())`, org)
	var bad *memory.Invalid
	if err == nil || !errors.As(memory.PinRefused(err), &bad) {
		t.Errorf("the CHECK's refusal %v is not a refusal", err)
	}
	if other := errors.New("boom"); memory.PinRefused(other) != other {
		t.Errorf("another error is changed")
	}
}
