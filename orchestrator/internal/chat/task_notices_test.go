package chat_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// childOf makes a task in BIL as a task's agent makes one (create_task:
// InheritOrigin from the parent task, in the parent's organisation).
func (w *world) childOf(parent, title string) string {
	w.t.Helper()
	id := "wi_" + strings.ToLower(strings.ReplaceAll(title, " ", "")) + "_" + w.org
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(w.ctx, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
			VALUES ($1, $2, $3, (SELECT COALESCE(max(number), 0) + 1 FROM tasks WHERE project_id = $3), $4, 'received')`,
			id, w.org, w.project, title); err != nil {
			return err
		}
		return delivery.InheritOrigin(w.ctx, tx, "tasks", id, "", parent)
	}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// A task made by the agent of a chat-routed task, and one made by that
// task's agent in turn, tell the routed task's thread when they start and
// end. Their origin keeps {provider, via, from}, the parent's thread
// snapshotted beside it.
func TestChatNoticesOfTasksATasksAgentMadePostInTheParentsThread(t *testing.T) {
	w := setup(t)
	parent, root := w.routedTask()
	child := w.childOf(parent, "Follow up")
	grandchild := w.childOf(child, "Follow up again")
	if o := w.one(`SELECT concat_ws('|', origin->>'provider', origin->>'via', origin->>'from', origin->'notify'->>'conversationId',
		origin->'notify'->>'threadId') FROM tasks WHERE id = $1`, grandchild); o != "slack|task|"+child+"|C1|"+root {
		t.Fatalf("the grandchild's origin %s", o)
	}
	for _, task := range []string{child, grandchild} {
		w.move(task, "running", "planning with the conductor")
		w.move(task, "done", "pull requests merged")
	}
	w.postNotices()
	want := []string{"Started BIL-2 Follow up.", "Done: BIL-2 Follow up.", "Started BIL-3 Follow up again.", "Done: BIL-3 Follow up again."}
	if got := w.threadNotices("C1", root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the parent's thread got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The parent's thread is snapshotted at creation: the parent's origin
	// changing since does not move a child's notices.
	later := w.childOf(parent, "Made before")
	w.exec(`UPDATE tasks SET origin = origin || '{"threadId":"9.9"}' WHERE id = $1`, parent)
	w.move(later, "running", "planning with the conductor")
	if th := w.one(`SELECT thread_id FROM chat_notices WHERE task_id = $1`, later); th != root {
		t.Errorf("the child's notice is in thread %q, not %q", th, root)
	}
}

// A task filed from a chat-born session, whose agent makes a follow-up:
// the follow-up's notices go to the session's thread, resolved through the
// session when it was made.
func TestChatNoticesOfATaskMadeByASessionFiledTasksAgentPostInTheSessionsThread(t *testing.T) {
	w := setup(t)
	root, session := w.sessionMention()
	filed := "wi_filed_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status, origin) VALUES ($1, $2, $3, 50, 'Filed', 'running',
		jsonb_build_object('provider', 'slack', 'via', 'session', 'from', $4::text))`, filed, w.org, w.project, session)
	child := w.childOf(filed, "Session follow up")
	if o := w.one(`SELECT concat_ws('|', origin->>'via', origin->>'from', origin->'notify'->>'conversationId', origin->'notify'->>'threadId')
		FROM tasks WHERE id = $1`, child); o != "task|"+filed+"|C1|"+root {
		t.Fatalf("the child's origin %s", o)
	}
	// Only the routing keys are snapshotted: never the session's permalink,
	// who asked, its routing, or anything else its origin holds.
	if keys, all := w.one(`SELECT string_agg(k, ',' ORDER BY k) FROM tasks, jsonb_object_keys(origin->'notify') k WHERE id = $1`, child),
		w.one(`SELECT string_agg(k, ',' ORDER BY k) FROM sessions, jsonb_object_keys(origin) k WHERE id = $1`, session); keys !=
		"conversationId,conversationKind,conversationName,provider,threadId,topLevel,workspaceId" {
		t.Fatalf("notify holds %s (the session's origin holds %s)", keys, all)
	}
	w.move(child, "running", "planning with the conductor")
	w.postNotices()
	key := "BIL-" + w.one(`SELECT number::text FROM tasks WHERE id = $1`, child)
	if got := w.threadNotices("C1", root); !strings.Contains(strings.Join(got, "\n"), "Started "+key+" Session follow up.") {
		t.Fatalf("the session's thread got %q", got)
	}
}

