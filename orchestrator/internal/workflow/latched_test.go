package workflow

import (
	"context"
	"encoding/json"
	"testing"
)

// A latched key is set by either writer: the outside write landing while
// the step runs survives the step's stale false, and the step's own true
// is kept where the row held false.
func TestALatchedKeyStaysSetByEitherWriter(t *testing.T) {
	h := newHarness(t)
	var id string
	h.rt.Register(&Definition{Type: "test.latched", InitialStep: "work", Latched: []string{"theirs", "mine", "unset"},
		Steps: map[string]Step{
			"work": func(ctx context.Context, sc StepContext) (Result, error) {
				var st map[string]any
				_ = json.Unmarshal(sc.State, &st)
				if _, err := h.owner.Exec(ctx, `UPDATE workflow_runs SET state = state || '{"theirs":true}' WHERE id = $1`, id); err != nil {
					return Result{}, err
				}
				st["theirs"], st["mine"], st["unset"] = false, true, false
				return Result{Next: "wait", State: st, AwaitSignals: []string{"never"}}, nil
			},
		}})
	id = h.start("test.latched")
	h.tick()
	var theirs, mine, unset bool
	if err := h.owner.QueryRow(context.Background(), `SELECT (state->>'theirs')::boolean, (state->>'mine')::boolean,
		(state->>'unset')::boolean FROM workflow_runs WHERE id = $1`, id).Scan(&theirs, &mine, &unset); err != nil {
		t.Fatal(err)
	}
	if !theirs || !mine || unset {
		t.Errorf("theirs %v mine %v unset %v, want true true false", theirs, mine, unset)
	}
}
