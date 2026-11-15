package orchestrator_test

// Review only (6b): the conductor asks the pull request's author on GitHub
// (ask_author), the author's answer reaches it, and the open question
// holds dude's verdict. A writing task sees none of it.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// openAsk is the task's one open question to the author.
func (w *world) openAsk(wi string) string {
	w.t.Helper()
	ids := w.ids(`SELECT id FROM review_questions WHERE task_id = $1 AND status = 'open' AND addressee = 'author'`, wi)
	if len(ids) != 1 {
		w.t.Fatalf("the open author question: %v", ids)
	}
	return ids[0]
}

// dudeConversation is pull request n's conversation comments by dude's
// account carrying an ask marker.
func (w *world) askComments(n int) []fakegithub.Comment {
	var out []fakegithub.Comment
	for _, c := range w.gh.Pull(n).Comments {
		if c.Author == fakegithub.Login && strings.Contains(c.Body, "<!-- dude:ask id=") {
			out = append(out, c)
		}
	}
	return out
}

func (w *world) conversationMessages(task string, id int64) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message'
		AND payload->'github'->>'feedbackId' = $2`, task, fmt.Sprintf("issue-comment-%d", id))
}

// toolList is what GET /tools lists the task's conductor.
func (w *world) toolList(task string) string {
	w.t.Helper()
	var spec lux.Spec
	_ = json.Unmarshal([]byte(w.conductorSpecOf(task)), &spec)
	req, _ := http.NewRequest("GET", w.syncer.Agent.ToolsURL+"/tools", nil)
	req.Header.Set("Authorization", toolsAuth(spec))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		w.t.Fatalf("GET /tools: %d %s", res.StatusCode, b)
	}
	return string(b)
}

// In the finding's thread: the conductor asks, alice answers there, the
// wake names the open question; answered, the finding dismissed as
// wrong, dude approves at the same head with no new round.
func TestAskTheAuthorInAFindingsThreadThenApprove(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	root := w.dudeLineComments(n)[0]
	finding := w.openFinding(wi)
	out := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Is greet called from the CLI only?","finding_id":%q}`, n, finding))
	q, _ := out["questionId"].(string)
	if q == "" || q != w.openAsk(wi) {
		t.Fatalf("ask_author: %v", out)
	}
	asked := repliesTo(w.gh.Pull(n), root.ID)
	if len(asked) != 1 || !strings.HasPrefix(asked[0].Body, "@alice Is greet called from the CLI only?") ||
		!strings.Contains(asked[0].Body, "<!-- dude:ask id="+q+" -->") || !strings.HasSuffix(asked[0].Body, "<!-- dude:conductor -->") {
		t.Fatalf("the question in dude's thread: %+v", asked)
	}
	reply := w.gh.ReplyInThread(n, "alice", root.ID, "Yes, and the CLI lower-cases on purpose.")
	w.until("the answer in Chat", func() bool { w.sync(); return w.replyMessages(wi, reply) == 1 })
	if !w.woke(wi, q+": @alice, Is greet called from the CLI only?") {
		t.Error("the wake does not name the open author question")
	}
	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi)
	w.must(wi, "answer_review_question", fmt.Sprintf(`{"id":%q,"answer":"alice: called from the CLI only, lower case on purpose"}`, q))
	w.must(wi, "dismiss_finding", `{"id":"`+finding+`","reason":"wrong: the author says the CLI lower-cases on purpose, greet.md:1"}`)
	w.agentsDone(wi, "approved at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) >= 2 && rs[len(rs)-1].State == "APPROVED" && w.waitingOnPR(wi)
	})
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); r != runs {
		t.Errorf("%d lens Runs for an answer", r-runs)
	}
}

