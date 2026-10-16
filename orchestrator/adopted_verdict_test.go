package orchestrator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// testingAdopted takes over a person's pull request to review, with the
// tester on and played by model, on a task of two criteria; its number.
func (w *world) testingAdopted(model string) (string, int) {
	w.t.Helper()
	w.onModel("qa_browser", model)
	mustExec(w.t, w.owner, `UPDATE projects SET delivery_policy = '{"requiredReviewers":["correctness"],"test":true}' WHERE id = $1`, w.project)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReview)
	mustExec(w.t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.startDelivery(wi)
	w.until("waiting on the adopted pull request", func() bool { return w.waitingOnPR(wi) })
	return wi, n
}

// verdictComments are the comments on pull request n that carry the
// task's verdict: dude's, ending in its marker.
func (w *world) verdictComments(wi string, n int) []fakegithub.Comment {
	var out []fakegithub.Comment
	for _, c := range w.gh.Pull(n).Comments {
		if strings.HasSuffix(strings.TrimSpace(c.Body), forge.VerdictMarker(wi)) {
			out = append(out, c)
		}
	}
	return out
}

// verdictEvents are the task's pull_request.verdict_commented payloads, in order.
func (w *world) verdictEvents(wi string) []map[string]any {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE task_id = $1
		AND event_type = 'pull_request.verdict_commented' ORDER BY cursor`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var p map[string]any
		if err := rows.Scan(&raw); err != nil {
			w.t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &p)
		out = append(out, p)
	}
	return out
}

// A person's pull request dude took over gets the test's verdict as one
// comment of dude's, never an edit of the person's description: the
// section a pull request dude opens carries, under a header, ending in the
// marker. A re-test after its fix edits that comment, and so does a new
// head dude pushes, which makes the verdict stale.
func TestAnAdoptedPullRequestGetsTheVerdictAsOneCommentKeptUpToDate(t *testing.T) {
	w := newWorld(t)
	wi, n := w.testingAdopted("fake/test-fail-once")
	if tests := w.phaseRuns(wi, "test"); tests != 2 {
		t.Fatalf("%d tests, want a failed one and its re-test", tests)
	}
	if body := w.gh.Pull(n).Body; body != "" {
		t.Fatalf("the person's description was changed:\n%s", body)
	}
	got := w.verdictComments(wi, n)
	if len(got) != 1 || len(w.gh.Pull(n).Comments) != 1 {
		t.Fatalf("verdict comments %+v of %d, want one", got, len(w.gh.Pull(n).Comments))
	}
	c := got[0]
	head := w.gh.SHA("feature/greet")
	for _, want := range []string{"dude's test verdict for this pull request", "## Verification", "Tested at `" + head[:7] + "`.",
		"- **pass** — it greets", "- **pass** — it says goodbye"} {
		if !strings.Contains(c.Body, want) {
			t.Errorf("the verdict comment has no %q:\n%s", want, c.Body)
		}
	}
	if c.Author != fakegithub.Login || c.UpdatedAt == "" || strings.Contains(c.Body, "fail") || strings.Contains(c.Body, "Stale") {
		t.Errorf("want dude's comment, edited from the failed test to the passing one:\n%+v", c)
	}
	events := w.verdictEvents(wi)
	if len(events) != 2 || events[0]["edited"] != false || events[1]["edited"] != true ||
		events[0]["commentId"] != float64(c.ID) || events[1]["commentId"] != float64(c.ID) || events[1]["head"] != head {
		t.Fatalf("verdict events %v, want posted then edited, comment %d at %s", events, c.ID, head)
	}

	// A fix of feedback moves the head past what was tested: the same
	// comment says so.
	fixed := w.prFixPushed(wi)
	w.until("the verdict marked stale", func() bool {
		got := w.verdictComments(wi, n)
		return len(got) == 1 && strings.Contains(got[0].Body, "**Stale:**")
	})
	if got := w.verdictComments(wi, n); got[0].ID != c.ID || !strings.Contains(got[0].Body, "Tested at `"+head[:7]+"`.") {
		t.Errorf("after the fix to %s: %+v, want comment %d still saying what was tested", fixed[:7], got[0], c.ID)
	}
}

// A verdict comment someone deleted is posted afresh at the next verdict,
// and that one is kept up to date from then on.
func TestADeletedVerdictCommentIsPostedAgain(t *testing.T) {
	w := newWorld(t)
	wi, n := w.testingAdopted("fake/scripted")
	first := w.verdictComments(wi, n)
	if len(first) != 1 {
		t.Fatalf("verdict comments %+v, want one", first)
	}
	w.gh.DeleteComment(n, first[0].ID)
	w.prFixPushed(wi)
	w.until("a new verdict comment", func() bool { return len(w.verdictComments(wi, n)) == 1 })
	again := w.verdictComments(wi, n)[0]
	if again.ID == first[0].ID || !strings.Contains(again.Body, "**Stale:**") {
		t.Fatalf("after deleting %d: %+v", first[0].ID, again)
	}
	events := w.verdictEvents(wi)
	if last := events[len(events)-1]; last["commentId"] != float64(again.ID) || last["edited"] != false {
		t.Fatalf("last verdict event %v, want the new comment %d posted", last, again.ID)
	}
}

// GitHub refusing the comment never holds the delivery up: it waits on
// the pull request as it would, the failure is in the ledger, and the next
// verdict tries again.
func TestAVerdictCommentGitHubRefusesDoesNotStopTheDelivery(t *testing.T) {
	w := newWorld(t)
	comments := func(r *http.Request) bool {
		return r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments") && strings.Contains(r.URL.Path, "/issues/") ||
			r.Method == "PATCH" && strings.Contains(r.URL.Path, "/issues/comments/")
	}
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if comments(r) {
				return http.StatusBadGateway
			}
			return 0
		}
	})
	wi, n := w.testingAdopted("fake/scripted")
	if got := w.verdictComments(wi, n); len(got) != 0 {
		t.Fatalf("verdict comments %+v while GitHub refused them", got)
	}
	events := w.verdictEvents(wi)
	if len(events) != 1 || !strings.Contains(events[0]["error"].(string), "502") || events[0]["commentId"] != nil {
		t.Fatalf("verdict events %v, want the refusal recorded", events)
	}

	w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
	w.prFixPushed(wi)
	w.until("the verdict comment, once GitHub takes it", func() bool { return len(w.verdictComments(wi, n)) == 1 })
}
