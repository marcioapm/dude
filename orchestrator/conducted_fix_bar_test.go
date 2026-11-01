package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// evidencedFinding is a correctness finding on FACTORY.md:1 with all its
// evidence, in a conducted reviewer's format: head is its severity line(s).
func evidencedFinding(head, title string) string {
	return head + "\ncategory: correctness\nfile: FACTORY.md\nline: 1\ntitle: " + title +
		"\ndescription: D.\nscenario: S.\nevidence_kind: command\nevidence: cat FACTORY.md\n"
}

// The owner's fix bar in a conducted loop: until the conductor runs the
// loop itself, the policy's exit routes it, and what blocks there is every
// finding at medium or above and every low marked trivial. A medium finding
// in a conducted delivery gets a fix round; the same finding in an
// un-conducted (Deliver) delivery does not (BlockingSeverities). A low one
// not marked trivial gets none in a conducted loop either.
func TestAConductedLoopFixesAMediumFindingThePolicyLoopShips(t *testing.T) {
	for _, c := range []struct {
		name          string
		conducted     bool
		finding, next string
	}{
		{"medium, conducted", true, "severity: medium", "fix"},
		{"medium, deliver", false, "severity: medium", "simplify"},
		{"trivial low, conducted", true, "severity: low\ntrivial: yes", "fix"},
		{"low, conducted", true, "severity: low\ntrivial: no", "simplify"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := conducting(t)
			scripted := w.lux.Decide
			reviews := 0
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
					if reviews++; reviews == 1 {
						return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding(c.finding, "FACTORY.md says the wrong thing") + "```\n"}
					}
					return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
				}
				return scripted(spec)
			}
			task := w.task()
			if c.conducted {
				w.talk(task)
				w.implemented(task)
				w.must(task, "decide", `{"action":"next"}`)
				w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
				w.must(task, "decide", `{"action":"next"}`)
			} else {
				w.deliver(task)
			}
			w.until("the next phase", func() bool { return w.phaseRuns(task, "fix")+w.phaseRuns(task, "simplify") > 0 })
			if got := map[bool]string{true: "fix", false: "simplify"}[w.phaseRuns(task, "fix") > 0]; got != c.next {
				t.Errorf("after the round: %s, want %s\n%s", got, c.next, w.describeRuns())
			}
		})
	}
}

// A delivery that had a conductor keeps its fix bar when the person lets
// Deliver finish it: a medium the next round still finds gets another fix
// round, as it would have conducted, rather than shipping.
func TestAHandedBackDeliveryKeepsTheConductedFixBar(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			switch reviews++; reviews {
			case 1:
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: medium", "FACTORY.md says the wrong thing") + "```\n"}
			case 2:
				return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand back: %d %v", status, out)
	}
	w.until("the round after the first fix", func() bool {
		return w.phaseRuns(task, "fix") >= 2 || w.phaseRuns(task, "simplify") > 0 || w.taskStatus(task) == "awaiting_input"
	})
	if n := w.phaseRuns(task, "fix"); n < 2 {
		t.Errorf("handed back, the still-open medium got %d fix rounds, want a second\n%s", n, w.describeRuns())
	}
}

// A conducted loop's fix bar takes a fix-caused medium as a blocker, as a
// Deliver delivery whose policy blocks on medium does; past the regression
// bound the round still parks for the conductor, never escalating
// not_converging: whether the loop converges is the conductor's judgement.
func TestAConductedLoopCountsAMediumRegression(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: medium", "Original defect") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n---\n" +
				evidencedFinding("severity: medium", fmt.Sprintf("Regression %d", reviews-1)) + "caused_by_fix: yes\n" + "```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	setPolicy(w, task, `{"maxAttemptsPerFinding":4,"maxReviewIterations":9,"maxRegressionRounds":2}`)
	w.implemented(task)
	w.carriedOut(task, "decide", `{"action":"next"}`)
	for round := 1; round <= 2; round++ {
		w.until(fmt.Sprintf("round %d reviewed", round), func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		w.carriedOut(task, "start_phase", `{"phase":"fix"}`)
		w.until(fmt.Sprintf("fix %d done", round), func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.carriedOut(task, "decide", `{"action":"next"}`)
	}
	w.until("round 3 reviewed, or escalated", func() bool {
		return w.escalationReason(task) != "" || w.decisionAt(task) == delivery.PointReviewed
	})
	if got := w.escalationReason(task); got != "" {
		t.Fatalf("after two medium regressions: escalated %q; want the round parked for the conductor\n%s", got, w.describeRuns())
	}
	// The regression's medium blocks: next is another fix, not the simplifier.
	fixes := w.phaseRuns(task, "fix")
	w.carriedOut(task, "decide", `{"action":"next"}`)
	if n := w.phaseRuns(task, "fix"); n != fixes+1 || w.phaseRuns(task, "simplify") != 0 {
		t.Errorf("after the round: %d fixes (want %d), %d simplifiers\n%s", n, fixes+1, w.phaseRuns(task, "simplify"), w.describeRuns())
	}
}

// A resumed reviewer of a conducted loop is asked, in its round message,
// for each finding's evidence, as a fresh one's instructions ask; a Deliver
// delivery's resumed reviewer is given the plain format.
func TestAResumedReviewerOfAConductedLoopIsAskedForEvidence(t *testing.T) {
	for _, conducted := range []bool{true, false} {
		t.Run(fmt.Sprintf("conducted=%v", conducted), func(t *testing.T) {
			w := conducting(t)
			w.onModel("reviewer", "llm-review")
			mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
			task := w.task()
			if conducted {
				w.talk(task)
				setPolicy(w, task, `{"resumeRounds":true}`)
				w.implemented(task)
				// On lux from here on (must, not carriedOut): a reviewer
				// lux keeps is the one its next round resumes.
				w.must(task, "decide", `{"action":"next"}`)
				w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
				w.must(task, "start_phase", `{"phase":"fix"}`)
				w.until("fix done", func() bool { return w.decisionAt(task) == delivery.PointFixed })
				w.must(task, "decide", `{"action":"next"}`)
			} else {
				w.names(task, w.repoID)
				p := scriptedPolicy()
				p.ResumeRounds = true
				w.start(task, p)
			}
			w.until("the resumed reviewer's round message", func() bool { return resumedReview(w) })
			var inputs []string
			for _, r := range w.luxRunsOf("review") {
				inputs = append(inputs, w.lux.Inputs(r.ID)...)
			}
			got := false
			for _, in := range inputs {
				if strings.Contains(in, "evidence_kind:") {
					got = true
				}
			}
			if got != conducted {
				t.Errorf("conducted=%v: the resumed reviewer's round message asks for evidence: %v\n%q", conducted, got, inputs)
			}
		})
	}
}

// resumedReview says a reviewer on lux has been resumed for a round.
func resumedReview(w *world) bool {
	for _, r := range w.luxRunsOf("review") {
		if r.Resumed > 0 && len(w.lux.Inputs(r.ID)) > 0 {
			return true
		}
	}
	return false
}
