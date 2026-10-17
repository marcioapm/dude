package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func TestChildAPIControls(t *testing.T) {
	ctx := context.Background()
	if err := dbtest.Reachable(ctx); err != nil {
		t.Fatal(err)
	}
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_api_child',$1,'P','p','P')`, org)
	exec(`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_api_child',$1,'prj_api_child',1,'T','G','running')`, org)
	exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('run_api_parent',$1,'prj_api_child','wi_api_child',1,'conductor','running')`, org)
	exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_api_child',$1,'wi_api_child','task.delivery','api-child','waiting','awaitPullRequest','{"attempt":1,"heads":{"repo":"same"}}')`, org)
	store := &delivery.Store{DB: app}
	in := delivery.StartChildInput{Parent: delivery.ChildParent{Kind: delivery.ChildParentTaskConductor, Run: delivery.RunRef{Org: org, ProjectID: "prj_api_child", TaskID: "wi_api_child", RunID: "run_api_parent"}}, Profile: "investigator", Key: "first"}
	id, err := store.StartChild(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Key = "sibling"
	sibling, err := store.StartChild(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: app}
	snapshot := func() string {
		var raw string
		if err := owner.QueryRow(ctx, `SELECT jsonb_build_object('t',to_jsonb(t),'w',to_jsonb(w),'s',(SELECT to_jsonb(r) FROM runs r WHERE r.id=$1))::text FROM tasks t JOIN workflow_runs w ON w.task_id=t.id WHERE t.id='wi_api_child'`, sibling).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	r.SetPathValue("id", id)
	if err := s.abort(httptest.NewRecorder(), r, org); err != nil {
		t.Fatal(err)
	}
	var fresh string
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		r.SetPathValue("id", id)
		r.Header.Set("Idempotency-Key", "stable")
		out := httptest.NewRecorder()
		if err := s.restartRun(out, r, org); err != nil {
			t.Fatal(err)
		}
		var got struct{ RunID string }
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.RunID == id || got.RunID == "" || i > 0 && got.RunID != fresh {
			t.Fatalf("replacement %s", got.RunID)
		}
		fresh = got.RunID
	}
	for _, body := range []string{`{"note":"not ignored"}`, `{"tier":"not ignored"}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.SetPathValue("id", fresh)
		r.Header.Set("Idempotency-Key", "override")
		if err := s.restartRun(httptest.NewRecorder(), r, org); err == nil {
			t.Fatal("override silently ignored")
		}
	}
	if snapshot() != before {
		t.Fatal("API child controls changed sibling/workflow/task")
	}
}
