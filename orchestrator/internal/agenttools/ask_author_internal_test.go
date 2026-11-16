package agenttools

import (
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// ask_author is a review-only task's conductor's alone: a writing task's
// conductor is neither listed it nor finds it by name, and its list is
// exactly the granted tools (the tool-lists golden); a reviewer on a
// review-only task is not given it either.
func TestAskAuthorIsAReviewOnlyConductorsAlone(t *testing.T) {
	names := func(c Caller) []string {
		var out []string
		for _, tl := range c.tools() {
			out = append(out, tl.name)
		}
		return out
	}
	writing := Caller{Role: delivery.RoleConductor, TaskID: "t", grants: capabilities.Default(delivery.RoleConductor)}
	granted := granted(writing.Role, writing.grants)
	if got := names(writing); slices.Contains(got, "ask_author") || len(got) != len(granted) {
		t.Fatalf("a writing conductor's tools: %v", got)
	}
	if _, ok := writing.findTool("ask_author"); ok {
		t.Fatal("a writing conductor finds ask_author")
	}
	review := writing
	review.reviewOnly = true
	if got := names(review); !slices.Contains(got, "ask_author") || len(got) != len(granted)+1 {
		t.Fatalf("a review-only conductor's tools: %v", got)
	}
	if _, ok := review.findTool("ask_author"); !ok {
		t.Fatal("a review-only conductor does not find ask_author")
	}
	reviewer := Caller{Role: "reviewer", TaskID: "t", reviewOnly: true, grants: capabilities.Default("reviewer")}
	if _, ok := reviewer.findTool("ask_author"); ok || slices.Contains(names(reviewer), "ask_author") {
		t.Fatal("a reviewer is given ask_author")
	}
	noConduct := review
	noConduct.grants = review.grants.Without(capabilities.Conduct)
	if _, ok := noConduct.findTool("ask_author"); ok {
		t.Fatal("a conductor without conduct finds ask_author")
	}
}
