package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// conductedPR is a conducted direct delivery waiting on its open pull
// request, its reviewers scripted by play (phase, its index among the
// Runs of that phase since arming, from 1) and the phases since recorded.
type conductedPR struct {
	*world
	task   string
	mu     sync.Mutex
	phases []string
}

func newConductedPR(t *testing.T, play func(phase string, n int, b *fakelux.Behaviour)) *conductedPR {
	w := conducting(t)
	w.toolsWithSync()
	w.onModel("reviewer", "llm-reviewer")
	c := &conductedPR{world: w, task: w.prOpen(w.task(), prAt{direct: true, test: true})}
	if status, out := w.chat(c.task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(c.task) })
	counts := map[string]int{}
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		phase, _ := labels["dude.phase"].(string)
		b := scripted(spec)
		if phase == "" {
			return b
		}
		c.mu.Lock()
		c.phases = append(c.phases, phase)
		counts[phase]++
		n := counts[phase]
		c.mu.Unlock()
		play(phase, n, &b)
		return b
	}
	return c
}

// nextAtRound is the conductor answering a review round of a conducted
// fix loop with decide next (what Deliver would do), if the delivery is
// parked on one: each round of such a loop is the conductor's. For a
// condition polled by until.
func (w *world) nextAtRound(task string) {
	w.t.Helper()
	if w.decisionAt(task) == delivery.PointReviewed {
		w.must(task, "decide", `{"action":"next"}`)
	}
}

func (c *conductedPR) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.phases)
}

// commented is a comment on the pull request, read, and the delivery at
// the conductor's decision on it: the cursor before the decision.
func (c *conductedPR) commented() int64 {
	c.gh.Comment(1, "alice", "Please rename the greeting.")
	c.until("feedback to decide on", func() bool { c.sync(); return c.decisionAt(c.task) == delivery.PointPRFeedback })
	return c.cursor(c.task)
}

// In a conducted delivery a direct pull request fix's narrow loop is the
// conductor's: each review round parks for it (after_review), with the
// round wake, rather than looping by the policy's rules. With then, the
// fix's review runs the queued reviewers with no decision after the fix.
// (The loop's bounds stay until step 3.5: set wide here.)
func TestDirectAConductedLoopsNarrowReviewWakesTheConductorEachRound(t *testing.T) {
	c := newConductedPR(t, func(phase string, n int, b *fakelux.Behaviour) {
		switch {
		case phase == "review" && n <= 2:
			b.Reply = "One problem:\n\n" + reviewFinding(fmt.Sprintf("The greeting is still wrong %d", n))
		case phase == "review":
			b.Reply = "Reviewed the fix.\n\n" + fakeagent.Verdict
		}
	})
	setPolicy(c.world, c.task, `{"maxPrFixReviewIterations":9,"maxReviewIterations":9,"maxAttemptsPerFinding":5}`)
	from := c.commented()
	c.must(c.task, "start_phase", `{"phase":"fix","note":"Rename it.","then":{"review":["tests"]}}`)
	for round := 1; round <= 3; round++ {
		c.until(fmt.Sprintf("round %d of the narrow loop", round), func() bool {
			c.sync()
			return c.escalationReason(c.task) != "" || c.decisionAt(c.task) == delivery.PointReviewed &&
				len(c.awaited(c.task, from)) == round
		})
		if r := c.escalationReason(c.task); r != "" {
			t.Fatalf("escalated %q after %v\n%s", r, c.seen(), c.describeRuns())
		}
		if round < 3 {
			c.must(c.task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
		}
	}
	want := []string{delivery.PointReviewed, delivery.PointReviewed, delivery.PointReviewed}
	if got := c.awaited(c.task, from); !slices.Equal(got, want) {
		t.Errorf("decisions: %v, want %v", got, want)
	}
	if got := c.seen(); !slices.Equal(got, []string{"fix", "review", "fix", "review", "fix", "review"}) {
		t.Errorf("phases: %v", got)
	}
	if got := c.reviewersSince(c.task, from); fmt.Sprint(got) != `[["tests"] ["tests"] ["tests"]]` {
		t.Errorf("each round's reviewers: %v, want the queued tests", got)
	}
	if n := c.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, c.task, delivery.WakeRound); n != 3 {
		t.Errorf("%d round wakes, want 3", n)
	}
	if n := c.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2`,
		c.task, delivery.RangePullRequestFix); n != 3 {
		t.Errorf("%d reviews over the fix's range, want 3\n%s", n, c.describeRuns())
	}
}

// A conducted direct fix the conductor starts undirected (decide next on
// the comment) is reviewed by the round's own reviewers, as before; its
// review round parks for the conductor.
func TestDirectAConductedUndirectedFixIsReviewedByTheRoundsReviewers(t *testing.T) {
	c := newConductedPR(t, func(phase string, n int, b *fakelux.Behaviour) {})
	from := c.commented()
	c.must(c.task, "decide", `{"action":"next"}`)
	c.until("the fix's review round", func() bool { c.sync(); return c.decisionAt(c.task) == delivery.PointReviewed })
	if got := c.awaited(c.task, from); !slices.Equal(got, []string{delivery.PointReviewed}) {
		t.Errorf("decisions: %v, want [%s]", got, delivery.PointReviewed)
	}
	if got := c.reviewersSince(c.task, from); len(got) != 1 || got[0] == `["tests"]` {
		t.Errorf("the round's reviewers: %v", got)
	}
	var stateRound int
	_ = c.owner.QueryRow(context.Background(), `SELECT COALESCE((state->>'round')::int, 0) FROM workflow_runs WHERE task_id = $1`, c.task).Scan(&stateRound)
	if stateRound != 1 {
		t.Errorf("the narrow loop's round %d, want 1", stateRound)
	}
}

// then.wake after_fix on an open pull request: the conductor is woken with
// the fix unreviewed on it, and its publish is refused until the queued
// review runs (I-3).
func TestDirectAConductedFixWokenAfterTheFixCannotPublishPastTheUnreviewedHead(t *testing.T) {
	c := newConductedPR(t, func(phase string, n int, b *fakelux.Behaviour) {})
	c.commented()
	c.must(c.task, "start_phase", `{"phase":"fix","note":"Rename it.","then":{"review":["tests"],"wake":"after_fix"}}`)
	c.until("after the fix, the review queued", func() bool { c.sync(); return c.fixedWith(c.task, `["tests"]`) })
	if n := c.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $3 AND strpos(line, $2) > 0`,
		c.task, delivery.MustText("wakes/facts/pipeline-unreviewed-push").Render(nil), delivery.WakeRound); n != 1 {
		t.Errorf("the after-fix wake does not say the push is unreviewed")
	}
	c.refused(c.task, "publish", `{"message":"one more thing"}`, "review")
	c.must(c.task, "decide", `{"action":"next"}`)
	c.until("the queued review's round", func() bool { c.sync(); return c.decisionAt(c.task) == delivery.PointReviewed })
}
