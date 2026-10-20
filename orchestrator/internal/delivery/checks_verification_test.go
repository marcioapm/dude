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

// A check's fields are clipped and then put in code spans no backtick of
// theirs can close, so a recorded command never breaks the account's markdown.
func TestAChecksRowKeepsItsMarkdownWhole(t *testing.T) {
	long := strings.Repeat("ü", 400)
	j := gatedJudgement{Role: "Fixer", Checks: []struct{ Command, Result, Decision, Reason, CallID string }{
		{Command: "echo `date` ``x``", Result: "pass", CallID: "mcall_1"},
		{Command: "`make check`", Decision: "no", Reason: "exit\n1"},
		{Command: "`" + long, Result: strings.Repeat("é", 400), Reason: strings.Repeat("r", 400), CallID: strings.Repeat("i", 400)},
	}}
	want := "- **Fixer** — not backed\n" +
		"  - ``` echo `date` ``x`` ``` — **pass** (`mcall_1`)\n" +
		"  - `` `make check` `` — **no** — exit 1\n" +
		"  - `` `" + strings.Repeat("ü", 298) + "… `` — **" + strings.Repeat("é", 299) + "…** — " + strings.Repeat("r", 299) + "… (`" + strings.Repeat("i", 299) + "…`)\n"
	if got := checksRow(j); got != want {
		t.Errorf("row\n%s\nwant\n%s", got, want)
	}
	// A Run whose every check is a run of backticks fences to over
	// checksRowsMax: it is left out, newest or not, and said to be.
	ticks := strings.Repeat("`", 400)
	var c []struct{ Command, Result, Decision, Reason, CallID string }
	for range 31 {
		c = append(c, struct{ Command, Result, Decision, Reason, CallID string }{ticks, ticks, "", ticks, ticks})
	}
	row := checksRow(gatedJudgement{Role: "Fixer", Checks: c})
	if n := utf8.RuneCountInString(row); n <= checksRowsMax {
		t.Fatalf("the backtick row is %d characters, within checksRowsMax", n)
	}
	if from := newestThatFit([]string{"- **Implementer** — backed\n", row}, checksRowsMax); from != 2 {
		t.Errorf("%d rows are left out, want both", from)
	}
}

// The most gated Runs an attempt can have (an implementer, FixRoundsPerPR's
// 50 fixers and a simplifier) still fit a pull request body beside a task's
// description and the test gate's section: 40 of them waived, the newest
// with a reason of about 1 MiB, the rest reporting the most checks with long
// commands and reasons. Each waiver's reason is clipped, the newest waivers
// and Runs are shown, and how many earlier ones are left out is said.
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
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_cbt',$1,'prj_cb','wi_cb',1,'test','completed')`,
		`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, heads, decision, decision_note, decided_at)
			SELECT $1, 'wi_cb', 'run_cbt', g, 'Criterion ' || g || ': ' || repeat('the page shows it ', 10),
				CASE WHEN g = 6 THEN 'blocked' ELSE 'pass' END, '{"api":"0123456789abcdef"}',
				CASE WHEN g = 6 THEN 'waived' END, CASE WHEN g = 6 THEN 'Checked on staging by hand.' END,
				CASE WHEN g = 6 THEN now() END
			FROM generate_series(1, 6) g`,
		`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload)
			SELECT 'ev_cb' || r.id, $1, 'run.checks.judged', r.id, 'wi_cb', 'system', 'dude', 'test',
				CASE WHEN r.id < 'run_cb40' THEN jsonb_build_object('turn', 1, 'verified', true, 'waived', true, 'checks', '[]'::jsonb,
					'message', repeat('ação ', CASE r.id WHEN 'run_cb39' THEN 149000 ELSE 2000 END))
				ELSE jsonb_build_object('turn', 1, 'verified', true, 'checks', (SELECT jsonb_agg(jsonb_build_object(
					'command', repeat('c', 5000), 'result', 'pass', 'decision', 'yes', 'reason', repeat('r ', 2500), 'callId', 'mcall_x'))
					FROM generate_series(1, 30))) END
			FROM runs r WHERE r.task_id = 'wi_cb' AND r.phase <> 'test'`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	var section string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		var err error
		section, _, err = verificationText(ctx, tx, &State{TaskID: "wi_cb", ProjectID: "prj_cb", Attempt: 1,
			Heads: map[string]string{"api": "0123456789abcdef"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A task's description as people write one: a few paragraphs and its criteria.
	goal := strings.Repeat("The settings page lets an admin choose the judgement tier, and the task page shows why checks were sent back. ", 30)
	var criteria []string
	for i := 1; i <= 6; i++ {
		criteria = append(criteria, fmt.Sprintf("Criterion %d: %s", i, strings.Repeat("the page shows it ", 10)))
	}
	body := replaceVerification(prBody(goal, criteria, "", "## Verification\nNot tested yet."), section)
	if n := utf8.RuneCountInString(body); n > 65_536 {
		t.Fatalf("the pull request body is %d characters, more than GitHub's 65,536", n)
	}
	if !strings.Contains(section, "## Verification\nTested at `0123456`.") {
		t.Fatalf("no test gate section in:\n%.400s", section)
	}
	_, got, _ := strings.Cut(section, "\n\n### Checks\n")
	head, rows, _ := strings.Cut(got, "\n\n")
	// Every reason is clipped to checksFieldMax characters; 25 lines of 310 fit checksWaiversMax.
	reason := strings.Repeat("ação ", 59) + "ação…"
	want := "Checks were waived by a person:\n15 earlier waivers omitted.\n" + strings.Repeat("- Fixer: "+reason+"\n", 25)
	if head+"\n" != want {
		t.Errorf("headline\n%.1000q\nwant\n%.1000q", head, want)
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