// A task's agent's task whose parent has no chat thread (made in dude)
// says nothing, nor does one an automation made.
func TestChatNoticesNothingForAChildOfAWebTaskOrAnAutomationsTask(t *testing.T) {
	w := setup(t)
	web := "wi_web_" + w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES ($1, $2, $3, 9, 'From the web', 'running')`,
		web, w.org, w.project)
	child := w.childOf(web, "Web child")
	auto := "wi_auto_" + w.org
	w.exec(`INSERT INTO automations (id, organization_id, project_id, name, kind, owner_id, trigger) VALUES ($1, $2, $3, 'Nightly', 'agent', $4,
		'{"kind":"schedule","cron":"0 * * * *","tz":"UTC"}')`, "aut_"+w.org, w.org, w.project, w.people["Márcia"][0])
	w.exec(`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ($1, $2, $3, 'running', 'schedule')`,
		"arn_"+w.org, w.org, "aut_"+w.org)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status, created_by_automation_run_id)
		VALUES ($1, $2, $3, 20, 'From an automation', 'received', $4)`, auto, w.org, w.project, "arn_"+w.org)
	for _, task := range []string{child, auto} {
		w.move(task, "running", "planning with the conductor")
		w.move(task, "done", "pull requests merged")
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE organization_id = $1`, w.org); n != 0 {
		t.Errorf("%d notices recorded", n)
	}
	if n := w.postNotices(); n != 0 || len(w.fake.BotMessages()) != 0 {
		t.Errorf("posted %d: %+v", n, w.fake.BotMessages())
	}
}

// Another organisation's chat-routed task is never a parent's destination:
// not under row-level security, and not on a connection that bypasses it.
func TestChatNoticesNeverResolveAnotherOrganisationsTask(t *testing.T) {
	w := setup(t)
	other := dbtest.Org(t, w.owner)
	foreign := "wi_foreign_" + other
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Other', 'oth', 'OTH')`, "prj_"+other, other)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status, origin) VALUES ($1, $2, $3, 1, 'Theirs', 'running',
		'{"provider":"slack","workspaceId":"T1","conversationId":"C1","conversationName":"#support","threadId":"5.5"}')`,
		foreign, other, "prj_"+other)
	insert := func(tx pgx.Tx, id string) error {
		if _, err := tx.Exec(w.ctx, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
			VALUES ($1, $2, $3, (SELECT COALESCE(max(number), 0) + 1 FROM tasks WHERE project_id = $3), 'Mine', 'received')`, id, w.org, w.project); err != nil {
			return err
		}
		return delivery.InheritOrigin(w.ctx, tx, "tasks", id, "", foreign)
	}
	viaApp, viaOwner := "wi_rls_"+w.org, "wi_owner_"+w.org
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error { return insert(tx, viaApp) }); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(w.ctx, w.owner, func(tx pgx.Tx) error { return insert(tx, viaOwner) }); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE id = ANY($1) AND origin ? 'notify'`, []string{viaApp, viaOwner}); n != 0 {
		t.Fatalf("%d tasks took another organisation's thread", n)
	}
	for _, task := range []string{viaApp, viaOwner} {
		w.move(task, "running", "planning with the conductor")
	}
	if n := w.count(`SELECT count(*) FROM chat_notices WHERE organization_id = $1`, w.org); n != 0 {
		t.Errorf("%d notices recorded", n)
	}
}
