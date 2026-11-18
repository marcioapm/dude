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

// Under set -e a failing command exits the shell before the echo runs, so
// a call that completed with no exit code may end with the command's own
// "exit=0": it is not the call's exit. With exit 0 recorded, every step
// ran, the echo too.
func TestAnEchoUnderSetEIsReadOnlyWithExitZero(t *testing.T) {
	for _, c := range []struct{ name, command, status, want string }{
		{"set -e, completed with no exit", "set -euo pipefail; ./run.sh; echo exit=$?", "completed", "unclear: under set -e its echo may not have run"},
		{"set -e, after &&", "set -euo pipefail && ./run.sh; echo exit=$?", "completed", "unclear: under set -e its echo may not have run"},
		{"pipefail only, completed with no exit", "set -o pipefail; ./run.sh; echo exit=$?", "completed", "exit 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			completionOnly(w, "k1", c.command, c.status, "step 1\nexit=0")
			var got []string
			if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) (err error) {
				got, _, err = recordedCalls(context.Background(), tx, w.run.ID, []delivery.Check{{Command: "./run.sh", Result: delivery.CheckPass}}, nil)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("read %q, want %q", got, c.want)
			}
		})
	}
	// With exit 0 recorded the echo ran.
	w := newHarnessWorld(t)
	bashCall(w, "k2", "set -euo pipefail; ./run.sh; echo exit=$?", "step 1\nexit=0", 0)
	if got := w.recordedAfter(nil, "./run.sh", delivery.CheckPass, nil); len(got) != 1 || got[0] != "exit 0" {
		t.Fatalf("read %q after exit 0, want exit 0", got)
	}
}
