package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// RoundChange is what changed since a previous round (Store.RoundDelta).
// Not Measured: there was no forge to ask, or it refused or cut the
// comparison short. Descends: measured, and every previous checkout is an
// ancestor of its head now, so the previous round's verdicts may rest on
// it (carryForward).
type RoundChange struct {
	Measured bool     `json:"measured"`
	Descends bool     `json:"descends,omitempty"`
	Lines    int      `json:"lines"`
	Paths    []string `json:"paths,omitempty"`
}

// deltaLines is the change as Continue counts it: an unmeasured one is
// over MaxResumeDeltaLines.
func (c RoundChange) deltaLines() int {
	if !c.Measured {
		return MaxResumeDeltaLines + 1
	}
	return c.Lines
}

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
		change, err := w.roundChange(ctx, sc, st, phase, p.BaseRefs)
		if err != nil {
			return nil, err
		}
		return &Continue{Predecessor: p.ID, DeltaLines: change.deltaLines()}, nil
	}
}

// roundChange is what changed from the checkouts from to the state's
// heads (Store.RoundDelta), measured once per round: the durable cache
// survives artifact-collection waits and step retries, and equal
// checkouts share a measurement across categories and between the test
// step's skip (carryForward) and its tester's Continue.
func (w *steps) roundChange(ctx context.Context, sc workflow.StepContext, st *State, phase string, from map[string]string) (RoundChange, error) {
	// Keyed by the identity's sha256, not the JSON, to keep the state small.
	identity, _ := json.Marshal([]any{phase, st.Attempt, st.Iteration, st.Retries, st.PRIteration, st.Decisions, st.Candidate, from, st.Heads})
	sum := sha256.Sum256(identity)
	cacheKey := hex.EncodeToString(sum[:])
	var cached []byte
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state->'roundDelta'->$2 FROM workflow_runs WHERE id = $1`, sc.WorkflowRunID, cacheKey).Scan(&cached)
	}); err != nil {
		return RoundChange{}, err
	}
	// A line count alone, cached before paths were measured, is measured again.
	var change RoundChange
	if len(cached) > 0 && cached[0] == '{' && json.Unmarshal(cached, &change) == nil {
		return change, nil
	}
	if w.forges == nil {
		return RoundChange{}, nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	switch {
	case forge.Transient(err):
		return RoundChange{}, forgeWait(err)
	case forge.Refused(err):
		// The production resolver returns a client or a database error;
		// injected resolvers can still report a permanent forge refusal.
		return RoundChange{}, nil
	case err != nil:
		// Not the forge's answer (a database error): the step retries.
		return RoundChange{}, err
	}
	if change, err = w.s.RoundDelta(ctx, sc.OrganizationID, st.TaskID, gh, from, st.Heads); err != nil {
		return RoundChange{}, err
	}
	// Only the test step's skip reads the paths: of a change that descends
	// and is all non-product. Any other's are not kept in the state.
	if phase != PhaseTest || !change.Descends || !NonProduct(st.Policy, change.Paths) {
		change.Paths = nil
	}
	raw, _ := json.Marshal(change)
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{roundDelta}', COALESCE(state->'roundDelta', '{}'::jsonb) || jsonb_build_object($2::text, $3::jsonb)) WHERE id = $1`, sc.WorkflowRunID, cacheKey, raw)
		return err
	})
	return change, err
}
