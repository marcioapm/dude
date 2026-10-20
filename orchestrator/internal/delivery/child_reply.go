package delivery

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ChildReplyLimit bounds, in characters, the child's final reply quoted in
// its parent's result directive, the elision marker included.
const ChildReplyLimit = 16000

var childReplyElidedTmpl = mustTemplate("contracts/child-reply-elided")

// childFinalReplyTx is the child's final reply: its agent.message events
// after its last tool call, joined. A child's output is drained and closed
// before it is settled (phases.Artifacts.collect), so every message the
// translator buffered is an event by now.
func childFinalReplyTx(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT payload->>'text' FROM events
		WHERE run_id = $1 AND event_type = 'agent.message' AND payload->>'text' IS NOT NULL
		AND cursor > COALESCE((SELECT max(cursor) FROM events WHERE run_id = $1
			AND event_type IN ('agent.tool.called', 'agent.tool.completed')), 0)
		ORDER BY cursor`, runID)
	if err != nil {
		return "", err
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), nil
}

// childReplyExcerpt is reply, its fences broken (fenced), within
// ChildReplyLimit: whole when it fits, else its head and its tail around a
// marker saying how much was left out. The tail is kept because a writer's
// checks block ends its reply.
func childReplyExcerpt(reply string) string {
	// One fenced pass leaves "```" inside a longer backtick run ("````" →
	// "`\u200b```"), and a cut there would open the tail with a fence that
	// closes the quote. With no "```" left, no cut can make one.
	for strings.Contains(reply, "```") {
		reply = fenced(reply)
	}
	r := []rune(reply)
	if len(r) <= ChildReplyLimit {
		return string(r)
	}
	marker := func(omitted int) string { return "\n\n" + render(childReplyElidedTmpl, omitted) + "\n\n" }
	// The marker for len(r) is at least as long as the real one: its count
	// has at least as many digits.
	keep := ChildReplyLimit - len([]rune(marker(len(r))))
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + marker(len(r)-keep) + string(r[len(r)-tail:])
}
