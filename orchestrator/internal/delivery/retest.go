// A later test round retests only what the change since the previous
// round can affect: a resumed tester may carry a criterion forward
// (carried, in acceptance.go), and a change of non-product paths alone
// starts no tester at all (carryForward). dude decides what a carried
// verdict is worth, never the tester; a round that starts fresh carries
// nothing.

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// EvTestSkipped is a test step that started no tester. Payload:
// {fromRunId, from, heads, paths, criteria}: the previous round whose
// verdicts stand, its checkouts, the heads they now stand for, the paths
// changed between (all non-product), and each verdict as {criterion,
// status, decision}.
const EvTestSkipped = "test.skipped"

// carryTx is what test Run runID's carried criteria may rest on: the
// round it resumed, when that is this attempt's previous test round
// (from), and its recorded verdicts. nil for a Run that resumed no round
// (a first round, a fresh one after a large change, a replay): it tests
// every criterion.
func carryTx(ctx context.Context, tx pgx.Tx, runID, from string) (*Carry, error) {
	var prev string
	err := tx.QueryRow(ctx, `SELECT p.id FROM runs r JOIN runs p ON p.id = r.continues_run_id
		WHERE r.id = $1 AND r.continues_how = 'resume' AND r.phase = 'test' AND p.phase = 'test'
		  AND p.task_id = r.task_id AND p.attempt = r.attempt`, runID).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT criterion, text, status, evidence, observed, problem, COALESCE(decision, ''),
			COALESCE(carried_from, '')
		FROM acceptance_verdicts WHERE run_id = $1`, prev)
	if err != nil {
		return nil, err
	}
	verdicts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[CriterionVerdict])
	if err != nil {
		return nil, err
	}
	c := &Carry{RunID: prev, Descends: prev == from, Verdicts: map[int]CriterionVerdict{}}
	for _, v := range verdicts {
		c.Verdicts[v.Criterion] = v
	}
	return c, nil
}

func (c *Carry) runID() string {
	if c == nil {
		return ""
	}
	return c.RunID
}

// NonProduct says paths, each <repo>/<path in repo>, are all among the
// policy's NonProductPaths, matched as ReviewersFor matches a rule: by
// either form. No paths is not a non-product change: nothing is known.
func NonProduct(p Policy, paths []string) bool {
	return len(paths) > 0 && !slices.ContainsFunc(paths, func(path string) bool {
		_, inRepo, _ := strings.Cut(path, "/")
		return !slices.ContainsFunc(p.NonProductPaths, func(g string) bool { return MatchesGlob(g, path) || MatchesGlob(g, inRepo) })
	})
}

// roundDelta is what changed from the previous round's checkouts to the
// heads now, by GitHub's compare: the paths (a rename's both), and whether
// every previous checkout is an ancestor of its head now. Descends is
// false when that cannot be told (no forge, a refusal, a comparison cut
// short): nothing may rest on the previous round then.
type roundDelta struct {
	Paths    []string
	Descends bool
}

func (w *steps) roundDelta(ctx context.Context, sc workflow.StepContext, st *State, from map[string]string) (roundDelta, error) {
	var d roundDelta
	if w.forges == nil {
		return d, nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	switch {
	case forge.Transient(err):
		return d, forgeWait(err)
	case forge.Refused(err) || err == nil && gh == nil:
		return d, nil
	case err != nil:
		return d, err
	}
	var repos []Repository
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
		repos, err = TaskRepositories(ctx, tx, st.TaskID)
		return err
	}); err != nil {
		return d, err
	}
	for _, repo := range repos {
		now, prev := st.Heads[repo.Name], from[repo.Name]
		if now == prev {
			continue
		}
		if now == "" || prev == "" {
			return roundDelta{}, nil
		}
		cmp, err := gh.Compare(ctx, forge.SlugFromURL(repo.URL), prev, now)
		if forge.Transient(err) {
			return roundDelta{}, forgeWait(err)
		}
		if err != nil || cmp.Truncated() || cmp.BehindBy > 0 {
			return roundDelta{}, nil
		}
		for _, f := range cmp.Files {
			d.Paths = append(d.Paths, repo.Name+"/"+f.Filename)
			if f.PreviousFilename != "" {
				d.Paths = append(d.Paths, repo.Name+"/"+f.PreviousFilename)
			}
		}
	}
	d.Descends = true
	return d, nil
}

// carryForward is the test step's choice before it starts a tester. It
// sets st.CarryFrom to the attempt's previous test round when this round's
// heads descend from that round's checkouts, so a resumed tester may carry
// criteria from it. It starts no tester (skip) when, besides, every path
// changed since is a non-product one, the tester has no finding to judge
// (judge), no fix of a failed test is due, the conductor did not ask for
// this test (directed), and the previous round passed the gate as it
// stands, on every one of the task's criteria as they read now: its
// verdicts are this round's, recorded as EvTestSkipped, and
// st.TestRunID names that round for the gate.
func (w *steps) carryForward(ctx context.Context, sc workflow.StepContext, st *State, judge []string, directed bool) (skip bool, err error) {
	st.CarryFrom = ""
	if st.Integration != nil {
		return false, nil
	}
	prev, err := w.s.PreviousRound(ctx, sc.OrganizationID, st.TaskID, st.Attempt, PhaseTest, "")
	if err != nil || prev == nil {
		return false, err
	}
	delta, err := w.roundDelta(ctx, sc, st, prev.BaseRefs)
	if err != nil || !delta.Descends {
		return false, err
	}
	st.CarryFrom = prev.ID
	if st.RetestDue || directed || len(judge) > 0 || !NonProduct(st.Policy, delta.Paths) {
		return false, nil
	}
	in, err := w.s.gateInput(ctx, sc.OrganizationID, st, prev.ID)
	if err != nil || len(in.Verdicts) == 0 || judgeGate(st.Policy, in).Outcome != "pass" {
		return false, err
	}
	var criteria []string
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT acceptance_criteria FROM tasks WHERE id = $1`, st.TaskID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &criteria)
	}); err != nil {
		return false, err
	}
	// The verdicts must be on the criteria as they read now: one added or
	// edited while the task was stopped has not been judged. A task
	// without criteria has the tester's derived checks, which nothing here
	// can compare, so it is tested.
	if len(in.Verdicts) != len(criteria) || slices.ContainsFunc(in.Verdicts, func(v StoredVerdict) bool {
		return v.Criterion < 1 || v.Criterion > len(criteria) || v.Text != criteria[v.Criterion-1]
	}) {
		return false, nil
	}
	verdicts := make([]map[string]any, len(in.Verdicts))
	for i, v := range in.Verdicts {
		verdicts[i] = map[string]any{"criterion": v.Criterion, "status": v.Status, "decision": v.Decision}
	}
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return emitTx(ctx, tx, sc.OrganizationID, st, EvTestSkipped, map[string]any{"fromRunId": prev.ID,
			"from": prev.BaseRefs, "heads": st.Heads, "paths": delta.Paths, "criteria": verdicts})
	}); err != nil {
		return false, err
	}
	st.TestRunID = prev.ID
	return true, nil
}

// carryFrom is the run a resumed round's message names for carried
// criteria: its predecessor, when the step found this round's heads
// descend from it (PhaseRun.CarryFrom); "" for a review, or a test round
// that retests every criterion.
func carryFrom(in PhaseRun, predecessor string) string {
	if in.Phase != PhaseTest || in.CarryFrom != predecessor {
		return ""
	}
	return predecessor
}

// RetestsAfreshTx says test Run runID starts afresh after an earlier
// test round of its attempt: it resumed none (a large change, a replay),
// so it tests every criterion and may carry none (FreshRetest).
func RetestsAfreshTx(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var fresh bool
	err := tx.QueryRow(ctx, `SELECT r.phase = 'test' AND r.continues_how IS DISTINCT FROM 'resume' AND EXISTS (
			SELECT 1 FROM runs p JOIN acceptance_verdicts v ON v.run_id = p.id
			WHERE p.task_id = r.task_id AND p.attempt = r.attempt AND p.phase = 'test' AND p.id <> r.id
			  AND p.created_at < r.created_at)
		FROM runs r WHERE r.id = $1`, runID).Scan(&fresh)
	return fresh, err
}
