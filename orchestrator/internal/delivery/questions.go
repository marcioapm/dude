package delivery

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// What one ask_person may put to a person: at most four questions, each
// with a short header (a tab's name), the question, and up to six choices.
// The person may always answer in their own words; dude offers that, never
// the agent. Lengths count characters (runes).
const (
	MaxQuestionItems     = 4
	QuestionHeaderMax    = 24
	QuestionTextMax      = 4000
	QuestionChoicesMax   = 6
	ChoiceLabelMax       = 120
	ChoiceDescriptionMax = 300
	AnswerTextMax        = 4000
	AnswerNoteMax        = 4000
)

// Choice is one answer offered to a question.
type Choice struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
}

// QuestionItem is one question of an ask (questions.items).
type QuestionItem struct {
	Header   string   `json:"header"`
	Question string   `json:"question"`
	Choices  []Choice `json:"choices"`
	Multiple bool     `json:"multiple"`
}

// ItemAnswer is a person's answer to one item (questions.answers): the
// choices picked, by index, and their own words.
type ItemAnswer struct {
	Choices []int  `json:"choices"`
	Text    string `json:"text"`
}

// Labels are the item's choices' labels, in order.
func (q QuestionItem) Labels() []string {
	out := make([]string, len(q.Choices))
	for i, c := range q.Choices {
		out[i] = c.Label
	}
	return out
}

