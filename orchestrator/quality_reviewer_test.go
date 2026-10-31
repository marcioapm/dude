package orchestrator_test

import (
	"context"
	"strings"
	"testing"
)

// The code quality and architecture reviewer is in the catalogue: a
// conductor that starts a review with it gets a reviewer of that category,
// briefed with the quality aspect and no other.
func TestAQualityReviewTheConductorStartsIsBriefedWithItsAspect(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	w.must(task, "start_phase", `{"phase":"review","categories":["quality"]}`)
	review := w.reviewOnLux(task)
	var category string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(category, '') FROM runs WHERE id = $1`, review).Scan(&category); err != nil {
		t.Fatal(err)
	}
	if category != "quality" {
		t.Errorf("the reviewer's category is %q", category)
	}
	f := w.instructionsOf(review)
	if !strings.Contains(f, "# Your aspect: quality") || strings.Contains(f, "# Your aspect: correctness") {
		t.Errorf("the quality reviewer's instruction file:\n%s", f)
	}
}
