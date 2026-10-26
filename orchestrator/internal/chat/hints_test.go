package chat_test

import (
	"strings"
	"testing"
)

func TestChatAHiddenProjectIsNeverRoutedTo(t *testing.T) {
	w := setup(t)
	sec := "prj_sec_" + w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Secret', 'sec', 'SEC')`, sec, w.org)
	w.exec(`INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id, conversation_id, project_id)
		VALUES ($1, 'slack', 'T1', 'C1', $2), ($1, 'slack', 'T1', 'C1', $3)`, w.org, sec, w.project)
	w.exec(`INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, w.org, sec)
	// An earlier request here made a task in it, and one in BIL.
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_sec_'||$1, $1, $2, 1, 'Old keys'),
		('wi_bil_'||$1, $1, $3, 90, 'Old export')`, w.org, sec, w.project)
	w.exec(`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id,
		external_user_id, route, result_kind, result_id) VALUES ('chr_sec', $1, 'slack', 'ev_sec', 'T1', 'C1', 'U2', 'task', 'task', 'wi_sec_'||$1),
		('chr_bil', $1, 'slack', 'ev_bil', 'T1', 'C1', 'U2', 'task', 'task', 'wi_bil_'||$1)`, w.org)
	// The model names it anyway: refused as an invented project is.
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.95, "reasoning": "the keys", "title": "Rotate keys",
		"goal": "Rotate the vault keys every month.", "project": "SEC"}))
	w.mention("Márcia", "C1", "", "file a task to rotate the SEC keys")
	sent := w.model.last()
	if strings.Contains(sent, "SEC (Secret)") || !strings.Contains(sent, "BIL (Billing)") {
		t.Fatalf("the hidden project is in the routing input: %s", sent)
	}
	if !strings.Contains(sent, `usually about\n\nBIL (a hint`) || !strings.Contains(sent, `earlier requests went\n\n- BIL: 1 of the last 2\n`) {
		t.Fatalf("hints: %s", sent)
	}
	// SEC is named nowhere but in the message: not a project, not either hint.
	if strings.Contains(sent, "- SEC:") || strings.Contains(sent, "Secret") {
		t.Fatalf("the hidden project is in the routing input: %s", sent)
	}
	got := w.requests()
	last := got[len(got)-1]
	if len(got) != 3 || last.AskedRoute != "task" || !strings.Contains(last.Reasoning, `project "SEC" is not one of the person's`) {
		t.Fatalf("requests %+v", got)
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE project_id = $1`, sec); n != 1 {
		t.Fatal("a task was filed in the hidden project")
	}
	if n := w.count(`SELECT count(*) FROM session_projects WHERE project_id = $1`, sec); n != 0 {
		t.Fatal("a session was linked to the hidden project")
	}
}

func TestChatRoutingIsToldWhereTheConversationsRequestsWent(t *testing.T) {
	w := setup(t)
	brainstorm := func(project any) string {
		return answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "r", "title": "t", "project": project})
	}
	for i := range 5 {
		var project any = "BIL"
		if i == 2 {
			project = nil
		}
		w.model.queue(brainstorm(project))
		w.mention("Márcia", "C1", "", "look at the exports again")
	}
	// Elsewhere: another conversation's requests are not this one's.
	w.model.queue(brainstorm("BIL"))
	w.mention("Márcia", "G1", "", "and here")

	w.model.queue(brainstorm(nil))
	w.mention("Márcia", "C1", "", "what's next?")
	sent := w.model.last()
	if !strings.Contains(sent, `## Where this conversation's earlier requests went\n\n- BIL: 4 of the last 5\n`) {
		t.Fatalf("no learned hint: %s", sent)
	}
	if strings.Contains(sent, "This channel is usually about") {
		t.Fatalf("an explicit hint nobody set: %s", sent)
	}
}

