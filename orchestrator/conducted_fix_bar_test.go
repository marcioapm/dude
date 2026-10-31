package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

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
						return fakelux.Behaviour{Reply: "```yaml\n" + c.finding + "\ncategory: correctness\nfile: FACTORY.md\nline: 1\n" +
							"title: FACTORY.md says the wrong thing\ndescription: D.\nscenario: S.\nevidence_kind: command\nevidence: cat FACTORY.md\n```\n"}
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
