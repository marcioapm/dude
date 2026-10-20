package delivery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The PR accounts for the current attempt's gated Runs only: an earlier
// attempt's waiver is not shown on a later attempt's verified work, whether
// the delivery names its attempt or predates attempts being kept (0).
func TestThePullRequestChecksAccountIsTheCurrentAttempts(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_cv',$1,'CV','cv','CV')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_cv',$1,'prj_cv',1,'CV','CV','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status,created_at)
			VALUES('run_cv1',$1,'prj_cv','wi_cv',1,'implement','completed',now()-interval '1 hour')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_cv2',$1,'prj_cv','wi_cv',2,'implement','completed')`,
		`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload) VALUES
			('ev_cv1a',$1,'run.checks.judged','run_cv1','wi_cv','system','dude','test',
				'{"turn":1,"verified":false,"message":"not backed","checks":[{"command":"go test ./...","result":"pass","decision":"no"}]}'),
			('ev_cv1b',$1,'run.checks.judged','run_cv1','wi_cv','human','usr','test',
				'{"turn":1,"verified":true,"waived":true,"message":"Old attempt waiver.","checks":[]}'),
			('ev_cv2',$1,'run.checks.judged','run_cv2','wi_cv','system','dude','test',
				'{"turn":1,"verified":true,"checks":[{"command":"go test ./...","result":"pass","decision":"yes","callId":"mcall_2"}]}')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	const want = "### Checks\n" +
		"Checks were verified against the Run's recorded commands.\n" +
		"\n" +
		"- **Implementer** — backed\n" +
		"  - `go test ./...` — **pass** (`mcall_2`)"
	for _, attempt := range []int{0, 2} {
		var got string
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			var err error
			got, err = checksVerificationText(ctx, tx, &State{TaskID: "wi_cv", ProjectID: "prj_cv", Attempt: attempt})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("attempt %d: the checks account is\n%s\nwant\n%s", attempt, got, want)
		}
	}
}

// The most gated Runs an attempt can have (an implementer, FixRoundsPerPR's
// 50 fixers and a simplifier), each reporting the most checks with long
// commands and reasons, still fit a pull request body: the headline and its
// waiver reason whole, the newest Runs, and how many earlier ones are left out.
func TestThePullRequestChecksAccountIsBounded(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_cb',$1,'CB','cb','CB')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_cb',$1,'prj_cb',1,'CB','CB','running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status,created_at)
			SELECT 'run_cb' || lpad(g::text, 2, '0'), $1, 'prj_cb', 'wi_cb', 1,
				(CASE g WHEN 0 THEN 'implement' WHEN 51 THEN 'simplify' ELSE 'fix' END)::run_phase, 'completed',
				now() - (52 - g) * interval '1 minute'
			FROM generate_series(0, 51) g`,
		`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload)
			SELECT 'ev_cb' || r.id, $1, 'run.checks.judged', r.id, 'wi_cb', 'system', 'dude', 'test',
				CASE WHEN r.phase = 'implement' THEN '{"turn":1,"verified":true,"waived":true,"message":"Hardware CI.","checks":[]}'::jsonb
				ELSE jsonb_build_object('turn', 1, 'verified', true, 'checks', (SELECT jsonb_agg(jsonb_build_object(
					'command', repeat('c', 5000), 'result', 'pass', 'decision', 'yes', 'reason', repeat('r ', 2500), 'callId', 'mcall_x'))
					FROM generate_series(1, 30))) END
			FROM runs r WHERE r.task_id = 'wi_cb'`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	var got string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		var err error
		got, err = checksVerificationText(ctx, tx, &State{TaskID: "wi_cb", ProjectID: "prj_cb", Attempt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(got); n > 60_000 {
		t.Fatalf("the checks account is %d characters", n)
	}
	head, rows, _ := strings.Cut(got, "\n\n")
	if want := "### Checks\nChecks were waived by a person:\n- Implementer: Hardware CI."; head != want {
		t.Errorf("headline %q, want %q", head, want)
	}
	lines := strings.Split(rows, "\n")
	var shown []string
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "- **") {
			shown = append(shown, l)
		}
	}
	if len(shown) == 0 || shown[len(shown)-1] != "- **Simplifier** — backed" {
		t.Fatalf("the newest Run is not shown last: %q", shown)
	}
	if want := fmt.Sprintf("%d earlier Runs omitted.", 52-len(shown)); lines[0] != want {
		t.Errorf("the rows open with %q, want %q", lines[0], want)
	}
}
