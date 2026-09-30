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

func (w *receiptWorld) failure() (failed bool, msg string) {
	var m *string
	_ = w.owner.QueryRow(context.Background(), `SELECT failed_at IS NOT NULL, error FROM directives WHERE id = 'dir_1'`).Scan(&failed, &m)
	if m != nil {
		msg = *m
	}
	return
}

// A failure, then "accepted" (with or without a receipt to follow): the
// failure stands, nothing is recorded as taken or delivered.
func TestAnAcceptedReceiptAfterAFailureChangesNothing(t *testing.T) {
	for _, receipt := range []bool{true, false} {
		t.Run(map[bool]string{true: "receipt", false: "no receipt"}[receipt], func(t *testing.T) {
			w := newReceiptWorld(t)
			w.receive(map[string]any{"requestId": "dir_1", "error": "the agent exited"})
			w.receive(map[string]any{"requestId": "dir_1", "phase": "accepted", "receipt": receipt, "lands": "next_step"})
			if a, d, _ := w.directive(); a || d {
				t.Errorf("accepted=%v delivered=%v after a failure", a, d)
			}
			if f, msg := w.failure(); !f || msg != "the agent exited" {
				t.Errorf("failed=%v error=%q, want the failure kept", f, msg)
			}
			if w.events(evDirectiveAccepted) != 0 || w.events(evDirectiveDelivered) != 0 {
				t.Errorf("accepted %d, delivered %d events after a failure", w.events(evDirectiveAccepted), w.events(evDirectiveDelivered))
			}
		})
	}
}

// A failure, then the agent's own read receipt (or an older lux's
// handoff): the read wins, and the failure goes with it.
func TestAReadReceiptAfterAFailureDeliversAndClearsIt(t *testing.T) {
	for _, receipt := range []map[string]any{
		{"requestId": "dir_1", "phase": "consumed"},
		{"requestId": "dir_1", "text": "also add a test"},
	} {
		t.Run(map[bool]string{true: "consumed", false: "legacy"}[receipt["phase"] != nil], func(t *testing.T) {
			w := newReceiptWorld(t)
			w.receive(map[string]any{"requestId": "dir_1", "error": "the agent exited"})
			w.receive(receipt)
			w.receive(receipt)
			if _, d, _ := w.directive(); !d {
				t.Error("a read receipt after a failure did not deliver")
			}
			if f, msg := w.failure(); f || msg != "" {
				t.Errorf("failed=%v error=%q kept on a delivered directive", f, msg)
			}
			if n := w.events(evDirectiveDelivered); n != 1 {
				t.Errorf("%d delivered events, want 1", n)
			}
		})
	}
}

// Delivered is final: a failure reported after it changes nothing.
func TestAFailureAfterDeliveryChangesNothing(t *testing.T) {
	for _, first := range []map[string]any{
		{"requestId": "dir_1", "phase": "consumed"},
		{"requestId": "dir_1", "phase": "accepted", "receipt": false, "lands": "next_turn"},
	} {
		t.Run(first["phase"].(string), func(t *testing.T) {
			w := newReceiptWorld(t)
			w.receive(first)
			w.receive(map[string]any{"requestId": "dir_1", "error": "the agent exited"})
			if _, d, _ := w.directive(); !d {
				t.Error("a failure undid a delivery")
			}
			if f, msg := w.failure(); f || msg != "" {
				t.Errorf("failed=%v error=%q recorded on a delivered directive", f, msg)
			}
			if n := w.events(evDirectiveFailed); n != 0 {
				t.Errorf("%d failed events after delivery", n)
			}
		})
	}
}

// Accepted, then failed: the failure stands against a steer only taken.
func TestAFailureAfterAcceptedIsRecorded(t *testing.T) {
	w := newReceiptWorld(t)
	w.receive(map[string]any{"requestId": "dir_1", "phase": "accepted", "receipt": true, "lands": "next_step"})
	w.receive(map[string]any{"requestId": "dir_1", "error": "the turn was cancelled before the agent read it"})
	if _, d, _ := w.directive(); d {
		t.Error("a failed steer was delivered")
	}
	if f, _ := w.failure(); !f || w.events(evDirectiveFailed) != 1 {
		t.Errorf("failed=%v, %d failed events", f, w.events(evDirectiveFailed))
	}
}