// On the conversation: an open question with no blocking finding is a
// COMMENT, never APPROVE, posted once however often the pull request is
// synced; the author's plain conversation comment wakes the conductor;
// the question answered, dude approves at the same head.
func TestAskTheAuthorOnTheConversationHoldsTheApproval(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.approvedAtH1()
	out := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Should greet also handle names?"}`, n))
	q, _ := out["questionId"].(string)
	if c := w.askComments(n); len(c) != 1 || !strings.HasPrefix(c[0].Body, "@alice Should greet also handle names?") {
		t.Fatalf("the question on the conversation: %+v", c)
	}
	w.agentsDone(wi, "a COMMENT at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 2 && w.waitingOnPR(wi)
	})
	rs := reviewsAt(w.dudeReviews(wi, n), h1)
	if rs[1].State != "COMMENTED" || !strings.Contains(rs[1].Body, "Before approving: @alice, Should greet also handle names?") {
		t.Fatalf("the review held by the question: %+v", rs[1])
	}
	if !strings.Contains(rs[1].Body, "https://") {
		t.Errorf("the COMMENT does not link the question: %q", rs[1].Body)
	}
	// The question holds the approval back: dude's APPROVE at H1 stands
	// no longer (I3).
	if rs[0].State != "DISMISSED" {
		t.Errorf("dude's approval at H1 still stands beside the COMMENT: %+v", rs[0])
	}
	w.syncs(4)
	w.advanced()
	if got := reviewsAt(w.dudeReviews(wi, n), h1); len(got) != 2 {
		t.Fatalf("a re-sync posted again: %+v", got)
	}
	// The verdict runs again at H1 with the same open question (a
	// recheck, as a withdrawn finding sends): its claim is the same, so
	// nothing is posted again (I1).
	claims := w.events(wi, delivery.EvReviewClaimed)
	w.recheck(wi, "recheck:probe")
	if got := reviewsAt(w.dudeReviews(wi, n), h1); len(got) != 2 || w.events(wi, delivery.EvReviewClaimed) != claims {
		t.Fatalf("the verdict's re-run at H1 posted again: %+v", got)
	}
	// Someone else's conversation comment is not an answer.
	bob := w.gh.Comment(n, "bob", "Looks fine to me.")
	answer := w.gh.Comment(n, "alice", "No, names come in a later pull request.")
	w.until("the answer in Chat", func() bool { w.sync(); return w.conversationMessages(wi, answer) == 1 })
	if m := w.conversationMessages(wi, bob); m != 0 {
		t.Errorf("bob's comment reached the conductor")
	}
	if !w.woke(wi, q+": @alice, Should greet also handle names?") {
		t.Error("the wake does not name the open author question")
	}
	w.must(wi, "answer_review_question", fmt.Sprintf(`{"id":%q,"answer":"alice: names come later"}`, q))
	w.agentsDone(wi, "approved again at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 3 && rs[2].State == "APPROVED" && w.waitingOnPR(wi)
	})
}

