package orchestrator_test

// The conductor's reply on a pull request (reply_on_pull_request), called
// with its Run's token as its agent would, against the fake GitHub.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// A reply showing Markdown with a code example inside a longer fence is
// still dude's own when read back, under a token that comments as a person:
// neither a message from its quoted @dude nor fixer feedback.
func TestAReplyWithANestedCodeExampleIsStillDudesOwn(t *testing.T) {
	for _, c := range []struct{ name, text, shown string }{
		{"in a longer fence", "Open a Go example in the README with:\n\n````markdown\n```go\n````\n\nand please close it the same way.", "````markdown\n```go\n````"},
		{"in an HTML pre block", "Use a preformatted HTML block:\n\n<pre>\n````markdown\n```go\n</pre>", "<pre>\n````markdown\n```go\n</pre>"},
	} {
		t.Run(c.name, func(t *testing.T) { aReplyIsStillDudesOwn(t, c.text, c.shown) })
	}
}

func aReplyIsStillDudesOwn(t *testing.T, text, shown string) {
	w := conducting(t)
	// The token comments as a person, not a bot: only the marker says the
	// reply is dude's.
	w.gh.Poster = "pat-owner"
	task := w.reviewing()
	issue := w.gh.Comment(1, "alice", "@dude how should the README show greet()?")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })

	args, _ := json.Marshal(map[string]string{"pr": "1", "text": text, "in_reply_to": fmt.Sprintf("issue-comment-%d", issue)})
	w.must(task, "reply_on_pull_request", string(args))
	posted := w.gh.Pull(1).Comments
	if last := posted[len(posted)-1]; last.Author != "pat-owner" || !strings.Contains(last.Body, shown) {
		t.Fatalf("the reply posted by %s: %q", last.Author, last.Body)
	}
	w.syncs(5)
	if n := w.mentions(task); n != 1 {
		t.Errorf("the reply came back as %d messages", n-1)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers for the conductor's reply", n)
	}
	if n := w.ownComments(task); n != 1 {
		t.Errorf("%d comments recorded as dude's own, want the reply", n)
	}
}

// A reply to a comment edited into a mention quotes the words it was
// answered for, not the ones first recorded; an edit that addresses nobody
// starts no fixer.
func TestAReplyToAnEditedMentionQuotesItsEditedWords(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	id := w.gh.Comment(1, "alice", "LGTM")
	w.syncs(2)
	w.gh.EditComment(1, id, "@dude why this choice?")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })

	w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":"Because.","in_reply_to":"issue-comment-%d"}`, id))
	posted := w.gh.Pull(1).Comments
	if last := posted[len(posted)-1]; last.Body != "> @alice: @dude why this choice?\n\nBecause.\n\n"+forge.ReplyMarker {
		t.Errorf("the reply quotes %q", last.Body)
	}

	other := w.gh.Comment(1, "bob", "Thanks!")
	w.syncs(2)
	w.gh.EditComment(1, other, "Please rename greet() to hello().")
	w.syncs(3)
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers for edited comments", n)
	}
	if n := w.mentions(task); n != 1 {
		t.Errorf("%d messages, want the one mention", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.comment_edited'`, task); n != 2 {
		t.Errorf("%d edits recorded, want one per edited comment", n)
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
	if n := w.replies(task); n != 0 {
		t.Errorf("%d replies recorded though GitHub refused", n)
	}
}

// replies is how many of the conductor's replies on a pull request were
// recorded in Chat.
func (w *world) replies(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message' AND payload->>'by' = 'conductor'`, task)
}

// A reply answers a comment on the pull request it is posted on: an id
// nobody left, or a comment on the task's other pull request, is refused,
// and nothing is posted or recorded.
func TestTheConductorRepliesOnlyToTheCommentsOfThatPullRequest(t *testing.T) {
	w := conducting(t)
	task := w.reviewingTwo()
	w.gh.Comment(1, "alice", "@dude hello")
	webComment := w.web.Comment(1, "alice", "@dude and here?")
	w.until("both messages", func() bool { w.sync(); return w.mentions(task) == 2 })

	before, beforeWeb := len(w.gh.Pull(1).Comments), len(w.web.Pull(1).Comments)
	w.refused(task, "reply_on_pull_request", `{"pr":"target#1","text":"Hello.","in_reply_to":"issue-comment-999999"}`,
		"issue-comment-999999 is not a comment on target#1")
	w.refused(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"target#1","text":"Hello.","in_reply_to":"issue-comment-%d"}`, webComment),
		fmt.Sprintf("issue-comment-%d is not a comment on target#1", webComment))
	if n, m := len(w.gh.Pull(1).Comments), len(w.web.Pull(1).Comments); n != before || m != beforeWeb {
		t.Errorf("refused replies posted %d comments", n-before+m-beforeWeb)
	}
	if n := w.replies(task); n != 0 {
		t.Errorf("%d replies recorded for refused calls", n)
	}
	// The same comment, on the pull request it was left on, is answered.
	w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"web#1","text":"Yes.","in_reply_to":"issue-comment-%d"}`, webComment))
	if n := w.replies(task); n != 1 {
		t.Errorf("%d replies recorded", n)
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
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'terminated' WHERE id = $1`, first)
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

// The owner answers the conductor's question while the conductor's reply
// waits on GitHub: the answer holds no lock the post does, so it is
// answered and committed while GitHub is still holding the post, and the
// reply, once GitHub answers, is posted and recorded in Chat. The held post
// is the ordering; the 5 s bound only ends the test, well inside GitHub's
// 15 s client timeout, so a reply timing out cannot pass for the answer.
func TestAnAnswerWhileTheConductorsReplyPostsDoesNotWaitForGitHub(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	w.gh.Comment(1, "alice", "@dude why greet()?")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", `{"question":"Alice asks why greet(): may I say the task asks for it?"}`)
	q := w.conductorQuestion(task)
	if q == "" {
		t.Fatal("the conductor's question was not recorded")
	}
	spec := w.conductorSpecOf(task)
	replied, release := w.replyHeldOnGitHub(spec, `{"pr":"1","text":"The task asks for it."}`)
	answered := make(chan int, 1)
	go func() {
		status, _ := w.callAs(ana, "/internal/questions/"+q+"/answer", map[string]any{"text": "Yes, say so."})
		answered <- status
	}()
	select {
	case status := <-answered:
		if status != 200 {
			t.Errorf("the owner's answer during the post: %d, want 200", status)
		}
		if n := w.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'answered' AND answer = 'Yes, say so.'`, q); n != 1 {
			t.Errorf("%d committed answers while GitHub holds the post, want 1", n)
		}
		select {
		case r := <-replied:
			t.Fatalf("the reply finished while GitHub held the post: %d %s", r.status, r.body)
		default:
		}
	case <-time.After(5 * time.Second):
		t.Error("the owner's answer waited on the conductor's GitHub post")
		release()
		if status := <-answered; status != 200 {
			t.Errorf("the owner's answer after the post: %d, want 200", status)
		}
	}
	release()
	if r := <-replied; r.status != 200 {
		t.Fatalf("the reply: %d %s", r.status, r.body)
	}
	if n := w.replies(task); n != 1 {
		t.Errorf("%d replies recorded in Chat, want 1", n)
	}
	posted := w.gh.Pull(1).Comments
	if last := posted[len(posted)-1]; !strings.HasPrefix(last.Body, "The task asks for it.") {
		t.Errorf("the comment posted: %q", last.Body)
	}
}

