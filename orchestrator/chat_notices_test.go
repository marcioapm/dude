package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// A task filed from a chat-born session's card is that thread's work: as
// its delivery starts, opens its pull request and is merged, dude says so
// in the thread the session came from, once each and in order. A task
// filed from a session started in dude says nothing anywhere.
func TestOriginATaskFiledFromAChatBornSessionPostsItsNoticesInTheSessionsThread(t *testing.T) {
	s := newSessionWorld(t)
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme")
	fake.AddConversation(fakeslack.Conversation{ID: "C0SUPPORT", Team: "T1", Name: "support", Kind: "public", Members: []string{"UBT1", "U1"}})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	mustExec(t, s.owner, `INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name,
		workspace_domain, bot_user_id, bot_token) VALUES ($1, $2, 'slack', 'T1', 'Acme', 'acme', 'UBT1', $3)`, "chi_"+s.org, s.org, fake.Token("T1"))
	svc := &chat.Service{DB: s.app, PublicURL: "https://dude.example.com", Providers: chat.Registry{"slack": slack.New(slack.Config{
		ClientID: "cid", ClientSecret: "csecret", APIURL: srv.URL + "/api"})}}
	post := func() {
		t.Helper()
		if _, err := svc.PostNotices(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	root := fake.Post("C0SUPPORT", "", "U1", "<@UBT1> the nightly export times out")
	session := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET origin = jsonb_build_object('provider', 'slack', 'workspaceId', 'T1', 'conversationId', 'C0SUPPORT',
		'conversationName', '#support', 'conversationKind', 'public', 'threadId', $2::text) WHERE id = $1`, session, root)
	task, _ := fileFromSession(s, session)
	if o := s.one(`SELECT concat_ws('|', origin->>'provider', origin->>'via', origin->>'from') FROM tasks WHERE id = $1`, task); o != "slack|session|"+session {
		t.Fatalf("the filed task's origin %s", o)
	}
	web := s.session()
	fileFromSession(s, web)
	// fileFromSession finds its task by title: the web session's is the newer.
	webTask := s.one(`SELECT id FROM tasks WHERE project_id = $1 AND title = 'Fix the export timeout' AND id <> $2`, s.project, task)

	// The filed task delivered to its pull request and merged, as GitHub
	// and the reconciler do; the web session's task moved alike.
	s.deliver(task)
	s.until("a pull request", func() bool { return len(s.gh.Pulls()) == 1 })
	s.gh.Merge(1)
	s.until("the task to be done", func() bool {
		_, _ = s.prs.Reconcile(context.Background(), 0)
		return s.taskStatus(task) == "done"
	})
	s.markMergedDone(webTask)
	post()
	post()
	var got []string
	for _, m := range fake.BotMessages() {
		if m.Channel != "C0SUPPORT" || m.ThreadTS != root {
			t.Errorf("a notice outside the thread: %+v", m)
		}
		got = append(got, m.Text)
	}
	key := s.one(`SELECT p.key_prefix || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, task)
	want := []string{"Started " + key + " Fix the export timeout.", "Opened a pull request for " + key + " Fix the export timeout: ",
		"Done: " + key + " Fix the export timeout."}
	if len(got) != 3 || got[0] != want[0] || !strings.HasPrefix(got[1], want[1]) || !strings.Contains(got[1], "/pull/") || got[2] != want[2] {
		t.Fatalf("the thread got %q", got)
	}
	if n := s.count(`SELECT count(*) FROM chat_notices WHERE task_id = $1`, webTask); n != 0 {
		t.Errorf("%d notices for the task filed from a web session", n)
	}
}
