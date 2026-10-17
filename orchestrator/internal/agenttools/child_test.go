package agenttools_test

import "testing"

func TestChildToolsRefuseWorkflowAndRecursion(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_report_child", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}' WHERE id='run_report_child'`)
	for _, name := range []string{"start_phase", "restart_run", "publish", "update_task", "ask_person", "create_task"} {
		status, _ := f.post(t, token, name, `{}`)
		if status != 422 && status != 404 {
			t.Fatalf("child tool %s returned %d", name, status)
		}
	}
	if status, _ := f.post(t, token, "list_tasks", `{}`); status != 200 {
		t.Fatalf("report read returned %d", status)
	}
}
