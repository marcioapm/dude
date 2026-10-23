package delivery

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A resumed tester's carried criterion is recorded as the previous round's
// pass, with that round's evidence and carried_from naming it; the same
// carry of a criterion that failed there, or naming another run, is
// recorded blocked. A Run that resumed no round (fresh after a large
// change) carries nothing.
func TestACarriedVerdictIsRecordedFromThePreviousRound(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, append([]any{org}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_k', $1, 'P', 'p', 'P')`)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
		VALUES ('wi_k', $1, 'prj_k', 1, 'Greet', 'greet', '["it greets", "it says goodbye", "it waves"]', 'running')`)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
		VALUES ('run_k1', $1, 'prj_k', 'wi_k', 1, 'completed', 'test', 'qa_browser', 'agent', now() - interval '1 hour')`)
	exec(`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence, observed)
		VALUES ($1, 'wi_k', 'run_k1', 1, 'it greets', 'pass', '{greet.webm}', 'It greets.'),
		       ($1, 'wi_k', 'run_k1', 2, 'it says goodbye', 'fail', '{bye.png}', 'No goodbye.'),
		       ($1, 'wi_k', 'run_k1', 3, 'it waves', 'pass', '{wave.png}', 'It waves.')`)
	reply := "```yaml\ncriterion: 1\nstatus: carried\nfrom_round: run_k1\nobserved: Only CI changed.\n" +
		"---\ncriterion: 2\nstatus: carried\nfrom_round: run_k1\nobserved: Only CI changed.\n" +
		"---\ncriterion: 3\nstatus: carried\nfrom_round: run_other\nobserved: Only CI changed.\n```\n"
	s := &Store{DB: app}
	for _, c := range []struct {
		run, how string
		want     []string
	}{
		{"run_k2", "resume", []string{"1 pass run_k1 greet.webm", "2 blocked  ", "3 blocked  "}},
		{"run_k3", "", []string{"1 blocked  ", "2 blocked  ", "3 blocked  "}},
	} {
		exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at,
				continues_run_id, continues_how)
			VALUES ($2, $1, 'prj_k', 'wi_k', 1, 'completed', 'test', 'qa_browser', 'agent', now(),
				CASE WHEN $3 <> '' THEN 'run_k1' END, NULLIF($3, ''))`, c.run, c.how)
		exec(`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_'||$2, $1, 'agent.message', $2, 'wi_k', 'agent', $2, 'test', jsonb_build_object('text', $3::text))`, c.run, reply)
		st := &State{TaskID: "wi_k", ProjectID: "prj_k", Attempt: 1, TestRunID: c.run, Policy: DefaultPolicy(), CarryFrom: "run_k1"}
		if _, err := s.RecordVerdict(ctx, org, st, c.run); err != nil {
			t.Fatal(err)
		}
		rows, err := owner.Query(ctx, `SELECT criterion || ' ' || status || ' ' || COALESCE(carried_from, '') || ' ' ||
				array_to_string(evidence, ','), problem
			FROM acceptance_verdicts WHERE run_id = $1 ORDER BY criterion`, c.run)
		if err != nil {
			t.Fatal(err)
		}
		var got, problems []string
		for rows.Next() {
			var line, problem string
			if err := rows.Scan(&line, &problem); err != nil {
				t.Fatal(err)
			}
			got, problems = append(got, line), append(problems, problem)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s's verdicts = %q, want %q", c.run, got, c.want)
		}
		for i, line := range got {
			if line[2:9] == "blocked" && problems[i] == "" {
				t.Errorf("%s criterion %d is blocked without saying why", c.run, i+1)
			}
		}
	}
}
