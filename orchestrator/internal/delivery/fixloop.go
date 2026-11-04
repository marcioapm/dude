// A pull request fix's own review loop (State.fixLoop): what its fixes
// are sent and what its exit weighs.

package delivery

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
)

// rangeFindings are the open findings of this attempt raised by a review
// of the range the delivery reviews now: what a fix of that loop is sent.
// A range grows when a fix changes a repository it lacked (awaitFix), so
// an earlier round's review matches by its anchors being a subset of r's.
func (s *Store) rangeFindings(ctx context.Context, org string, st *State, r *ReviewRange) ([]string, error) {
	raw, _ := json.Marshal(r)
	var out []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM review_findings
			WHERE task_id = $1 AND status = 'open' AND `+thisAttempt+`
			  AND run_id IN (SELECT id FROM runs WHERE task_id = $1 AND review_range->>'of' = ($3::jsonb->>'of')
			    AND ($3::jsonb->'base') @> (review_range->'base'))
			ORDER BY created_at`, st.TaskID, st.Attempt, raw)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return out, err
}

// loopsOwn keeps the findings a pull request's fix loop owns: its own
// reviews' — of the fix, and of its simplifier's diff — never
// the findings a person let the pull request open with. After a failed
// test SimplifyBase is empty: the narrow review's open findings are not the
// loop's. Both the fix it sends and the exit it weighs read these.
// Elsewhere it keeps every finding.
func (s *Store) loopsOwn(ctx context.Context, org string, st *State, findings []FindingState) ([]FindingState, error) {
	if !st.fixLoop() {
		return findings, nil
	}
	ranges := []*ReviewRange{{Of: RangePullRequestFix, Base: st.FixRange}}
	if len(st.SimplifyBase) > 0 {
		ranges = append(ranges, &ReviewRange{Of: RangeSimplify, Base: st.SimplifyBase})
	}
	var ours []string
	for _, r := range ranges {
		ids, err := s.rangeFindings(ctx, org, st, r)
		if err != nil {
			return nil, err
		}
		ours = append(ours, ids...)
	}
	return slices.DeleteFunc(findings, func(f FindingState) bool { return !slices.Contains(ours, f.ID) }), nil
}

// runBaseRefs is the commit per repository a Run was created to start
// from, and the branch it publishes to ("" for the task branch).
func (s *Store) runBaseRefs(ctx context.Context, org, runID string) (map[string]string, string, error) {
	var heads map[string]string
	var branch string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT base_refs, COALESCE(branch, '') FROM runs WHERE id = $1`, runID).Scan(&heads, &branch)
	})
	return heads, branch, err
}