func TestChatRoutingIsToldWhatTheChannelIsUsuallyAboutButTheMessageDecides(t *testing.T) {
	w := setup(t)
	abs := "prj_abs_" + w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Acme Backend', 'abs', 'ABS')`, abs, w.org)
	w.exec(`INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id, conversation_id, project_id)
		VALUES ($1, 'slack', 'T1', 'C1', $2)`, w.org, w.project)
	// The model reads the message as about ABS despite the BIL hint: ABS it is.
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "the message names ABS",
		"title": "ABS logins", "project": "ABS"}))
	w.mention("Márcia", "C1", "", "the ABS login page is broken")
	sent := w.model.last()
	if !strings.Contains(sent, `## This channel is usually about\n\nBIL (a hint, not a rule)`) {
		t.Fatalf("no explicit hint: %s", sent)
	}
	if !strings.Contains(sent, "The hints are a prior, not a rule") {
		t.Fatalf("the instructions do not say hints are a prior: %s", sent)
	}
	session := w.requests()[0].ResultID
	if n := w.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, session, abs); n != 1 {
		t.Fatal("the session is not linked to ABS")
	}
	if n := w.count(`SELECT count(*) FROM session_projects WHERE session_id = $1`, session); n != 1 {
		t.Fatalf("%d projects linked", n)
	}
}

func TestChatAnAdminSetsWhatTheChannelIsUsuallyAboutFromChat(t *testing.T) {
	w := setup(t)
	abs := "prj_abs_" + w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Acme Backend', 'abs', 'ABS')`, abs, w.org)
	set := func(add, remove []string) string {
		return answer(map[string]any{"route": "channel_projects", "confidence": 0.95, "reasoning": "they say what the channel is about",
			"channel_projects": map[string]any{"add": add, "remove": remove}})
	}
	usual := func() string {
		return w.one(`SELECT string_agg(p.key_prefix, ',' ORDER BY p.key_prefix) FROM chat_channel_projects c JOIN projects p ON p.id = c.project_id
			WHERE c.organization_id = $1 AND c.conversation_id = 'C1'`, w.org)
	}
	// Eli is a member, not an admin: refused privately, nothing changes.
	w.model.queue(set([]string{"ABS"}, nil))
	w.mention("Eli", "C1", "", "this channel is usually about ABS")
	if got := usual(); got != "" {
		t.Fatalf("a member changed it: %q", got)
	}
	eph := w.fake.Ephemerals()
	if len(eph) != 1 || !strings.Contains(eph[0].Text, "Only Acme's admins") {
		t.Fatalf("ephemerals %+v", eph)
	}
	if r := w.requests(); r[0].Route != "channel_projects" || r[0].ResultKind != "" {
		t.Fatalf("requests %+v", r)
	}

	w.model.queue(set([]string{"ABS", "BIL"}, nil))
	w.mention("Márcia", "C1", "", "this channel is usually about ABS and BIL")
	if got := usual(); got != "ABS,BIL" {
		t.Fatalf("usual %q", got)
	}
	if reply := lastBot(t, w.fake); !strings.Contains(reply.Text, "Done: added ABS, BIL. #support is now usually about ABS, BIL.") {
		t.Fatalf("reply %+v", reply)
	}
	w.model.queue(set(nil, []string{"BIL"}))
	w.mention("Márcia", "C1", "", "this channel is not about BIL anymore")
	if got := usual(); got != "ABS" {
		t.Fatalf("usual %q", got)
	}
	if reply := lastBot(t, w.fake); !strings.Contains(reply.Text, "Done: removed BIL. #support is now usually about ABS.") {
		t.Fatalf("reply %+v", reply)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat_channel.projects_set'`, w.org); n != 2 {
		t.Fatalf("%d ledger events", n)
	}
	// A project dude does not know is refused as an invented one is: nothing changes.
	w.model.queue(set([]string{"NOPE"}, nil))
	w.mention("Márcia", "C1", "", "this channel is usually about NOPE")
	if got := usual(); got != "ABS" {
		t.Fatalf("usual %q", got)
	}
}
