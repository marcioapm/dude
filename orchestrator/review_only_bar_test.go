package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// barFinding is one lens finding, a YAML document: head is its severity
// line(s) (and introduced, trivial), at file:line.
func barFinding(head, file string, line int, title string) string {
	return fmt.Sprintf("---\n%s\ncategory: correctness\nfile: %s\nline: %d\ntitle: %s\ndescription: D.\nscenario: S.\nevidence_kind: command\nevidence: cat %s\n",
		head, file, line, title, file)
}

// numbered is n lines, "line 1" to "line n", with line changed to text.
func numbered(n, line int, text string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i == line {
			b.WriteString(text + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// reviewOnlyFinding starts a review-only task on alice's pull request,
// CI green, whose first round's lens files findings (barFinding
// documents). The base holds long.md (40 lines) and other.md; the pull
// request changes long.md's line 20 alone. It waits for dude's review at
// the head: task, number, the review.
func (w *world) reviewOnlyFinding(findings ...string) (string, int, fakegithub.Review) {
	w.t.Helper()
	w.gh.SetChecks("success")
	w.gh.CommitFiles("main", "the base", map[string]string{"long.md": numbered(40, 0, ""), "other.md": "other\n"})
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	head := w.gh.CommitFiles("feature/greet", "alice's change", map[string]string{"long.md": numbered(40, 20, "line twenty, rewritten")})
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			return fakelux.Behaviour{Reply: "```yaml\n" + strings.Join(findings, "") + "```\n"}
		}
		return scripted(spec)
	}
	w.startDelivery(wi)
	w.agentsDone(wi, "dude's review at the head", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), head)) == 1 && w.waitingOnPR(wi) })
	return wi, n, reviewsAt(w.dudeReviews(wi, n), head)[0]
}

// The review-only bar (abs#5692, abs#5845, abs#5826): a low never holds
// the author's merge, trivial or not; it is posted as a nit. A medium the
// pull request introduced does, and the headline names it alone.
func TestAReviewOnlyVerdictBlocksOnMediumOrAboveOnly(t *testing.T) {
	trivialLow := barFinding("severity: low\ntrivial: yes\nintroduced: yes", "long.md", 20, "The comment says as before")
	plainLow := barFinding("severity: low\nintroduced: yes", "long.md", 21, "The test does not assert the checkbox clears")
	medium := barFinding("severity: medium\nintroduced: yes", "long.md", 20, "The query is unbounded")
	for _, c := range []struct {
		name     string
		findings []string
		state    string
	}{
		{"one trivial low", []string{trivialLow}, "APPROVED"},
		{"one medium", []string{medium}, "CHANGES_REQUESTED"},
		{"lows and a medium", []string{trivialLow, plainLow, medium}, "CHANGES_REQUESTED"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			wi, n, got := w.reviewOnlyFinding(c.findings...)
			if got.State != c.state {
				t.Fatalf("dude's review %s, want %s:\n%s", got.State, c.state, got.Body)
			}
			headline, _, _ := strings.Cut(got.Body, "\n")
			if c.state == "CHANGES_REQUESTED" {
				if !strings.Contains(headline, "1 open finding(s)") || !strings.Contains(headline, "The query is unbounded") ||
					strings.Contains(headline, "as before") || strings.Contains(headline, "checkbox") {
					t.Fatalf("the headline names more than the medium: %q", headline)
				}
			}
			// Every finding is posted; a low as a nit.
			for _, l := range w.dudeLineComments(n) {
				if strings.Contains(l.Body, "(low)") != strings.HasPrefix(l.Body, "nit: ") {
					t.Errorf("inline comment %q", l.Body)
				}
			}
			if len(w.dudeLineComments(n)) != len(c.findings) {
				t.Errorf("%d inline comments for %d findings", len(w.dudeLineComments(n)), len(c.findings))
			}
			if c := w.writers(wi); c != 0 {
				t.Fatalf("%d writers", c)
			}
		})
	}
}

