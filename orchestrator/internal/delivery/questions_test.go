package delivery

import (
	"errors"
	"strings"
	"testing"
)

func fourItems() []QuestionItem {
	return []QuestionItem{
		{Header: "Retry scope", Question: "Which failures should the payment call retry?", Choices: []Choice{
			{Label: "5xx and network errors only", Description: "A 4xx means our request is wrong.", Recommended: true},
			{Label: "Everything (current behaviour)"}, {Label: "Nothing — show the error"}}},
		{Header: "Old route", Question: "The legacy /pay route has its own retry. What should happen to it?", Choices: []Choice{
			{Label: "Fold it into this change"}, {Label: "Leave it; file a task", Recommended: true}, {Label: "Delete the route"}}},
		{Header: "Tests", Question: "Which layers should cover the split?", Multiple: true, Choices: []Choice{
			{Label: "Unit (PaymentSplitter)"}, {Label: "API contract"}, {Label: "Browser e2e on checkout"}}},
		{Header: "Button", Question: "What should the split button say?", Choices: []Choice{{Label: "Split payment"}, {Label: "Pay in parts"}}},
	}
}

func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	var r Refusal
	if !errors.As(err, &r) || !strings.Contains(r.Msg, want) {
		t.Errorf("refusal %v, want one saying %q", err, want)
	}
}

func TestAnAskIsOneToFourQuestionsWithinTheirLimits(t *testing.T) {
	if _, err := NormalizeItems(fourItems()); err != nil {
		t.Fatalf("four good questions refused: %v", err)
	}
	five := append(fourItems(), QuestionItem{Header: "Fifth", Question: "And?"})
	_, err := NormalizeItems(five)
	refusedWith(t, err, "1 to 4")
	_, err = NormalizeItems(nil)
	refusedWith(t, err, "1 to 4")

	mut := func(f func(items []QuestionItem)) error {
		items := fourItems()
		f(items)
		_, err := NormalizeItems(items)
		return err
	}
	refusedWith(t, mut(func(it []QuestionItem) { it[1].Header = "  " }), "header is required")
	refusedWith(t, mut(func(it []QuestionItem) { it[1].Header = strings.Repeat("h", 25) }), "at most 24")
	refusedWith(t, mut(func(it []QuestionItem) { it[2].Question = strings.Repeat("é", 4001) }), "at most 4000")
	refusedWith(t, mut(func(it []QuestionItem) { it[0].Question = " " }), "question is required")
	refusedWith(t, mut(func(it []QuestionItem) {
		it[0].Choices = append(it[0].Choices, Choice{Label: "d"}, Choice{Label: "e"}, Choice{Label: "f"}, Choice{Label: "g"})
	}), "at most 6 choices")
	refusedWith(t, mut(func(it []QuestionItem) { it[3].Choices[0].Label = strings.Repeat("x", 121) }), "at most 120")
	refusedWith(t, mut(func(it []QuestionItem) { it[3].Choices[0].Description = strings.Repeat("x", 301) }), "at most 300")
	refusedWith(t, mut(func(it []QuestionItem) { it[0].Choices[1].Recommended = true }), "at most one")
	refusedWith(t, mut(func(it []QuestionItem) { it[3].Choices[1].Label = " split PAYMENT " }), "same answer")
	refusedWith(t, mut(func(it []QuestionItem) { it[3].Choices[1].Label = "" }), "no label")

	// At the limits, counted in characters: accepted.
	ok := fourItems()
	ok[0].Header = strings.Repeat("é", 24)
	ok[0].Question = strings.Repeat("😀", 4000)
	ok[1].Choices = []Choice{{Label: strings.Repeat("é", 120), Description: strings.Repeat("é", 300)},
		{Label: "b"}, {Label: "c"}, {Label: "d"}, {Label: "e"}, {Label: "f"}}
	if _, err := NormalizeItems(ok); err != nil {
		t.Errorf("at the limits: %v", err)
	}
	// One question needs no header.
	if _, err := NormalizeItems([]QuestionItem{SingleItem("Proceed?", nil)}); err != nil {
		t.Errorf("one question without a header: %v", err)
	}
}

func TestPromptAndOptionsKeepWhatTheirReadersShow(t *testing.T) {
	items, _ := NormalizeItems(fourItems())
	if got := AskPrompt(items); got != "4 questions: Retry scope, Old route, Tests, Button" {
		t.Errorf("prompt %q", got)
	}
	if got := AskOptions(items); len(got) != 0 {
		t.Errorf("options for several: %v", got)
	}
	one := []QuestionItem{SingleItem("Proceed?", []string{"Yes", "No"})}
	if AskPrompt(one) != "Proceed?" || strings.Join(AskOptions(one), ",") != "Yes,No" {
		t.Errorf("one question: %q %v", AskPrompt(one), AskOptions(one))
	}
}

func badWith(t *testing.T, err error, want string) {
	t.Helper()
	var r AnswerRefusal
	if !errors.As(err, &r) || !strings.Contains(r.Msg, want) {
		t.Errorf("answer refusal %v, want one saying %q", err, want)
	}
}

func goodAnswers() []ItemAnswer {
	return []ItemAnswer{{Choices: []int{0}}, {Choices: []int{1}}, {Choices: []int{1, 0}}, {Text: "Pay with two cards"}}
}

