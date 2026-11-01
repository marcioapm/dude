package orchestrator_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// spiralWorld is a conducted world whose first review raises one evidenced
// high finding on FACTORY.md:1, whose fixer commits FACTORY.md, and whose
// second review judges it fixed and raises second, an evidenced high
// finding of its own. patch, when not nil, is what GitHub's compare shows
// of FACTORY.md: "" for no patch.
func spiralWorld(t *testing.T, second string, patch *string) *world {
	w := conducting(t)
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: high", wrongThing) + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n---\n" + second + "```\n"}
		case "fix", "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": "one\ntwo\nthree\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	if patch != nil {
		w.gh.Set(func(s *fakegithub.Server) {
			s.CompareAnswer = func(base, head string, answer map[string]any) {
				files, _ := answer["files"].([]any)
				for _, f := range files {
					if f, ok := f.(map[string]any); ok && f["filename"] == "FACTORY.md" {
						if *patch == "" {
							delete(f, "patch")
						} else {
							f["patch"] = *patch
						}
					}
				}
			}
		})
	}
	return w
}

// findingAt is an evidenced high finding on FACTORY.md at line.
func findingAt(line int, title string) string {
	return strings.Replace(evidencedFinding("severity: high", title), "line: 1\n", fmt.Sprintf("line: %d\n", line), 1)
}

