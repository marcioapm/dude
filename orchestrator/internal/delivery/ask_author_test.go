package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// An open question to the author: never APPROVE. Alone, a COMMENT naming
// it ("Before approving: @author, <question> (<link>)"); with a blocking
// finding, REQUEST_CHANGES listing it too. Its claim reason names the
// questions, and an approval after they are settled is a claim of its own.
func TestDecideVerdictWithAnAuthorQuestion(t *testing.T) {
	ask := AuthorAsk{ID: "rvq_1", Login: "alice", Question: "Is greet called from the CLI only?", URL: "https://github.test/c/1"}
	questions := GateItem{Key: ReadyQuestions, Detail: "1 review question(s) open"}
	d := decideVerdict(verdictInput{Gate: gateAt(questions), Asks: []AuthorAsk{ask}})
	if d.Event != forge.EventComment || d.Reason != "author-question:rvq_1" ||
		d.Headline != "Reviewed at a8f3e1c. Before approving: @alice, Is greet called from the CLI only? (https://github.test/c/1)" {
		t.Fatalf("alone: %+v", d)
	}
	finding := GateItem{Key: ReadyFindings, Detail: "1 open finding(s) at the fix bar introduced by the pull request: F1"}
	d = decideVerdict(verdictInput{Gate: gateAt(finding, questions), Asks: []AuthorAsk{ask}})
	if d.Event != forge.EventRequestChanges || d.Reason != "findings:rvq_1" || !strings.Contains(strings.Join(d.Notes, "\n"), "Before approving: @alice, Is greet") {
		t.Fatalf("with a finding: %+v", d)
	}
	// Failing CI not looked at yet does not hide the question either way:
	// the question comes first among the non-defects.
	if d := decideVerdict(verdictInput{Gate: gateAt(questions, GateItem{Key: ReadyChecks, Detail: "failing: build"}), Asks: []AuthorAsk{ask}}); d.Event != forge.EventComment {
		t.Fatalf("with red CI: %+v", d)
	}
	if d := decideVerdict(verdictInput{Gate: gateAt(), Settled: []string{"rvq_1"}}); d.Event != forge.EventApprove || d.Reason != "approve:rvq_1" {
		t.Fatalf("settled: %+v", d)
	}
	if d := decideVerdict(verdictInput{Gate: gateAt()}); d.Reason != reasonApprove {
		t.Fatalf("no question: %+v", d)
	}
}

// The asked author's conversation comment answers; anyone else's, a line
// comment, dude's own marked reply, and a writing task's (nil) never do.
func TestAuthorAsksAnswers(t *testing.T) {
	a := &AuthorAsks{Conversation: map[string]bool{"alice": true}}
	for _, c := range []struct {
		f    forge.Feedback
		want bool
	}{
		{forge.Feedback{Author: "alice", Body: "No.", Kind: forge.KindComment}, true},
		{forge.Feedback{Author: "Alice", Body: "No.", Kind: forge.KindComment}, true},
		{forge.Feedback{Author: "bob", Body: "No.", Kind: forge.KindComment}, false},
		{forge.Feedback{Author: "alice", Body: "No.", Kind: forge.KindLineComment}, false},
		{forge.Feedback{Author: "alice", Body: "  ", Kind: forge.KindComment}, false},
		{forge.Feedback{Author: "alice", Body: "x\n\n" + forge.ReplyMarker, Kind: forge.KindComment}, false},
	} {
		if got := a.Answers(c.f); got != c.want {
			t.Errorf("%+v: %v", c.f, got)
		}
	}
	var none *AuthorAsks
	if none.Answers(forge.Feedback{Author: "alice", Body: "No.", Kind: forge.KindComment}) || none.Lines() != nil {
		t.Error("a writing task's nil asks answer")
	}
	if got := authorAsksLines([]AuthorAsk{{ID: "rvq_1", Login: "alice", Question: "Why?"}}); !strings.Contains(got, "- rvq_1: @alice, Why?") {
		t.Errorf("the wake's list: %q", got)
	}
	if authorAsksLines(nil) != "" {
		t.Error("no questions, yet a list")
	}
	if got := askMarker("rvq_9"); got != "<!-- dude:ask id=rvq_9 -->" {
		t.Errorf("marker %q", got)
	}
}
