package delivery

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// The rows packages/domain/test/attachments.test.ts reads too: the two
// parsers must find the same references in each text.
func TestImageRefsMatchTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/domain/test/attachment-references.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][2]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 {
		t.Fatalf("only %d rows", len(rows))
	}
	for _, row := range rows {
		var text string
		var want []string
		if err := json.Unmarshal(row[0], &text); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(row[1], &want); err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, r := range ImageRefs(text) {
			got = append(got, r.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: ids %v, want %v", text, got, want)
		}
	}
}

// A reference's title is the dialog's layout (size, wrap): presentation for
// people. The agent reads the image's place only.
func TestALayoutTitleNeverReachesTheAgent(t *testing.T) {
	in := PromptInput{Title: "T", Goal: "Like this ![mock.png](attachment:att_m \"small right\") please.",
		AcceptanceCriteria: []string{"Matches ![mock.png](<attachment:att_m> '320 left')"},
		Images:             []PromptImage{{"att_m", "mock.png"}}}
	if r := ImageRefs(in.Goal); len(r) != 1 || r[0].Title != "small right" {
		t.Fatalf("refs %+v", r)
	}
	for _, phase := range []string{PhaseImplement, PhaseReview} {
		got := Prompt(phase, in)
		for _, want := range []string{"Like this [Image 1: mock.png] please.", "- Matches [Image 1: mock.png]"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s prompt lacks %q:\n%s", phase, want, got)
			}
		}
		for _, leak := range []string{"small", "right", "320", "left"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s prompt has %q:\n%s", phase, leak, got)
			}
		}
	}
}

func TestImageRefsSkipCodeAndReadEveryURLForm(t *testing.T) {
	text := strings.Join([]string{
		"See ![a](attachment:att_a) and ![b](<attachment:att_b> \"t\") and ![c \\] d](attachment:att_c 'x')",
		"`![no](attachment:att_span)` then ``a ` ![no](attachment:att_span2) ``",
		"```",
		"![no](attachment:att_fence)",
		"```",
		"![e](attachment:att_e) ![web](https://x.test/a.png) [link](attachment:att_link)",
	}, "\n")
	var ids, alts []string
	for _, r := range ImageRefs(text) {
		ids = append(ids, r.ID)
		alts = append(alts, r.Alt)
		if !strings.HasPrefix(text[r.From:r.To], "![") || !strings.HasSuffix(text[r.From:r.To], ")") {
			t.Errorf("offsets of %s cover %q", r.ID, text[r.From:r.To])
		}
	}
	if want := []string{"att_a", "att_b", "att_c", "att_e"}; !slices.Equal(ids, want) {
		t.Errorf("ids %v, want %v", ids, want)
	}
	if want := []string{"a", "b", "c ] d", "e"}; !slices.Equal(alts, want) {
		t.Errorf("alts %v, want %v", alts, want)
	}
}

// The agent's list is the goal's images, then the criteria's, each once,
// in order of first appearance; each reference reads as its place in it.
func TestPromptNumbersImagesInFirstAppearanceOrder(t *testing.T) {
	goal := "The header ![header](attachment:att_h) overlaps.\n\nCompare ![mock](attachment:att_m) with ![header](attachment:att_h)."
	criteria := []string{"Looks like ![mock](attachment:att_m)", "No scrollbar, as in ![after](attachment:att_a)"}
	ids := TaskImageIDs(goal, criteria)
	if want := []string{"att_h", "att_m", "att_a"}; !slices.Equal(ids, want) {
		t.Fatalf("TaskImageIDs = %v, want %v", ids, want)
	}
	in := PromptInput{Title: "Fix the header", Goal: goal, AcceptanceCriteria: criteria,
		Images: []PromptImage{{"att_h", "header.png"}, {"att_m", "mock.png"}, {"att_a", "after.webp"}}}
	for _, phase := range []string{PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify, PhaseTest, PhaseInvestigate} {
		got := Prompt(phase, in)
		for _, want := range []string{
			"The header [Image 1: header.png] overlaps.\n\nCompare [Image 2: mock.png] with [Image 1: header.png].",
			"- Looks like [Image 2: mock.png]\n- No scrollbar, as in [Image 3: after.webp]",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s prompt lacks %q:\n%s", phase, want, got)
			}
		}
		if strings.Contains(got, "attachment:") {
			t.Errorf("%s prompt still has a reference:\n%s", phase, got)
		}
	}
}

// A reference to an image the Run is not given (another task's, removed)
// reads as unavailable, never as an error.
func TestAMissingImageReadsAsUnavailable(t *testing.T) {
	in := PromptInput{Title: "T", Goal: "Before ![gone.png](attachment:att_gone) after, and ![x](attachment:att_x).",
		Images: []PromptImage{{"att_x", "x.png"}}}
	got := Prompt(PhaseImplement, in)
	if want := "Before [Image unavailable: gone.png] after, and [Image 1: x.png]."; !strings.Contains(got, want) {
		t.Errorf("prompt lacks %q:\n%s", want, got)
	}
}

// A saved prompt's {{task.goal}} reads the goal as the task section does.
func TestASavedPromptsGoalNamesImagesToo(t *testing.T) {
	org := "Goal: {{task.goal}}"
	got := Prompt(PhaseImplement, PromptInput{Title: "T", Goal: "![a](attachment:att_a)", OrgPrompt: &org,
		Images: []PromptImage{{"att_a", "a.png"}}})
	if !strings.HasPrefix(got, "Goal: [Image 1: a.png]") {
		t.Errorf("prompt starts %q", got[:min(len(got), 60)])
	}
}

func TestAPullRequestNamesTheImages(t *testing.T) {
	body := prBody("See ![shot.png](attachment:att_s).", []string{"Matches ![mock](attachment:att_m)"}, nil, 0)
	if !strings.Contains(body, "See [Image: shot.png].") || !strings.Contains(body, "Matches [Image: mock]") || strings.Contains(body, "attachment:") {
		t.Errorf("pull request body:\n%s", body)
	}
}

// The conductor's briefing clips the goal and each criterion. A cut never
// leaves half a reference for its prompt to pass on raw, and a fence it
// leaves open is closed, so the references after it stay references.
func TestTheBriefingsClipKeepsReferencesWhole(t *testing.T) {
	ref := "![shot.png](attachment:att_s)"
	text := strings.Repeat("a", 20) + ref + " tail"
	for n := 21; n < 20+len(ref)+2; n++ {
		got := clipTaskText(text, n)
		if strings.Contains(got, "attachment:") && !strings.Contains(got, ref) {
			t.Errorf("clip at %d splits the reference: %q", n, got)
		}
	}
	if got := clipTaskText(text, len(text)); got != text {
		t.Errorf("a text within the limit changed: %q", got)
	}
	briefing := "Goal:\n\n" + clipTaskText("```go\nx := 1\n"+strings.Repeat("y", 50), 30) + "\n\n- Matches " + ref
	got := ConductorPrompt(briefing, PromptInput{Images: []PromptImage{{"att_s", "shot.png"}}})
	if !strings.Contains(got, "- Matches [Image 1: shot.png]") || strings.Contains(got, "attachment:") {
		t.Errorf("the conductor's prompt:\n%s", got)
	}
}
