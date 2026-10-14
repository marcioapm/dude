package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// fixBatch is the pull request feedback the task's latest fixer was sent.
func (w *world) fixBatch(wi string) []forge.ActionableFeedback {
	var raw []byte
	_ = w.owner.QueryRow(context.Background(), `SELECT pr_feedback FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND pr_feedback <> '[]'::jsonb ORDER BY created_at DESC LIMIT 1`, wi).Scan(&raw)
	var out []forge.ActionableFeedback
	_ = json.Unmarshal(raw, &out)
	return out
}

// A bot's comment wakes nobody. A review by someone asked for one wakes a
// fixer, and the bots' comments made since the last fix go with it, marked
// as bots'.
func TestBotCommentsRideAlongWithARequestedReview(t *testing.T) {
	w := newWorld(t)
	w.askingFor("jerv")
	wi := w.reviewing()

	w.gh.Comment(1, "coderabbitai[bot]", "Please consider a constant here.")
	w.syncs(3)
	if n := w.fixes(wi); n != 0 {
		t.Fatalf("a bot's comment woke %d fixers", n)
	}

	w.gh.ReviewSaying(1, "jerv", "CHANGES_REQUESTED", "Please rename the greeting.")
	w.until("a fixer", func() bool { w.sync(); return w.fixes(wi) == 1 })
	batch := w.fixBatch(wi)
	if len(batch) != 2 || batch[0].Author != "jerv" || batch[0].Source != "review" ||
		batch[1].Author != "coderabbitai[bot]" || batch[1].Source != forge.SourceBot {
		t.Fatalf("batch %+v, want jerv's review then the bot's comment", batch)
	}
}

// A review by someone nobody asked wakes a fixer on its own: no bot rides along.
func TestBotCommentsStayBehindAnUnrequestedReview(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Comment(1, "coderabbitai[bot]", "Please consider a constant here.")
	w.syncs(2)
	w.gh.ReviewSaying(1, "ana", "CHANGES_REQUESTED", "Please rename the greeting.")
	w.until("a fixer", func() bool { w.sync(); return w.fixes(wi) == 1 })
	if batch := w.fixBatch(wi); len(batch) != 1 || batch[0].Author != "ana" {
		t.Fatalf("batch %+v, want ana's review alone", batch)
	}
}
