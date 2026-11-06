package delivery

import (
	"strings"
	"testing"
)

// The standing authorization's validity, one guard per row: delivery,
// attempt, the failing criteria and blocking findings as a subset of what
// the person was asked about, and the commits since only the simplifier's.
func TestOpenAuthorityValid(t *testing.T) {
	at := map[string]string{"app": "a1"}
	base := func() *OpenAuthority {
		return &OpenAuthority{QuestionID: "qst_1", Choice: ChoiceOpen, WorkflowID: "wf_1", Attempt: 1,
			Heads: at, Criteria: []int{1}, Findings: []string{"test: Criterion 1 is not met"}}
	}
	facts := func() AuthorityFacts {
		return AuthorityFacts{WorkflowID: "wf_1", Attempt: 1, Heads: map[string]string{"app": "a1"},
			Criteria: []int{1}, Findings: []string{"test: Criterion 1 is not met"}}
	}
	simplified := func(a *OpenAuthority) *OpenAuthority {
		a.Moves = []AuthorityMove{{Source: PhaseSimplify, RunID: "run_s", From: at, To: map[string]string{"app": "s1"}}}
		return a
	}
	for _, c := range []struct {
		name string
		a    func() *OpenAuthority
		now  func() AuthorityFacts
		ok   bool
		why  string
	}{
		{"as answered", base, facts, true, ""},
		{"draft", func() *OpenAuthority { a := base(); a.Choice = ChoiceDraft; return a }, facts, true, ""},
		{"none recorded", func() *OpenAuthority { return nil }, facts, false, "no answer"},
		{"a choice that does not open", func() *OpenAuthority { a := base(); a.Choice = ""; return a }, facts, false, "not open or draft"},
		{"another delivery", base, func() AuthorityFacts { f := facts(); f.WorkflowID = "wf_2"; return f }, false, "another delivery"},
		{"next attempt", base, func() AuthorityFacts { f := facts(); f.Attempt = 2; return f }, false, "another attempt"},
		{"fewer failing", base, func() AuthorityFacts { f := facts(); f.Criteria, f.Findings = nil, nil; return f }, true, ""},
		{"a new failing criterion", base, func() AuthorityFacts { f := facts(); f.Criteria = []int{1, 2}; return f }, false, "criterion 2"},
		{"a new blocking finding", base, func() AuthorityFacts { f := facts(); f.Findings = append(f.Findings, "fnd_9"); return f }, false, "finding fnd_9"},
		{"simplifier commits", func() *OpenAuthority { return simplified(base()) },
			func() AuthorityFacts { f := facts(); f.Heads = map[string]string{"app": "s1"}; return f }, true, ""},
		{"a fixer commit", base, func() AuthorityFacts { f := facts(); f.Heads = map[string]string{"app": "f1"}; return f }, false, "not the simplifier's"},
		{"a fixer commit after the simplifier's", func() *OpenAuthority { return simplified(base()) },
			func() AuthorityFacts { f := facts(); f.Heads = map[string]string{"app": "f2"}; return f }, false, "not the simplifier's"},
		{"a move of a source not listed", func() *OpenAuthority {
			a := base()
			a.Moves = []AuthorityMove{{Source: PhaseFix, RunID: "run_f", From: at, To: map[string]string{"app": "f1"}}}
			return a
		}, func() AuthorityFacts { f := facts(); f.Heads = map[string]string{"app": "f1"}; return f }, false, "not the simplifier's"},
		{"a simplifier move from other heads", func() *OpenAuthority {
			a := base()
			a.Moves = []AuthorityMove{{Source: PhaseSimplify, RunID: "run_s", From: map[string]string{"app": "x"}, To: map[string]string{"app": "s1"}}}
			return a
		}, func() AuthorityFacts { f := facts(); f.Heads = map[string]string{"app": "s1"}; return f }, false, "not the simplifier's"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ok, why := OpenAuthorityValid(c.a(), c.now())
			if ok != c.ok || !strings.Contains(why, c.why) {
				t.Errorf("= %v %q, want %v %q", ok, why, c.ok, c.why)
			}
		})
	}
	if len(AuthorityCommitSources) != 1 || AuthorityCommitSources[0] != PhaseSimplify {
		t.Errorf("the allowed commit sources are %v, want the simplifier's alone", AuthorityCommitSources)
	}
}

// decide ask_person's question away from the gate: choices with actions,
// or the person's own words; answers enumerated only in prose refused.
func TestDecideItems(t *testing.T) {
	sim := "Criterion 1 cannot be checked here. Open the PR so CI measures / run Docker locally / accept partly met?"
	if _, _, err := decideItems(sim, DecideAsk{}); !IsRefusal(err) || !strings.Contains(err.Error(), "choices") {
		t.Errorf("prose options: %v, want refused", err)
	}
	if _, _, err := decideItems("Which:\n- Open the PR\n- Run Docker locally", DecideAsk{}); !IsRefusal(err) {
		t.Errorf("a listed note: %v, want refused", err)
	}
	items, actions, err := decideItems("What should the greeting say in Portuguese?", DecideAsk{})
	if err != nil || len(items[0].Choices) != 0 || actions != nil {
		t.Errorf("a free question: %v %v %v", items, actions, err)
	}
	items, actions, err = decideItems("Criterion 1 cannot be checked here. How to go on?", DecideAsk{Choices: []DecideChoice{
		{Label: "Open the PR so CI measures", Action: ChoiceOpen}, {Label: "Run Docker locally"}, {Label: "Accept partly met"}},
		Recommend: "Open the PR so CI measures"})
	if err != nil || len(items[0].Choices) != 3 || !items[0].Choices[0].Recommended || strings.Join(actions, ",") != "open,," {
		t.Errorf("choices: %+v %v %v", items, actions, err)
	}
	if _, _, err := decideItems("Q?", DecideAsk{Choices: []DecideChoice{{Label: "Merge", Action: "merge"}, {Label: "No"}}}); !IsRefusal(err) {
		t.Errorf("an unknown action: %v", err)
	}
	if _, _, err := decideItems("Q?", DecideAsk{Choices: []DecideChoice{{Label: "Only"}}}); !IsRefusal(err) {
		t.Errorf("one choice: %v", err)
	}
	choices, gateActions, err := gateRecommended("")
	if err != nil || len(choices) < 2 || !choices[0].Recommended || gateActions[0] != ChoiceOpen || gateActions[1] != ChoiceDraft {
		t.Errorf("the gate's choices: %+v %v %v", choices, gateActions, err)
	}
	n := 0
	for _, c := range choices {
		if c.Recommended {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d recommended", n)
	}
	if choices, _, _ := gateRecommended("draft"); !choices[1].Recommended || choices[0].Recommended {
		t.Errorf("recommend draft: %+v", choices)
	}
}
