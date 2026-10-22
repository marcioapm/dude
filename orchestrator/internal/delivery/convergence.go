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
	// Row estimates after filters on CTE columns are far below the real
	// counts, so any findings join inside a per-review loop rescans findings
	// once per review. shown therefore materializes every review's
	// sent-and-judged ids first, and findings is hash-joined to it once.
	// facts columns: id, fix, verdicts, fix_at, review_at, shown, fixed,
	// raised, raised_open. The first branch gives exactly one row per review
	// (r.id and x.id are primary keys), which max(fix) and verdicts[1] rely on;
	// HAVING count(fix) > 0 keeps only those reviews, dropping fix-caused
	// findings of reviews outside a fix round.
	rows, err := tx.Query(ctx, `WITH attempt_runs AS MATERIALIZED (
		SELECT id, attempt, phase, status, replaced_by, parent_run_id, finding_ids, verdicts, review_range, created_at
		FROM runs WHERE task_id = $1
		AND attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1))
	), origins AS MATERIALIZED (
		SELECT origin.id FROM attempt_runs origin
		WHERE origin.phase = 'review' AND origin.status = 'completed' AND origin.replaced_by IS NULL
		AND ($4::jsonb = 'null'::jsonb OR (origin.review_range->>'of' = $4::jsonb->>'of'
			AND ($4::jsonb->'base') @> (origin.review_range->'base')))
	), findings AS MATERIALIZED (
		SELECT f.id, f.run_id, f.status, f.severity, f.caused_by_fix FROM review_findings f
		JOIN origins o ON o.id = f.run_id WHERE f.task_id = $1
	), reviews AS MATERIALIZED (
		SELECT r.id, x.id fix, r.verdicts, x.finding_ids sent, r.finding_ids judged,
			x.created_at fix_at, r.created_at review_at
		FROM attempt_runs r JOIN runs x ON x.id = r.parent_run_id
		WHERE r.phase = 'review' AND r.status = 'completed' AND r.replaced_by IS NULL
			AND x.task_id = $1 AND x.attempt = r.attempt
			AND x.phase = 'fix' AND x.replaced_by IS NULL AND r.created_at > $3
			AND ($6::text IS NULL OR x.branch = $6)
			AND ($4::jsonb = 'null'::jsonb OR (r.review_range->>'of' = $4::jsonb->>'of'
				AND ($4::jsonb->'base') @> (r.review_range->'base')))
	), shown AS MATERIALIZED (
		SELECT v.id, v.verdicts, s.id finding
		FROM reviews v CROSS JOIN LATERAL (SELECT unnest(v.sent) INTERSECT SELECT unnest(v.judged)) s(id)
	), facts AS (
		SELECT v.id, v.fix, v.verdicts, v.fix_at, v.review_at,
			NULL::text shown, false fixed, NULL::text raised, NULL::text raised_open
		FROM reviews v
		UNION ALL
		SELECT v.id, NULL, NULL, NULL, NULL, CASE WHEN f.status = 'open' THEN f.id END,
			v.verdicts->>f.id = 'fixed' AND f.severity::text = ANY($5::text[]), NULL, NULL
		FROM shown v JOIN findings f ON f.id = v.finding
		UNION ALL
		SELECT f.run_id, NULL, NULL, NULL, NULL, NULL, false, f.id, CASE WHEN f.status = 'open' THEN f.id END
		FROM findings f WHERE f.caused_by_fix AND f.severity::text = ANY($5::text[])
	)
	SELECT max(fix), (array_agg(verdicts) FILTER (WHERE fix IS NOT NULL))[1],
		COALESCE(array_agg(shown ORDER BY shown) FILTER (WHERE shown IS NOT NULL), '{}'), bool_or(fixed),
		COALESCE(array_agg(raised ORDER BY raised) FILTER (WHERE raised IS NOT NULL), '{}'),
		COALESCE(array_agg(raised_open ORDER BY raised_open) FILTER (WHERE raised_open IS NOT NULL), '{}')
	FROM facts GROUP BY id HAVING count(fix) > 0
	ORDER BY max(fix_at), max(fix), max(review_at), id`,
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
