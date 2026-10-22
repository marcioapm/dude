package delivery

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Convergence reads the current review loop's explicit judgements after fixes.
func (s *Store) Convergence(ctx context.Context, org string, st *State) (Convergence, error) {
	var out Convergence
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		out, err = ConvergenceTx(ctx, tx, st)
		return err
	})
	return out, err
}

// ConvergenceTx shares the caller's snapshot, including uncommitted review rows.
func ConvergenceTx(ctx context.Context, tx pgx.Tx, st *State) (Convergence, error) {
	rangeJSON, err := json.Marshal(st.reviewRange())
	if err != nil {
		return Convergence{}, err
	}
	// A candidate's fixes publish to its branch; a parent fix elsewhere is
	// not part of the candidate's loop.
	var candidateBranch *string
	if st.checkingCandidate() {
		candidateBranch = &st.Candidate.Branch
	}
	// Range containment matches rangeFindings: earlier anchors may be a subset
	// of the current range after a fix starts changing another repository.
	// The origin reviews are one array and the fix-caused findings grouped
	// once per review, so a generic plan cannot rescan findings per row.
	rows, err := tx.Query(ctx, `WITH attempt_runs AS MATERIALIZED (
		SELECT id, phase, status, replaced_by, parent_run_id, finding_ids, verdicts, branch, review_range, created_at
		FROM runs WHERE task_id = $1
		AND attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1))
	), origins AS MATERIALIZED (
		SELECT COALESCE(array_agg(origin.id), '{}') ids FROM attempt_runs origin
		WHERE origin.phase = 'review' AND origin.status = 'completed' AND origin.replaced_by IS NULL
		AND ($4::jsonb = 'null'::jsonb OR (origin.review_range->>'of' = $4::jsonb->>'of'
			AND ($4::jsonb->'base') @> (origin.review_range->'base')))
	), findings AS MATERIALIZED (
		SELECT f.id, f.run_id, f.status, f.severity, f.caused_by_fix FROM review_findings f, origins o
		WHERE f.task_id = $1 AND f.run_id = ANY(o.ids)
	), raised AS MATERIALIZED (
		SELECT run_id, array_agg(id ORDER BY id) ids, array_agg(id ORDER BY id) FILTER (WHERE status = 'open') open_ids
		FROM findings WHERE caused_by_fix AND severity::text = ANY($5::text[]) GROUP BY run_id
	)
	SELECT x.id, r.verdicts,
		ARRAY(SELECT f.id FROM findings f WHERE f.status = 'open'
			AND f.id IN (SELECT unnest(x.finding_ids) INTERSECT SELECT unnest(r.finding_ids)) ORDER BY f.id),
		EXISTS (SELECT 1 FROM findings f WHERE f.id IN (SELECT unnest(x.finding_ids) INTERSECT SELECT unnest(r.finding_ids))
			AND r.verdicts->>f.id = 'fixed' AND f.severity::text = ANY($5::text[])),
		COALESCE(n.ids, '{}'), COALESCE(n.open_ids, '{}')
	FROM attempt_runs r JOIN attempt_runs x ON x.id = r.parent_run_id LEFT JOIN raised n ON n.run_id = r.id
	WHERE r.phase = 'review' AND r.status = 'completed' AND r.replaced_by IS NULL
		AND x.phase = 'fix' AND x.replaced_by IS NULL AND r.created_at > $3
		AND ($6::text IS NULL OR x.branch = $6)
		AND ($4::jsonb = 'null'::jsonb OR (r.review_range->>'of' = $4::jsonb->>'of'
			AND ($4::jsonb->'base') @> (r.review_range->'base')))
	ORDER BY x.created_at, x.id, r.created_at, r.id`,
		st.TaskID, st.Attempt, st.ConvergenceFrom, rangeJSON, st.Policy.BlockingSeverities, candidateBranch)
	if err != nil {
		return Convergence{}, err
	}
	defer rows.Close()
	out := Convergence{StillRounds: map[string]int{}}
	var parent string
	var fixedAny bool
	var raised, open []string
	// judged is the round's verdict per finding: "fixed" from any review
	// outranks "still", which outranks silence.
	judged := map[string]string{}
	// A round is every review of one parent fix: a fixed blocker and a
	// fix-caused blocker may come from different reviewers of its fan-out,
	// and a fix re-reviewed counts once. A round counts whatever its new
	// findings' status is now; only the newest round's still-open ones are
	// named.
	finish := func() {
		for id, v := range judged {
			if v == "still" {
				out.StillRounds[id]++
			} else {
				out.StillRounds[id] = 0
			}
		}
		clear(judged)
		if fixedAny && len(raised) > 0 {
			out.RegressionRounds++
			out.FindingIDs = slices.Compact(slices.Sorted(slices.Values(open)))
		} else {
			out.RegressionRounds = 0
			out.FindingIDs = nil
		}
	}
	for rows.Next() {
		var fix string
		var verdicts map[string]string
		var shown, newIDs, openIDs []string
		var fixed bool
		if err := rows.Scan(&fix, &verdicts, &shown, &fixed, &newIDs, &openIDs); err != nil {
			return Convergence{}, err
		}
		if fix != parent {
			if parent != "" {
				finish()
			}
			parent, fixedAny, raised, open = fix, false, nil, nil
		}
		for _, id := range shown {
			if v := verdicts[id]; v == "fixed" || v == "still" && judged[id] != "fixed" {
				judged[id] = v
			} else if _, ok := judged[id]; !ok {
				judged[id] = ""
			}
		}
		fixedAny = fixedAny || fixed
		raised = append(raised, newIDs...)
		open = append(open, openIDs...)
	}
	if err := rows.Err(); err != nil {
		return Convergence{}, err
	}
	if parent != "" {
		finish()
	}
	return out, nil
}
