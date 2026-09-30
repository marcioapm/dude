package phases

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// receiptWorld is one Run with one queued directive, and a translator for
// it: what lux reports about the directive is fed straight to the
// translator, each receipt in its own transaction as a batch would be.
type receiptWorld struct {
	t     *testing.T
	s     *Syncer
	tr    *translator
	owner *pgx.Conn
	org   string
}

func newReceiptWorld(t *testing.T) *receiptWorld {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1)`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('other_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 2)`,
		`INSERT INTO directives (id, organization_id, task_id, run_id, text, sent_at) VALUES ('dir_1', $1, 'wi_'||$1, 'run_'||$1, 'also add a test', now())`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	run := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org}
	return &receiptWorld{t: t, s: &Syncer{DB: app}, tr: &translator{run: run}, owner: owner, org: org}
}

func (w *receiptWorld) receive(data map[string]any) {
	w.t.Helper()
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.tr.shimEvent(context.Background(), tx, w.s, "lux.input", data, 1)
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *receiptWorld) events(typ string) int {
	var n int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = $1`, typ).Scan(&n)
	return n
}

func (w *receiptWorld) directive() (accepted, delivered bool, lands string) {
	var l *string
	_ = w.owner.QueryRow(context.Background(), `SELECT accepted_at IS NOT NULL, delivered_at IS NOT NULL, lands FROM directives WHERE id = 'dir_1'`).
		Scan(&accepted, &delivered, &l)
	if l != nil {
		lands = *l
	}
	return
}

// Accepted is not read: a steer the harness took is delivered only when the
// agent's next step has it, and each receipt counts once however often lux
// repeats it.
func TestASteerIsAcceptedThenReadEachOnce(t *testing.T) {
	w := newReceiptWorld(t)
	accepted := map[string]any{"requestId": "dir_1", "phase": "accepted", "receipt": true, "lands": "next_step", "text": "also add a test"}
	w.receive(accepted)
	w.receive(accepted)
	if a, d, lands := w.directive(); !a || d || lands != "next_step" {
		t.Fatalf("after accepted: accepted=%v delivered=%v lands=%q", a, d, lands)
	}
	if n := w.events(evDirectiveAccepted); n != 1 {
		t.Errorf("%d accepted events, want 1", n)
	}
	if n := w.events(evDirectiveDelivered); n != 0 {
		t.Errorf("a steer the harness only took was reported delivered")
	}

	consumed := map[string]any{"requestId": "dir_1", "phase": "consumed"}
	w.receive(consumed)
	w.receive(consumed)
	if _, d, _ := w.directive(); !d {
		t.Fatal("a consumed steer was not delivered")
	}
	if n := w.events(evDirectiveDelivered); n != 1 {
		t.Errorf("%d delivered events for one directive, want 1", n)
	}
	// A late accepted after the read changes nothing.
	w.receive(accepted)
	if n := w.events(evDirectiveAccepted); n != 1 {
		t.Errorf("an accepted after the read was recorded again")
	}
}

// With no read receipt to come, accepted is all there will be: delivered then.
func TestAnAcceptedSteerWithNoReceiptIsDeliveredAtOnce(t *testing.T) {
	w := newReceiptWorld(t)
	w.receive(map[string]any{"requestId": "dir_1", "phase": "accepted", "receipt": false, "lands": "next_turn"})
	if a, d, lands := w.directive(); !a || !d || lands != "next_turn" {
		t.Fatalf("accepted=%v delivered=%v lands=%q", a, d, lands)
	}
	if w.events(evDirectiveAccepted) != 1 || w.events(evDirectiveDelivered) != 1 {
		t.Errorf("accepted %d, delivered %d events", w.events(evDirectiveAccepted), w.events(evDirectiveDelivered))
	}
}

// An older lux acknowledges once, with no phase: delivered on it, once.
func TestALegacyReceiptDeliversOnce(t *testing.T) {
	w := newReceiptWorld(t)
	legacy := map[string]any{"requestId": "dir_1", "text": "also add a test"}
	w.receive(legacy)
	w.receive(legacy)
	if _, d, _ := w.directive(); !d {
		t.Fatal("a legacy receipt did not deliver")
	}
	if n := w.events(evDirectiveDelivered); n != 1 {
		t.Errorf("%d delivered events for repeated legacy receipts, want 1", n)
	}
	if n := w.events(evDirectiveAccepted); n != 0 {
		t.Errorf("a legacy receipt claimed an accepted phase")
	}
}

// A receipt is for this Run's directive only: another Run's stream naming
// the id does not deliver it.
func TestAReceiptOnAnotherRunDoesNothing(t *testing.T) {
	w := newReceiptWorld(t)
	w.tr.run.ID = "other_" + w.org
	w.receive(map[string]any{"requestId": "dir_1", "phase": "consumed"})
	if _, d, _ := w.directive(); d {
		t.Error("another Run's receipt delivered the directive")
	}
}

func TestAFailedSteerIsRecordedOnceAndNotDelivered(t *testing.T) {
	w := newReceiptWorld(t)
	failed := map[string]any{"requestId": "dir_1", "error": "the agent exited"}
	w.receive(failed)
	w.receive(failed)
	if _, d, _ := w.directive(); d {
		t.Error("a failed steer was delivered")
	}
	if n := w.events(evDirectiveFailed); n != 1 {
		t.Errorf("%d failed events, want 1", n)
	}
}