// An "Interrupt now" whose own request failed is delivered, failure
// cleared, when the agent reads the original: the same precedence on the
// superseding row.
func TestAReadOriginalClearsItsFailedInterrupt(t *testing.T) {
	w := newReceiptWorld(t)
	if _, err := w.owner.Exec(context.Background(), `INSERT INTO directives (id, organization_id, task_id, run_id, text, supersedes, resends, interrupt, interrupt_only, sent_at)
		VALUES ('dir_2', $1, 'wi_'||$1, 'run_'||$1, 'also add a test', 'dir_1', 'dir_1', true, true, now())`, w.org); err != nil {
		t.Fatal(err)
	}
	w.receive(map[string]any{"requestId": "dir_2", "error": "workload not reachable"})
	w.receive(map[string]any{"requestId": "dir_1", "phase": "consumed"})
	var delivered, failed bool
	_ = w.owner.QueryRow(context.Background(), `SELECT delivered_at IS NOT NULL, failed_at IS NOT NULL OR error IS NOT NULL FROM directives WHERE id = 'dir_2'`).
		Scan(&delivered, &failed)
	if !delivered || failed {
		t.Errorf("interrupt: delivered=%v failed=%v, want delivered with no failure", delivered, failed)
	}
}

// interruptOnly adds an "Interrupt now" resending dir_1, sent as the
// interrupt alone.
func (w *receiptWorld) interruptOnly(id string) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), `INSERT INTO directives (id, organization_id, task_id, run_id, text, supersedes, resends, interrupt, interrupt_only, sent_at)
		VALUES ($2, $1, 'wi_'||$1, 'run_'||$1, 'also add a test', 'dir_1', 'dir_1', true, true, now())`, w.org, id); err != nil {
		w.t.Fatal(err)
	}
}

func (w *receiptWorld) state(id string) (delivered bool, failed string) {
	var e *string
	_ = w.owner.QueryRow(context.Background(), `SELECT delivered_at IS NOT NULL, error FROM directives WHERE id = $1`, id).Scan(&delivered, &e)
	if e != nil {
		failed = *e
	}
	return
}

// The words fail: each interrupt that relied on them fails with the same
// reason, one failed event each, however often lux repeats the error.
func TestAnInterruptAloneFailsWithItsWords(t *testing.T) {
	w := newReceiptWorld(t)
	w.interruptOnly("dir_2")
	w.interruptOnly("dir_3")
	failed := map[string]any{"requestId": "dir_1", "error": "the turn was cancelled before the agent read it"}
	w.receive(failed)
	w.receive(failed)
	for _, id := range []string{"dir_2", "dir_3"} {
		if d, f := w.state(id); d || f != "the turn was cancelled before the agent read it" {
			t.Errorf("%s: delivered=%v error=%q, want failed with the words' reason", id, d, f)
		}
	}
	if n := w.events(evDirectiveFailed); n != 3 {
		t.Errorf("%d failed events, want one for each of three", n)
	}
	// Read after all: the words and both interrupts are delivered.
	w.receive(map[string]any{"requestId": "dir_1", "phase": "consumed"})
	for _, id := range []string{"dir_2", "dir_3"} {
		if d, f := w.state(id); !d || f != "" {
			t.Errorf("%s: delivered=%v error=%q after the read", id, d, f)
		}
	}
}

// The words fail while a retry carrying them is still with lux: the
// interrupt waits on the retry, and is delivered when it is read.
func TestAnInterruptAloneWaitsOnARetryCarryingItsWords(t *testing.T) {
	w := newReceiptWorld(t)
	w.interruptOnly("dir_2")
	if _, err := w.owner.Exec(context.Background(), `INSERT INTO directives (id, organization_id, task_id, run_id, text, supersedes, resends, interrupt, interrupt_only, sent_at)
		VALUES ('dir_3', $1, 'wi_'||$1, 'run_'||$1, 'also add a test', 'dir_2', 'dir_1', true, false, now())`, w.org); err != nil {
		t.Fatal(err)
	}
	w.receive(map[string]any{"requestId": "dir_1", "error": "the agent exited"})
	if d, f := w.state("dir_2"); d || f != "" {
		t.Fatalf("interrupt: delivered=%v error=%q while a retry carries its words", d, f)
	}
	w.receive(map[string]any{"requestId": "dir_3", "phase": "consumed"})
	if d, f := w.state("dir_2"); !d || f != "" {
		t.Errorf("interrupt: delivered=%v error=%q after the retry was read", d, f)
	}
	var n int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = $1
		AND payload->>'directiveId' = 'dir_2' AND (payload->>'interruptOnly')::boolean`, evDirectiveDelivered).Scan(&n)
	if n != 1 {
		t.Errorf("%d interrupt-only delivered events, want 1", n)
	}
}
