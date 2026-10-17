package orchestrator_test

// A pull request's fix checked on its candidate branch while GitHub merges
// the base into the pull request's branch (its Update branch, a person's
// click, dude's own update in another sync): promotion merges the
// candidate's head in on top rather than refusing the moved branch. Any
// other move of the branch still stops for a person (pull_request_moved).

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// The production race under candidates: the base is merged into the pull
// request's branch while its fix is tested on the candidate. The pull
// request ends at GitHub's merge of the candidate's head onto that base
// merge, recorded as the delivery's and the pull request's head, and its
// earlier reviewer is asked to look at that merge. Nobody is asked to
// decide.
func TestAPromotionMergesTheCandidateInOverABaseMergeThatLandedDuringItsCheck(t *testing.T) {
	w := newPRFixWorld(t)
	w.gh.Review(1, "jerv", "COMMENTED")
	var x, checked string
	w.onTest = func() {
		checked = w.candidate()
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		x = w.gh.MergeInto(w.branch, "main", nil)
		w.sync()
	}
	w.comment("Please rename the greeting.")
	w.untilPromoted()
	if r := w.escalationReason(w.wi); r != "" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	m := w.gh.SHA(w.branch)
	if got := w.gh.Parents(x); len(got) != 2 || got[0] != w.before {
		t.Fatalf("GitHub's base merge %.8s has parents %v, want the pull request's %.8s first", x, got, w.before)
	}
	if got := w.gh.Parents(m); checked == "" || !slices.Equal(got, []string{x, checked}) {
		t.Fatalf("the pull request's head %.8s has parents %v, want the base merge %.8s then the checked candidate %.8s", m, got, x, checked)
	}
	w.headsAre(m)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'
		AND payload->>'from' = $2 AND payload->>'to' = $3`, w.wi, x, m); n != 1 {
		t.Errorf("%d moves recorded from the base merge to the candidate's merge", n)
	}
	asked := false
	for _, e := range w.rerequests(w.wi) {
		asked = asked || e["head"] == m && slices.Contains(strs(e["reviewersRequested"]), "jerv")
	}
	if !asked {
		t.Errorf("jerv was not asked to review the merge %.8s: %v", m, w.rerequests(w.wi))
	}
	w.sync()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.pushed' AND payload->>'to' = $2`, w.wi, m); n != 0 {
		t.Errorf("the candidate's merge recorded as someone else's push %d times", n)
	}
}

// A person's own commit lands on the pull request's branch while the fix
// is checked: promotion refuses it as before, and the branch keeps it.
func TestAPromotionIsRefusedOverAPersonsCommitThatLandedDuringItsCheck(t *testing.T) {
	w := newPRFixWorld(t)
	var theirs string
	w.onTest = func() {
		theirs = w.gh.CommitFiles(w.branch, "A person's own fix", map[string]string{"PERSON.md": "mine\n"})
	}
	w.refusedOver(&theirs)
}