// GitHub refusing the post withdraws the question (a Refusal); a post
// whose answer was lost keeps it open, and the next sync finds the
// comment by its marker.
func TestAskTheAuthorRefusedAndLost(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.approvedAtH1()
	pull := w.gh.Pull(n)
	w.gh.Set(func(*fakegithub.Server) { pull.Locked = true })
	w.refused(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Why?"}`, n), "GitHub did not take the question")
	if c := w.count(`SELECT count(*) FROM review_questions WHERE task_id = $1 AND addressee = 'author' AND status = 'withdrawn'`, wi); c != 1 {
		t.Fatalf("%d refused questions withdrawn", c)
	}
	// The refused question held nothing: the verdict run again at H1
	// claims and posts nothing new (tests M1).
	claims := w.events(wi, delivery.EvReviewClaimed)
	w.recheck(wi, "recheck:refused")
	if rs := reviewsAt(w.dudeReviews(wi, n), h1); len(rs) != 1 || w.events(wi, delivery.EvReviewClaimed) != claims {
		t.Fatalf("a refused question changed the verdict at H1: %+v", rs)
	}
	w.gh.Set(func(s *fakegithub.Server) { pull.Locked = false; s.CommentTimeoutOnce = true })
	out := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Why lower case?"}`, n))
	q, _ := out["questionId"].(string)
	if w.count(`SELECT count(*) FROM review_questions WHERE id = $1 AND status = 'open' AND github_comment_id IS NULL`, q) != 1 {
		t.Fatalf("the lost post's question: %v", out)
	}
	// bob quotes the marker, and GitHub lists his comment first: only
	// dude's own comment is the question's.
	bob := w.gh.Comment(n, "bob", "quoting: <!-- dude:ask id="+q+" -->")
	w.gh.Set(func(*fakegithub.Server) {
		last := len(pull.Comments) - 1
		pull.Comments = append([]fakegithub.Comment{pull.Comments[last]}, pull.Comments[:last]...)
	})
	w.until("its comment recovered", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM review_questions WHERE id = $1 AND github_comment_id LIKE 'issue-comment-%'`, q) == 1
	})
	if c := w.askComments(n); len(c) != 1 || fmt.Sprintf("issue-comment-%d", c[0].ID) != w.ids(`SELECT github_comment_id FROM review_questions WHERE id = $1`, q)[0] {
		t.Errorf("recovered a comment other than dude's: %+v", c)
	}
	if got := w.ids(`SELECT github_comment_id FROM review_questions WHERE id = $1`, q)[0]; got == fmt.Sprintf("issue-comment-%d", bob) {
		t.Errorf("recovered bob's comment quoting the marker")
	}
}

// A post that never reached GitHub, its answer lost: the question held
// the verdict at H1 (COMMENT, dude's approval dismissed); withdrawn, dude
// approves at H1 again (correctness M1).
func TestALostAskNeverPostedThenWithdrawnApprovesAgain(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.approvedAtH1()
	w.gh.Set(func(s *fakegithub.Server) { s.CommentLostOnce = true })
	q, _ := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Why lower case?"}`, n))["questionId"].(string)
	if len(w.askComments(n)) != 0 {
		t.Fatal("the lost post reached GitHub")
	}
	w.agentsDone(wi, "a COMMENT at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 2 && rs[0].State == "DISMISSED" && w.waitingOnPR(wi)
	})
	w.syncs(2)
	w.must(wi, "answer_review_question", fmt.Sprintf(`{"id":%q,"withdraw":true,"reason":"it never posted"}`, q))
	w.agentsDone(wi, "a standing approval at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 3 && rs[2].State == "APPROVED" && w.waitingOnPR(wi)
	})
}

// A post that reached GitHub whose record was never written (RecordAskTx
// failed, or the orchestrator restarted): the open question held the
// verdict at H1 all the same; withdrawn, dude approves at H1 again
// (correctness L2). The record's loss is simulated by removing what
// RecordAskTx wrote: the asked event and the comment.
func TestAnAskWhoseRecordWasLostThenWithdrawnApprovesAgain(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.approvedAtH1()
	q, _ := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Why lower case?"}`, n))["questionId"].(string)
	mustExec(t, w.owner, `DELETE FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvAuthorAsked)
	mustExec(t, w.owner, `UPDATE review_questions SET github_comment_id = NULL, github_url = NULL WHERE id = $1`, q)
	w.agentsDone(wi, "a COMMENT at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 2 && rs[0].State == "DISMISSED" && w.waitingOnPR(wi)
	})
	w.syncs(2)
	w.must(wi, "answer_review_question", fmt.Sprintf(`{"id":%q,"withdraw":true,"reason":"not needed"}`, q))
	w.agentsDone(wi, "a standing approval at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 3 && rs[2].State == "APPROVED" && w.waitingOnPR(wi)
	})
}

// recheck has the verdict run again at the head (as a withdrawn finding
// does), and waits for the wait to consume it and come back.
func (w *world) recheck(wi, key string) {
	w.t.Helper()
	wf := w.str(`SELECT id FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi)
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalPRFeedback, map[string]any{"kind": delivery.SignalKindRecheck}, key); err != nil {
		w.t.Fatal(err)
	}
	// Consumed, the wait went on to the verdict step and back.
	w.agentsDone(wi, "the verdict ran again", func() bool {
		return w.count(`SELECT count(*) FROM workflow_signals WHERE workflow_run_id = $1 AND consumed_at IS NULL`, wf) == 0 && w.waitingOnPR(wi)
	})
	w.syncs(2)
	w.advanced()
}

