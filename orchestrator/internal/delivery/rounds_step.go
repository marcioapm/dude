package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// RoundChange is what changed since a previous round (Store.RoundDelta).
// Not Measured: there was no forge to ask, or it refused or cut the
// comparison short.
type RoundChange struct {
	Measured bool     `json:"measured"`
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

// ReviewChoice is the reviewers the policy chose for one review round,
// named by the round's creation key, at the heads it was measured to, and
// why each runs.
type ReviewChoice struct {
	Round      string              `json:"round"`
	Heads      map[string]string   `json:"heads"`
	Categories []string            `json:"categories"`
	Reasons    map[string][]string `json:"reasons"`
}

// keepReviewChoice records the round's choice on the workflow row before
// any of its Runs is created: reviewChoice is Owned, so it outlives a step
// that fails part-way through creating them.
func (w *steps) keepReviewChoice(ctx context.Context, sc workflow.StepContext, c ReviewChoice) error {
	raw, _ := json.Marshal(c)
	return w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{reviewChoice}', $2::jsonb) WHERE id = $1`, sc.WorkflowRunID, raw)
		return err
	})
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
// checkouts share a measurement across categories and with the review
// step's choice of reviewers.
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
	// Only a review round's choice of reviewers reads the paths, and not
	// past MaxResumeDeltaLines; the cache is rewritten with the state at
	// every step transition.
	if phase != PhaseReview || change.Lines > MaxResumeDeltaLines {
		change.Paths = nil
	}
	raw, _ := json.Marshal(change)
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{roundDelta}', COALESCE(state->'roundDelta', '{}'::jsonb) || jsonb_build_object($2::text, $3::jsonb)) WHERE id = $1`, sc.WorkflowRunID, cacheKey, raw)
		return err
	})
	return change, err
}

// lastReviewRound is where the checkouts of the attempt's latest completed
// review round were, of any category; nil before its first. Integration
// reviews, replaced Runs and adopted ones are not rounds of the loop.
func (s *Store) lastReviewRound(ctx context.Context, org string, st *State) (map[string]string, error) {
	var base map[string]string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT `+roundEnd+` FROM runs r
			WHERE r.task_id = $1 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1), 1)
			  AND r.phase = 'review' AND r.status = 'completed'
			  AND r.replaced_by IS NULL AND r.source_kind IS NULL AND `+notIntegration+`
			ORDER BY r.ended_at DESC, r.id DESC LIMIT 1`, st.TaskID, st.Attempt).Scan(&base)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	return base, err
}
