package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The gate waits on a finished test Run's files only when one that is
// still due could turn a verdict: a pass citing a name not recorded yet.
// A cited name already recorded, or a report with no pass to verify, is
// judged at once.
func TestTheGateWaitsForEvidenceOnlyWhenItCouldChangeTheVerdict(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(t *testing.T, _ any, sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_v', $1, 'P', 'p', 'P')`, org)
	report := func(status, evidence string) string {
		return "```yaml\n---\ncriterion: 1\nstatus: " + status + "\nevidence: " + evidence + "\nobserved: Seen.\n```\n"
	}
	cases := []struct {
		name, reply string
		recorded    []string
		wait        bool
		verdict     string
	}{
		{"a pass citing a file still on its way", report("pass", "walkthrough.webm"), nil, true, ""},
		{"a pass citing a file recorded", report("pass", "walkthrough.webm"), []string{"walkthrough.webm"}, false, VerdictPass},
		{"a failure", report("fail", "walkthrough.webm"), nil, false, VerdictFail},
		{"blocked", "```yaml\n---\ncriterion: 1\nstatus: blocked\nobserved: It did not start.\n```\n", nil, false, VerdictBlocked},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task, run := "wi_v"+string(rune('a'+i)), "run_v"+string(rune('a'+i))
			exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
				VALUES ($1, $2, 'prj_v', $3, 'Greet', 'greet', '["it greets"]', 'running')`, task, org, i+1)
			// Ended a minute ago, its exit's files not all reported yet.
			exec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind,
					ended_at, artifacts_due_at)
				VALUES ($1, $2, 'prj_v', $3, 1, 'completed', 'test', 'qa_browser', 'agent', now() - interval '1 minute', now())`,
				run, org, task)
			exec(t, owner, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
				VALUES ($1, $2, 'agent.message', $3, $4, 'agent', $3, 'test', jsonb_build_object('text', $5::text))`,
				"ev_"+run, org, run, task, c.reply)
			for _, name := range c.recorded {
				exec(t, owner, `INSERT INTO artifacts (id, organization_id, run_id, kind, name, storage_key, sha256)
					VALUES ($1, $2, $3, 'published', $4, $1, 'x')`, "art_"+run, org, run, name)
			}
			st := &State{TaskID: task, ProjectID: "prj_v", Attempt: 1, TestRunID: run, Policy: DefaultPolicy()}
			raw, _ := json.Marshal(st)
			w := &steps{s: &Store{DB: app}}
			before := time.Now()
			res, err := w.testVerdict(ctx, workflow.StepContext{WorkflowRunID: "wf_" + run, OrganizationID: org,
				Step: "testVerdict", State: raw})
			if err != nil {
				t.Fatal(err)
			}
			var status string
			_ = owner.QueryRow(ctx, `SELECT COALESCE((SELECT status FROM acceptance_verdicts WHERE run_id = $1 AND criterion = 1), '')`,
				run).Scan(&status)
			waited := res.Next == "testVerdict" && !res.SleepUntil.IsZero()
			if waited != c.wait || status != c.verdict {
				t.Fatalf("waited %v (next %q, until %v), verdict %q; want waited %v, verdict %q",
					waited, res.Next, res.SleepUntil, status, c.wait, c.verdict)
			}
			if c.wait {
				// A tenth of the minute since the Run ended.
				if d := res.SleepUntil.Sub(before); d < 5*time.Second || d > 10*time.Second {
					t.Errorf("waits %v, want about 6s", d)
				}
				// Once its files are all in, it judges.
				exec(t, owner, `UPDATE runs SET artifacts_due_at = NULL WHERE id = $1`, run)
				res, err = w.testVerdict(ctx, workflow.StepContext{WorkflowRunID: "wf_" + run, OrganizationID: org,
					Step: "testVerdict", State: raw})
				if err != nil {
					t.Fatal(err)
				}
				_ = owner.QueryRow(ctx, `SELECT status FROM acceptance_verdicts WHERE run_id = $1 AND criterion = 1`, run).Scan(&status)
				if res.Next == "testVerdict" || status != VerdictUnverified {
					t.Errorf("after the files were in: next %q, verdict %q; want judged unverified", res.Next, status)
				}
			}
		})
	}
}

// A test that fails a criterion without a finding of its own gets dude's,
// even while an earlier test's finding about another criterion is open:
// that one says nothing about this failure.
func TestAFailureGetsAFindingWhileAnEarlierTestsIsOpen(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_q', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
			VALUES ('wi_q', $1, 'prj_q', 1, 'Greet', 'greet', '["it greets", "it says goodbye"]', 'running')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
			VALUES ('run_q1', $1, 'prj_q', 'wi_q', 1, 'completed', 'test', 'qa_browser', 'agent', now() - interval '1 hour'),
			       ('run_q2', $1, 'prj_q', 'wi_q', 1, 'completed', 'test', 'qa_browser', 'agent', now())`,
		`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description, fix_attempts)
			VALUES ('fnd_q1', $1, 'wi_q', 'run_q1', 'acceptance', 'blocking', 'Criterion 1 is not met', 'No greeting.', 1)`,
		`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_q2', $1, 'agent.message', 'run_q2', 'wi_q', 'agent', 'run_q2', 'test', jsonb_build_object('text',
			E'` + "```yaml\\n---\\nverdicts:\\n  F1: still\\n---\\ncriterion: 1\\nstatus: fail\\nobserved: Still no greeting.\\n" +
			"---\\ncriterion: 2\\nstatus: fail\\nobserved: No goodbye either.\\n```" + `'))`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{DB: app}
	st := &State{TaskID: "wi_q", ProjectID: "prj_q", Attempt: 1, TestRunID: "run_q2", Policy: DefaultPolicy()}
	if _, err := s.RecordVerdict(ctx, org, st, "run_q2"); err != nil {
		t.Fatal(err)
	}
	var titles []string
	rows, err := owner.Query(ctx, `SELECT title FROM review_findings WHERE run_id = 'run_q2' ORDER BY title`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title string
		_ = rows.Scan(&title)
		titles = append(titles, title)
	}
	rows.Close()
	want := []string{"Acceptance criterion 1 fails: it greets", "Acceptance criterion 2 fails: it says goodbye"}
	if len(titles) != 2 || titles[0] != want[0] || titles[1] != want[1] {
		t.Errorf("this test's findings %q, want %q", titles, want)
	}
}

// Two tests that each fail a criterion with no finding of their own raise
// one finding for it: the second's failure is the open one's.
func TestARepeatedFailureGetsOneFinding(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	reply := "```yaml\\n---\\ncriterion: 1\\nstatus: fail\\nobserved: No greeting.\\n```"
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_d', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
			VALUES ('wi_d', $1, 'prj_d', 1, 'Greet', 'greet', '["it greets"]', 'running')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
			VALUES ('run_d1', $1, 'prj_d', 'wi_d', 1, 'completed', 'test', 'qa_browser', 'agent', now() - interval '1 hour'),
			       ('run_d2', $1, 'prj_d', 'wi_d', 1, 'completed', 'test', 'qa_browser', 'agent', now())`,
		`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_d1', $1, 'agent.message', 'run_d1', 'wi_d', 'agent', 'run_d1', 'test', jsonb_build_object('text', E'` + reply + `')),
			       ('ev_d2', $1, 'agent.message', 'run_d2', 'wi_d', 'agent', 'run_d2', 'test', jsonb_build_object('text', E'` + reply + `'))`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{DB: app}
	for _, run := range []string{"run_d1", "run_d2"} {
		st := &State{TaskID: "wi_d", ProjectID: "prj_d", Attempt: 1, TestRunID: run, Policy: DefaultPolicy()}
		if _, err := s.RecordVerdict(ctx, org, st, run); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM review_findings WHERE task_id = 'wi_d' AND status = 'open'
		AND title LIKE 'Acceptance criterion 1 fails:%'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d open findings for criterion 1, want 1", n)
	}
}