func TestEveryQuestionIsAnsweredAndOnlyAsItAllows(t *testing.T) {
	items, _ := NormalizeItems(fourItems())
	if _, err := CheckAnswers(items, goodAnswers(), ""); err != nil {
		t.Fatalf("a complete answer refused: %v", err)
	}
	mut := func(f func(a []ItemAnswer)) error {
		a := goodAnswers()
		f(a)
		_, err := CheckAnswers(items, a, "")
		return err
	}
	_, err := CheckAnswers(items, goodAnswers()[:3], "")
	badWith(t, err, "3 answers for 4 questions")
	badWith(t, mut(func(a []ItemAnswer) { a[1] = ItemAnswer{Text: "  "} }), "not answered")
	badWith(t, mut(func(a []ItemAnswer) { a[0].Choices = []int{3} }), "not one of its 3 choices")
	badWith(t, mut(func(a []ItemAnswer) { a[0].Choices = []int{-1} }), "not one of")
	badWith(t, mut(func(a []ItemAnswer) { a[0].Choices = []int{0, 1} }), "does not take several")
	badWith(t, mut(func(a []ItemAnswer) { a[0].Text = "and also this" }), "not both")
	badWith(t, mut(func(a []ItemAnswer) { a[2].Choices = []int{1, 1} }), "picked twice")
	badWith(t, mut(func(a []ItemAnswer) { a[3].Text = strings.Repeat("é", 4001) }), "at most 4000")
	_, err = CheckAnswers(items, goodAnswers(), strings.Repeat("é", 4001))
	badWith(t, err, "a note of at most 4000")
	// A several-answer question takes picks and words together.
	if err := mut(func(a []ItemAnswer) { a[2].Text = "and a smoke test" }); err != nil {
		t.Errorf("picks and words on a multiple question: %v", err)
	}
}

func TestSeveralAnswersAreToldAsOneMessage(t *testing.T) {
	items, _ := NormalizeItems(fourItems())
	a, err := CheckAnswers(items, goodAnswers(), " Keep the retry budget under 10s total — checkout times out at 15. ")
	if err != nil {
		t.Fatal(err)
	}
	want := `Answers to your 4 questions:

1. Retry scope — Which failures should the payment call retry?
   → 5xx and network errors only
2. Old route — The legacy /pay route has its own retry. What should happen to it?
   → Leave it; file a task
3. Tests — Which layers should cover the split? (several allowed)
   → Unit (PaymentSplitter); API contract
4. Button — What should the split button say?
   → in their own words: "Pay with two cards"

Also from marcio:
Keep the retry budget under 10s total — checkout times out at 15.`
	if got := AnswerDirective(AskPrompt(items), items, a, "marcio", false); got != want {
		t.Errorf("directive:\n%s\n\nwant:\n%s", got, want)
	}
	if got := AnswerDirective(AskPrompt(items), items, a, "Ana Nunes", true); !strings.HasPrefix(got, "Ana Nunes answered your 4 questions:\n\n1. Retry scope") {
		t.Errorf("a session's: %q", got)
	}
}

// One question is told and kept exactly as before: the gate and an
// escalation's question match on the answer's text.
func TestOneQuestionIsToldAndKeptAsItAlwaysWas(t *testing.T) {
	items := []QuestionItem{SingleItem("Open the pull request?", []string{"Open", "Draft"})}
	a, err := AnswerFromText(items, "Open")
	if err != nil || a.Text != "Open" || len(a.Answers) != 1 || len(a.Answers[0].Choices) != 1 || a.Answers[0].Choices[0] != 0 {
		t.Fatalf("a pick by text: %+v %v", a, err)
	}
	if got := AnswerDirective("Open the pull request?", items, a, "marcio", false); got != "Answer to your question \"Open the pull request?\":\n\nOpen" {
		t.Errorf("directive %q", got)
	}
	if got := AnswerDirective("Open the pull request?", items, a, "Ana", true); got != "Ana answered your question \"Open the pull request?\":\n\nOpen" {
		t.Errorf("a session's directive %q", got)
	}
	// Through the form: the label picked is the answer's text.
	b, err := CheckAnswers(items, []ItemAnswer{{Choices: []int{1}}}, "")
	if err != nil || b.Text != "Draft" {
		t.Errorf("a pick through the form: %+v %v", b, err)
	}
	c, err := CheckAnswers(items, []ItemAnswer{{Text: " Not yet "}}, "")
	if err != nil || c.Text != "Not yet" {
		t.Errorf("own words through the form: %+v %v", c, err)
	}
	_, err = CheckAnswers(items, []ItemAnswer{{Choices: []int{0}}}, "a note")
	badWith(t, err, "a note goes with several")
	_, err = AnswerFromText(fourItems(), "Open")
	badWith(t, err, "answer each")
	// Own words by text are kept and told exactly as typed.
	d, err := AnswerFromText(items, " Not yet\n")
	if err != nil || d.Text != " Not yet\n" {
		t.Fatalf("own words by text: %+v %v", d, err)
	}
	if got := AnswerDirective("Open the pull request?", items, d, "marcio", false); got != "Answer to your question \"Open the pull request?\":\n\n Not yet\n" {
		t.Errorf("directive %q", got)
	}
}
