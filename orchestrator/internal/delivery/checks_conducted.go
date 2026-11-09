package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Ledger events of a conducted checks gate, on the writer's Run: the gate
// asked the conductor about a turn's report (run.checks.asked: what was
// reported, the waivers it could carry and the task's bases, so the verdict
// is checked against exactly what was asked), and the conductor decided it
// (run.checks.conducted: {turn, epoch, checks, missing, conductorRunId},
// actor agent with the conductor's Run as ActorID). The verdict is the
// immutable audit record the gate's judgement cites by cursor.
const (
	EvRunChecksAsked     = "run.checks.asked"
	EvRunChecksConducted = "run.checks.conducted"
)

// WakeChecks is the conductor_wakes kind of a checks report to decide,
// keyed checks:<run>:<turn>.
const WakeChecks = "checks"

// checksLineChars bounds a checks wake's line: the report's summary and
// one question per check (migration 250).
const checksLineChars = 2000

// The conductor's verdicts on a checks report (decide_checks).
const (
	VerdictBacked       = "backed"
	VerdictUnbacked     = "unbacked"
	VerdictSameAsWaived = "same_as_waived"
	VerdictPreexisting  = "preexisting"
	VerdictDeferred     = "deferred"
	VerdictReject       = "reject"
	MissingNone         = "none"
	MissingFound        = "missing"
)

// ChecksAnswerWait is how long the gate waits for the conductor's verdict
// before its fallback (ConductorAnswerWait). A variable: tests and the
// orchestrator's settings shorten it.
var ChecksAnswerWait = ConductorAnswerWait

// ChecksBase is a target-branch commit a failure is measured against.
type ChecksBase struct{ Repo, Branch, SHA string }

// ChecksAsked is what the gate asked the conductor about one turn.
type ChecksAsked struct {
	Turn  int64 `json:"turn"`
	Epoch int   `json:"epoch"`
	// When the gate stops waiting (ChecksAnswerWait from the ask).
	Deadline time.Time `json:"deadline"`
	Check    []Check   `json:"checks"`
	// Waivers: per reported check (by index), the attempt waiver it may be
	// the same failure as.
	Waivers map[int]*CarriedWaiver `json:"waivers,omitempty"`
	// Bases: the target-branch commits a failure is measured against,
	// "repo: branch at sha".
	Bases     []ChecksBase `json:"bases,omitempty"`
	TestPhase bool         `json:"testPhase,omitempty"`
}