// A pull request author who may not wake anyone (WhoCanWake) may still
// answer dude's question: that comment alone. Their mention of dude in a
// line comment, heard in the same sync, stays not permitted (L2).
func TestOnlyTheAnswerOfAnAuthorWhoMayNotWakeIsHeard(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, _ := w.approvedAtH1()
	w.gh.Set(func(s *fakegithub.Server) { s.Permissions["alice"] = "none" })
	w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Should greet also handle names?"}`, n))
	answer := w.gh.Comment(n, "alice", "No, names come later.")
	line := w.gh.LineComment(n, "alice", "greet.md", "@dude please rename this too.")
	w.until("the answer in Chat", func() bool { w.sync(); return w.conversationMessages(wi, answer) == 1 })
	w.syncs(2)
	// The answer is the conductor's, not ignored as not permitted (L2).
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'feedbackId' = $2 AND payload->>'addressed' = 'conductor' AND NOT payload ? 'ignored'`, wi, fmt.Sprintf("issue-comment-%d", answer)); c != 1 {
		t.Errorf("alice's answer is not shown as addressed to the conductor")
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'feedbackId' = $2 AND payload->>'ignored' = 'not_permitted'`, wi, fmt.Sprintf("line-comment-%d", line)); c != 1 {
		t.Errorf("alice's line comment mentioning dude is not ignored as not permitted")
	}
	if m := w.replyMessages(wi, line); m != 0 {
		t.Errorf("alice's line comment reached the conductor: %d", m)
	}
}

// waitPass has the task's pull request wait run once more (a readiness
// signal, which reruns no verdict that is not due), and waits for it.
func (w *world) waitPass(wi, key string) {
	w.t.Helper()
	wf := w.str(`SELECT id FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi)
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalPRFeedback, map[string]any{"kind": "readiness"}, key); err != nil {
		w.t.Fatal(err)
	}
	w.until("the wait ran again", func() bool {
		w.advanced()
		return w.count(`SELECT count(*) FROM workflow_signals WHERE workflow_run_id = $1 AND consumed_at IS NULL`, wf) == 0 && w.waitingOnPR(wi)
	})
}

// The project's timeout of an hour: a question asked half an hour ago has
// not timed out after a pass of the wait's timer, and the wait sleeps
// until it is due, an hour after it was asked (I4).
func TestAnAuthorQuestionWaitsForTheProjectsTimeout(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"authorQuestionTimeoutMinutes":60}' WHERE id = $1`, w.project)
	wi, n, h1 := w.approvedAtH1()
	q, _ := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Is a changelog entry planned?"}`, n))["questionId"].(string)
	w.agentsDone(wi, "a COMMENT at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 2 && w.waitingOnPR(wi) })
	// Asked half an hour ago: not due for another half hour.
	mustExec(t, w.owner, `UPDATE review_questions SET created_at = now() - interval '30 minutes' WHERE id = $1`, q)
	w.waitPass(wi, "pass:1h")
	if e := w.events(wi, delivery.EvAuthorQuestionTimedOut); e != 0 {
		t.Fatalf("a question asked half an hour ago timed out: %d", e)
	}
	var off float64
	if err := w.owner.QueryRow(context.Background(), `SELECT extract(epoch FROM w.wake_at - (q.created_at + interval '1 hour'))
		FROM workflow_runs w, review_questions q WHERE w.task_id = $1 AND w.status = 'waiting' AND q.id = $2`, wi, q).Scan(&off); err != nil {
		t.Fatal(err)
	}
	if off < -2 || off > 2 {
		t.Errorf("the wait sleeps until %+.0fs from the question's due time", off)
	}
}

// Start over ends attempt 1's delivery, and its question to the author
// with it: attempt 2's verdict is not held by it, alice's next comment is
// not routed to attempt 2's conductor as its answer, and it never times
// out in attempt 2 (correctness M2).
func TestStartOverWithAnOpenAuthorQuestion(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.approvedAtH1()
	ana := w.ownedBy(wi, "Ana")
	q, _ := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Why lower case?"}`, n))["questionId"].(string)
	w.agentsDone(wi, "a COMMENT at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 2 && rs[1].State == "COMMENTED" && w.waitingOnPR(wi)
	})
	if out := w.stop(ana, wi, map[string]any{"andRestart": true}); out["attempt"] != float64(2) {
		t.Fatalf("stop and start over answered %v", out)
	}
	w.agentsDone(wi, "attempt 2's verdict at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) == 3 && w.waitingOnPR(wi) &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2 AND step = 'awaitPullRequest'`, wi) == 1
	})
	if rs := reviewsAt(w.dudeReviews(wi, n), h1); rs[2].State != "APPROVED" || strings.Contains(rs[2].Body, "Why lower case?") {
		t.Fatalf("attempt 2's verdict is held by attempt 1's question: %+v", rs[2])
	}
	answer := w.gh.Comment(n, "alice", "Because the CLI lower-cases.")
	w.syncs(3)
	if m := w.conversationMessages(wi, answer); m != 0 {
		t.Errorf("alice's comment reached attempt 2's conductor as an answer")
	}
	// Long past its timeout, and a pass of attempt 2's wait.
	mustExec(t, w.owner, `UPDATE review_questions SET created_at = now() - interval '4 days' WHERE id = $1`, q)
	w.waitPass(wi, "pass:attempt-2")
	w.syncs(2)
	w.advanced()
	if e := w.events(wi, delivery.EvAuthorQuestionTimedOut); e != 0 {
		t.Errorf("attempt 1's question timed out in attempt 2: %d", e)
	}
	for _, r := range reviewsAt(w.dudeReviews(wi, n), h1)[2:] {
		if r.State != "APPROVED" {
			t.Errorf("attempt 2 posted %s at H1: %q", r.State, r.Body)
		}
	}
	// Nor is it attempt 2's conductor's to settle (correctness L3).
	w.refused(wi, "answer_review_question", fmt.Sprintf(`{"id":%q,"withdraw":true,"reason":"stale"}`, q), q+" is not a reviewer question of this task")
	if s := w.ids(`SELECT status FROM review_questions WHERE id = $1`, q)[0]; s != "open" {
		t.Errorf("attempt 1's question is %s", s)
	}
}