// A fix of two pull requests is checked while GitHub merges the base into
// target's and a person pushes to web's: target's base merge alone would be
// merged into, but web's push refuses the promotion before either moves.
func TestAPromotionAdmissibleInOneRepositoryButNotTheOtherMovesNeither(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var x, theirs string
	w.onTest = func() {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		x = w.gh.MergeInto(w.branch, "main", nil)
		theirs = w.web.CommitFiles(w.branch, "A person's own fix", map[string]string{"PERSON.md": "mine\n"})
	}
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	if at := w.gh.SHA(w.branch); x == "" || at != x {
		t.Errorf("target's pull request at %.8s, want the base merge %.8s kept", at, x)
	}
	if at := w.web.SHA(w.branch); theirs == "" || at != theirs {
		t.Errorf("web's pull request at %.8s, want the person's %.8s kept", at, theirs)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 0 {
		t.Errorf("%d moves recorded", n)
	}
}

// GitHub's merge of the base that also carries a person's edit (resolved
// in GitHub's editor) lands during the check: not only the base's, so
// promotion refuses it.
func TestAPromotionIsRefusedOverAnEvilBaseMergeThatLandedDuringItsCheck(t *testing.T) {
	w := newPRFixWorld(t)
	var x string
	w.onTest = func() {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		x = w.gh.MergeInto(w.branch, "main", map[string]string{"PERSON.md": "theirs\n"})
	}
	w.refusedOver(&x)
	if got := w.gh.Parents(x); len(got) != 2 || got[0] != w.before {
		t.Errorf("the evil merge %.8s has parents %v, want the pull request's %.8s first", x, got, w.before)
	}
}

// Someone merges the checked candidate's head into the pull request's
// branch themselves, over a base merge, during the check. dude never asked
// for that merge: promotion does not take it as its own.
func TestAPromotionDoesNotAdoptAPersonsMergeOfTheCandidate(t *testing.T) {
	w := newPRFixWorld(t)
	var m string
	w.onTest = func() {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		w.gh.MergeInto(w.branch, "main", nil)
		m = w.gh.MergeCommit(w.branch, w.candidate())
	}
	w.refusedOver(&m)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, w.wi, "pull_request.fix_merge_attempted"); n != 0 {
		t.Errorf("%d merge attempts recorded", n)
	}
}

// The base merged in during the check rewrites the file the fix writes:
// GitHub will not merge the candidate in (409), the branch keeps the base
// merge, and a person is asked, told why.
func TestAPromotionConflictingWithABaseMergeIsRefused(t *testing.T) {
	w := newPRFixWorld(t)
	var x string
	w.onTest = func() {
		w.gh.MergeCommit("main", w.gh.SHA(w.branch))
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{fakeagent.FixedFile: "the base's\n"})
		x = w.gh.MergeInto(w.branch, "main", nil)
	}
	w.refusedOver(&x)
	var why string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->'detail'->>'error', '') FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&why)
	if !strings.Contains(why, "main was merged into the task branch meanwhile") || !strings.Contains(why, "conflicts with that merge") {
		t.Errorf("the escalation says %q, want the base merge's conflict", why)
	}
}

// A person pushes to the pull request's branch between promotion judging
// a base merge on it and GitHub merging the candidate in: GitHub merges onto
// the person's commit. That merge is not adopted, and a person is asked,
// told what the merge's parents are.
func TestAPromotionsMergeOntoAPersonsCommitIsNotAdopted(t *testing.T) {
	w := newPRFixWorld(t)
	again, theirs := landOnce(isMergesCall, func(*http.Request) string {
		return w.gh.CommitFiles(w.branch, "A person's own fix", map[string]string{"PERSON.md": "mine\n"})
	})
	w.onGitHub(again)
	var x string
	w.onTest = func() {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		x = w.gh.MergeInto(w.branch, "main", nil)
	}
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	m := w.gh.SHA(w.branch)
	if got := w.gh.Parents(m); theirs() == "" || len(got) != 2 || got[0] != theirs() || w.gh.Parents(theirs())[0] != x {
		t.Fatalf("the branch's head %.8s has parents %v, want GitHub's merge onto the person's %.8s on the base merge %.8s", m, got, theirs(), x)
	}
	var why string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->'detail'->>'error', '') FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&why)
	if !strings.Contains(why, "automatic recovery is refused") || !strings.Contains(why, theirs()) {
		t.Errorf("the escalation says %q, want the unsafe merge onto %.8s", why, theirs())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 0 {
		t.Errorf("%d moves recorded", n)
	}
}