// ConductedCheck is the conductor's verdict on one reported check.
type ConductedCheck struct {
	Command string `json:"command"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	// The waiver's cursor a same_as_waived verdict cites.
	WaiverCursor int64 `json:"waiverCursor,omitempty"`
}

// ConductedMissing is the conductor's verdict on whether a check is
// obviously missing from the report.
type ConductedMissing struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// ConductedChecks is a recorded verdict; Cursor its event's.
type ConductedChecks struct {
	Turn           int64            `json:"turn"`
	Epoch          int              `json:"epoch"`
	Checks         []ConductedCheck `json:"checks"`
	Missing        ConductedMissing `json:"missing"`
	ConductorRunID string           `json:"conductorRunId"`
	Cursor         int64            `json:"-"`
}

// ChecksWaiting (SQL, over a Run aliased r): the gate asked the conductor
// about the Run's latest turn, which it has neither decided nor been judged
// on, within ConductorAnswerWait. It holds the turn like an open question,
// so the Run is parked past its grace, and its end (a verdict, or the
// deadline) lets the park resume.
const ChecksWaiting = `EXISTS (SELECT 1 FROM ` + checksAsked + `)`

// checksAsked (SQL, over a Run aliased r): the gate's open ask of
// ChecksWaiting, as events a.
const checksAsked = `events a WHERE a.run_id = r.id AND a.event_type = '` + EvRunChecksAsked + `'
	AND (a.payload->>'deadline')::timestamptz > now()
	AND (a.payload->>'turn')::bigint = (SELECT COALESCE(max(t.cursor), 0) FROM events t WHERE t.run_id = r.id AND t.event_type = 'agent.session.stopped')
	AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = r.id AND v.event_type IN ('` + EvRunChecksConducted + `', '` + EvRunChecksJudged + `')
		AND v.payload->>'turn' = a.payload->>'turn')`

// ChecksKey is the wake key of a turn's checks report.
func ChecksKey(runID string, turn int64) string { return fmt.Sprintf("checks:%s:%d", runID, turn) }

// Conducted says the task's live delivery is decided by its conductor.
func (d *Delivery) Conducted() bool { return d != nil && d.Live() && d.State.conducted() }

// ChecksConductorUnavailableTx is the availability predicate writer
// questions use (questionConductorUnavailableTx): no live conductor, and
// either no model or the latest conductor Run failed.
func ChecksConductorUnavailableTx(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	return questionConductorUnavailableTx(ctx, tx, taskID)
}

// AskChecksTx records that the gate asks the conductor about ref's turn,
// wakes it once by key and starts the Run's wait, so the existing grace
// and park apply. A repeat for the same turn changes nothing.
func AskChecksTx(ctx context.Context, tx pgx.Tx, ref RunRef, asked ChecksAsked, line string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = $2 AND (payload->>'turn')::bigint = $3)`,
		ref.RunID, EvRunChecksAsked, asked.Turn).Scan(&exists); err != nil || exists {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT now() + make_interval(secs => $1)`, ChecksAnswerWait.Seconds()).Scan(&asked.Deadline); err != nil {
		return err
	}
	raw, _ := json.Marshal(asked)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunChecksAsked, ledger.ActorSystem, payload)); err != nil {
		return err
	}
	if _, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, WakeChecks, ChecksKey(ref.RunID, asked.Turn), line); err != nil {
		return err
	}
	return waitOnPersonTx(ctx, tx, ref.RunID)
}

// ChecksAskedTx is what the gate asked about runID's turn; nil, with
// overdue false, when it asked nothing. overdue: ConductorAnswerWait has
// passed since, on the database's clock.
func ChecksAskedTx(ctx context.Context, tx pgx.Tx, runID string, turn int64) (asked *ChecksAsked, overdue bool, err error) {
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload, (payload->>'deadline')::timestamptz <= now() FROM events
		WHERE run_id = $1 AND event_type = $2 AND (payload->>'turn')::bigint = $3 ORDER BY cursor LIMIT 1`,
		runID, EvRunChecksAsked, turn).Scan(&raw, &overdue)
	if db.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	asked = &ChecksAsked{}
	return asked, overdue, json.Unmarshal(raw, asked)
}

// ConductedChecksTx is the conductor's verdict on runID's turn; nil for
// none. Keyed on the turn: a verdict for a superseded turn is never read.
func ConductedChecksTx(ctx context.Context, tx pgx.Tx, runID string, turn int64) (*ConductedChecks, error) {
	var raw []byte
	var c ConductedChecks
	err := tx.QueryRow(ctx, `SELECT payload, cursor FROM events WHERE run_id = $1 AND event_type = $2
		AND (payload->>'turn')::bigint = $3 ORDER BY cursor LIMIT 1`, runID, EvRunChecksConducted, turn).Scan(&raw, &c.Cursor)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, json.Unmarshal(raw, &c)
}

// DecideChecks is decide_checks' input.
type DecideChecks struct {
	RunID   string           `json:"runId"`
	Turn    int64            `json:"turn"`
	Checks  []ConductedCheck `json:"checks"`
	Missing ConductedMissing `json:"missing"`
}

