package agenttools_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// What the machine tools say past delivery's offer and refusals: the size
// list's description and the request's own refusals, as a Run gets them.
// Rewritten by the package's -update.
func TestGoldenMachineToolWords(t *testing.T) {
	f, token := machineRun(t, func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error { return nil })
	var b strings.Builder
	say := func(name, text string) { b.WriteString("## " + name + "\n\n" + text + "\n\n") }
	refused := func(body string) string {
		_, out := f.post(t, token, "request_machine", body)
		return fmt.Sprint(out["error"])
	}
	say("list", toolDescriptions(t, f, token)["list_machine_sizes"])
	say("not-larger", refused(`{"size":"Standard","reason":"oom"}`))
	mustExec(t, f.owner, `UPDATE runs SET status = 'paused' WHERE id = 'run_mach'`)
	say("not-live", refused(`{"size":"Large","reason":"oom"}`))
	mustExec(t, f.owner, `UPDATE runs SET status = 'running' WHERE id = 'run_mach'`)
	for i := range 3 {
		mustExec(t, f.owner, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply, settled_at)
			VALUES ($1, $2, $3, 'run_mach', 'reviewer', 'raise', '{}', 'ms_large', 'resize', now())`, fmt.Sprintf("mmv_%d", i), f.org, f.item)
	}
	mustExec(t, f.owner, `UPDATE runs SET machine = NULL WHERE id = 'run_mach'`)
	say("limit-no-machine", refused(`{"size":"Large","reason":"oom"}`))
	mustExec(t, f.owner, `DELETE FROM machine_moves WHERE run_id = 'run_mach'`)
	mustExec(t, f.owner, `UPDATE runs SET machine = '{"sizeId":"ms_xl","name":"XL","cpus":8,"memoryMiB":32768,"diskGiB":80,"poolId":"pool_high","from":"task"}' WHERE id = 'run_mach'`)
	say("list-largest", toolDescriptions(t, f, token)["list_machine_sizes"])
	// A pending move whose size was deleted (to_size_id SET NULL) names none.
	mustExec(t, f.owner, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply)
		VALUES ('mmv_gone', $1, $2, 'run_mach', 'reviewer', 'raise', '{}', NULL, 'resize')`, f.org, f.item)
	say("moving-to-a-removed-size", refused(`{"size":"Large","reason":"oom"}`))
	org := f.str(t, `SELECT name FROM organizations WHERE id = $1`, f.org)
	path := filepath.Join("..", "delivery", "agents", "testdata", "results-machines.golden.md")
	got := strings.ReplaceAll(b.String(), org+"'s", "ORG's")
	if updating() {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || got != string(want) {
		t.Errorf("results-machines differs from %s (%v):\n%s", path, err, got)
	}
}

// updating says whether the test binary runs with -update, the flag the
// package's internal goldens define.
func updating() bool {
	u := flag.Lookup("update")
	return u != nil && u.Value.String() == "true"
}