// The base merge lands between promotion's read of the pull request's
// branch and its move: GitHub refuses the fast-forward (422), and the
// candidate is merged in over the base merge.
func TestAPromotionMergesTheCandidateInOverABaseMergeThatLandedBeforeItsMove(t *testing.T) {
	w := newPRFixWorld(t)
	hook, landed := landOnce(isMoveOf(w.branch), func(*http.Request) string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		return w.gh.MergeInto(w.branch, "main", nil)
	})
	w.onGitHub(hook)
	var checked string
	w.onTest = func() { checked = w.candidate() }
	w.comment("Please rename the greeting.")
	w.untilPromoted()
	if r := w.escalationReason(w.wi); r != "" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	m, x := w.gh.SHA(w.branch), landed()
	if got := w.gh.Parents(m); checked == "" || !slices.Equal(got, []string{x, checked}) {
		t.Fatalf("the pull request's head %.8s has parents %v, want the base merge %.8s then the checked candidate %.8s", m, got, x, checked)
	}
	w.headsAre(m)
}

// Promotion's merge of the candidate lands on GitHub and its answer is
// lost: the step's retry finds the merge on the branch and adopts it, as
// the promotion recorded attempting it, sending no second merge.
func TestAPromotionsMergeWhoseAnswerWasLostIsAdoptedOnRetry(t *testing.T) {
	w := newPRFixWorld(t)
	var merges atomic.Int32
	w.onGitHub(func(r *http.Request) int {
		if isMergesCall(r) {
			merges.Add(1)
		}
		return 0
	})
	var x, checked string
	w.onTest = func() {
		checked = w.candidate()
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		x = w.gh.MergeInto(w.branch, "main", nil)
		w.gh.Set(func(s *fakegithub.Server) { s.LoseMergeResponses = 1 })
	}
	w.comment("Please rename the greeting.")
	w.untilPromoted()
	if r := w.escalationReason(w.wi); r != "" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	m := w.gh.SHA(w.branch)
	if got := w.gh.Parents(m); checked == "" || !slices.Equal(got, []string{x, checked}) {
		t.Fatalf("the pull request's head %.8s has parents %v, want the base merge %.8s then the checked candidate %.8s", m, got, x, checked)
	}
	if n := merges.Load(); n != 1 {
		t.Errorf("%d merge requests, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'commit' = $3`,
		w.wi, "pull_request.fix_merge_attempted", checked); n != 1 {
		t.Errorf("%d merge attempts recorded for the candidate's head, want 1", n)
	}
	w.headsAre(m)
}

// untilPromoted waits for the fix to reach the pull request, or the
// delivery to stop for a person.
func (w *prFixWorld) untilPromoted() {
	w.t.Helper()
	w.until("the fix on the pull request", func() bool {
		w.sync()
		s := w.taskStatus(w.wi)
		return s == "awaiting_input" || s == "review" && w.state("candidate") == "" && w.gh.SHA(w.branch) != w.before
	})
}

// refusedOver comments, waits for the delivery to stop for a person, and
// asserts it stopped as pull_request_moved with the branch at *found.
func (w *prFixWorld) refusedOver(found *string) {
	w.t.Helper()
	w.comment("Please rename the greeting.")
	w.until("a person asked", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		w.t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	if *found == "" || w.gh.SHA(w.branch) != *found {
		w.t.Errorf("the pull request's branch at %.8s, want %.8s kept", w.gh.SHA(w.branch), *found)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi); n != 0 {
		w.t.Errorf("%d moves recorded", n)
	}
}

// headsAre asserts the delivery and the pull request record head as the
// pull request's.
func (w *prFixWorld) headsAre(head string) {
	w.t.Helper()
	var heads map[string]string
	if err := json.Unmarshal([]byte(w.state("heads")), &heads); err != nil {
		w.t.Fatal(err)
	}
	if heads["target"] != head {
		w.t.Errorf("the delivery's head %.8s, want %.8s", heads["target"], head)
	}
	var stored string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(head_sha, '') FROM pull_requests WHERE task_id = $1`, w.wi).Scan(&stored)
	if stored != head {
		w.t.Errorf("the pull request recorded at %.8s, want %.8s", stored, head)
	}
}