// ConductChecksTx records the task's live conductor's verdict on a
// writer's checks report the gate is waiting on, as run.checks.conducted
// on the writer's Run. It refuses a Run not of the task, a turn that is
// not the Run's latest, a report the gate is not waiting on, and a
// verdict set that does not decide every reported check exactly once with
// a verdict its result allows and a reason.
func ConductChecksTx(ctx context.Context, tx pgx.Tx, ref RunRef, in DecideChecks) (int64, error) {
	if err := liveConductor(ctx, tx, ref); err != nil {
		return 0, err
	}
	var ofTask bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1 AND task_id = $2 AND phase IS NOT NULL AND role IS DISTINCT FROM 'conductor'
		AND attempt = task_attempt($2))`, in.RunID, ref.TaskID).Scan(&ofTask); err != nil {
		return 0, err
	}
	if !ofTask {
		return 0, refuseText(rChecksRunNotOfTask)
	}
	// The writer's row, locked: a verdict and the gate's own reading of it
	// are serialised on it.
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(project_id, '') FROM runs WHERE id = $1 FOR UPDATE`, in.RunID).Scan(&projectID); err != nil {
		return 0, err
	}
	latest, err := LatestTurnTx(ctx, tx, in.RunID)
	if err != nil {
		return 0, err
	}
	if in.Turn != latest {
		return 0, refusef(rChecksNotLatestTurn, latest)
	}
	asked, _, err := ChecksAskedTx(ctx, tx, in.RunID, in.Turn)
	if err != nil {
		return 0, err
	}
	var settled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type IN ($2, $3)
		AND (payload->>'turn')::bigint = $4)`, in.RunID, EvRunChecksConducted, EvRunChecksJudged, in.Turn).Scan(&settled); err != nil {
		return 0, err
	}
	if asked == nil || settled {
		return 0, refuseText(rChecksNotWaiting)
	}
	if err := checkVerdicts(asked, in); err != nil {
		return 0, err
	}
	writer := ref
	writer.RunID, writer.ProjectID = in.RunID, projectID
	ev := writer.Event(EvRunChecksConducted, ledger.ActorAgent, map[string]any{"turn": in.Turn, "epoch": asked.Epoch,
		"checks": in.Checks, "missing": in.Missing, "conductorRunId": ref.RunID})
	ev.ActorID = ref.RunID
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return 0, err
	}
	var cursor int64
	err = tx.QueryRow(ctx, `SELECT max(cursor) FROM events WHERE run_id = $1 AND event_type = $2`, in.RunID, EvRunChecksConducted).Scan(&cursor)
	return cursor, err
}

// checksReasonMax bounds a verdict's reason, and missing's: a reason goes
// into the writer's send-back and the ledger verbatim.
const checksReasonMax = 2000

// checkVerdicts refuses a verdict set that does not decide what was asked.
func checkVerdicts(asked *ChecksAsked, in DecideChecks) error {
	if len(in.Checks) != len(asked.Check) {
		return refusef(rChecksIncomplete, checksCommands(asked))
	}
	for i, c := range asked.Check {
		v := in.Checks[i]
		if strings.TrimSpace(v.Command) != strings.TrimSpace(c.Command) {
			return refusef(rChecksIncomplete, checksCommands(asked))
		}
		if strings.TrimSpace(v.Reason) == "" {
			return refusef(rChecksNeedsReason, c.Command)
		}
		if n := utf8.RuneCountInString(v.Reason); n > checksReasonMax {
			return refusef(rChecksReasonLong, checksReasonMax, fmt.Sprintf("%q", c.Command), n)
		}
		allowed := []string{VerdictBacked, VerdictUnbacked}
		switch c.Result {
		case CheckFail:
			allowed = []string{VerdictPreexisting, VerdictReject}
			if asked.Waivers[i] != nil {
				allowed = append(allowed, VerdictSameAsWaived)
			}
		case CheckNotRun:
			allowed = []string{VerdictDeferred, VerdictReject}
		case CheckPass:
		default:
			allowed = []string{VerdictReject}
		}
		if !slices.Contains(allowed, v.Verdict) {
			return refusef(rChecksVerdictNotAllowed, v.Verdict, c.Command, c.Result, strings.Join(allowed, ", "))
		}
		if v.Verdict == VerdictPreexisting && len(asked.Bases) == 0 {
			return refuseText(rChecksNoBase)
		}
		if v.Verdict == VerdictSameAsWaived && v.WaiverCursor != asked.Waivers[i].Cursor {
			return refusef(rChecksWaiverCursor, c.Command, asked.Waivers[i].Cursor)
		}
	}
	if in.Missing.Verdict != MissingNone && in.Missing.Verdict != MissingFound || strings.TrimSpace(in.Missing.Reason) == "" {
		return refuseText(rChecksMissingVerdict)
	}
	if n := utf8.RuneCountInString(in.Missing.Reason); n > checksReasonMax {
		return refusef(rChecksReasonLong, checksReasonMax, "missing", n)
	}
	return nil
}

// checksCommands lists what was asked, in order: each command, with the
// cursor a same_as_waived verdict on it cites when it may carry a waiver.
func checksCommands(asked *ChecksAsked) string {
	out := make([]string, len(asked.Check))
	for i, c := range asked.Check {
		out[i] = fmt.Sprintf("%q", c.Command)
		if w := asked.Waivers[i]; w != nil {
			out[i] += fmt.Sprintf(" (waiverCursor %d)", w.Cursor)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}
