package delivery

import (
	"context"
	"testing"

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
