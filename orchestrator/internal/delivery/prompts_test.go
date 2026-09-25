package delivery

import (
	"strings"
	"testing"
)

// A person's answer is part of the task from then on: a reviewer judging
// the change, or a fixer after it, is told what was decided rather than
// flagging it again (a real run's reviewers reported "chosen with no
// recorded decision" about an answered question).
func TestEveryPhaseIsToldWhatPeopleDecided(t *testing.T) {
	in := PromptInput{Title: "Add a reading-level score", Decisions: []Decision{{
		Question: "Which formula should reading_level use?\nThey differ a lot.",
		Answer:   "Flesch-Kincaid grade level",
	}}}
	for _, phase := range []string{PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify} {
		p := Prompt(phase, in)
		if !strings.Contains(p, "Q: Which formula should reading_level use? They differ a lot.") ||
			!strings.Contains(p, "A: Flesch-Kincaid grade level") {
			t.Errorf("%s's prompt lacks the decision:\n%s", phase, p)
		}
	}
}
