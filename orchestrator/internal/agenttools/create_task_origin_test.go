package agenttools_test

import (
	"context"
	"testing"
)

// create_task from the agent of a chat-routed task snapshots the parent's
// thread beside {provider, via, from}; from a task with no chat thread, or
// an automation's run, nothing names a thread.
func TestCreateTaskFromAChatTasksAgentCarriesItsThread(t *testing.T) {
	f := setup(t)
	mustExec(t, f.owner, `UPDATE tasks SET origin = '{"provider":"slack","workspaceId":"T1","conversationId":"C1","conversationName":"#support",
		"conversationKind":"public","threadId":"1.5","permalink":"https://acme.slack.example.com/p1","thread":[{"text":"quoted"}]}' WHERE id = $1`, f.item)
	token := f.run(t, "run_impl_"+f.org, "implementer", "running")
	if status, out := f.post(t, token, "create_task", `{"title":"Follow up","goal":"Measure the export once the fix is in."}`); status != 200 {
		t.Fatalf("create_task: %d %v", status, out)
	}
	var origin string
	if err := f.owner.QueryRow(context.Background(), `SELECT origin::text FROM tasks WHERE project_id = $1 AND title = 'Follow up'`, f.project).
		Scan(&origin); err != nil {
		t.Fatal(err)
	}
	want := `{"via": "task", "from": "` + f.item + `", "notify": {"provider": "slack", "threadId": "1.5", "workspaceId": "T1", "conversationId": "C1", ` +
		`"conversationKind": "public", "conversationName": "#support"}, "provider": "slack"}`
	if origin != want {
		t.Fatalf("the child's origin\n%s\nwant\n%s", origin, want)
	}

	// Its parent made in dude: the provider is inherited from nowhere, nothing at all.
	mustExec(t, f.owner, `UPDATE tasks SET origin = NULL WHERE id = $1`, f.item)
	if status, out := f.post(t, token, "create_task", `{"title":"Web follow up","goal":"Measure the export once the fix is in."}`); status != 200 {
		t.Fatalf("create_task: %d %v", status, out)
	}
	var none *string
	if err := f.owner.QueryRow(context.Background(), `SELECT origin::text FROM tasks WHERE project_id = $1 AND title = 'Web follow up'`, f.project).
		Scan(&none); err != nil || none != nil {
		t.Fatalf("a web task's child has origin %v (%v)", none, err)
	}
}
