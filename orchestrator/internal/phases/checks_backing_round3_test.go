package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Hints elided for space are not "none near": the pass whose nearest
// calls did not fit says they are omitted and where to read them, and that
// survives the ask's round trip through the ledger (#484 review).
func TestElidedHintsAreNotNoneNear(t *testing.T) {
	w := newHarnessWorld(t)
	checks := make([]delivery.Check, 3)
	for i := range 3 {
		for j := range 3 {
			bashCall(w, fmt.Sprintf("call_%d_%d", i, j), fmt.Sprintf("npx jest suite%d.test.ts --opt%d %s", i, j, strings.Repeat("x", 150)), "", 0)
		}
		checks[i] = delivery.Check{Command: fmt.Sprintf("npx jest suite%d.test.ts", i), Result: delivery.CheckPass}
	}
	checks = append(checks, delivery.Check{Command: "cargo build", Result: delivery.CheckPass})
	var closest map[int][]string
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
		_, closest, err = recordedCalls(context.Background(), tx, w.run.ID, checks, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(closest[0]) == 0 {
		t.Fatalf("the first pass names no call: %q", closest)
	}
	if c, ok := closest[2]; !ok || c != nil {
		t.Fatalf("the third pass's calls, over the budget, read %q (present %v), want elided (nil)", c, ok)
	}
	if c, ok := closest[3]; !ok || c == nil || len(c) != 0 {
		t.Fatalf("cargo build, near nothing, reads %q (present %v), want none near (empty)", c, ok)
	}
	raw, err := json.Marshal(delivery.ChecksAsked{Closest: closest})
	if err != nil {
		t.Fatal(err)
	}
	var back delivery.ChecksAsked
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	elided := unbackedHint(checks[2].Command, back.Closest[2], true)
	none := unbackedHint(checks[3].Command, back.Closest[3], true)
	if !strings.Contains(elided, "omitted") || !strings.Contains(elided, "run_chat") || strings.Contains(elided, "none it recorded is near") {
		t.Errorf("elided: %s", elided)
	}
	if !strings.Contains(none, "none it recorded is near it") || strings.Contains(none, "omitted") {
		t.Errorf("none near: %s", none)
	}
	if got := unbackedHint("make test", nil, false); got != "" {
		t.Errorf("a matched pass is given a hint: %s", got)
	}
}
