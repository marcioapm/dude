package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ReasonSetupFailed: a repository's setup script failed a phase Run before
// its agent started. The same script on the same checkout fails the same
// way, so nothing tries again by itself: a person fixes the script (or what
// it needs) and retries, resumes the kept Run, or stops.
const ReasonSetupFailed = "setup_failed"

// SetupFailure is a repository's setup script that failed a Run, as the
// phase syncer recorded it in its run.failed (phases.SetupFailure), its
// output already redacted.
type SetupFailure struct {
	Repository string `json:"repository"`
	ExitCode   int    `json:"exitCode"`
	Output     string `json:"output"`
}

// setupFailureTx is the setup script that failed the Run, nil when it
// failed otherwise.
func setupFailureTx(ctx context.Context, tx pgx.Tx, runID string) (*SetupFailure, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload->'setup' FROM events WHERE run_id = $1 AND event_type = 'run.failed'
		AND payload ? 'setup' ORDER BY cursor DESC LIMIT 1`, runID).Scan(&raw)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f SetupFailure
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("Run %s's setup failure: %w", runID, err)
	}
	return &f, nil
}

// failedReason is the escalation for a phase Run that failed: its own
// reason (implement_failed, fix_failed…), or setup_failed when a setup
// script failed it, whatever the phase.
func (o Outcome) failedReason(phaseReason string) string {
	if o.Setup != nil {
		return ReasonSetupFailed
	}
	return phaseReason
}

// failedDetail is what the escalation for a failed phase Run carries: the
// Run and its error, and for a setup script, which, its exit code and the
// end of what it printed.
func (o Outcome) failedDetail(runID string) map[string]any {
	d := map[string]any{"runId": runID, "error": o.Error}
	if o.Setup != nil {
		d["repository"], d["exitCode"], d["output"] = o.Setup.Repository, o.Setup.ExitCode, o.Setup.Output
	}
	return d
}

// setupFailedConductor (SQL, over runs x): a conductor Run a repository's
// setup script failed.
const setupFailedConductor = `(x.status = 'failed' AND EXISTS (SELECT 1 FROM events sf WHERE sf.run_id = x.id
	AND sf.event_type = 'run.failed' AND sf.payload ? 'setup'))`

// setupBlockedOf (SQL, given the task id expression between its two
// halves): the task's latest conductor was failed by a setup script. dude
// starts no conductor for such a task by itself — not for a wake, not to
// hand on what the failed one never read — since the next would fail the
// same way, in a loop. A person's Try again, or their message in Chat,
// starts one; it is then the latest, and the hold is over.
const (
	setupBlockedOf  = `COALESCE((SELECT ` + setupFailedConductor + ` FROM runs x WHERE x.task_id = `
	setupBlockedEnd = ` AND x.role = 'conductor' AND x.kind = 'agent' ORDER BY x.created_at DESC, x.id DESC LIMIT 1), false)`
	// setupBlockedWake is it over conductor_wakes c (Wakeable).
	setupBlockedWake = setupBlockedOf + `c.task_id` + setupBlockedEnd
	// setupBlockedRun is it over the ended conductor u (Unheard).
	setupBlockedRun = setupBlockedOf + `u.task_id` + setupBlockedEnd
)

// SetupBlockedTx says whether the task's latest conductor was failed by a
// setup script (setupBlockedOf).
func SetupBlockedTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT `+setupBlockedOf+`$1`+setupBlockedEnd, taskID).Scan(&blocked)
	return blocked, err
}

// aboutSetupFailed is dude's notice in Chat that a setup script failed the
// task's conductor and dude will not start another by itself.
const aboutSetupFailed = "setup_failed"

// ConductorSetupFailedTx says in the task's Chat, once per conductor Run,
// that a setup script failed it: which repository, the line it failed on,
// and that dude starts no other conductor until a person fixes the script
// and tries again (setupBlockedOf). why is the failure in one sentence,
// as run.failed says it.
func ConductorSetupFailedTx(ctx context.Context, tx pgx.Tx, ref RunRef, f SetupFailure, why string) error {
	why = strings.TrimSuffix(strings.TrimSpace(why), ".")
	text := fmt.Sprintf("%s, so the conductor could not start. dude will not start another by itself: "+
		"fix %s's setup script in the project's repository settings, then Try again.", why, f.Repository)
	_, err := ledger.Append(ctx, tx, ref.Event(EvChatNotice, ledger.ActorSystem, map[string]any{"about": aboutSetupFailed,
		"text": text, "repository": f.Repository, "exitCode": f.ExitCode}))
	return err
}
