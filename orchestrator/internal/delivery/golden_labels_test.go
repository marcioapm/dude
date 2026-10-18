package delivery

import (
	"strings"
	"testing"
)

// The labels and words Go puts around data an agent reads: image
// references, a preview item's page line, a peer message's label, a
// blocker's state and an auto-start's way of starting.
func TestGoldenDeliveryLabels(t *testing.T) {
	text := "See ![the bar](attachment:att_bar), ![](attachment:att_gone) and ![old](attachment:att_old \"t\")."
	images := []PromptImage{{ID: "att_x", Name: "x.png"}, {ID: "att_bar", Name: "bar.png"}}
	var lines [][2]string
	add := func(name, text string) { lines = append(lines, [2]string{name, text}) }
	add("images-given", ReplaceImageRefs(text, imageText(images)))
	add("images-none-given", ReplaceImageRefs(text, imageText(nil)))
	add("images-as-text", ImagesAsText(text))
	add("criteria-images-as-text", strings.Join(CriteriaImagesAsText([]string{text, "plain"}), "\n"))

	page := func(url string, viewport, rect bool) string {
		a := FeedbackAnchor{Kind: "preview", URL: url}
		if viewport {
			a.Viewport = &struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}{1280, 720}
		}
		if rect {
			a.Rect = &struct {
				X      float64 `json:"x"`
				Y      float64 `json:"y"`
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			}{151.6, 431.4, 92.2, 37.5}
		}
		return a.PageLine()
	}
	add("page-line-full", page("https://preview.example/cart?x=1", true, true))
	add("page-line-window", page("https://preview.example/a%20b/c", true, false))
	add("page-line-element", page("https://preview.example", false, true))
	add("page-line-bad-url", page("://bad", true, true))
	add("page-line-none", "["+page("https://preview.example/cart", false, false)+"]")
	add("page-line-not-preview", "["+FeedbackAnchor{Kind: "general"}.PageLine()+"]")

	add("message", MessageDirective("GLD-8's implementer", "pm_1", "", "Which endpoint?"))
	add("message-reply", MessageDirective("GLD-9's conductor", "pm_2", "pm_1", "GET /invoices"))

	for _, s := range []string{"review", "running", "awaiting_input", "ready_to_merge", "failed", "aborted", "received", "queued",
		"intake", "awaiting_confirmation", "done", "odd_new_state", ""} {
		add("blocker-"+s, Blocker{Key: "GLD-8", Status: s}.Words())
	}
	add("blocked-many", BlockedError{TaskKey: "GLD-7", Blockers: []Blocker{{Key: "GLD-8", Status: "intake"},
		{Key: "GLD-9", Status: "awaiting_confirmation"}, {Key: "GLD-10", Status: "queued"}}}.Error())
	goldenTexts(t, "delivery-labels", lines)
}