// SingleItem is the one item a question asked the old way is: its text and
// its choices' labels.
func SingleItem(question string, choices []string) QuestionItem {
	item := QuestionItem{Question: question, Choices: make([]Choice, len(choices))}
	for i, c := range choices {
		item.Choices[i] = Choice{Label: c}
	}
	return item
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// NormalizeItems trims an ask's items and refuses one a person could not
// answer well: none or more than four, a question empty or too long, a
// header missing (when there are several: it names the tab) or too long,
// more than six choices, a choice empty, too long or the same as another
// (case and spaces aside), more than one recommended.
func NormalizeItems(items []QuestionItem) ([]QuestionItem, error) {
	if len(items) == 0 || len(items) > MaxQuestionItems {
		return nil, refusef("ask 1 to %d questions in one call; you asked %d", MaxQuestionItems, len(items))
	}
	out := make([]QuestionItem, len(items))
	for i, it := range items {
		n := ""
		if len(items) > 1 {
			n = fmt.Sprintf("question %d: ", i+1)
		}
		q := QuestionItem{Header: strings.TrimSpace(it.Header), Question: strings.TrimSpace(it.Question), Multiple: it.Multiple,
			Choices: make([]Choice, len(it.Choices))}
		switch {
		case q.Question == "":
			return nil, refusef("%sthe question is required", n)
		case runes(q.Question) > QuestionTextMax:
			return nil, refusef("%sthe question is too long: at most %d characters", n, QuestionTextMax)
		case len(items) > 1 && q.Header == "":
			return nil, refusef("%sa header is required when you ask several: a few words naming it (at most %d characters)",
				n, QuestionHeaderMax)
		case runes(q.Header) > QuestionHeaderMax:
			return nil, refusef("%sthe header %q is too long: at most %d characters", n, q.Header, QuestionHeaderMax)
		case len(it.Choices) > QuestionChoicesMax:
			return nil, refusef("%sat most %d choices; dude adds a way to answer in their own words", n, QuestionChoicesMax)
		}
		recommended := 0
		for j, c := range it.Choices {
			c = Choice{Label: strings.TrimSpace(c.Label), Description: strings.TrimSpace(c.Description), Recommended: c.Recommended}
			switch {
			case c.Label == "":
				return nil, refusef("%schoice %d has no label", n, j+1)
			case runes(c.Label) > ChoiceLabelMax:
				return nil, refusef("%schoice %q is too long: a label of at most %d characters (say more in its description)",
					n, c.Label, ChoiceLabelMax)
			case runes(c.Description) > ChoiceDescriptionMax:
				return nil, refusef("%schoice %q: a description of at most %d characters", n, c.Label, ChoiceDescriptionMax)
			}
			for _, prev := range q.Choices[:j] {
				if strings.EqualFold(prev.Label, c.Label) {
					return nil, refusef("%schoices %q and %q read as the same answer: make each one distinct", n, prev.Label, c.Label)
				}
			}
			if c.Recommended {
				recommended++
			}
			q.Choices[j] = c
		}
		if recommended > 1 {
			return nil, refusef("%srecommend at most one choice", n)
		}
		out[i] = q
	}
	return out, nil
}

// AskPrompt is what questions.prompt keeps for an ask: the question itself
// for one; for several, a line naming them ("4 questions: Retry scope, …"),
// which every reader of prompt shows.
func AskPrompt(items []QuestionItem) string {
	if len(items) == 1 {
		return items[0].Question
	}
	headers := make([]string, len(items))
	for i, it := range items {
		headers[i] = it.Header
	}
	return fmt.Sprintf("%d questions: %s", len(items), strings.Join(headers, ", "))
}

// AskOptions is what questions.options keeps: one item's labels, as
// before; none for several.
func AskOptions(items []QuestionItem) []string {
	if len(items) == 1 {
		return items[0].Labels()
	}
	return []string{}
}

// AnswerRefusal is an answer that does not answer what was asked: a 422.
type AnswerRefusal struct{ Msg string }

func (r AnswerRefusal) Error() string { return r.Msg }

func badAnswer(format string, a ...any) error { return AnswerRefusal{fmt.Sprintf(format, a...)} }

// Answered is a person's answer to an ask, as it is kept and told.
type Answered struct {
	// Text is questions.answer: for one item the label picked (labels, for
	// several picks) or the person's words, exactly as before — what the
	// pull request gate and an escalation's question match on; for several
	// items the rendered answers.
	Text    string
	Answers []ItemAnswer
	Note    string
}

// AnswerFromText is a person's answer to a one-item ask given as text (the
// answer route's {text}, a Chat message): the choice it names, else their
// own words. Its length is the route's own bound, as it always was.
func AnswerFromText(items []QuestionItem, text string) (Answered, error) {
	if len(items) != 1 {
		return Answered{}, badAnswer("this asks %d questions: answer each (answers), not with one text", len(items))
	}
	a := ItemAnswer{Choices: []int{}}
	if i := choiceOf(items[0].Labels(), text); i >= 0 {
		a.Choices = []int{i}
	} else {
		a.Text = strings.TrimSpace(text)
	}
	return Answered{Text: text, Answers: []ItemAnswer{a}}, nil
}

// CheckAnswers holds a person's structured answers to what was asked: one
// per item, each answered (a pick or their words), picks in range and
// distinct, several only where the item allows it, a one-answer item with
// a pick or words but not both; the words and the note within bounds.
func CheckAnswers(items []QuestionItem, answers []ItemAnswer, note string) (Answered, error) {
	if len(answers) != len(items) {
		return Answered{}, badAnswer("%d answers for %d questions: answer each one", len(answers), len(items))
	}
	if runes(note) > AnswerNoteMax {
		return Answered{}, badAnswer("a note of at most %d characters", AnswerNoteMax)
	}
	// One question's answer is told as it always was; a note goes with
	// several, on the review where they are sent together.
	if len(items) == 1 && strings.TrimSpace(note) != "" {
		return Answered{}, badAnswer("a note goes with several answers; for one question, say it in your own words")
	}
	out := make([]ItemAnswer, len(items))
	for i, a := range answers {
		it := items[i]
		n := ""
		if len(items) > 1 {
			n = fmt.Sprintf("question %d (%s): ", i+1, it.Header)
		}
		text := strings.TrimSpace(a.Text)
		picks := slices.Clone(a.Choices)
		if picks == nil {
			picks = []int{}
		}
		switch {
		case len(picks) == 0 && text == "":
			return Answered{}, badAnswer("%snot answered: pick a choice or say it in your own words", n)
		case runes(a.Text) > AnswerTextMax:
			return Answered{}, badAnswer("%san answer of at most %d characters", n, AnswerTextMax)
		case !it.Multiple && len(picks) > 1:
			return Answered{}, badAnswer("%sone answer only: it does not take several", n)
		case !it.Multiple && len(picks) == 1 && text != "":
			return Answered{}, badAnswer("%sone answer only: a choice or your own words, not both", n)
		}
		for j, p := range picks {
			if p < 0 || p >= len(it.Choices) {
				return Answered{}, badAnswer("%schoice %d is not one of its %d choices", n, p+1, len(it.Choices))
			}
			if slices.Contains(picks[:j], p) {
				return Answered{}, badAnswer("%schoice %d picked twice", n, p+1)
			}
		}
		slices.Sort(picks)
		out[i] = ItemAnswer{Choices: picks, Text: text}
	}
	ans := Answered{Answers: out, Note: strings.TrimSpace(note)}
	if len(items) == 1 {
		ans.Text = itemAnswerText(items[0], out[0], asTyped)
	} else {
		ans.Text = answersBlock(items, out)
	}
	return ans, nil
}

// itemAnswerText is one item's answer in words: the labels picked, then
// the person's own words as words(text) puts them.
func itemAnswerText(it QuestionItem, a ItemAnswer, words func(string) string) string {
	parts := make([]string, 0, len(a.Choices)+1)
	for _, p := range a.Choices {
		parts = append(parts, it.Choices[p].Label)
	}
	if a.Text != "" {
		parts = append(parts, words(a.Text))
	}
	return strings.Join(parts, "; ")
}

func asTyped(s string) string { return s }

func inTheirWords(s string) string { return fmt.Sprintf("in their own words: %q", s) }

// answersBlock is several items' answers for the agent and the record:
// each question, numbered with its header, and its answer under it.
func answersBlock(items []QuestionItem, answers []ItemAnswer) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		q := strings.ReplaceAll(it.Question, "\n", "\n   ")
		fmt.Fprintf(&b, "%d. %s — %s", i+1, it.Header, q)
		if it.Multiple {
			b.WriteString(" (several allowed)")
		}
		fmt.Fprintf(&b, "\n   → %s", strings.ReplaceAll(itemAnswerText(it, answers[i], inTheirWords), "\n", "\n     "))
	}
	return b.String()
}

// AnswerDirective is what the agent is told: for one item exactly what it
// always was ("Answer to your question %q", or "<name> answered your
// question %q" in a session); for several, each question and its answer.
// A note follows, signed.
func AnswerDirective(prompt string, items []QuestionItem, a Answered, name string, session bool) string {
	var text string
	switch {
	case len(items) == 1 && session:
		text = fmt.Sprintf("%s answered your question %q:\n\n%s", name, prompt, a.Text)
	case len(items) == 1:
		text = fmt.Sprintf("Answer to your question %q:\n\n%s", prompt, a.Text)
	case session:
		text = fmt.Sprintf("%s answered your %d questions:\n\n%s", name, len(items), a.Text)
	default:
		text = fmt.Sprintf("Answers to your %d questions:\n\n%s", len(items), a.Text)
	}
	if a.Note != "" {
		who := name
		if who == "" {
			who = "the person"
		}
		text += fmt.Sprintf("\n\nAlso from %s:\n%s", who, a.Note)
	}
	return text
}
