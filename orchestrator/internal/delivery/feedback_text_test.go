package delivery_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// previewItem is a preview item whose anchor is the JSON the backend keeps.
func previewItem(t *testing.T, anchor string) delivery.FeedbackItem {
	t.Helper()
	it := delivery.FeedbackItem{ID: "fbi_1", Author: "Ana", Body: "This button is cut off."}
	if err := json.Unmarshal([]byte(anchor), &it.Anchor); err != nil {
		t.Fatal(err)
	}
	return it
}

// A preview item says where on its page it was made: the route, the window
// and the element's box, each clause only with its data.
func TestFeedbackTextSaysWhereOnThePageAPreviewItemWasMade(t *testing.T) {
	const page = `"kind":"preview","url":"https://web-abs-1-shop.preview.example/cart?step=2#top","selector":"main > button"`
	cases := []struct {
		name, anchor, want string
	}{
		{"window and element", `{` + page + `,"viewport":{"width":1280,"height":720},"rect":{"x":152.4,"y":430.6,"width":92,"height":38.2}}`,
			"Route /cart in a 1280×720 window · the element is 92×38 at 152, 431"},
		{"window only", `{` + page + `,"viewport":{"width":390,"height":844}}`, "Route /cart in a 390×844 window"},
		{"element only", `{` + page + `,"rect":{"x":0,"y":8,"width":10,"height":12}}`, "Route /cart · the element is 10×12 at 0, 8"},
		{"site root", `{"kind":"preview","url":"https://web.preview.example","viewport":{"width":800,"height":600}}`,
			"Route / in a 800×600 window"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := delivery.FeedbackText([]delivery.FeedbackItem{previewItem(t, c.anchor)}, true)
			if !strings.Contains(text, "\n\n"+c.want+"\n") {
				t.Errorf("want the line %q in\n%s", c.want, text)
			}
			if n := strings.Count(text, "Route "); n != 1 {
				t.Errorf("%d route lines in\n%s", n, text)
			}
		})
	}
	// Neither a window nor a box: the URL already says the page.
	text := delivery.FeedbackText([]delivery.FeedbackItem{previewItem(t, `{`+page+`}`)}, true)
	if strings.Contains(text, "Route ") || strings.Contains(text, "window") {
		t.Errorf("a route line with no window or box:\n%s", text)
	}
	// A code item never has one, whatever else it carries.
	code := delivery.FeedbackItem{ID: "fbi_2", Author: "Bo", Body: "Rename it",
		Anchor: delivery.FeedbackAnchor{Kind: "code", Path: "a.go", StartLine: 1, EndLine: 1, Side: "new", HeadSHA: "abcdef1234"}}
	if text := delivery.FeedbackText([]delivery.FeedbackItem{code}, true); strings.Contains(text, "Route ") {
		t.Errorf("a code item with a route line:\n%s", text)
	}
}

// The picture line is written only for a picture sent with the message
// (MarkPictures), never from the anchor naming one.
func TestFeedbackTextSaysAPictureIsAttachedOnlyWhenItIs(t *testing.T) {
	const anchor = `{"kind":"preview","url":"https://web.preview.example/","attachmentId":"att_1","viewport":{"width":800,"height":600}}`
	const line = "Picture: fb_d1-element.png, attached (the element, outlined)"
	items := []delivery.FeedbackItem{previewItem(t, anchor)}

	if text := delivery.FeedbackText(items, true); strings.Contains(text, "Picture:") {
		t.Errorf("a picture named by the anchor and not sent:\n%s", text)
	}
	delivery.MarkPictures(items, []delivery.SentAttachment{{ID: "att_other", Name: "other.png"}})
	if text := delivery.FeedbackText(items, true); strings.Contains(text, "Picture:") {
		t.Errorf("another item's picture sent:\n%s", text)
	}
	delivery.MarkPictures(items, []delivery.SentAttachment{{ID: "att_task", Name: "design.png"}, {ID: "att_1", Name: "fb_d1-element.png"}})
	text := delivery.FeedbackText(items, true)
	if !strings.Contains(text, "\n\n"+line+"\n") {
		t.Errorf("want %q in\n%s", line, text)
	}
	// After the route line, before the person's words.
	if r, p, q := strings.Index(text, "Route /"), strings.Index(text, line), strings.Index(text, "> This button"); !(r < p && p < q) {
		t.Errorf("the lines are out of order:\n%s", text)
	}
	// Marked again for a message without it: gone.
	delivery.MarkPictures(items, nil)
	if text := delivery.FeedbackText(items, true); strings.Contains(text, "Picture:") {
		t.Errorf("a picture line after the picture was not sent:\n%s", text)
	}
}
