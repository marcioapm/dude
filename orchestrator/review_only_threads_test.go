package orchestrator_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// findingThenVerdict starts a review-only task on alice's pull request
// adding greet.md: its first round's lens files one medium finding on it,
// later rounds answer F1 with verdict() ("fixed" or "still"). It waits for
// REQUEST_CHANGES at H1; task, number, H1.
func (w *world) findingThenVerdict(verdict func() string) (string, int, string) {
	w.t.Helper()
	w.gh.SetChecks("success")
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	h1 := w.gh.CommitFiles("feature/greet", "alice's greeting", map[string]string{"greet.md": "hello\n"})
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	round := 0
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] != "review" {
			return scripted(spec)
		}
		round++
		if round == 1 {
			return fakelux.Behaviour{Reply: "```yaml\nseverity: medium\ncategory: correctness\nfile: greet.md\nline: 1\ntitle: The greeting is rude" +
				"\ndescription: D.\nscenario: S.\nevidence_kind: command\nevidence: cat greet.md\n```\n"}
		}
		return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: " + verdict() + "\n```\n"}
	}
	w.startDelivery(wi)
	// On the fake lux, through the syncer: a later round can resume it.
	w.until("changes requested at H1", func() bool { w.sync(); return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[0].State != "CHANGES_REQUESTED" {
		w.t.Fatalf("at H1: %+v", got)
	}
	if c := w.dudeLineComments(n); len(c) != 1 {
		w.t.Fatalf("dude's inline comments at H1: %+v", c)
	}
	return wi, n, h1
}

// dudeLineComments are pull request n's line comments by dude's account
// that start a thread.
func (w *world) dudeLineComments(n int) []fakegithub.Comment {
	var out []fakegithub.Comment
	for _, c := range w.gh.Pull(n).LineComments {
		if c.Author == fakegithub.Login && c.InReplyTo == 0 {
			out = append(out, c)
		}
	}
	return out
}

func repliesTo(p *fakegithub.Pull, id int64) []fakegithub.Comment {
	var out []fakegithub.Comment
	for _, c := range p.LineComments {
		if c.InReplyTo == id {
			out = append(out, c)
		}
	}
	return out
}

// A finding fixed at H2, after each later verdict (H2's, then H3's):
// dude's thread on it has the one reply it should and is resolved as it
// should be. Fixed, "Fixed at <H2>." and resolved, never posted again; a
// thread a person resolved is left alone; a thread read that fails never
// holds the verdict, is recorded once, and is not retried at H3's.
func TestDudesThreadsAfterAFix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		threads  int  // ThreadsStatus until H2's verdict
		resolved bool // by a person before the push
		reply    bool // dude's "Fixed at <H2>."
		failures int
	}{
		{"fixed", 0, false, true, 0},
		{"resolved by a person", 0, true, false, 0},
		{"sync failure", 502, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.reRounds(time.Second)
			wi, n, _ := w.findingThenVerdict(func() string { return "fixed" })
			root := w.dudeLineComments(n)[0]
			thread := w.gh.ThreadID(root.ID)
			w.gh.Set(func(s *fakegithub.Server) {
				s.ThreadsStatus, s.ResolvedThreads = tc.threads, map[string]bool{thread: tc.resolved}
			})
			h2 := w.push("polite.md")
			w.agentsDone(wi, "approved at H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
			if got := reviewsAt(w.dudeReviews(wi, n), h2); got[0].State != "APPROVED" {
				t.Fatalf("at H2: %+v", got)
			}
			w.gh.Set(func(s *fakegithub.Server) { s.ThreadsStatus = 0 })
			h3 := w.push("third.md")
			w.agentsDone(wi, "reviewed H3", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h3)) == 1 && w.waitingOnPR(wi) })
			r := repliesTo(w.gh.Pull(n), root.ID)
			if want := map[bool]int{true: 1}[tc.reply]; len(r) != want || want == 1 && r[0].Body != "Fixed at "+h2[:7]+"." {
				t.Fatalf("replies in dude's thread: %+v", r)
			}
			if w.gh.ResolvedThreads[thread] != (tc.reply || tc.resolved) {
				t.Fatalf("thread resolved: %v", w.gh.ResolvedThreads[thread])
			}
			if e := w.events(wi, delivery.EvReviewThreadSyncFailed); e != tc.failures {
				t.Fatalf("%d thread sync failures recorded, want %d", e, tc.failures)
			}
		})
	}
}

// Still open at H2: no new inline comment; the body lists it as still
// open from H1, with a link; the thread stays open. Round 2's lens is told
// the author pushed (not that a fix was made): fresh, in its prompt
// (phases/sync.go); resumed (resumeRounds), in the resume's message
// (roundMessageTx). Both rounds run on the fake lux, which records them.
func TestAStillOpenFindingIsListedNotRepeated(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint("resume=", resume), func(t *testing.T) {
			w := newWorld(t)
			w.reRounds(time.Second)
			w.onModel("reviewer", "llm-review") // not scripted: lux keeps the prompt
			if resume {
				mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
			}
			wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
			root := w.dudeLineComments(n)[0]
			h2 := w.push("more.md")
			w.until("reviewed H2", func() bool { w.sync(); return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
			got := reviewsAt(w.dudeReviews(wi, n), h2)[0]
			if got.State != "CHANGES_REQUESTED" || !strings.Contains(got.Body, "**Still open from "+h1[:7]+"**") ||
				!strings.Contains(got.Body, "The greeting is rude") || !strings.Contains(got.Body, "#discussion_r") {
				t.Fatalf("at H2: %+v", got)
			}
			if c := w.dudeLineComments(n); len(c) != 1 {
				t.Fatalf("the open finding was commented on inline again: %+v", c)
			}
			if r := repliesTo(w.gh.Pull(n), root.ID); len(r) != 0 || w.gh.ResolvedThreads[w.gh.ThreadID(root.ID)] {
				t.Fatalf("an open finding's thread was answered or resolved: %+v", r)
			}
			told := false
			for _, r := range w.lux.Runs() {
				said := []string{r.Prompt()}
				if resume {
					said = append(w.lux.Inputs(r.ID), w.lux.ResumeInputs(r.ID)...)
				}
				told = told || slices.ContainsFunc(said, func(s string) bool { return strings.Contains(s, "The author has pushed since your last review") })
			}
			if !told {
				t.Fatal("round 2's lens was not told the author pushed")
			}
			if c := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2
				AND continues_run_id IS NOT NULL`, wi, h2); (c > 0) != resume {
				t.Fatalf("%d lens Runs at H2 went on from H1 (resume %v)", c, resume)
			}
		})
	}
}

// The conductor withdraws the only finding at H: the verdict is weighed
// again at H, no new round, and approves.
func TestAWithdrawalApprovesAtTheSameHead(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	root := w.dudeLineComments(n)[0]
	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi)
	var finding string
	_ = w.owner.QueryRow(t.Context(), `SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi).Scan(&finding)
	w.must(wi, "dismiss_finding", `{"id":"`+finding+`","reason":"withdrawn: the author showed it is intended"}`)
	w.agentsDone(wi, "approved at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 2 && w.waitingOnPR(wi) })
	if got := reviewsAt(w.dudeReviews(wi, n), h1); got[1].State != "APPROVED" {
		t.Fatalf("after the withdrawal: %+v", got)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); r != runs {
		t.Fatalf("%d lens Runs for a withdrawal", r-runs)
	}
	r := repliesTo(w.gh.Pull(n), root.ID)
	if len(r) != 1 || r[0].Body != "Withdrawn: the author showed it is intended." {
		t.Fatalf("replies in dude's thread: %+v", r)
	}
	if !w.gh.ResolvedThreads[w.gh.ThreadID(root.ID)] {
		t.Fatal("the withdrawn finding's thread is not resolved")
	}
}
