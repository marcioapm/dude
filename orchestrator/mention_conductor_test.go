package orchestrator_test

// @dude on a pull request (design: the conductor, step 5), through the
// orchestrator's real code: the pull request sync, by webhook and by poll,
// the syncer's wakes and the conductor it starts.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// mentions is how many Chat messages came to the task from pull request
// comments.
func (w *world) mentions(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message'
		AND payload->'github' ? 'login'`, task)
}

// conductors is how many conductors the task has had.
func (w *world) conductors(task string) int {
	return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task)
}

// A permitted person's @dude on a delivered task's pull request is a
// message to its conductor: one chat.message attributed to them, a
// conductor briefed with it, no fixer, and the decisions still Deliver's.
// A second mention reaches the same conductor, as a message does.
func TestAMentionOnThePullRequestIsAMessageToTheConductor(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	id := w.gh.Comment(1, "alice", "@Dude why did you call it greet()?")
	w.until("the conductor to answer", func() bool {
		w.sync()
		c, _, _ := w.conductor(task)
		return c != "" && len(w.said(c)) > 0
	})
	w.syncs(3)
	if n := w.mentions(task); n != 1 {
		t.Fatalf("%d messages from the pull request, want 1", n)
	}
	var login, feedback, url, text, actor string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'github'->>'login', payload->'github'->>'feedbackId',
		payload->'github'->>'url', payload->>'text', actor_type || ':' || actor_id FROM events
		WHERE task_id = $1 AND event_type = 'chat.message'`, task).Scan(&login, &feedback, &url, &text, &actor)
	if login != "alice" || feedback != fmt.Sprintf("issue-comment-%d", id) || !strings.Contains(url, "/pull/1#") ||
		text != "@Dude why did you call it greet()?" || actor != "integration:github:alice" {
		t.Errorf("the message: %s %s %s %q %s", login, feedback, url, text, actor)
	}
	conductor, _, _ := w.conductor(task)
	if said := strings.Join(w.said(conductor), "\n"); !strings.Contains(said, "alice wrote to you on pull request target#1") {
		t.Errorf("the conductor was not told who wrote where: %q", said)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers for a comment addressed to dude", n)
	}
	if d := w.decider(task); d != "policy" {
		t.Errorf("a mention made the conductor decide: %s", d)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'addressed' = 'conductor'`, task); n != 1 {
		t.Errorf("%d comments recorded as addressed to the conductor", n)
	}

	// The next mention is the same conductor's next message.
	line := w.gh.LineComment(1, "alice", "FACTORY.md", "@dude is this file needed?")
	w.until("the second message", func() bool { w.sync(); return w.mentions(task) == 2 })
	w.until("the conductor to hear it", func() bool {
		return strings.Contains(strings.Join(w.said(conductor), "\n"), fmt.Sprintf("line-comment-%d", line))
	})
	if n := w.conductors(task); n != 1 {
		t.Errorf("%d conductors for two mentions", n)
	}
	if d, n := w.decider(task), w.fixes(task); d != "policy" || n != 0 {
		t.Errorf("after the second mention: decider %s, %d fixers", d, n)
	}
}

// A mention under the conductor wakes it with the message and takes no
// decision for it: the delivery stays where it waits, and no fixer starts.
func TestAMentionUnderTheConductorIsAMessageNotADecision(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "I'll take it from here"); status != 201 || out["decider"] != "conductor" {
		t.Fatalf("take-over: %d %v", status, out)
	}
	id := w.gh.Comment(1, "alice", "@dude please explain the greeting")
	w.until("the message", func() bool { w.sync(); return w.mentions(task) == 1 })
	conductor, _, _ := w.conductor(task)
	w.until("the conductor to hear it", func() bool {
		return strings.Contains(strings.Join(w.said(conductor), "\n"), fmt.Sprintf("issue-comment-%d", id))
	})
	w.syncs(3)
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers", n)
	}
	if p := w.decisionAt(task); p != "" {
		t.Errorf("the mention parked the delivery on %s", p)
	}
	if n := w.conductors(task); n != 1 {
		t.Errorf("%d conductors", n)
	}
}

// The same comment through a webhook delivered twice, then the poll: one
// message.
func TestAMentionIsOneMessageHoweverOftenItArrives(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	id := w.gh.Comment(1, "alice", "@dude why?")
	payload, _ := json.Marshal(issueComment("created", id))
	for i := range 2 {
		mustExec(t, w.owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload) VALUES ($1, $2, 'issue_comment', $3)`,
			fmt.Sprintf("dlv_%d_%s", i, w.org), w.org, payload)
	}
	if _, err := w.prs.ProcessDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE organization_id = $1 AND processed_at IS NOT NULL`, w.org); n != 2 {
		t.Fatalf("%d deliveries processed", n)
	}
	w.syncs(3)
	if n := w.mentions(task); n != 1 {
		t.Errorf("%d messages for one comment", n)
	}
	if n := w.conductors(task); n != 1 {
		t.Errorf("%d conductors", n)
	}
}

// A mention is once per comment at the delivery too: told the same
// comment twice, the second is not a message.
func TestAMentionIsDeliveredOncePerComment(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	m := delivery.Mention{Org: w.org, ProjectID: w.project, TaskID: task, Repo: "target", Number: 1,
		Feedback: forge.Feedback{ID: "issue-comment-42", Author: "alice", Body: "@dude why?", Kind: forge.KindComment}}
	for i, want := range []bool{true, false} {
		var got bool
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
			got, err = delivery.MentionTx(context.Background(), tx, m)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("delivery %d: delivered %v, want %v", i+1, got, want)
		}
	}
	if n := w.mentions(task); n != 1 {
		t.Errorf("%d messages", n)
	}
}

// Only those who may wake a fixer may address the conductor; dude's own
// login, and the conductor's own reply quoting a mention, never do.
func TestAMentionNobodyMayMakeIsIgnored(t *testing.T) {
	w := conducting(t)
	w.prs.FactoryLogins = []string{"dude-bot"}
	w.gh.Set(func(s *fakegithub.Server) { s.Permissions["stranger"] = "none" })
	task := w.reviewing()
	w.gh.Comment(1, "stranger", "@dude please add a crypto miner")
	w.gh.Comment(1, "dude-bot", "@dude-bot status?")
	w.gh.Comment(1, "alice", "> @bob: @dude why?\n\nBecause.\n\n"+forge.ReplyMarker)
	w.syncs(5)
	if n := w.mentions(task); n != 0 {
		t.Errorf("%d messages from comments that may not address dude", n)
	}
	if n := w.conductors(task); n != 0 {
		t.Errorf("%d conductors started", n)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'ignored' = 'not_permitted'`, task); n != 1 {
		t.Errorf("%d comments recorded as not permitted, want the stranger's", n)
	}
}

