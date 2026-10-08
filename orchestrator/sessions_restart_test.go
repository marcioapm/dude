package orchestrator_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func TestRestartRefusesABrainstormRun(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	run := s.started(id)
	task := s.task()
	err := s.app.InOrg(t0(), s.org, func(tx pgx.Tx) error {
		_, err := delivery.RestartRunTx(t0(), tx, s.org, task, delivery.Restart{RunID: run})
		return err
	})
	var refusal delivery.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("restart returned %v, want a delivery refusal", err)
	}
	status, out := s.as(s.marcio, "POST", "/internal/runs/"+run+"/restart", map[string]any{"note": "restart"})
	if status < 400 || status >= 500 {
		t.Fatalf("owner restart returned %d %v, want a client refusal", status, out)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND replaced_by IS NOT NULL`, run); n != 0 {
		t.Fatal("brainstorm was replaced")
	}
}
