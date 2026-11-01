package orchestrator_test

import (
	"fmt"
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

// roundTwoWake is the wake round 2 says with its one finding, at line,
// shown with touches, and the fix before it.
func (w *world) roundTwoWake(task, id, title string, line int, touches bool, fixFacts map[string]any) string {
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
	for k, v := range fixFacts {
		data[k] = v
	}
	fixLine := delivery.MustText("wakes/facts/round-fix").Render(data)
	shown := []map[string]any{{"ID": id, "Severity": "high", "Title": title, "Where": fmt.Sprintf("FACTORY.md:%d", line),
		"Introduced": "", "Trivial": false, "Unverified": false, "Touches": touches}}
	return roundWake(2, 1, 0, rng, []string{"tests"}, shown, 0, fixLine, false)
}

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
			if got, want := w.roundWakeLine(task), w.roundTwoWake(task, id, title, c.line, c.want, nil); got != want {
				t.Errorf("round 2's wake:\n got %q\nwant %q", got, want)
			}
		})
	}
}
