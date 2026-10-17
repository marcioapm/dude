package delivery

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The Chat notice an auto-start writes, for each way it starts the task.
func TestGoldenAutoStartedNotice(t *testing.T) {
	var lines [][2]string
	for _, how := range []string{HowDeliver, HowTalk} {
		w := newGoldenWorld(t)
		w.exec(t, `UPDATE tasks SET status = 'received', epic_id = NULL WHERE id = 'wi_g'`)
		w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_u', $1, 'task.unblocked', 'wi_g', 'system', 'dude', 'test', '{"by":"GLD-8","pr":12,"blockers":[]}')`)
		w.exec(t, `INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person, due_at)
			VALUES ('wi_g', $1, $2, 'per_ana', now())`, how)
		a := &AutoStarts{Starter: goldenStarter{}}
		lines = append(lines, [2]string{"auto-started-" + how, w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			var claimed string
			if err := a.fire(ctx, tx, w.org, "wi_g", &claimed); err != nil {
				return "", err
			}
			var text string
			err := tx.QueryRow(ctx, `SELECT payload->>'text' FROM events WHERE task_id = 'wi_g' AND event_type = $1
				AND payload->>'about' = 'auto_started'`, EvChatNotice).Scan(&text)
			return text, err
		})})
	}
	goldenTexts(t, "auto-started-notice", lines)
}

// decide_escalation's refusals that send the conductor to the banner: no
// escalation waits on a decision, and the owner has not answered.
func TestGoldenDecideEscalationRefusals(t *testing.T) {
	var lines [][2]string
	for _, c := range []struct{ name, step, state string }{
		{"not-waiting", "review", `{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor"}`},
		{"not-answered", "decide", `{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor",
			"escalation":{"reason":"stuck","step":"review"}}`},
	} {
		w := newGoldenWorld(t)
		w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind, created_at)
			VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent', '2026-01-02 09:00Z')`)
		w.delivery(t, c.step, c.state)
		ref := RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}
		var refused string
		w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			_, err := ConductDecideEscalation(ctx, tx, ref, "retry", "")
			if err == nil {
				t.Fatalf("%s: decide_escalation was not refused", c.name)
			}
			refused = err.Error()
			return "", nil
		})
		lines = append(lines, [2]string{c.name, refused})
	}
	goldenTexts(t, "decide-escalation-refusals", lines)
}
