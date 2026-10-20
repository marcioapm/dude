package delivery

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"
)

// ChecksGated selects phases independently of whether they publish code.
var ChecksGated = map[string]bool{
	PhaseImplement: true,
	PhaseFix:       true,
	PhaseSimplify:  true,
}

const (
	CheckPass   = "pass"
	CheckFail   = "fail"
	CheckNotRun = "not-run"
)

type Check struct {
	Command  string `json:"command" yaml:"command"`
	Result   string `json:"result" yaml:"result"`
	Evidence string `json:"evidence" yaml:"evidence"`
}

// ParseChecks decodes the last checks document, never falling back to an
// earlier valid report when the last one is malformed. A report of no
// checks is valid input for the model's missing-check question.
func ParseChecks(reply string) ([]Check, bool) {
	lines := strings.Split(reply, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "checks:") {
			start = i
		}
	}
	if start < 0 {
		return nil, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") || line == "---" {
			end = i
			break
		}
	}
	var doc struct {
		Checks []Check `yaml:"checks"`
	}
	if yaml.Unmarshal([]byte(strings.Join(lines[start:end], "\n")), &doc) != nil || doc.Checks == nil {
		return nil, false
	}
	for _, c := range doc.Checks {
		if strings.TrimSpace(c.Command) == "" || (c.Result != CheckPass && c.Result != CheckFail && c.Result != CheckNotRun) {
			return nil, false
		}
	}
	return doc.Checks, true
}

// LatestTurnTx is the cursor of the Run's latest ended turn (its newest
// agent.session.stopped), 0 before any: the turn a checks verdict is for.
func LatestTurnTx(ctx context.Context, tx pgx.Tx, runID string) (int64, error) {
	var turn int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor), 0) FROM events WHERE run_id = $1 AND event_type = 'agent.session.stopped'`,
		runID).Scan(&turn)
	return turn, err
}

// RunReplyTailTx is the last budget bytes of what a Run's agent said,
// joined as RunReplyTx joins it, read from the newest messages up to the
// first that reaches budget. A report starting before those bytes is cut
// or gone, so ParseChecks finds none or a malformed one and the gate fails
// closed.
func RunReplyTailTx(ctx context.Context, tx pgx.Tx, runID string, budget int) (string, error) {
	rows, err := tx.Query(ctx, `SELECT t FROM (
			SELECT cursor, payload->>'text' AS t,
				sum(COALESCE(octet_length(payload->>'text'), 0) + 1) OVER (ORDER BY cursor DESC) AS upto,
				COALESCE(octet_length(payload->>'text'), 0) + 1 AS own
			FROM events WHERE run_id = $1 AND event_type = 'agent.message') m
		WHERE upto - own < $2 ORDER BY cursor`, runID, budget)
	if err != nil {
		return "", err
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[*string])
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range texts {
		if t != nil {
			b.WriteString(*t)
		}
		b.WriteString("\n")
	}
	tail := b.String()
	if len(tail) > budget {
		tail = tail[len(tail)-budget:]
		// Drop the continuation bytes of a rune the cut split.
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	return tail, nil
}
