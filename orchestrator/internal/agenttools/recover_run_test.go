package agenttools_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Asking again for a recovery that has ended answers that recovery, as it
// ended: its status and why, and — as it pushed nothing — that another try
// needs another branch; never a wake, which it already had.
func TestRecoverRunAskedAgainAfterItEndedSaysSo(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_dead', $1, $2, $3, 1, 'failed', 'implement', 'implementer')`, f.org, f.project, f.item)
	prefix := delivery.RecoveredBranchPrefix(f.item)
	why := "lux kept no checkpoint of its checkouts"
	mustExec(t, f.owner, `INSERT INTO run_recoveries (id, organization_id, task_id, run_id, branch, status, error, ended_at)
		VALUES ('rcv_ended', $1, $2, 'run_dead', $3, 'failed', $4, now())`, f.org, f.item, prefix+"run_dead", why)
	var out map[string]any
	if code := f.postAs(t, token, "recover_run", `{"run":"run_dead"}`, &out); code != 200 {
		t.Fatalf("recover_run: %d %v", code, out)
	}
	want := map[string]any{"recoveryId": "rcv_ended", "run": "run_dead", "branch": prefix + "run_dead", "status": "failed",
		"ended": true, "error": why}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %v, want %v", k, out[k], v)
		}
	}
	// Its words are the golden's (results-tools, recover-run-ended-failed).
	if next, _ := out["next"].(string); next != agenttools.RecoverEndedNext("failed", why, prefix) || next == agenttools.RecoverNext {
		t.Errorf("an ended recovery's next: %v", out["next"])
	}
}
