package delivery

import (
	"context"

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
		if w.forges == nil {
			return c, nil
		}
		gh, err := w.forges.For(ctx, sc.OrganizationID)
		if err != nil {
			return nil, err
		}
		if c.DeltaLines, err = w.s.DeltaLines(ctx, sc.OrganizationID, st.TaskID, gh, p.BaseRefs, st.Heads); err != nil {
			return nil, err
		}
		return c, nil
	}
}
