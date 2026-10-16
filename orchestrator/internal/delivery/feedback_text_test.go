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
