package orchestrator_test

// A reply in dude's own review thread on a review-only task (review-only
// mode, 6a): a message to its conductor, mention or not. A writing task's
// comments are routed as they always were.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// woke says whether the task's conductor was told text: in the briefing
// it started with, or in a directive queued for it.
func (w *world) woke(task, text string) bool {
	return w.count(`SELECT (SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor' AND strpos(prompt, $2) > 0)
		+ (SELECT count(*) FROM directives WHERE task_id = $1 AND strpos(text, $2) > 0)`, task, text) > 0
}

// replyMessages is how many Chat messages came from feedback id.
func (w *world) replyMessages(task string, id int64) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message'
		AND payload->'github'->>'feedbackId' = $2`, task, fmt.Sprintf("line-comment-%d", id))
}

// The author answers dude's finding in its thread, mentioning nobody: the
// conductor is woken once with the reply, told which finding it disputes
// and what dude said; the reply edited is not delivered again. The
// conductor dismisses it as wrong: APPROVE at the same head, no new round,
// and dude's thread says it was withdrawn and is resolved.
func TestAnAuthorsReplyInDudesThreadReachesTheConductor(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	root := w.dudeLineComments(n)[0]
	var finding string
	if err := w.owner.QueryRow(t.Context(), `SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi).Scan(&finding); err != nil {
		t.Fatalf("the open finding: %v", err)
	}
	reply := w.gh.ReplyInThread(n, "alice", root.ID, "This is intentional: the caller already checks for nil.")
	w.until("the reply in Chat", func() bool { w.sync(); return w.replyMessages(wi, reply) == 1 })
	w.until("the conductor told", func() bool { return w.woke(wi, "alice wrote to you on pull request") })
	for _, want := range []string{"This is intentional", "It replies in dude's thread on finding " + finding + " (medium): The greeting is rude.",
		"**The greeting is rude** (medium)\n\nD."} {
		if !w.woke(wi, want) {
			t.Errorf("the wake does not say %q", want)
		}
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'feedbackId' = $2 AND payload->>'addressed' = 'conductor'`, wi, fmt.Sprintf("line-comment-%d", reply)); c != 1 {
		t.Errorf("%d replies recorded as addressed to the conductor", c)
	}
	w.gh.EditComment(n, reply, "This is intentional: the caller checks for nil at greet.go:3.")
	w.syncs(3)
	if m := w.replyMessages(wi, reply); m != 1 {
		t.Errorf("the edited reply came %d times", m)
	}

	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi)
	w.must(wi, "dismiss_finding", `{"id":"`+finding+`","reason":"wrong: the caller checks for nil at greet.go:3, as the author showed"}`)
	w.agentsDone(wi, "approved at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 2 && w.waitingOnPR(wi) })
	if got := reviewsAt(w.dudeReviews(wi, n), h1); got[1].State != "APPROVED" {
		t.Fatalf("after the dismissal: %+v", got)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); r != runs {
		t.Errorf("%d lens Runs for a dismissal", r-runs)
	}
	var withdrawn []string
	for _, c := range repliesTo(w.gh.Pull(n), root.ID) {
		if c.Author == fakegithub.Login {
			withdrawn = append(withdrawn, c.Body)
		}
	}
	if len(withdrawn) != 1 || withdrawn[0] != "Withdrawn: the caller checks for nil at greet.go:3, as the author showed." {
		t.Errorf("dude's replies in its thread: %q", withdrawn)
	}
	if !w.gh.ResolvedThreads[w.gh.ThreadID(root.ID)] {
		t.Error("the withdrawn finding's thread is not resolved")
	}
}

// What is not a message to the conductor: a reply in another reviewer's
// thread, a bot's reply in dude's, and one by an author who may not wake.
func TestRepliesThatDoNotReachTheConductor(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.gh.Set(func(s *fakegithub.Server) { s.Permissions["stranger"] = "none" })
	wi, n, _ := w.findingThenVerdict(func() string { return "still" })
	root := w.dudeLineComments(n)[0]
	bobs := w.gh.LineComment(n, "bob", "greet.md", "Should this be capitalised?")
	before := w.mentions(wi)
	replies := []int64{
		w.gh.ReplyInThread(n, "alice", bobs, "No, it is lower case on purpose."),
		w.gh.ReplyInThread(n, "lint-bot[bot]", root.ID, "This is intentional."),
		w.gh.ReplyInThread(n, "stranger", root.ID, "This is intentional."),
	}
	w.syncs(5)
	for _, id := range replies {
		if m := w.replyMessages(wi, id); m != 0 {
			t.Errorf("line-comment-%d reached the conductor", id)
		}
	}
	if m := w.mentions(wi); m != before {
		t.Errorf("%d messages from the pull request", m-before)
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'addressed' = 'conductor'`, wi); c != 0 {
		t.Errorf("%d comments recorded as addressed to the conductor", c)
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'feedbackId' = $2 AND payload->>'ignored' = 'not_permitted'`, wi, fmt.Sprintf("line-comment-%d", replies[2])); c != 1 {
		t.Errorf("the stranger's reply was not recorded as not permitted")
	}
}

// The token comments as a person (a PAT) and no factory login is set:
// dude's own unmarked "Withdrawn: …" reply in its thread is still dude's,
// not a person's reply. No Chat message, no addressed record, no wake. A
// person's reply in the same thread still reaches the conductor.
func TestDudesOwnThreadReplyIsNotAPersonsReply(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	root := w.dudeLineComments(n)[0]
	w.gh.Set(func(s *fakegithub.Server) { s.Poster = "pat-owner" })
	var finding string
	if err := w.owner.QueryRow(t.Context(), `SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi).Scan(&finding); err != nil {
		t.Fatalf("the open finding: %v", err)
	}
	before := w.mentions(wi)
	w.must(wi, "dismiss_finding", `{"id":"`+finding+`","reason":"wrong: the caller checks for nil at greet.go:3"}`)
	w.agentsDone(wi, "approved at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 2 && w.waitingOnPR(wi) })
	var withdrawn int64
	for _, c := range repliesTo(w.gh.Pull(n), root.ID) {
		if c.Author == "pat-owner" && strings.HasPrefix(c.Body, "Withdrawn: ") {
			withdrawn = c.ID
		}
	}
	if withdrawn == 0 {
		t.Fatalf("no Withdrawn reply by pat-owner: %+v", repliesTo(w.gh.Pull(n), root.ID))
	}
	w.syncs(5)
	if m := w.replyMessages(wi, withdrawn); m != 0 || w.mentions(wi) != before {
		t.Errorf("dude's Withdrawn reply reached the conductor: %d, %d messages", m, w.mentions(wi)-before)
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'addressed' = 'conductor'`, wi); c != 0 {
		t.Errorf("%d comments recorded as addressed to the conductor", c)
	}
	if w.woke(wi, "Withdrawn: ") {
		t.Error("the conductor was woken by dude's own reply")
	}
	reply := w.gh.ReplyInThread(n, "alice", root.ID, "Thanks, agreed.")
	w.until("alice's reply in Chat", func() bool { w.sync(); return w.replyMessages(wi, reply) == 1 })
}

// The window between dude's reply in its thread and its thread_replied
// event (or an event lost): the pull request sync lists dude's own
// "Withdrawn: …" with nothing recorded for it. It is still dude's, by the
// token's login and syncThreads' words: no Chat message, no addressed
// record, no wake. A person's reply in the same thread still arrives.
func TestDudesUnrecordedThreadReplyIsNotAPersonsReply(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	// A PAT whose owner comments, from the first sync on: the login read
	// (cached per credential for the process) is pat-owner's. A secret of
	// this test's own keeps another test's cached login out.
	w.gh.Set(func(s *fakegithub.Server) { s.Poster = "pat-owner" })
	mustExec(t, w.owner, `UPDATE forge_credentials SET secret = 'ghp_window_' || organization_id WHERE organization_id = $1`, w.org)
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	roots := w.dudeLineComments(n)
	if len(roots) != 1 || roots[0].Author != "pat-owner" {
		t.Fatalf("dude's thread, by the PAT's owner: %+v", roots)
	}
	root := roots[0]
	var finding string
	if err := w.owner.QueryRow(t.Context(), `SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi).Scan(&finding); err != nil {
		t.Fatalf("the open finding: %v", err)
	}
	before := w.mentions(wi)
	w.must(wi, "dismiss_finding", `{"id":"`+finding+`","reason":"wrong: the caller checks for nil at greet.go:3"}`)
	w.agentsDone(wi, "approved at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 2 && w.waitingOnPR(wi) })
	var withdrawn int64
	for _, c := range repliesTo(w.gh.Pull(n), root.ID) {
		if c.Author == "pat-owner" && c.Body == "Withdrawn: the caller checks for nil at greet.go:3." {
			withdrawn = c.ID
		}
	}
	if withdrawn == 0 {
		t.Fatalf("no Withdrawn reply by pat-owner: %+v", repliesTo(w.gh.Pull(n), root.ID))
	}
	// Its thread_replied never recorded, and the reply never listed yet:
	// only the window path (OwnThreadReplies) can tell it is dude's.
	id := fmt.Sprintf("line-comment-%d", withdrawn)
	mustExec(t, w.owner, `DELETE FROM events WHERE task_id = $1 AND event_type = 'review.thread_replied'`, wi)
	mustExec(t, w.owner, `DELETE FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented' AND payload->>'feedbackId' = $2`, wi, id)
	w.syncs(5)
	if m := w.replyMessages(wi, withdrawn); m != 0 || w.mentions(wi) != before {
		t.Errorf("dude's unrecorded Withdrawn reply reached the conductor: %d, %d messages", m, w.mentions(wi)-before)
	}
	if c := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'addressed' = 'conductor'`, wi); c != 0 {
		t.Errorf("%d comments recorded as addressed to the conductor", c)
	}
	if w.woke(wi, "Withdrawn: ") {
		t.Error("the conductor was woken by dude's own reply")
	}
	reply := w.gh.ReplyInThread(n, "alice", root.ID, "Thanks, agreed.")
	w.until("alice's reply in Chat", func() bool { w.sync(); return w.replyMessages(wi, reply) == 1 })
}

// A reply recorded before dude's thread was known (its review_submitted
// not yet read as listing it) is recorded as plain feedback; edited once
// the thread is known, it reaches the conductor once, by the edited path.
func TestAReplyRecordedBeforeItsThreadWasKnownIsDeliveredOnceEdited(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, _ := w.findingThenVerdict(func() string { return "still" })
	root := w.dudeLineComments(n)[0]
	// dude's review is not read as listing its thread yet: its comments
	// are moved aside, and back once the reply is recorded.
	move := func(from, to string) {
		mustExec(t, w.owner, `UPDATE events SET payload = jsonb_set(payload - $2::text, ARRAY[$3::text], payload->$2::text)
			WHERE task_id = $1 AND event_type = 'pull_request.review_submitted' AND payload ? $2::text`, wi, from, to)
	}
	move("comments", "hidden")
	reply := w.gh.ReplyInThread(n, "alice", root.ID, "This is intentional.")
	w.until("the reply recorded", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
			AND payload->>'feedbackId' = $2`, wi, fmt.Sprintf("line-comment-%d", reply)) == 1
	})
	if m := w.replyMessages(wi, reply); m != 0 {
		t.Fatalf("delivered before its thread was known: %d", m)
	}
	move("hidden", "comments")
	w.gh.EditComment(n, reply, "This is intentional: see greet.go:3.")
	w.until("the edited reply in Chat", func() bool { w.sync(); return w.replyMessages(wi, reply) == 1 })
	w.syncs(3)
	if m := w.replyMessages(wi, reply); m != 1 {
		t.Errorf("the reply came %d times", m)
	}
}

