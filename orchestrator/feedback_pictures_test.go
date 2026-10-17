package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// previewFeedback drafts a preview item as person on the task's current
// attempt, with a picture or none, and submits it, as the backend does.
func (w *world) previewFeedback(key, wi, id, body, picture string) (int, map[string]any) {
	w.t.Helper()
	anchor := map[string]any{"kind": "preview", "url": "https://web-abs-1-shop.preview.example/cart",
		"viewport": map[string]int{"width": 1280, "height": 720}, "rect": map[string]float64{"x": 152, "y": 431, "width": 92, "height": 38}}
	if picture != "" {
		anchor["attachmentId"] = picture
	}
	raw, _ := json.Marshal(anchor)
	mustExec(w.t, w.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor)
		SELECT $1, $2, $3, task_attempt($3), person_id, $5, 'preview', $6::jsonb FROM api_keys WHERE id = $4`,
		id, w.org, wi, key, body, string(raw))
	return w.callAs(key, "/internal/tasks/"+wi+"/feedback/submit", map[string]any{"draftIds": []string{id}, "submissionKey": "k-" + id})
}

// steerTo is the directive carrying item to the Run.
func (w *world) steerTo(item string) (directive string) {
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(directive_id, '') FROM feedback_items WHERE id = $1`, item).Scan(&directive)
	return directive
}

// A preview item steered to the working implementer carries its picture
// in the same message, as a person's steer carries its images, and the
// words say so; an item with no picture is sent with none and no line.
func TestAPreviewItemsPictureGoesToTheWorkingAgentWithItsWords(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	ana := w.person("Ana")
	wi, run := w.hangingRun()
	w.upload(b, "att_fb", wi, "fb_d1-element.png", screenshot)

	if code, body := w.previewFeedback(ana, wi, "fbi_pic", "The Pay button is cut off.", "att_fb"); code != 201 || body["routedTo"] != "steer" {
		t.Fatalf("submit: %d %v", code, body)
	}
	dir := w.steerTo("fbi_pic")
	luxRun := w.lux.Runs()[0].ID
	w.until("lux to have the picture", func() bool { return len(w.lux.Attachments(luxRun)[dir]) == 1 })
	got := w.lux.Attachments(luxRun)[dir][0]
	if got.Name != "fb_d1-element.png" || got.ContentType != "image/png" || !bytes.Equal(got.Data, screenshot) {
		t.Errorf("the agent got %s %s (%d bytes)", got.Name, got.ContentType, len(got.Data))
	}
	var text map[string]any
	_ = json.Unmarshal([]byte(w.lux.InputBodies(luxRun, dir)[0]), &text)
	words, _ := text["text"].(string)
	for _, want := range []string{"> The Pay button is cut off.", "Route /cart in a 1280×720 window · the element is 92×38 at 152, 431",
		"Picture: fb_d1-element.png, attached (the element, outlined)"} {
		if !strings.Contains(words, want) {
			t.Errorf("the steer lacks %q:\n%s", want, words)
		}
	}
	if n := w.count(`SELECT count(*) FROM attachments WHERE id = 'att_fb' AND directive_id = $1`, dir); n != 1 {
		t.Error("the picture is not recorded as sent with the steer")
	}

	if code, body := w.previewFeedback(ana, wi, "fbi_bare", "And the total wraps.", ""); code != 201 || body["runId"] != run {
		t.Fatalf("submit: %d %v", code, body)
	}
	bare := w.steerTo("fbi_bare")
	w.until("the second steer sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, bare) == 1
	})
	if n := len(w.lux.Attachments(luxRun)[bare]); n != 0 {
		t.Errorf("an item with no picture was sent %d images", n)
	}
	_ = json.Unmarshal([]byte(w.lux.InputBodies(luxRun, bare)[0]), &text)
	if words, _ := text["text"].(string); strings.Contains(words, "Picture:") || !strings.Contains(words, "> And the total wraps.") {
		t.Errorf("the second steer:\n%s", words)
	}
}

