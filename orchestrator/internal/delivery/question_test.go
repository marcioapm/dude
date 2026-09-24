package delivery

import (
	"slices"
	"testing"
)

func TestAQuestionWithChoices(t *testing.T) {
	q, ok := ParseQuestion("I need a decision.\n\n```question\nShould `stats` with no PATH read standard input?\n- yes\n- no\n```\n")
	if !ok || q.Prompt != "Should `stats` with no PATH read standard input?" || !slices.Equal(q.Options, []string{"yes", "no"}) {
		t.Errorf("got %+v, %v", q, ok)
	}
}

func TestAFreeQuestionHasNoChoices(t *testing.T) {
	q, ok := ParseQuestion("```question\nWhich locale should dates use?\n```")
	if !ok || q.Prompt != "Which locale should dates use?" || q.Options != nil {
		t.Errorf("got %+v, %v", q, ok)
	}
}

func TestAReplyWithoutAQuestionBlockAsksNothing(t *testing.T) {
	// A question in prose, or a code block, is not a request to stop.
	for _, reply := range []string{
		"Should I also add a CLI? I went ahead and did.",
		"```python\nprint('question')\n```",
		"```question\n```",
	} {
		if q, ok := ParseQuestion(reply); ok {
			t.Errorf("%q asked %+v", reply, q)
		}
	}
}

func TestOnlyTheLastQuestionCounts(t *testing.T) {
	q, _ := ParseQuestion("```question\nFirst?\n```\nThought about it more.\n```question\nSecond?\n```")
	if q.Prompt != "Second?" {
		t.Errorf("got %q", q.Prompt)
	}
}
