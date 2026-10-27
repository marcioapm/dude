package delivery

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// heldAround holds the merge fixture's delivery at its head (round 1),
// after before (nil: nothing) set it up; then between changes the branches,
// answer (nil: none) sets GitHub's answers given round 1, and it holds the
// delivery again at st's heads (round 2).
func heldAround(t *testing.T, before func(*verdictFixture, *State), between func(*verdictFixture, *State, workflow.StepContext),
	answer func(*verdictFixture, Hold)) (first, second Hold) {
	t.Helper()
	f, st, sc := mergeFixture(t, map[string]string{"go.mod": "module example.com/acme\n"})
	if before != nil {
		before(f, st)
	}
	if err := f.steps.holdAt(f.ctx, sc, st); err != nil {
		t.Fatal(err)
	}
	first = *st.Hold
	between(f, st, sc)
	if answer != nil {
		answer(f, first)
	}
	if err := f.steps.holdAt(f.ctx, sc, st); err != nil {
		t.Fatal(err)
	}
	return first, *st.Hold
}

// heldTwice is heldAround with taskCommits of the task's own committed to
// repo's branch, then GitHub merging main in (syncBeforePR).
func heldTwice(t *testing.T, taskCommits int, answer func(*verdictFixture, Hold)) (first, second Hold) {
	t.Helper()
	return heldAround(t, nil, func(f *verdictFixture, st *State, sc workflow.StepContext) {
		taskCommit(f.gh, st, "repo", taskCommits)
		f.merged(st, sc, "repo")
	}, answer)
}

// taskCommit commits n changes of the task's own to repo's branch.
func taskCommit(gh *fakegithub.Server, st *State, repo string, n int) {
	for i := range n {
		st.Heads[repo] = gh.CommitFiles("feature", "A fix", map[string]string{"app/greet.go": "package app // " + strings.Repeat("x", i+1)})
	}
}

// gitOn runs git in a fake's repository; its trimmed output.
func gitOn(t *testing.T, gh *fakegithub.Server, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", gh.Repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
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

// A merge of main made with main as its first parent (a person's
// `git checkout main && git merge feature`, pushed to feature) is main's,
// not the task's.
func TestAHoldCountsAMergeByWhetherItBringsInTheBase(t *testing.T) {
	for _, c := range []struct {
		name    string
		between func(*verdictFixture, *State, workflow.StepContext)
		want    int
	}{
		{"main merged with the branch as its second parent", func(f *verdictFixture, st *State, _ workflow.StepContext) {
			taskCommit(f.gh, st, "repo", 1)
			gitOn(t, f.gh, "update-ref", "refs/heads/onmain", "refs/heads/main")
			merge := f.gh.MergeLocally("onmain", "feature", nil)
			if merge == "" || f.gh.Parents(merge)[0] != f.gh.SHA("main") {
				t.Fatalf("merge %q of feature into main's head", merge)
			}
			gitOn(t, f.gh, "update-ref", "refs/heads/feature", merge)
			st.Heads["repo"] = merge
		}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, second := heldAround(t, nil, c.between, nil)
			if second.Round != 2 || second.CommitsSince == nil || *second.CommitsSince != c.want {
				t.Errorf("round %d commitsSince %v, want %d", second.Round, countOf(second.CommitsSince), c.want)
			}
		})
	}
}

// A compare the count needs that GitHub fails or lists short (base...the
// branch now, or base...the head the first hold showed) leaves the count
// out.
func TestAHoldWhoseCompareFailsOrIsTruncatedLeavesTheCountOut(t *testing.T) {
	failing := func(previous bool) func(*verdictFixture, Hold) {
		return func(f *verdictFixture, first Hold) {
			from := first.Heads["repo"]
			f.gh.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if strings.Contains(r.URL.Path, "/compare/") && strings.HasSuffix(r.URL.Path, "..."+from) == previous {
						return 502
					}
					return 0
				}
			})
		}
	}
	truncated := func(previous bool) func(*verdictFixture, Hold) {
		return func(f *verdictFixture, first Hold) {
			from := first.Heads["repo"]
			f.gh.Set(func(s *fakegithub.Server) {
				s.CompareAnswer = func(_, head string, a map[string]any) {
					if (head == from) == previous {
						a["total_commits"] = 251
					}
				}
			})
		}
	}
	for name, answer := range map[string]func(*verdictFixture, Hold){
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
