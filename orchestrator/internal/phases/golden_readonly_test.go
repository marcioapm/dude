package phases

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The cause a read-only checkout records, as the conductor reads it in
// publish's refusal and in the refusal of a publish as it lands.
func TestGoldenReadOnlyCause(t *testing.T) {
	ctx := context.Background()
	if *update {
		if err := dbtest.Reachable(ctx); err != nil {
			t.Fatalf("-update needs the test database: %v", err)
		}
	}
	var lines [][2]string
	for _, c := range []struct {
		name  string
		cause error
	}{
		{"lux-said", &lux.Error{Status: 400, Code: "bad_mode", Message: "sync mode\n  ff is not supported " + strings.Repeat("x", 130)}},
		{"lux-said-nothing", &lux.Error{Status: 400, Code: "bad_mode"}},
		{"not-lux", errors.New("connection reset")},
	} {
		app, owner := dbtest.Open(t)
		org := dbtest.Org(t, owner)
		for _, q := range []string{
			`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
			`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G', 'running')`,
			`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind, push_branch)
				VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'running', 'conductor', 'agent', 'dude/conductor')`,
			`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
				VALUES ('wfr_'||$1, $1, 'task.delivery', 'wfr_'||$1, 'wi_'||$1, 'waiting', 'review', jsonb_build_object('taskId', 'wi_'||$1,
				'projectId', 'prj_'||$1, 'attempt', 1, 'decider', 'conductor'))`,
		} {
			if _, err := owner.Exec(ctx, q, org); err != nil {
				t.Fatal(err)
			}
		}
		s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		r := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Role: "conductor"}
		if err := s.checkoutReadOnly(ctx, r, c.cause); err != nil {
			t.Fatal(err)
		}
		var refused, landing string
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := delivery.ConductPublish(ctx, tx, delivery.RunRef{Org: org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID}, "m")
			if err == nil {
				t.Fatalf("%s: publish was not refused", c.name)
			}
			refused = err.Error()
			landing, err = delivery.PublishEligibleTx(ctx, tx, delivery.PublishOf{RunID: r.ID, TaskID: r.TaskID})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, [2]string{c.name, refused}, [2]string{c.name + "-landing", landing})
	}
	agentGolden(t, "read-only-cause", lines)
}