// A writing task: a plain reply in a thread whose last word is the
// conductor's reply_on_pull_request is fixer feedback, as before; only
// the @mention reached the conductor.
func TestAWritingDeliveryRoutesAReplyToItsConductorAsBefore(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	line := w.gh.LineComment(1, "bob", "FACTORY.md", "@dude is this needed?")
	w.until("the mention", func() bool { w.sync(); return w.mentions(task) == 1 })
	w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":"Yes: the factory reads it.","in_reply_to":"line-comment-%d"}`, line))
	lines := w.gh.Pull(1).LineComments
	ours := lines[len(lines)-1]
	if ours.Author != fakegithub.Login || ours.InReplyTo != line {
		t.Fatalf("the conductor's reply: %+v", ours)
	}
	// Answering the conductor's reply: GitHub points it at the thread's
	// root, bob's line comment, not at the conductor's reply.
	reply := w.gh.ReplyInThread(1, "bob", ours.ID, "Then please document why it is needed.")
	if got := repliesTo(w.gh.Pull(1), line); len(got) != 2 || got[1].ID != reply {
		t.Fatalf("bob's reply is not in reply to the root: %+v", got)
	}
	w.until("a fixer for the reply", func() bool { w.sync(); return w.fixes(task) == 1 })
	w.syncs(3)
	if m := w.replyMessages(task, reply); m != 0 || w.mentions(task) != 1 {
		t.Errorf("the reply reached the conductor: %d, %d messages", m, w.mentions(task))
	}
	var addressed string
	if err := w.owner.QueryRow(t.Context(), `SELECT COALESCE(payload->>'addressed', '') FROM events WHERE task_id = $1
		AND event_type = 'pull_request.commented' AND payload->>'feedbackId' = $2`, task, fmt.Sprintf("line-comment-%d", reply)).Scan(&addressed); err != nil {
		t.Fatalf("the reply's record: %v", err)
	}
	if addressed != "" {
		t.Errorf("the reply was recorded as addressed to %q", addressed)
	}
	if strings.Contains(strings.Join(w.ids(`SELECT text FROM directives WHERE task_id = $1`, task), "\n"), "Then please document") {
		t.Error("the conductor was told the reply")
	}
}
