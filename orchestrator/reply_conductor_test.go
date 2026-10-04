package orchestrator_test

// The conductor's reply on a pull request (reply_on_pull_request), called
// with its Run's token as its agent would, against the fake GitHub.

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// reply_on_pull_request: a reply quoting an issue comment on the
// conversation, and one in a line comment's thread, each as dude's login
// with its marker, each a chat.message from the conductor linking to it;
// neither wakes anything when the pull request is read again.
func TestTheConductorRepliesOnThePullRequest(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	issue := w.gh.Comment(1, "alice", "@dude why greet()?\nIt reads oddly.")
	line := w.gh.LineComment(1, "bob", "FACTORY.md", "@dude is this needed?")
	w.until("both messages", func() bool { w.sync(); return w.mentions(task) == 2 })

	out := w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":"Because the task says greet.","in_reply_to":"issue-comment-%d"}`, issue))
	if out["commentId"] == "" || !strings.Contains(fmt.Sprint(out["url"]), "/pull/1#issuecomment-") {
		t.Errorf("the reply: %v", out)
	}
	posted := w.gh.Pull(1).Comments
	last := posted[len(posted)-1]
	if last.Author != fakegithub.Login || last.Body != "> @alice: @dude why greet()?\n\nBecause the task says greet.\n\n"+forge.ReplyMarker {
		t.Errorf("the comment posted: %s %q", last.Author, last.Body)
	}

	w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"target#1","text":"Yes: the factory reads it.","in_reply_to":"line-comment-%d"}`, line))
	lines := w.gh.Pull(1).LineComments
	reply := lines[len(lines)-1]
	if reply.InReplyTo != line || reply.Author != fakegithub.Login || !strings.HasPrefix(reply.Body, "Yes: the factory reads it.") {
		t.Errorf("the thread's reply: %+v", reply)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message' AND payload->>'by' = 'conductor'
		AND payload->'github'->>'url' LIKE 'https://github.test/acme/target/pull/1#%'`, task); n != 2 {
		t.Errorf("%d replies recorded in Chat", n)
	}

	// Read again, the replies are dude's own: no message, no fixer, no wake.
	before := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task)
	w.syncs(5)
	if n := w.mentions(task); n != 2 {
		t.Errorf("the replies came back as %d messages", n-2)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers for the conductor's replies", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != before {
		t.Errorf("the replies recorded %d reasons to wake", n-before)
	}
}

// reply_on_pull_request refuses another task's pull request, and says when
// GitHub refuses, posting and recording nothing.
func TestTheConductorRepliesOnlyOnItsOwnPullRequests(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	other := w.task()
	w.deliver(other)
	w.until("the other's pull request", func() bool { return len(w.gh.Pulls()) == 2 })
	w.gh.Comment(1, "alice", "@dude hello")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })

	w.refused(task, "reply_on_pull_request", `{"pr":"2","text":"Not mine."}`, "not a pull request of your task")
	w.refused(task, "reply_on_pull_request", `{"pr":"target#2","text":"Not mine."}`, "not a pull request of your task")
	if n := len(w.gh.Pull(2).Comments); n != 0 {
		t.Errorf("%d comments on another task's pull request", n)
	}

	w.gh.LockConversation(1)
	before := len(w.gh.Pull(1).Comments)
	w.refused(task, "reply_on_pull_request", `{"pr":"1","text":"Hello."}`, "GitHub did not take the reply")
	if n := len(w.gh.Pull(1).Comments); n != before {
		t.Errorf("a comment was posted on a locked pull request")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message' AND payload->>'by' = 'conductor'`, task); n != 0 {
		t.Errorf("%d replies recorded though GitHub refused", n)
	}
}

// A conductor replaced after its call was authenticated posts nothing.
func TestASupersededConductorRepliesNowhere(t *testing.T) {
	w := conducting(t)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Forges: forge.Resolver{DB: w.app},
		BeforeCall: func(tool string) {
			if tool == "reply_on_pull_request" {
				once.Do(func() { close(paused); <-resume })
			}
		}}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	task := w.reviewing()
	w.gh.Comment(1, "alice", "@dude hello")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })
	oldSpec := w.conductorSpecOf(task)
	first, _, _ := w.conductor(task)
	done := make(chan string, 1)
	go func() {
		status, body := w.callTool(tools.URL, oldSpec, "reply_on_pull_request", `{"pr":"1","text":"Hi."}`)
		done <- fmt.Sprint(status, " ", body)
	}()
	<-paused
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'stopped' WHERE id = $1`, first)
	if status, out := w.chat(task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	before := len(w.gh.Pull(1).Comments)
	close(resume)
	if r := <-done; !strings.HasPrefix(r, "422 ") || !strings.Contains(r, "no longer this task's conductor") {
		t.Fatalf("the replaced conductor's reply: %s", r)
	}
	if n := len(w.gh.Pull(1).Comments); n != before {
		t.Errorf("the replaced conductor posted")
	}
}