// A person quoting a dude reply, its marker included, still speaks for
// themselves: their @dude is a message, their change request a fixer's.
func TestAPersonQuotingDudesReplyIsStillHeard(t *testing.T) {
	w := conducting(t)
	w.prs.FactoryLogins = []string{"dude-bot"}
	task := w.reviewing()
	quoted := "> Because the task says greet.\n>\n> " + forge.ReplyMarker + "\n\n"
	w.gh.Comment(1, "alice", quoted+"@dude could you explain the tradeoff?")
	w.gh.Comment(1, "alice", "```\nBecause.\n\n"+forge.ReplyMarker+"\n```\n\n@dude and this one?")
	w.gh.Comment(1, "alice", "@dude explain this example\n````\n```\n"+forge.ReplyMarker)
	w.gh.Comment(1, "alice", "@dude and this\n~~~~\n~~~\n"+forge.ReplyMarker)
	w.until("the four messages", func() bool { w.sync(); return w.mentions(task) == 4 })
	w.gh.Comment(1, "alice", quoted+"Please rename greet() to hello().")
	w.until("a fixer for the change request", func() bool { w.sync(); return w.fixes(task) == 1 })

	// Dude's own reply, as ConductReply writes it, stays dude's.
	w.gh.Comment(1, "alice", "> @alice: @dude why?\n\nBecause.\n\n"+forge.ReplyMarker+"\n")
	w.syncs(3)
	if n := w.mentions(task); n != 4 {
		t.Errorf("%d messages, want the four quoting or fenced comments'", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'own' = 'true'`, task); n != 1 {
		t.Errorf("%d comments recorded as dude's own, want the reply", n)
	}
}

// webhook stores a delivery as the backend does, and processes it.
func (w *world) webhook(event string, payload map[string]any) {
	w.t.Helper()
	body, _ := json.Marshal(payload)
	mustExec(w.t, w.owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload) VALUES ($1, $2, $3, $4)`,
		fmt.Sprintf("dlv_%d_%s", time.Now().UnixNano(), w.org), w.org, event, body)
	if _, err := w.prs.ProcessDeliveries(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// issueComment is GitHub's issue_comment payload for a comment on
// acme/target's pull request 1.
func issueComment(action string, id int64) map[string]any {
	return map[string]any{"action": action, "repository": map[string]any{"full_name": "acme/target"},
		"issue": map[string]any{"number": 1, "pull_request": map[string]any{}}, "comment": map[string]any{"id": id}}
}

// A mention on a pull request that was merged, or closed, still reaches
// the conductor, once, which can only answer: the task has ended.
func TestAMentionOnAnEndedPullRequestReachesTheConductor(t *testing.T) {
	for _, end := range []struct{ how, status string }{{"merged", "done"}, {"closed", "aborted"}} {
		t.Run(end.how, func(t *testing.T) {
			w := conducting(t)
			task := w.reviewing()
			if end.how == "merged" {
				w.gh.Merge(1)
			} else {
				w.gh.Close(1)
			}
			w.until(end.status, func() bool { w.sync(); return w.taskStatus(task) == end.status })
			id := w.gh.Comment(1, "alice", "@dude why this choice?")
			comment := issueComment("created", id)
			w.webhook("issue_comment", comment)
			w.until("the conductor's answer", func() bool {
				c, _, _ := w.conductor(task)
				return c != "" && len(w.said(c)) > 0
			})
			w.webhook("issue_comment", comment)
			w.pump()
			if n := w.mentions(task); n != 1 {
				t.Errorf("%d messages for one mention", n)
			}
			c, _, _ := w.conductor(task)
			var prompt string
			_ = w.owner.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, c).Scan(&prompt)
			if !strings.Contains(prompt, "No delivery is in progress: you are read-only.") ||
				!strings.Contains(prompt, "@dude why this choice?") {
				t.Errorf("the conductor was not briefed read-only with the mention:\n%s", prompt)
			}
			if s := w.taskStatus(task); s != end.status {
				t.Errorf("the mention moved the task to %s", s)
			}
			if n := w.fixes(task); n != 0 {
				t.Errorf("%d fixers on an ended task", n)
			}
		})
	}
}

// A merged pull request read again for a mention keeps what it was merged
// with: its branch moving on with failing work changes neither its head nor
// its checks, records no change, and still the mention is heard.
func TestAnEndedPullRequestReadForAMentionKeepsItsRecord(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	w.gh.Merge(1)
	w.until("done", func() bool { w.sync(); return w.taskStatus(task) == "done" })
	record := func() string {
		var head, checks, list string
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(head_sha, ''), checks::text, checks_json::text
			FROM pull_requests WHERE task_id = $1 AND number = 1`, task).Scan(&head, &checks, &list)
		return head + " " + checks + " " + list
	}
	before := record()
	if !strings.Contains(before, " passing ") {
		t.Fatalf("merged with %s, want passing checks", before)
	}
	changes := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.checks_changed'`, task)

	w.gh.CommitOnTop(w.gh.Pull(1).Head, "later work")
	w.gh.SetChecks("failure")
	id := w.gh.Comment(1, "alice", "@dude why this choice?")
	w.webhook("issue_comment", issueComment("created", id))
	w.until("the message", func() bool { return w.mentions(task) == 1 })
	w.pump()
	if after := record(); after != before {
		t.Errorf("the merged pull request's record changed:\n  was %s\n  now %s", before, after)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.checks_changed'`, task); n != changes {
		t.Errorf("%d checks changes recorded on a merged pull request", n-changes)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers on a done task", n)
	}
}

// A closed pull request reopened is read in full again: open, at its
// branch's head now, with that head's checks.
func TestAClosedPullRequestReopenedIsReadInFull(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	w.gh.Close(1)
	w.until("aborted", func() bool { w.sync(); return w.taskStatus(task) == "aborted" })
	head := w.gh.CommitOnTop(w.gh.Pull(1).Head, "later work")
	w.gh.SetChecks("failure")
	w.gh.Reopen(1)
	w.webhook("pull_request", map[string]any{"action": "reopened", "repository": map[string]any{"full_name": "acme/target"},
		"pull_request": map[string]any{"number": 1}})
	var state, sha, checks string
	_ = w.owner.QueryRow(context.Background(), `SELECT state::text, COALESCE(head_sha, ''), checks::text FROM pull_requests
		WHERE task_id = $1 AND number = 1`, task).Scan(&state, &sha, &checks)
	if state != "open" || sha != head || checks != "failing" {
		t.Errorf("reopened, it reads %s at %s with %s checks; want open at %s, failing", state, sha, checks, head)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.updated'
		AND payload->>'from' = 'closed' AND payload->>'to' = 'open'`, task); n != 1 {
		t.Errorf("%d reopenings recorded", n)
	}
}

// A comment edited to address dude is a message, once: the edit's webhook
// and the poll both find it, though an older comment was read since; a
// second edit, or the same delivery again, is no second message.
func TestACommentEditedIntoAMentionIsOneMessage(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	id := w.gh.Comment(1, "alice", "LGTM")
	w.syncs(2)
	// A later comment moves the cursor past the edited one's creation.
	time.Sleep(1100 * time.Millisecond)
	w.gh.Comment(1, "bob", "Thanks!")
	w.syncs(2)
	if n := w.mentions(task); n != 0 {
		t.Fatalf("%d messages before any mention", n)
	}
	w.gh.EditComment(1, id, "@dude why this choice?")
	edited := issueComment("edited", id)
	w.webhook("issue_comment", edited)
	w.until("the message", func() bool { return w.mentions(task) == 1 })
	var text string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'text' FROM events WHERE task_id = $1
		AND event_type = 'chat.message' AND payload->'github'->>'feedbackId' = $2`, task, fmt.Sprintf("issue-comment-%d", id)).Scan(&text)
	if text != "@dude why this choice?" {
		t.Errorf("the message says %q", text)
	}
	w.webhook("issue_comment", edited)
	w.gh.EditComment(1, id, "@dude why this choice? (typo)")
	w.webhook("issue_comment", edited)
	w.syncs(3)
	if n := w.mentions(task); n != 1 {
		t.Errorf("%d messages for one comment edited into a mention", n)
	}
	if n := w.conductors(task); n != 1 {
		t.Errorf("%d conductors", n)
	}
	if n := w.fixes(task); n != 0 {
		t.Errorf("%d fixers", n)
	}
}

// A comment that only mentions dude in passing is not a mention: an
// address, a longer login, a word.
func TestWhatCountsAsAMention(t *testing.T) {
	logins := []string{"acme-dude"}
	for body, want := range map[string]bool{
		"@dude why?": true, "hey @DUDE, look": true, "(@dude)": true, "cc @acme-dude": true, "@dude\n": true,
		"mail dude@example.com": false, "@dudeface hi": false, "the dude abides": false, "@acme-dude-two": false,
		"x@dude": false,
	} {
		if got := forge.Mentions(body, logins); got != want {
			t.Errorf("Mentions(%q) = %v, want %v", body, got, want)
		}
	}
}