// The timeout, on dude's clock: a finding's question is withdrawn and the
// finding stands (REQUEST_CHANGES again, no COMMENT for the question); a
// free-standing one wakes the conductor once, and stays open.
func TestAuthorQuestionsTimeOut(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.withDeliveries(&delivery.Store{AuthorQuestionTimeout: 2 * time.Second})
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	finding := w.openFinding(wi)
	tied, _ := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Is greet called from the CLI only?","finding_id":%q}`, n, finding))["questionId"].(string)
	free, _ := w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Is a changelog entry planned?"}`, n))["questionId"].(string)
	w.until("both timed out", func() bool {
		w.sync()
		w.advanced()
		return w.events(wi, delivery.EvAuthorQuestionTimedOut) == 2
	})
	if s := w.ids(`SELECT status FROM review_questions WHERE id = $1`, tied)[0]; s != "withdrawn" {
		t.Errorf("the finding's question is %s", s)
	}
	if s := w.ids(`SELECT status FROM review_questions WHERE id = $1`, free)[0]; s != "open" {
		t.Errorf("the free-standing question is %s", s)
	}
	if f := w.count(`SELECT count(*) FROM review_findings WHERE id = $1 AND status = 'open'`, finding); f != 1 {
		t.Error("the finding did not stand")
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'review.author_question_timed_out'
		AND payload->>'reason' = 'no answer from @alice in 2s'`, wi); c != 2 {
		t.Errorf("%d timeouts with the reason", c)
	}
	if c := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = $2`, wi, "author-question:"+free+":timed-out"); c != 1 {
		t.Errorf("%d wakes for the free-standing question", c)
	}
	w.syncs(3)
	w.advanced()
	// Another pass of the wait's timer: the free-standing question, still
	// open, is not timed out again (L1).
	w.waitPass(wi, "pass:after-timeouts")
	if e := w.events(wi, delivery.EvAuthorQuestionTimedOut); e != 2 {
		t.Errorf("%d timeouts after more syncs", e)
	}
	// Withdrawn by the conductor: the verdict proceeds, the finding still
	// open, REQUEST_CHANGES at H1 again.
	w.must(wi, "answer_review_question", fmt.Sprintf(`{"id":%q,"withdraw":true,"reason":"no answer; not needed for the verdict"}`, free))
	w.agentsDone(wi, "changes requested at H1", func() bool {
		rs := reviewsAt(w.dudeReviews(wi, n), h1)
		return len(rs) >= 2 && rs[len(rs)-1].State == "CHANGES_REQUESTED" && w.waitingOnPR(wi)
	})
	for _, r := range reviewsAt(w.dudeReviews(wi, n), h1) {
		if r.State == "APPROVED" {
			t.Fatalf("approved with a finding standing: %+v", r)
		}
	}
}