// A fixer given a preview item in its prompt is given its picture with
// the prompt, after the task's own images, and the prompt says so; one
// whose picture is gone from the task is given the words without the line.
func TestAFixerGivenAPreviewItemIsGivenItsPicture(t *testing.T) {
	cases := []struct {
		name string
		// picture uploads the item's picture, or leaves it gone from the task.
		picture func(w *world, b bucket, wi string)
		want    bool
	}{
		{name: "the task's", want: true, picture: func(w *world, b bucket, wi string) {
			w.upload(b, "att_fb", wi, "fb_d1-element.png", screenshot)
		}},
		{name: "deleted", picture: func(w *world, b bucket, wi string) {
			w.upload(b, "att_fb", wi, "fb_d1-element.png", screenshot)
			mustExec(w.t, w.owner, `DELETE FROM attachments WHERE id = 'att_fb'`)
		}},
		{name: "another task's", picture: func(w *world, b bucket, wi string) {
			w.upload(b, "att_fb", w.task(), "fb_d1-element.png", screenshot)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			b := w.withImages()
			ana := w.person("Ana")
			wi := w.reviewing()
			w.onModel("implementer", "llm-impl")
			c.picture(w, b, wi)
			imgs, prompt := w.fixerGiven(ana, wi)
			if !c.want {
				if len(imgs) != 0 || strings.Contains(prompt, "Picture:") || !strings.Contains(prompt, "> The Pay button is cut off.") {
					t.Fatalf("the fixer's spec carried %d images; prompt:\n%s", len(imgs), prompt)
				}
				return
			}
			if len(imgs) != 1 || imgs[0].Name != "fb_d1-element.png" || !bytes.Equal(imgs[0].Data, screenshot) {
				t.Fatalf("the fixer's spec carried %d images", len(imgs))
			}
			if !strings.Contains(prompt, "Picture: fb_d1-element.png, attached (the element, outlined)") {
				t.Errorf("the fixer's prompt does not say the picture is attached:\n%s", prompt)
			}
		})
	}
}

// A task whose text shows as many images as one message carries leaves no
// room for a preview item's picture: the fixer is given the task's images
// and the item's words, with no Picture: line.
func TestAFixerWithAFullMessageIsGivenNoPicture(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	ana := w.person("Ana")
	wi := w.reviewing()
	w.onModel("implementer", "llm-impl")
	goal := "Fix the cart."
	for i := 1; i <= delivery.MaxAttachmentsPerMessage; i++ {
		id := fmt.Sprintf("att_t%d", i)
		w.upload(b, id, wi, id+".png", screenshot)
		goal += fmt.Sprintf(" ![%s.png](attachment:%s)", id, id)
	}
	w.describe(wi, goal)
	w.upload(b, "att_fb", wi, "fb_d1-element.png", screenshot)
	imgs, prompt := w.fixerGiven(ana, wi)
	var names []string
	for _, im := range imgs {
		names = append(names, im.Name)
	}
	if len(imgs) != delivery.MaxAttachmentsPerMessage || slices.Contains(names, "fb_d1-element.png") {
		t.Errorf("the fixer's spec carried %v", names)
	}
	if strings.Contains(prompt, "Picture:") || !strings.Contains(prompt, "> The Pay button is cut off.") {
		t.Errorf("the fixer's prompt:\n%s", prompt)
	}
}

// fixerGiven submits a preview item naming att_fb on a task in review and
// returns the images and prompt of the fixer given it.
func (w *world) fixerGiven(person, wi string) ([]struct {
	Name, ContentType string
	Data              []byte
}, string) {
	w.t.Helper()
	if code, body := w.previewFeedback(person, wi, "fbi_pr", "The Pay button is cut off.", "att_fb"); code != 201 || body["routedTo"] != "workflow" {
		w.t.Fatalf("submit: %d %v", code, body)
	}
	var fixer string
	w.until("a fixer given the item", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_pr' = ANY(feedback_item_ids)`, wi).Scan(&fixer)
		return fixer != ""
	})
	w.until("the fixer's prompt", func() bool { return w.promptOf(fixer) != "" })
	var spec []byte
	for _, r := range w.lux.Runs() {
		if strings.Contains(string(r.Spec), `"dude.run":"`+fixer+`"`) {
			spec = r.Spec
		}
	}
	return specImages(w.t, spec), w.promptOf(fixer)
}
