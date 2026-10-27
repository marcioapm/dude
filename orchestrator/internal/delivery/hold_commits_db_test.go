package delivery

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// heldTwice holds the merge fixture's delivery at its head (round 1), then
// commits taskCommits of the task's own to the branch, has GitHub merge
// main in (syncBeforePR), sets GitHub's answers with answer (given round
// 1's head), and holds it again at the merged head (round 2).
func heldTwice(t *testing.T, taskCommits int, answer func(s *fakegithub.Server, from string)) (first, second Hold) {
	t.Helper()
	f, st, sc := mergeFixture(t, map[string]string{"go.mod": "module example.com/acme\n"})
	if err := f.steps.holdAt(f.ctx, sc, st); err != nil {
		t.Fatal(err)
	}
	first = *st.Hold
	for i := range taskCommits {
		st.Heads["repo"] = f.gh.CommitFiles("feature", "A fix", map[string]string{"app/greet.go": "package app // " + strings.Repeat("x", i+1)})
	}
	f.merged(st, sc, "repo")
	if answer != nil {
		from := first.Heads["repo"]
		f.gh.Set(func(s *fakegithub.Server) { answer(s, from) })
	}
	if err := f.steps.holdAt(f.ctx, sc, st); err != nil {
		t.Fatal(err)
	}
	return first, *st.Hold
}

// The first hold has no commits since; the second counts only the task's
// commits since the first, not main's commit nor GitHub's merge of it.
func TestAHoldCountsTheTasksOwnCommitsSinceThePreviousHold(t *testing.T) {
	for _, own := range []int{0, 1, 2} {
		first, second := heldTwice(t, own, nil)
		if first.Round != 1 || first.CommitsSince != nil || first.Previous != nil || len(first.Repos) != 1 || first.Repos[0].Additions == nil {
			t.Fatalf("first hold %+v, want round 1 with no commits since and its lines counted", first)
		}
		if second.Round != 2 || second.CommitsSince == nil || *second.CommitsSince != own {
			t.Errorf("%d commits of the task's and a merge of main: round %d commitsSince %v, want %d", own, second.Round, countOf(second.CommitsSince), own)
		}
	}
}

// A compare the count needs that GitHub fails or lists short (base...the
// branch now, or base...the head the first hold showed) leaves the count
// out.
func TestAHoldWhoseCompareFailsOrIsTruncatedLeavesTheCountOut(t *testing.T) {
	failing := func(previous bool) func(*fakegithub.Server, string) {
		return func(s *fakegithub.Server, from string) {
			s.Intercept = func(r *http.Request) int {
				if strings.Contains(r.URL.Path, "/compare/") && strings.HasSuffix(r.URL.Path, "..."+from) == previous {
					return 502
				}
				return 0
			}
		}
	}
	truncated := func(previous bool) func(*fakegithub.Server, string) {
		return func(s *fakegithub.Server, from string) {
			s.CompareAnswer = func(_, head string, a map[string]any) {
				if (head == from) == previous {
					a["total_commits"] = 251
				}
			}
		}
	}
	for name, answer := range map[string]func(*fakegithub.Server, string){
		"now failed": failing(false), "previous failed": failing(true),
		"now truncated": truncated(false), "previous truncated": truncated(true),
	} {
		t.Run(name, func(t *testing.T) {
			_, second := heldTwice(t, 1, answer)
			if second.Round != 2 || second.CommitsSince != nil {
				t.Errorf("round %d commitsSince %v, want it left out", second.Round, countOf(second.CommitsSince))
			}
		})
	}
}

func countOf(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}