// A writing task: ask_author is not its conductor's (not listed, refused
// by name), and nothing of author questions is in its wake.
func TestAWritingDeliveryHasNoAskAuthor(t *testing.T) {
	w := conducting(t)
	w.gh.Set(func(s *fakegithub.Server) { s.Permissions["stranger"] = "none" })
	task := w.reviewing()
	w.gh.Comment(1, "bob", "@dude is this needed?")
	w.until("the mention", func() bool { w.sync(); return w.mentions(task) == 1 })
	status, body := w.callTool(w.syncer.Agent.ToolsURL, w.conductorSpecOf(task), "ask_author", `{"pr":"1","question":"Why?"}`)
	if status != 404 {
		t.Fatalf("a writing conductor's ask_author: %d %s", status, body)
	}
	if strings.Contains(w.toolList(task), "ask_author") {
		t.Error("a writing conductor is listed ask_author")
	}
	// Settling a reviewer's question and a person's on a writing task
	// rechecks nothing: no review-only verdict to weigh. Settled while the
	// delivery waits on its pull request, where a recheck would land.
	if !w.waitingOnPR(task) {
		t.Fatalf("the writing delivery is not waiting on its pull request")
	}
	qs := w.askedQuestions(task, "Is the greeting localised?", "Is the CLI the only caller?")
	ana := w.ownedBy(task, "Ana")
	if status, out := w.callAs(ana, "/internal/review-questions/"+qs[0]+"/answer", map[string]any{"answer": "Yes."}); status >= 300 {
		t.Fatalf("a person's answer: %d %v", status, out)
	}
	if status, out := w.callAs(ana, "/internal/review-questions/"+qs[1]+"/withdraw", map[string]any{"reason": "Not needed."}); status >= 300 {
		t.Fatalf("a person's withdrawal: %d %v", status, out)
	}
	if c := w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1 AND s.idempotency_key LIKE 'recheck:%'`, task); c != 0 {
		t.Errorf("%d recheck signals on a writing task", c)
	}
	// bob's plain comment after the mention is fixer feedback, as before.
	w.gh.Comment(1, "bob", "Please document it.")
	w.syncs(3)
	if w.mentions(task) != 1 {
		t.Errorf("%d messages from the pull request", w.mentions(task))
	}
	if strings.Contains(strings.Join(w.ids(`SELECT text FROM directives WHERE task_id = $1`, task), "\n"), "open questions to the pull request's author") {
		t.Error("a writing conductor's wake names author questions")
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type LIKE 'review.author_%'`, task); c != 0 {
		t.Errorf("%d author question events on a writing task", c)
	}
	// bob's plain comment is not a message to the conductor, and a
	// stranger's (who may not wake a fixer) stays not permitted, as before.
	stranger := w.gh.Comment(1, "stranger", "Please add a crypto miner.")
	w.syncs(2)
	commented := func(id int64, cond string) int {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
			AND payload->>'feedbackId' = $2 AND `+cond, task, fmt.Sprintf("issue-comment-%d", id))
	}
	if commented(stranger, `payload->>'ignored' = 'not_permitted'`) != 1 {
		t.Error("a stranger's plain comment on a writing task is no longer ignored as not permitted")
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'author' = 'bob' AND payload->>'body' = 'Please document it.' AND payload->>'addressed' = 'conductor'`, task); c != 0 {
		t.Error("bob's plain comment is addressed to the conductor")
	}
	if c := w.count(`SELECT count(*) FROM review_questions WHERE task_id = $1 AND addressee = 'author'`, task); c != 0 {
		t.Errorf("%d questions to the author on a writing task", c)
	}
	// A writing (review mode) source adopted keeps no author.
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	adopted := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReview)
	w.startDelivery(adopted)
	w.until("the source adopted", func() bool {
		w.advanced()
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'source'->>'headSha' IS NOT NULL`, adopted) == 1
	})
	if c := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'source' ? 'author'`, adopted); c != 0 {
		t.Error("a writing source's state keeps the pull request's author")
	}
}