// toFixedRoundTwo takes a conducted task through round 1, then its fix
// and the review queued behind it, to the decision after round 2.
func (w *world) toFixedRoundTwo(task string) {
	w.t.Helper()
	w.toRoundOne(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
	w.until("round 2", func() bool { return w.reviewedRound(task, 2) || w.escalationReason(task) != "" })
	if r := w.escalationReason(task); r != "" {
		w.t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
}

// roundTwoFinding is the finding round 2 raised: its id and the fix it
// touches ("" for none).
func (w *world) roundTwoFinding(task string) (id, touches string) {
	w.t.Helper()
	id = w.str(`SELECT id FROM review_findings WHERE task_id = $1 AND review_round = 2`, task)
	touches = w.str(`SELECT COALESCE(touches_fix_run_id, '') FROM review_findings WHERE id = $1`, id)
	return id, touches
}

// changeFact is what a round wake says of the fix's kept change, as dude
// renders it (wakes/facts/round-change): delta nil when not known.
func changeFact(delta *[2]int, removed []string, removedMore int, outside []string, outsideMore int) string {
	var d any
	if delta != nil {
		d = map[string]any{"Add": delta[0], "Del": delta[1]}
	}
	return strings.TrimSpace(delivery.MustText("wakes/facts/round-change").Render(map[string]any{
		"Delta": d, "Removed": removed, "RemovedMore": removedMore, "Outside": outside, "OutsideMore": outsideMore}))
}

// roundTwoWake is the wake round 2 says with its one finding, at line,
// shown with touches, and the fix before it, change what its kept change
// says.
func (w *world) roundTwoWake(task, id, title string, line int, touches bool, change string) string {
	w.t.Helper()
	fix := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	head := w.str(`SELECT heads->'target'->>'sha' FROM runs WHERE id = $1`, fix)
	review := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2 ORDER BY created_at LIMIT 1`, task)
	of := w.str(`SELECT COALESCE(review_range->>'of', '') FROM runs WHERE id = $1`, review)
	base := w.str(`SELECT COALESCE(review_range->'base'->>'target', '') FROM runs WHERE id = $1`, review)
	var repos []string
	if base != "" {
		repos = []string{"target@" + base[:7] + ".." + head[:7]}
	}
	rng := delivery.MustText("wakes/facts/round-range").Render(map[string]any{"Of": of, "Repos": repos})
	data := map[string]any{"Phase": "fix", "RunID": fix, "Outcome": "", "Note": "", "Commits": []string{"target@" + head[:7]}}
	fixLine := delivery.MustText("wakes/facts/round-fix").Render(data)
	shown := []map[string]any{{"ID": id, "Severity": "high", "Title": title, "Where": fmt.Sprintf("FACTORY.md:%d", line),
		"Introduced": "", "Trivial": false, "Unverified": false, "Touches": touches}}
	return roundWakeTouching(2, 1, 0, rng, []string{"tests"}, shown, 0, 0, fixLine, change, false)
}

// contextPatch is a one-line change at line 10 as GitHub's compare shows
// it: three lines of context either side.
const contextPatch = "@@ -7,7 +7,7 @@ func f() {\n a\n b\n c\n-d\n+D\n e\n f\n g"

// Design test 21: a finding on lines the fix before its round changed —
// the fix's hunk +1,3 on FACTORY.md, the finding at FACTORY.md:2 — names
// that fix (touches_fix_run_id), and the round wake marks it. A finding
// past the hunk, or on a file GitHub showed no patch for, names none and
// is not marked. A pure deletion's hunk holds the line after it.
func TestTheSpiralFactNamesTheFixWhoseLinesAFindingTouches(t *testing.T) {
	for _, c := range []struct {
		name  string
		patch string
		line  int
		want  bool
	}{
		{"on the fix's lines", "@@ -1,1 +1,3 @@\n-x\n+one\n+two\n+three", 2, true},
		{"past them", "@@ -1,1 +1,3 @@\n-x\n+one\n+two\n+three", 40, false},
		{"no patch", "", 2, false},
		{"the line after a pure deletion", "@@ -2,3 +1,0 @@\n-a\n-b\n-c", 2, true},
		// GitHub's patches carry three lines of context either side: a
		// finding on one is not on the fix's lines; one on its + line is.
		{"a context line", contextPatch, 8, false},
		{"the + line among context", contextPatch, 10, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			title := "The second line is wrong"
			w := spiralWorld(t, findingAt(c.line, title), &c.patch)
			task := w.task()
			w.toFixedRoundTwo(task)
			fix := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
			id, touches := w.roundTwoFinding(task)
			if want := map[bool]string{true: fix, false: ""}[c.want]; touches != want {
				t.Errorf("touches_fix_run_id %q, want %q (the fix %s)", touches, want, fix)
			}
			// The fix wrote three lines over one (git's count; the patch is
			// GitHub's answer); with no patch, its change is not known.
			change := changeFact(&[2]int{3, 1}, nil, 0, nil, 0)
			if c.patch == "" {
				change = ""
			}
			if got, want := w.roundWakeLine(task), w.roundTwoWake(task, id, title, c.line, c.want, change); got != want {
				t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// changeWorld is a conducted world whose first review raises first (YAML
// documents), whose fixer commits commit, whose second review judges F1
// fixed and raises nothing, and whose GitHub shows patches[file] as that
// file's patch where set.
func changeWorld(t *testing.T, first string, commit, patches map[string]string) *world {
	w := conducting(t)
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + first + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "fix", "simplify":
			return fakelux.Behaviour{Commit: commit, Message: "fix"}
		}
		return scripted(spec)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		s.CompareAnswer = func(base, head string, answer map[string]any) {
			files, _ := answer["files"].([]any)
			for _, f := range files {
				if f, ok := f.(map[string]any); ok {
					if p, ok := patches[fmt.Sprint(f["filename"])]; ok {
						f["patch"] = p
					}
				}
			}
		}
	})
	return w
}

// fixedRoundTwoWake is round 2's wake with nothing at the bar, the fix
// before it, and change.
func (w *world) fixedRoundTwoWake(task, change string) string {
	w.t.Helper()
	fix := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix'`, task)
	head := w.str(`SELECT heads->'target'->>'sha' FROM runs WHERE id = $1`, fix)
	review := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND review_round = 2 ORDER BY created_at LIMIT 1`, task)
	of := w.str(`SELECT COALESCE(review_range->>'of', '') FROM runs WHERE id = $1`, review)
	base := w.str(`SELECT COALESCE(review_range->'base'->>'target', '') FROM runs WHERE id = $1`, review)
	var repos []string
	if base != "" {
		repos = []string{"target@" + base[:7] + ".." + head[:7]}
	}
	rng := delivery.MustText("wakes/facts/round-range").Render(map[string]any{"Of": of, "Repos": repos})
	fixLine := delivery.MustText("wakes/facts/round-fix").Render(map[string]any{"Phase": "fix", "RunID": fix, "Outcome": "", "Note": "",
		"Commits": []string{"target@" + head[:7]}})
	return roundWakeTouching(2, 0, 0, rng, []string{"tests"}, nil, 0, 0, fixLine, change, false)
}

// The fix's production-line delta counts lines added and removed outside
// test files: a test file's lines are not in it.
func TestTheRoundWakeSaysTheFixsProductionLineDelta(t *testing.T) {
	w := changeWorld(t, evidencedFinding("severity: high", wrongThing),
		map[string]string{"FACTORY.md": "one\ntwo\nthree\n", "factory_test.go": "package f\n\nfunc helper() {}\n"}, nil)
	task := w.task()
	w.toFixedRoundTwo(task)
	if got, want := w.roundWakeLine(task), w.fixedRoundTwoWake(task, changeFact(&[2]int{3, 1}, nil, 0, []string{"factory_test.go"}, 0)); got != want {
		t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
	}
}

// A test declaration the fix's diff removes and does not add again under
// its name is said removed; one removed and added back (a rewrite) is not.
func TestTheRoundWakeNamesTheTestDeclarationsTheFixRemoved(t *testing.T) {
	w := changeWorld(t, evidencedFinding("severity: high", wrongThing),
		map[string]string{"FACTORY.md": "one\ntwo\nthree\n", "factory_test.go": "package f\n\nfunc TestKept(t *testing.T) {}\n"},
		map[string]string{"factory_test.go": "@@ -1,6 +1,3 @@\n package f\n-func TestGone(t *testing.T) {\n-}\n-func TestKept(t *testing.T) {\n-}\n+\n+func TestKept(t *testing.T) {}"})
	task := w.task()
	w.toFixedRoundTwo(task)
	if got, want := w.roundWakeLine(task), w.fixedRoundTwoWake(task, changeFact(&[2]int{3, 1}, []string{"TestGone"}, 0, []string{"factory_test.go"}, 0)); got != want {
		t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
	}
}

// The files the fix changed that its brief did not name are listed: a
// file of a finding it was handed is named, as is one under a directory
// its note names; a brief that names no path lists none.
func TestTheRoundWakeListsTheFilesOutsideTheFixsBrief(t *testing.T) {
	commit := map[string]string{"FACTORY.md": "one\ntwo\nthree\n", "lib/a.go": "package lib\n", "docs/x.md": "x\n", "other.go": "package o\n"}
	t.Run("named by a finding or the note", func(t *testing.T) {
		w := changeWorld(t, evidencedFinding("severity: high", wrongThing), commit, nil)
		task := w.task()
		w.toRoundOne(task)
		w.must(task, "start_phase", `{"phase":"fix","note":"Keep the change in lib/ and \u0060docs/x.md:1\u0060.","then":{"review":["tests"]}}`)
		w.until("round 2", func() bool { return w.reviewedRound(task, 2) })
		if got, want := w.roundWakeLine(task), w.fixedRoundTwoWake(task, changeFact(&[2]int{6, 1}, nil, 0, []string{"other.go"}, 0)); got != want {
			t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
		}
	})
	t.Run("a brief with no path", func(t *testing.T) {
		placeless := "severity: high\ncategory: correctness\ntitle: " + wrongThing +
			"\ndescription: D.\nscenario: S.\nevidence_kind: command\nevidence: cat FACTORY.md\n"
		w := changeWorld(t, placeless, commit, nil)
		task := w.task()
		w.toFixedRoundTwo(task)
		if got, want := w.roundWakeLine(task), w.fixedRoundTwoWake(task, changeFact(&[2]int{6, 1}, nil, 0, nil, 0)); got != want {
			t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
		}
	})
}

// A fix whose change is not known — the compare its publish counted with
// refused, or cut short at GitHub's file cap — still publishes, keeps no
// change (or one marked truncated), and the round wake says nothing of it:
// no marker on a finding on its lines, and no fact of its change.
func TestAFixWhoseChangeIsNotKnownLeavesTheRoundWakeSilent(t *testing.T) {
	title := "The second line is wrong"
	for _, c := range []struct {
		name string
		set  func(s *fakegithub.Server)
	}{
		{"the compare refused", func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				if strings.Contains(r.URL.Path, "/compare/main...") && s.Repo != "" {
					return 422
				}
				return 0
			}
		}},
		{"the compare truncated", func(s *fakegithub.Server) {
			s.CompareAnswer = func(base, head string, answer map[string]any) {
				files, _ := answer["files"].([]any)
				for len(files) < 300 {
					files = append(files, map[string]any{"filename": fmt.Sprintf("gen/%03d.txt", len(files)), "status": "added",
						"additions": 1, "deletions": 0, "patch": "@@ -0,0 +1 @@\n+x"})
				}
				answer["files"] = files
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := spiralWorld(t, findingAt(2, title), nil)
			task := w.task()
			w.toRoundOne(task)
			w.gh.Set(c.set)
			w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
			w.until("round 2", func() bool { return w.reviewedRound(task, 2) || w.escalationReason(task) != "" })
			if r := w.escalationReason(task); r != "" {
				t.Fatalf("escalated %q\n%s", r, w.describeRuns())
			}
			w.gh.Set(func(s *fakegithub.Server) { s.Intercept, s.CompareAnswer = nil, nil })
			id, touches := w.roundTwoFinding(task)
			if touches != "" {
				t.Errorf("touches_fix_run_id %q, want none", touches)
			}
			if got, want := w.roundWakeLine(task), w.roundTwoWake(task, id, title, 2, false, ""); got != want {
				t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
			}
		})
	}
}