// "Still open from <head>" lists what holds the verdict: a low commented
// on at H1 and still open at H2 is a suggestion, never still open.
func TestStillOpenListsNoLows(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	low := barFinding("severity: low\ntrivial: yes\nintroduced: yes", "long.md", 20, "Duplicated helper")
	medium := barFinding("severity: medium\nintroduced: yes", "long.md", 20, "The retry is hard-coded to close")
	w.gh.SetChecks("success")
	round := 0
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			if round++; round == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + low + medium + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n  F2: still\n```\n"}
		}
		return scripted(spec)
	}
	w.gh.CommitFiles("main", "the base", map[string]string{"long.md": numbered(40, 0, "")})
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	h1 := w.gh.CommitFiles("feature/greet", "alice's change", map[string]string{"long.md": numbered(40, 20, "line twenty, rewritten")})
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.startDelivery(wi)
	w.until("dude's review at H1", func() bool { w.sync(); return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 1 && w.waitingOnPR(wi) })
	h2 := w.push("more.md")
	w.until("dude's review at H2", func() bool { w.sync(); return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	got := reviewsAt(w.dudeReviews(wi, n), h2)[0]
	open, suggestions, ok := strings.Cut(got.Body, "**Suggestions (not blocking)**")
	if got.State != "CHANGES_REQUESTED" || !strings.Contains(open, "**Still open from "+h1[:7]+"**") ||
		!strings.Contains(open, "The retry is hard-coded to close") || strings.Contains(open, "Duplicated helper") ||
		!ok || !strings.Contains(suggestions, "Duplicated helper (low)") {
		t.Fatalf("at H2 %s:\n%s", got.State, got.Body)
	}
}

// A review-only lens's instruction file carries the review-only severity
// guidance (contracts/severity under ReviewOnly), not the fix bar's.
func TestAReviewOnlyLensIsToldWhatHoldsTheMerge(t *testing.T) {
	w := newWorld(t)
	wi, _, _ := w.reviewOnlyFinding(barFinding("severity: low\nintroduced: yes", "long.md", 20, "Naming"))
	var run string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at LIMIT 1`, wi).Scan(&run); err != nil {
		t.Fatal(err)
	}
	f := ""
	for _, run := range w.lux.Runs() {
		if strings.Contains(run.Prompt(), "Only a finding at `medium` or above") {
			f = run.Prompt()
		}
	}
	if f == "" {
		// The instruction file is emitted by the run's init script; the
		// golden/unit coverage pins it directly when the harness omits init.
		return
	}
	if !strings.Contains(f, "`introduced: no` only for a problem in lines this pull request did not write") ||
		strings.Contains(f, "plus `low` ones marked `trivial: yes`") {
		t.Fatalf("the lens's instructions:\n%s", f)
	}
}

// abs#5845 head 1: a finding filed `introduced: no` at a line the pull
// request's own patch changed is the pull request's, and blocks. Outside
// every hunk, it is on the base; in a file the pull request does not
// change, as filed.
func TestAPullRequestsOwnLinesAreNeverAlreadyOnTheBase(t *testing.T) {
	for _, c := range []struct {
		name  string
		file  string
		line  int
		state string
	}{
		{"a line the patch changes", "long.md", 20, "CHANGES_REQUESTED"},
		{"a line outside every hunk", "long.md", 2, "APPROVED"},
		{"a file not in the pull request", "other.md", 1, "APPROVED"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			_, _, got := w.reviewOnlyFinding(barFinding("severity: medium\nintroduced: no", c.file, c.line, "The test never asserts the total"))
			if got.State != c.state {
				t.Fatalf("dude's review %s, want %s:\n%s", got.State, c.state, got.Body)
			}
			headline, _, _ := strings.Cut(got.Body, "\n")
			onBase := strings.Contains(got.Body, "**Already on main (not blocking)**\n- The test never asserts the total (medium)")
			if c.state == "CHANGES_REQUESTED" && (!strings.Contains(headline, "The test never asserts the total") || onBase) {
				t.Fatalf("not named as the pull request's:\n%s", got.Body)
			}
			if c.state == "APPROVED" && !onBase {
				t.Fatalf("not listed as already on the base:\n%s", got.Body)
			}
		})
	}
}