// heldPost holds the fake GitHub's next comment post until the returned
// release; held is closed once it arrives.
func (w *world) heldPost() (held <-chan struct{}, release func()) {
	g := newGate()
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
				g.wait()
			}
			return 0
		}
	})
	w.t.Cleanup(g.release)
	return g.held, g.release
}

// toolResult is a tool call's HTTP status and body.
type toolResult struct {
	status int
	body   string
}

// replyHeldOnGitHub calls reply_on_pull_request as the conductor whose spec
// is given and returns once its comment post has reached the fake GitHub,
// which holds it until release.
func (w *world) replyHeldOnGitHub(spec, args string) (replied <-chan toolResult, release func()) {
	w.t.Helper()
	held, release := w.heldPost()
	out := make(chan toolResult, 1)
	go func() {
		status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "reply_on_pull_request", args)
		out <- toolResult{status, body}
	}()
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		w.t.Fatal("the reply was never posted")
	}
	return out, release
}

// A conductor replaced while its reply waits on GitHub did post it: the
// call answers with the comment, and Chat records it on the Run that
// posted it, not on its successor.
func TestAConductorReplacedWhileItsReplyPostsStillRecordsIt(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	w.gh.Comment(1, "alice", "@dude hello")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })
	spec := w.conductorSpecOf(task)
	first, _, _ := w.conductor(task)
	replied, release := w.replyHeldOnGitHub(spec, `{"pr":"1","text":"Hi."}`)
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'terminated' WHERE id = $1`, first)
	if status, out := w.chat(task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	if next, _, _ := w.conductor(task); next == first {
		t.Fatal("the conductor was not replaced")
	}
	release()
	r := <-replied
	var out map[string]any
	_ = json.Unmarshal([]byte(r.body), &out)
	if r.status != 200 || out["commentId"] == "" {
		t.Fatalf("the replaced conductor's reply: %d %s, want 200 with the comment", r.status, r.body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message' AND payload->>'by' = 'conductor'
		AND run_id = $2 AND payload->'github'->>'feedbackId' = $3`, task, first, out["commentId"]); n != 1 {
		t.Errorf("%d replies recorded on the conductor that posted it, want 1", n)
	}
}
