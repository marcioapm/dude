// A later test round retests only what the change since the previous
// round can affect: a resumed tester may carry a criterion forward
// (carried, in acceptance.go), and a change of non-product paths alone
// starts no tester at all (carryForward). dude decides what a carried
// verdict is worth, never the tester; a round that starts fresh carries
// nothing.

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
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
// policy's NonProductPaths, each glob matched against the path in its
// repository only: "docs/**" is a docs/ directory, never every file of a
// repository named docs. No paths is not a non-product change: nothing is
// known.
func NonProduct(p Policy, paths []string) bool {
	return len(paths) > 0 && !slices.ContainsFunc(paths, func(path string) bool {
		_, inRepo, _ := strings.Cut(path, "/")
		return !slices.ContainsFunc(p.NonProductPaths, func(g string) bool { return MatchesGlob(g, inRepo) })
	})
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
		if !st.Integration.cleanServerMerge() || st.RetestDue || directed || len(judge) > 0 {
			return false, nil
		}
		return w.carryOverMerge(ctx, sc, st)
	}
	prev, err := w.s.PreviousRound(ctx, sc.OrganizationID, st.TaskID, st.Attempt, PhaseTest, "")
	if err != nil || prev == nil {
		return false, err
	}
	// The same measurement, under the same key, as goesOn's for the tester.
	delta, err := w.roundChange(ctx, sc, st, PhaseTest, prev.BaseRefs)
	if err != nil || !delta.Descends {
		return false, err
	}
	st.CarryFrom = prev.ID
	if st.RetestDue || directed || len(judge) > 0 || !NonProduct(st.Policy, delta.Paths) {
		return false, nil
	}
	verdicts, ok, err := w.passedAsCriteriaRead(ctx, sc, st, prev.ID)
	if err != nil || !ok {
		return false, err
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

// passedAsCriteriaRead says test Run runID passed the gate as it stands,
// on every one of the task's criteria as they read now, and returns its
// verdicts as EvTestSkipped records them.
func (w *steps) passedAsCriteriaRead(ctx context.Context, sc workflow.StepContext, st *State, runID string) ([]map[string]any, bool, error) {
	in, err := w.s.gateInput(ctx, sc.OrganizationID, st, runID)
	if err != nil || len(in.Verdicts) == 0 || judgeGate(st.Policy, in).Outcome != "pass" {
		return nil, false, err
	}
	var criteria []string
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT acceptance_criteria FROM tasks WHERE id = $1`, st.TaskID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &criteria)
	}); err != nil {
		return nil, false, err
	}
	// The verdicts must be on the criteria as they read now: one added or
	// edited while the task was stopped has not been judged. A task
	// without criteria has the tester's derived checks, which nothing here
	// can compare, so it is tested.
	if len(in.Verdicts) != len(criteria) || slices.ContainsFunc(in.Verdicts, func(v StoredVerdict) bool {
		return v.Criterion < 1 || v.Criterion > len(criteria) || v.Text != criteria[v.Criterion-1]
	}) {
		return nil, false, nil
	}
	verdicts := make([]map[string]any, len(in.Verdicts))
	for i, v := range in.Verdicts {
		verdicts[i] = map[string]any{"criterion": v.Criterion, "status": v.Status, "decision": v.Decision}
	}
	return verdicts, true, nil
}

// MergeUntouchedWhy is EvTestSkipped's why for a clean merge of the target
// branch before the pull request (carryOverMerge).
const MergeUntouchedWhy = "merged target branch touched none of the task's files"

// cleanServerMerge says the integration is the merge GitHub made, cleanly,
// of the target branch into the task branch before its pull request opened
// (syncBeforePR's MergeBase): not a head found moved, not an agent's
// integration, not one on an open pull request.
func (i *Integration) cleanServerMerge() bool {
	return i.BeforePR && i.Published && i.ServerMerge && i.Number == 0 && strings.HasPrefix(i.RunID, "server-")
}

// carryOverMerge is carryForward's choice for a clean before-PR merge: no
// tester when the paths the merge brought in (the target branch's change
// from the task's merge base to BaseHead) share none with the task's own
// (from that merge base to the task's head before the merge), and the
// previous test round passed the gate at the heads before the merge. The
// pull request's CI checks the merge itself. Unmeasured: tested.
func (w *steps) carryOverMerge(ctx context.Context, sc workflow.StepContext, st *State) (bool, error) {
	i := st.Integration
	pre := maps.Clone(st.Heads)
	pre[i.Repo] = i.Head
	prev, err := w.s.PreviousRound(ctx, sc.OrganizationID, st.TaskID, st.Attempt, PhaseTest, "")
	if err != nil || prev == nil {
		return false, err
	}
	if ok, err := w.s.stoodAt(ctx, sc.OrganizationID, st.TaskID, prev, pre); err != nil || !ok {
		return false, err
	}
	m, err := w.mergeChange(ctx, sc, st)
	if err != nil || !m.Measured || len(m.Merged) == 0 || intersects(m.Merged, m.Task) {
		return false, err
	}
	verdicts, ok, err := w.passedAsCriteriaRead(ctx, sc, st, prev.ID)
	if err != nil || !ok {
		return false, err
	}
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		if err := carryGateOverMergeTx(ctx, tx, sc.WorkflowRunID, st, pre); err != nil {
			return err
		}
		// News for a live conductor, held for its next note (heldWakePrefix):
		// it never starts or resumes one.
		if _, err := tx.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
			SELECT $1, $2, $3, 'checkout', $4, $5 WHERE EXISTS (SELECT 1 FROM runs r WHERE r.task_id = $3 AND `+LiveConductor+`)
			ON CONFLICT (task_id, key) DO NOTHING`, ids.New("cwk"), sc.OrganizationID, st.TaskID,
			heldWakePrefix+"merge:"+i.pair(), mergeNotRetestedLine(i.Repo, i.Base, prev.ID, len(m.Merged))); err != nil {
			return err
		}
		return emitTx(ctx, tx, sc.OrganizationID, st, EvTestSkipped, map[string]any{"fromRunId": prev.ID,
			"from": pre, "heads": st.Heads, "paths": m.Merged, "criteria": verdicts, "why": MergeUntouchedWhy,
			"merge": map[string]any{"repo": i.Repo, "base": i.Base, "baseHead": i.BaseHead}})
	}); err != nil {
		return false, err
	}
	st.TestRunID = prev.ID
	return true, nil
}

