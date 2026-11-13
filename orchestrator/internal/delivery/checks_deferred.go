package delivery

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A deferred check is a promise: the conductor passed a check not run
// because a later phase runs it (VerdictDeferred, CheckDeferred). Once the
// pull request is open at a head and its CI has reported there, that later
// phase has come: dude asks the conductor again about the deferred checks
// still outstanding (a second checks ask, its run.checks.asked carrying
// Discharge), where deferred is no longer a verdict. Backed keeps the
// promise at those heads; reject leaves it broken, for a person to settle
// (checks_unverified, as a report held after the rounds). The ready gate's
// deferred item holds only when every deferred check of the attempt is
// kept or a person waived it.

// ChecksDischarge marks a second checks ask (and its answer): the attempt,
// the open pull requests' heads it was asked at (repository to head), and
// its key (DischargeKey).
type ChecksDischarge struct {
	Attempt int               `json:"attempt"`
	Heads   map[string]string `json:"heads"`
	Key     string            `json:"key"`
}

// DischargeKey is the wake key of the second ask about runID's turn at
// heads in attempt: one per attempt and heads.
func DischargeKey(runID string, turn int64, attempt int, heads map[string]string) string {
	return fmt.Sprintf("%s:d%d:%s", ChecksKey(runID, turn), attempt, headsID(heads))
}

