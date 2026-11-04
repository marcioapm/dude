package delivery

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// What ToPersonTx writes, byte for byte: a writer's question row and its
// question.asked, and an escalation's question.asked. Their readers (the
// API, the replay, the push body, the conductor's wake) read these keys; a
// key renamed, swapped or dropped reaches none of them, so it fails here,
// in the package that writes them. Ids and cursors are the run's own and
// are put back by name; jsonb's text orders the keys.
func TestToPersonWritesTheRowAndPayloadsItsReadersRead(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ('run_impl1', $1, 'prj_g', 'wi_g', 1, 'running', 'implement', 'implementer', 'agent')`)
	ctx := context.Background()
	one := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := w.owner.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	t.Run("a writer's question", func(t *testing.T) {
		ref := RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_impl1"}
		// As ask_person passes them: normalized.
		asked, err := NormalizeItems([]QuestionItem{{Question: "Which format?",
			Choices: []Choice{{Label: "CSV", Recommended: true}, {Label: "XLSX", Description: "needs a library"}}}})
		if err != nil {
			t.Fatal(err)
		}
		id := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return ToPersonTx(ctx, tx, ToPerson{Ref: ref, Items: asked, Why: WhyWriter})
		})
		if id == "" {
			t.Fatal("no question id")
		}
		put := func(s string) string {
			return strings.NewReplacer(id, "$QID", w.org, "$ORG").Replace(s)
		}
		row := one(`SELECT (to_jsonb(q) - 'asked_at')::text FROM questions q WHERE id = $1`, id)
		items := `"items": [{"header": "", "choices": [{"label": "CSV", "description": "", "recommended": true}, ` +
			`{"label": "XLSX", "description": "needs a library", "recommended": false}], "multiple": false, "question": "Which format?"}]`
		wantRow := `{"id": "$QID", ` + items + `, "answer": null, "prompt": "Which format?", "relays": null, "run_id": "run_impl1", ` +
			`"status": "open", "actions": null, "answers": null, "options": ["CSV", "XLSX"], "task_id": "wi_g", "blocking": true, ` +
			`"addressee": "person", "to_person": null, "escalation": null, "expires_at": null, "session_id": null, "answered_at": null, ` +
			`"answered_by": null, "pr_gate_heads": null, "answered_by_run": null, "organization_id": "$ORG", "answered_by_person": null}`
		if got := put(row); got != wantRow {
			t.Errorf("the questions row:\n got %s\nwant %s", got, wantRow)
		}

		var payload, cursor string
		if err := w.owner.QueryRow(ctx, `SELECT a.payload::text, m.cursor::text FROM events a, events m
			WHERE a.task_id = 'wi_g' AND a.event_type = $1 AND m.task_id = 'wi_g' AND m.event_type = $2`,
			EvQuestionAsked, EvTaskStatusChanged).Scan(&payload, &cursor); err != nil {
			t.Fatal(err)
		}
		if _, err := strconv.Atoi(cursor); err != nil {
			t.Fatalf("the move's cursor %q", cursor)
		}
		wantPayload := `{"kind": "agent", ` + items + `, "prompt": "Which format?", "options": ["CSV", "XLSX"], ` +
			`"questionId": "$QID", "taskStatus": "running", "waitCursor": $CURSOR}`
		if got := strings.Replace(put(payload), `"waitCursor": `+cursor+`}`, `"waitCursor": $CURSOR}`, 1); got != wantPayload {
			t.Errorf("question.asked:\n got %s\nwant %s", got, wantPayload)
		}
		if got := one(`SELECT concat_ws(' ', run_id, actor_type, actor_id) FROM events WHERE task_id = 'wi_g' AND event_type = $1`, EvQuestionAsked); got != "run_impl1 agent run_impl1" {
			t.Errorf("question.asked's run and actor: %s", got)
		}
	})

	t.Run("an escalation", func(t *testing.T) {
		w.exec(t, `UPDATE tasks SET status = 'running' WHERE id = 'wi_p1'`)
		e := &Escalation{Reason: "stuck", Detail: map[string]any{"round": 3, "findings": []string{"fnd_1"}}, Step: "fix"}
		st := &State{TaskID: "wi_p1", ProjectID: "prj_g", Escalation: e}
		w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return ToPersonTx(ctx, tx, ToPerson{Ref: RunRef{Org: w.org}, State: st, Escalation: e.heldBy(HolderPerson),
				Reason: "stuck", Why: WhyEscalation})
		})
		got := one(`SELECT concat_ws(' ', actor_type, actor_id, payload::text) FROM events WHERE task_id = 'wi_p1' AND event_type = $1`, EvQuestionAsked)
		want := `system workflow {"kind": "escalation", "detail": {"round": 3, "findings": ["fnd_1"]}, "reason": "stuck", "actions": ["retry", "accept", "stop"]}`
		if got != want {
			t.Errorf("question.asked:\n got %s\nwant %s", got, want)
		}
		if got := one(`SELECT status::text FROM tasks WHERE id = 'wi_p1'`); got != "awaiting_input" {
			t.Errorf("the task is %s, want awaiting_input", got)
		}
	})
}
