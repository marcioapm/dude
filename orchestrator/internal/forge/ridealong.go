package forge

import (
	"slices"
	"strings"
)

// SourceBot marks a bot's comment in a fixer's batch: context to weigh,
// never what woke it.
const SourceBot = "bot"

// Requested is who was asked for a review of a pull request as it was last
// read (Status.Reviews): asked and not reviewed yet, or asked again after
// reviewing. Read from the state before a review arrives: GitHub takes a
// reviewer off requested_reviewers once they submit.
func Requested(reviews []Review) []string {
	var out []string
	for _, r := range reviews {
		if r.Team {
			continue
		}
		if r.State == "REQUESTED" || r.Rerequested {
			out = append(out, r.Login)
		}
	}
	return out
}

// WokenByRequestedReview says a batch includes a review — its text, a line
// comment, a change request — by someone who was asked for one.
func WokenByRequestedReview(batch []ActionableFeedback, requested []string) bool {
	return slices.ContainsFunc(batch, func(f ActionableFeedback) bool {
		return f.Source == "review" && f.IsReview() &&
			slices.ContainsFunc(requested, func(l string) bool { return strings.EqualFold(l, f.Author) })
	})
}

// RideAlong is a fixer's batch with the bots' comments added: only when a
// requested reviewer's review woke it. A bot's comment never wakes a fixer
// on its own (IsActionableComment); with a person's review it is context,
// marked SourceBot. bots are the bots' comments not yet sent to a fixer,
// oldest first; one already in the batch, or empty, is not added again.
func RideAlong(batch []ActionableFeedback, requested []string, bots []Feedback) []ActionableFeedback {
	if !WokenByRequestedReview(batch, requested) {
		return batch
	}
	out := slices.Clone(batch)
	for _, b := range bots {
		if !isBot(b.Author) || strings.TrimSpace(b.Body) == "" || Own(b) ||
			slices.ContainsFunc(out, func(f ActionableFeedback) bool { return f.ID != "" && f.ID == b.ID }) {
			continue
		}
		out = append(out, ActionableFeedback{Source: SourceBot, ID: b.ID, Author: b.Author, Body: b.Body, Path: b.Path, Kind: b.Kind})
	}
	return out
}
