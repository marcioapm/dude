package delivery

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// goesOn is, per category ("" for the tester), what a round's Run of
// phase goes on from (PhaseRun.Continue): its previous round and the
// lines changed since, as GitHub compares them; nil when the policy or
// the round has it start fresh, or there is no previous round.
func (w *steps) goesOn(ctx context.Context, sc workflow.StepContext, st *State, phase string) func(category string) (*Continue, error) {
	return func(category string) (*Continue, error) {
		if !RoundContinues(st.Policy, phase, st.Integration != nil) {
			return nil, nil
		}
		p, err := w.s.PreviousRound(ctx, sc.OrganizationID, st.TaskID, st.Attempt, phase, category)
		if err != nil || p == nil {
			return nil, err
		}
		c := &Continue{Predecessor: p.ID}
		// The durable cache survives artifact-collection waits and step retries.
		// Equal predecessor checkouts share a measurement across categories.
		cacheKey, _ := json.Marshal([]any{phase, st.Attempt, st.Iteration, st.Retries, st.PRIteration, st.Decisions, st.Candidate, p.BaseRefs, st.Heads})
		var cached *int
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT (state->'roundDelta'->>$2)::int FROM workflow_runs WHERE id = $1`, sc.WorkflowRunID, string(cacheKey)).Scan(&cached)
		}); err != nil {
			return nil, err
		}
		if cached != nil {
			c.DeltaLines = *cached
			return c, nil
		}
		if w.forges == nil {
			c.DeltaLines = MaxResumeDeltaLines + 1
			return c, nil
		}
		gh, err := w.forges.For(ctx, sc.OrganizationID)
		switch {
		case forge.Transient(err):
			return nil, forgeWait(err)
		case forge.Refused(err):
			c.DeltaLines = MaxResumeDeltaLines + 1
			return c, nil
		case err != nil:
			// Not the forge's answer (a database error): the step retries.
			return nil, err
		}
		if c.DeltaLines, err = w.s.DeltaLines(ctx, sc.OrganizationID, st.TaskID, gh, p.BaseRefs, st.Heads); err != nil {
			return nil, err
		}
		if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{roundDelta}', COALESCE(state->'roundDelta', '{}'::jsonb) || jsonb_build_object($2::text, $3::int)) WHERE id = $1`, sc.WorkflowRunID, string(cacheKey), c.DeltaLines)
			return err
		}); err != nil {
			return nil, err
		}
		return c, nil
	}
}