// headsID names a set of heads: the same heads, the same name.
func headsID(heads map[string]string) string {
	h := sha256.New()
	for _, repo := range slices.Sorted(maps.Keys(heads)) {
		fmt.Fprintf(h, "%s=%s;", repo, heads[repo])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// deferredCheck is one deferred check of the attempt: the newest gated
// Run whose judgement deferred it, that judgement's turn, the command and
// the conductor's reason.
type deferredCheck struct {
	RunID, ProjectID string
	Turn             int64
	Command, Reason  string
}

// dischargeAnswer is the conductor's answer about one check at a second
// ask: at which heads, its verdict and reason, and its event's cursor.
type dischargeAnswer struct {
	Heads           string
	Verdict, Reason string
	Cursor          int64
}

// deferredState is where each deferred check of the attempt stands.
type deferredState struct {
	checks []deferredCheck
	// By command: the latest answer, a backed answer by heads, the heads
	// it was asked at, and a person's waiver after the latest reject.
	latest map[string]dischargeAnswer
	backed map[string]map[string]bool
	asked  map[string]map[string]bool
	waived map[string]int64
}

// readDeferredTx reads the attempt's deferred checks and every second ask,
// answer and waiver about them.
func readDeferredTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (*deferredState, error) {
	// Each gated Run's newest judgement that is not unchanged, as gatedRuns
	// reads it.
	rows, err := tx.Query(ctx, `SELECT id, project_id, turn, checks FROM (
		SELECT DISTINCT ON (r.id) r.id, COALESCE(r.project_id, '') AS project_id, r.created_at,
			COALESCE((e.payload->>'turn')::bigint, 0) AS turn, COALESCE(e.payload->'checks', '[]'::jsonb) AS checks
		FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND r.phase IN ('implement','fix','simplify') AND e.event_type = $3
		 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1))
		ORDER BY r.id, (e.payload->>'unchanged')::boolean IS TRUE, e.cursor DESC) latest
		ORDER BY created_at, id`, taskID, attempt, EvRunChecksJudged)
	if err != nil {
		return nil, err
	}
	s := &deferredState{latest: map[string]dischargeAnswer{}, backed: map[string]map[string]bool{},
		asked: map[string]map[string]bool{}, waived: map[string]int64{}}
	at := map[string]int{}
	var runID, projectID string
	var turn int64
	var raw []byte
	if _, err := pgx.ForEachRow(rows, []any{&runID, &projectID, &turn, &raw}, func() error {
		var checks []struct{ Command, Result, Reason string }
		if json.Unmarshal(raw, &checks) != nil {
			return nil
		}
		for _, c := range checks {
			if c.Result != CheckDeferred {
				continue
			}
			d := deferredCheck{runID, projectID, turn, strings.TrimSpace(c.Command), c.Reason}
			if i, ok := at[d.Command]; ok {
				// The newest Run that deferred it holds the promise.
				s.checks[i] = d
				continue
			}
			at[d.Command] = len(s.checks)
			s.checks = append(s.checks, d)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(s.checks) == 0 {
		return s, nil
	}
	// The second asks, their answers and a person's waivers, oldest first.
	rows, err = tx.Query(ctx, `SELECT e.event_type, e.cursor, e.payload FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1))
		AND e.event_type IN ($3, $4, $5) AND e.payload ? 'discharge'
		ORDER BY e.cursor`, taskID, attempt, EvRunChecksAsked, EvRunChecksConducted, EvRunChecksDecided)
	if err != nil {
		return nil, err
	}
	var typ string
	var cursor int64
	_, err = pgx.ForEachRow(rows, []any{&typ, &cursor, &raw}, func() error {
		var p struct {
			Discharge json.RawMessage  `json:"discharge"`
			Checks    []ConductedCheck `json:"checks"`
			Action    string           `json:"action"`
			Commands  []string         `json:"commands"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil
		}
		var dis ChecksDischarge
		_ = json.Unmarshal(p.Discharge, &dis)
		heads := headsID(dis.Heads)
		mark := func(m map[string]map[string]bool, command string) {
			if m[command] == nil {
				m[command] = map[string]bool{}
			}
			m[command][heads] = true
		}
		switch typ {
		case EvRunChecksAsked:
			var asked struct {
				Check []Check `json:"checks"`
			}
			_ = json.Unmarshal(raw, &asked)
			for _, c := range asked.Check {
				mark(s.asked, strings.TrimSpace(c.Command))
			}
		case EvRunChecksConducted:
			for _, v := range p.Checks {
				command := strings.TrimSpace(v.Command)
				s.latest[command] = dischargeAnswer{heads, v.Verdict, v.Reason, cursor}
				if v.Verdict == VerdictBacked {
					mark(s.backed, command)
				}
			}
		case EvRunChecksDecided:
			if p.Action == ActionWaive {
				for _, c := range p.Commands {
					s.waived[strings.TrimSpace(c)] = cursor
				}
			}
		}
		return nil
	})
	return s, err
}

// Where an outstanding deferred check stands at the heads read.
const (
	deferredKept     = "kept"
	deferredRejected = "rejected"
	deferredAsked    = "asked"
	deferredToAsk    = "to ask"
)

// standing is where check stands at heads.
func (s *deferredState) standing(c deferredCheck, heads string) string {
	last, answered := s.latest[c.Command]
	switch {
	case answered && last.Verdict == VerdictReject && s.waived[c.Command] > last.Cursor:
		// A person waived it after the conductor rejected it.
		return deferredKept
	case answered && last.Verdict == VerdictReject:
		return deferredRejected
	case s.backed[c.Command][heads]:
		return deferredKept
	case s.asked[c.Command][heads]:
		return deferredAsked
	}
	return deferredToAsk
}

// outstanding are the deferred checks not kept at heads.
func (s *deferredState) outstanding(heads string) []deferredCheck {
	var out []deferredCheck
	for _, c := range s.checks {
		if s.standing(c, heads) != deferredKept {
			out = append(out, c)
		}
	}
	return out
}

// deferredItemMax bounds a reason quoted in the gate's deferred item.
const deferredItemMax = 200

// gateDeferredTx is the deferred item: no deferred check of the attempt
// outstanding at heads (the open pull requests' heads). Its detail names
// each one: its command, the conductor's reason for deferring it, and
// where it stands. conducted: a conductor decides the delivery, so it is
// asked; without one, a person settles it.
func gateDeferredTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, heads map[string]string, conducted bool) (GateItem, error) {
	s, err := readDeferredTx(ctx, tx, taskID, attempt)
	if err != nil {
		return GateItem{}, err
	}
	return deferredItem(s, headsID(heads), conducted), nil
}

// deferredItem is the deferred item from s at the heads named id.
func deferredItem(s *deferredState, id string, conducted bool) GateItem {
	open := s.outstanding(id)
	if len(open) == 0 {
		if len(s.checks) == 0 {
			return GateItem{Key: ReadyDeferred, OK: true, Detail: "no check deferred"}
		}
		return GateItem{Key: ReadyDeferred, OK: true, Detail: fmt.Sprintf("%d deferred check(s) kept", len(s.checks))}
	}
	parts := make([]string, len(open))
	for i, c := range open {
		var stands string
		switch st := s.standing(c, id); {
		case st == deferredRejected:
			stands = "the conductor rejected it (" + clip(oneLine(cmp.Or(s.latest[c.Command].Reason, "no reason given")), deferredItemMax) +
				"): a person settles it"
		case st == deferredAsked:
			stands = "the conductor is asked to keep it at this head"
		case !conducted:
			stands = "no conductor decides this delivery: a person settles it"
		default:
			stands = "the conductor is asked once CI has reported at the head"
		}
		parts[i] = fmt.Sprintf("%q (deferred: %s) — %s", c.Command, clip(oneLine(cmp.Or(c.Reason, "no reason given")), deferredItemMax), stands)
	}
	return GateItem{Key: ReadyDeferred, Detail: fmt.Sprintf("%d deferred check(s) not kept: %s", len(open), strings.Join(parts, "; "))}
}

var checksDeferredWake = mustTemplate("wakes/checks-deferred")

// AskDeferredTx asks the task's conductor, once per attempt and heads, to
// keep the deferred checks of the attempt still outstanding at heads (the
// open pull requests' heads, each with CI reported): a second checks ask
// on the newest Run that deferred one, its wake keyed DischargeKey. A
// check the conductor rejected, or already asked about at these heads, is
// not asked again. Returns whether it asked now.
func AskDeferredTx(ctx context.Context, tx pgx.Tx, org, taskID string, attempt int, heads map[string]string) (bool, error) {
	s, err := readDeferredTx(ctx, tx, taskID, attempt)
	if err != nil || len(s.checks) == 0 {
		return false, err
	}
	id := headsID(heads)
	var ask []deferredCheck
	var anchor deferredCheck
	for _, c := range s.checks {
		if s.standing(c, id) == deferredToAsk {
			ask = append(ask, c)
			anchor = c // checks are oldest Run first: the newest is last
		}
	}
	if len(ask) == 0 {
		return false, nil
	}
	if attempt == 0 {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt), 0) FROM runs WHERE task_id = $1`, taskID).Scan(&attempt); err != nil {
			return false, err
		}
	}
	key := DischargeKey(anchor.RunID, anchor.Turn, attempt, heads)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = $2 AND payload->'discharge'->>'key' = $3)`,
		anchor.RunID, EvRunChecksAsked, key).Scan(&exists); err != nil || exists {
		return false, err
	}
	asked := ChecksAsked{Turn: anchor.Turn, Discharge: &ChecksDischarge{Attempt: attempt, Heads: heads, Key: key}}
	for _, c := range ask {
		asked.Check = append(asked.Check, Check{Command: c.Command, Result: CheckDeferred, Evidence: c.Reason})
	}
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&asked.Deadline); err != nil {
		return false, err
	}
	raw, _ := json.Marshal(asked)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	ref := RunRef{Org: org, ProjectID: anchor.ProjectID, TaskID: taskID, RunID: anchor.RunID}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunChecksAsked, ledger.ActorSystem, payload)); err != nil {
		return false, err
	}
	_, err = RecordWakeTx(ctx, tx, org, taskID, WakeChecks, key, deferredWakeLine(anchor, asked))
	return err == nil, err
}

// deferredWakeLine is the second ask's wake: the Run and turn to answer,
// the heads, and one line per check with the conductor's own reason for
// deferring it.
func deferredWakeLine(anchor deferredCheck, asked ChecksAsked) string {
	type check struct{ Command, Reason string }
	checks := make([]check, len(asked.Check))
	for i, c := range asked.Check {
		checks[i] = check{oneLine(c.Command), clip(oneLine(cmp.Or(c.Evidence, "no reason given")), deferredItemMax)}
	}
	var heads []string
	for _, repo := range slices.Sorted(maps.Keys(asked.Discharge.Heads)) {
		heads = append(heads, repo+" at "+short(asked.Discharge.Heads[repo]))
	}
	return render(checksDeferredWake, struct {
		RunID, Heads string
		Turn         int64
		Checks       []check
	}{anchor.RunID, strings.Join(heads, ", "), anchor.Turn, checks})
}

// openDischargeTx is the newest second ask about runID's turn the
// conductor has not answered; nil for none.
func openDischargeTx(ctx context.Context, tx pgx.Tx, runID string, turn int64) (*ChecksAsked, int64, error) {
	var raw []byte
	var cursor int64
	err := tx.QueryRow(ctx, `SELECT a.payload, a.cursor FROM events a WHERE a.run_id = $1 AND a.event_type = $2
		AND (a.payload->>'turn')::bigint = $3 AND a.payload ? 'discharge'
		AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = a.run_id AND v.event_type = $4
			AND (v.payload->>'askCursor')::bigint = a.cursor)
		ORDER BY a.cursor DESC LIMIT 1`, runID, EvRunChecksAsked, turn, EvRunChecksConducted).Scan(&raw, &cursor)
	if err == pgx.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	asked := &ChecksAsked{}
	return asked, cursor, json.Unmarshal(raw, asked)
}

// conductDischargeTx records the conductor's answer to a second ask: each
// deferred check backed (kept at the ask's heads) or rejected. The
// delivery weighs readiness again; a rejected check holds the gate until a
// person settles it (DeferredRejectedTx).
func conductDischargeTx(ctx context.Context, tx pgx.Tx, ref RunRef, in DecideChecks, projectID string, asked *ChecksAsked, askCursor int64) (int64, error) {
	if err := checkVerdicts(asked, in); err != nil {
		return 0, err
	}
	writer := ref
	writer.RunID, writer.ProjectID = in.RunID, projectID
	ev := writer.Event(EvRunChecksConducted, ledger.ActorAgent, map[string]any{"turn": in.Turn, "checks": in.Checks,
		"discharge": asked.Discharge, "askCursor": askCursor, "conductorRunId": ref.RunID})
	ev.ActorID = ref.RunID
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return 0, err
	}
	var cursor int64
	if err := tx.QueryRow(ctx, `SELECT max(cursor) FROM events WHERE run_id = $1 AND event_type = $2`, in.RunID, EvRunChecksConducted).Scan(&cursor); err != nil {
		return 0, err
	}
	return cursor, GateItemSettledTx(ctx, tx, ref.Org, ref.TaskID, fmt.Sprintf("deferred:%d", cursor))
}

// DeferredRejectedTx is the escalation's detail for the attempt's deferred
// checks the conductor rejected at a second ask and no person has settled
// since; nil for none. The escalation is checks_unverified, as a report
// held after the rounds: a person waives (or stops); there is no writer's
// turn to send back, so no retry (Discharge).
func DeferredRejectedTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (map[string]any, error) {
	s, err := readDeferredTx(ctx, tx, taskID, attempt)
	if err != nil {
		return nil, err
	}
	var anchor deferredCheck
	var commands []string
	var unverified []UnverifiedCheck
	var lines []string
	for _, c := range s.checks {
		if s.standing(c, "") != deferredRejected {
			continue
		}
		anchor = c
		why := clip(oneLine(cmp.Or(s.latest[c.Command].Reason, "no reason given")), escalatedItemMax)
		commands = append(commands, c.Command)
		unverified = append(unverified, UnverifiedCheck{clip(oneLine(c.Command), escalatedItemMax), why})
		lines = append(lines, fmt.Sprintf("- %s: deferred, then rejected by the conductor once the pull request was open: %s", c.Command, why))
	}
	if len(commands) == 0 {
		return nil, nil
	}
	return map[string]any{"runId": anchor.RunID, "rounds": 0, "discharge": true, "commands": commands, "unverified": unverified,
		"message": "Deferred checks not kept:\n" + strings.Join(lines, "\n")}, nil
}

// discharge says a checks_unverified escalation is of deferred checks the
// conductor rejected after the pull request opened (DeferredRejectedTx),
// not of a writer's held report.
func (e *Escalation) discharge() bool {
	if e == nil || e.Reason != ReasonChecksUnverified {
		return false
	}
	var d struct {
		Discharge bool `json:"discharge"`
	}
	e.decode(&d)
	return d.Discharge
}
