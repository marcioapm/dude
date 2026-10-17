package delivery

import (
	"context"
	"fmt"
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
// escalation waits on a decision, the owner has not answered, the answer
// was to an earlier conductor or not the owner's, and an ownerless task's
// reason given by someone not an admin.
func TestGoldenDecideEscalationRefusals(t *testing.T) {
	const waiting = `{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor","escalation":{"reason":%q,"step":"review"}}`
	answered := func(asker, by string) string {
		return `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, answer, answered_by_person, answered_at,
			escalation, options) VALUES ('qst_e', $1, 'wi_g', '` + asker + `', 'Retry?', 'answered', 'it is fine as it is', '` + by + `', now(),
			'wfr_g:0', '["Retry","Accept"]')`
	}
	var lines [][2]string
	for _, c := range []struct {
		name, step, state, action string
		setup                     []string
	}{
		{name: "not-waiting", step: "review", state: `{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor"}`, action: "retry"},
		{name: "not-answered", step: "decide", state: fmt.Sprintf(waiting, "stuck"), action: "retry"},
		{name: "earlier-conductor", step: "decide", state: fmt.Sprintf(waiting, "stuck"), action: "retry",
			setup: []string{answered("run_cond0", "per_ana")}},
		{name: "not-owner", step: "decide", state: fmt.Sprintf(waiting, "stuck"), action: "retry",
			setup: []string{`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ('wi_g', 'per_ana', $1, 0)`,
				answered("run_cond1", "per_bo")}},
		{name: "not-admin", step: "decide", state: fmt.Sprintf(waiting, ReasonTestFailedCriteria), action: "accept",
			setup: []string{answered("run_cond1", "per_bo")}},
	} {
		w := newGoldenWorld(t)
		w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind, created_at)
			VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent', '2026-01-02 09:00Z'),
			('run_cond0', $1, 'prj_g', 'wi_g', 1, 'completed', 'conductor', 'agent', '2026-01-02 08:00Z')`)
		w.delivery(t, c.step, c.state)
		for _, q := range c.setup {
			w.exec(t, q)
		}
		ref := RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}
		var refused string
		w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			_, err := ConductDecideEscalation(ctx, tx, ref, c.action, "")
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