// carryGateOverMergeTx moves a person's Open or Draft given at the heads
// before the merge (pre) to the merged heads: the merge is what opening the
// pull request brought in, not new work to authorize. One given at other
// heads, or none, is left as it is. gateAt is Owned: read and written in
// the row, locked.
func carryGateOverMergeTx(ctx context.Context, tx pgx.Tx, wfID string, st *State, pre map[string]string) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT state->'gateAt' FROM workflow_runs WHERE id = $1 FOR UPDATE`, wfID).Scan(&raw); err != nil {
		return err
	}
	var at *GateAt
	if len(raw) == 0 || json.Unmarshal(raw, &at) != nil || at == nil || !maps.Equal(nonNilMap(at.Heads), nonNilMap(pre)) {
		return nil
	}
	at.Heads = maps.Clone(nonNilMap(st.Heads))
	moved, _ := json.Marshal(at)
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{gateAt}', $2::jsonb) WHERE id = $1`, wfID, moved); err != nil {
		return err
	}
	st.GateAt = at
	return nil
}

// stoodAt says the verdicts of prev were the delivery's at heads: its
// round ended there, or a later skip carried them there.
func (s *Store) stoodAt(ctx context.Context, org, taskID string, prev *Predecessor, heads map[string]string) (bool, error) {
	if maps.Equal(nonNilMap(prev.BaseRefs), nonNilMap(heads)) {
		return true, nil
	}
	raw, _ := json.Marshal(nonNilMap(heads))
	var carried bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
			AND payload->>'fromRunId' = $3 AND payload->'heads' = $4::jsonb)`, taskID, EvTestSkipped, prev.ID, raw).Scan(&carried)
	})
	return carried, err
}

// MergeChange is what a clean before-PR merge brought in and what the task
// changed, as <repo>/<path>s. Not Measured: refused, cut short, or no forge.
type MergeChange struct {
	Measured bool     `json:"measured"`
	Merged   []string `json:"merged,omitempty"`
	Task     []string `json:"task,omitempty"`
}

// mergeChange measures st.Integration's two sides with GitHub's compare,
// once per merge: cached in roundDelta under the merge's pair, so a step
// that waits on GitHub and runs again asks nothing it already has.
func (w *steps) mergeChange(ctx context.Context, sc workflow.StepContext, st *State) (MergeChange, error) {
	i := st.Integration
	sum := sha256.Sum256([]byte("merge:" + i.pair()))
	cacheKey := hex.EncodeToString(sum[:])
	var cached []byte
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state->'roundDelta'->$2 FROM workflow_runs WHERE id = $1`, sc.WorkflowRunID, cacheKey).Scan(&cached)
	}); err != nil {
		return MergeChange{}, err
	}
	var m MergeChange
	if len(cached) > 0 && json.Unmarshal(cached, &m) == nil {
		return m, nil
	}
	if w.forges == nil {
		return MergeChange{}, nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	switch {
	case forge.Transient(err):
		return MergeChange{}, forgeWait(err)
	case err != nil && forge.Refused(err), err == nil && gh == nil:
		return MergeChange{}, nil
	case err != nil:
		return MergeChange{}, err
	}
	var slug string
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		repos, err := TaskRepositories(ctx, tx, st.TaskID)
		for _, r := range repos {
			if r.Name == i.Repo {
				slug = forge.SlugFromURL(r.URL)
			}
		}
		return err
	}); err != nil || slug == "" {
		return MergeChange{}, err
	}
	paths := func(base, head string) ([]string, bool, error) {
		cmp, err := gh.Compare(ctx, slug, base, head)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, false, ctx.Err()
		case forge.Transient(err):
			return nil, false, forgeWait(err)
		case err != nil, cmp.Truncated():
			return nil, false, nil
		}
		var out []string
		for _, f := range cmp.Files {
			out = append(out, i.Repo+"/"+f.Filename)
			if f.PreviousFilename != "" {
				out = append(out, i.Repo+"/"+f.PreviousFilename)
			}
		}
		return out, true, nil
	}
	// Three dots: each side from the merge base of the two.
	m.Merged, m.Measured, err = paths(i.Head, i.BaseHead)
	if err != nil || !m.Measured {
		return MergeChange{}, err
	}
	if m.Task, m.Measured, err = paths(i.BaseHead, i.Head); err != nil || !m.Measured {
		return MergeChange{}, err
	}
	raw, _ := json.Marshal(m)
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{roundDelta}', COALESCE(state->'roundDelta', '{}'::jsonb) || jsonb_build_object($2::text, $3::jsonb)) WHERE id = $1`, sc.WorkflowRunID, cacheKey, raw)
		return err
	})
	return m, err
}

func intersects(a, b []string) bool {
	return slices.ContainsFunc(a, func(p string) bool { return slices.Contains(b, p) })
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
// so it tests every criterion and may carry none (PromptInput.RetestsAfresh).
func RetestsAfreshTx(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var fresh bool
	err := tx.QueryRow(ctx, `SELECT r.phase = 'test' AND r.continues_how IS DISTINCT FROM 'resume' AND EXISTS (
			SELECT 1 FROM runs p JOIN acceptance_verdicts v ON v.run_id = p.id
			WHERE p.task_id = r.task_id AND p.attempt = r.attempt AND p.phase = 'test' AND p.id <> r.id
			  AND p.created_at < r.created_at)
		FROM runs r WHERE r.id = $1`, runID).Scan(&fresh)
	return fresh, err
}
