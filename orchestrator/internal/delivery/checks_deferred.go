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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A deferred check is a promise: the conductor passed a check not run
// because a later phase runs it (VerdictDeferred, CheckDeferred). Once the
// pull request is open at a head and its CI has reported there, that later
// phase has come: dude asks the conductor again about the deferred checks
// still outstanding (a second checks ask, its run.checks.asked carrying
// Discharge), where deferred is no longer a verdict. Backed keeps the
// promise at those heads; reject leaves it broken, for a person to settle
// (checks_unverified, as a report held after the rounds), and so does no
// answer by the ask's deadline, or no conductor deciding the delivery at
// all. The ready gate's deferred item holds only when every deferred check
// of the attempt is kept at the pull requests' heads or a person waived it.
//
// Outstanding is level-triggered from the ledger, read once per weigh
// (readDeferredTx), every judgement of the attempt's writers in cursor
// order: one that defers a check makes a promise (the union over their
// judged turns, not each Run's newest: a person's waiver of a held report,
// or a later turn that no longer lists the command, keeps no promise); a
// newer one that lists the command as kept (pass, pre-existing, waived)
// ends it, as the gate passed it. From its newest deferral only a backed
// answer at the heads, or a person's discharge waiver, keeps it.

// ChecksDischarge marks a second checks ask (and its answer): the attempt,
// the open pull requests' heads it was asked at (repository to head), and
// its key (DischargeKey), which the wake names and the answer must cite.
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

// deferredCheck is one deferred check of the attempt: the newest writer
// judgement that deferred it (its Run and turn), the command and the
// conductor's reason.
type deferredCheck struct {
	RunID, ProjectID string
	Turn             int64
	Command, Reason  string
}

// dischargeAnswer is the conductor's answer about one check at a second
// ask: its verdict and reason.
type dischargeAnswer struct {
	Verdict, Reason string
}

// dischargeAsk is one second ask: its key, its deadline, whether that
// has passed (on the database's clock) and whether it is answered.
type dischargeAsk struct {
	Key               string
	Deadline          time.Time
	Overdue, Answered bool
}

// deferredState is where each deferred check of the attempt stands, read
// once (readDeferredTx). Every map is by command, and holds only what
// happened since the command's newest deferral.
type deferredState struct {
	checks []deferredCheck
	// conducted: a live conductor decides the delivery.
	conducted bool
	// The latest answer; the heads it was backed at; the asks about it
	// by heads (their cursor, a key of asks); a person's waiver.
	latest map[string]dischargeAnswer
	backed map[string]map[string]bool
	asked  map[string]map[string]int64
	waived map[string]bool
	asks   map[int64]*dischargeAsk
}

func newDeferredState() *deferredState {
	return &deferredState{latest: map[string]dischargeAnswer{}, backed: map[string]map[string]bool{},
		asked: map[string]map[string]int64{}, waived: map[string]bool{}, asks: map[int64]*dischargeAsk{}}
}

// deferredRead (SQL, $1 the task, $2 its attempt, 0 for the latest): the
// attempt's writer judgements (each may defer a check, or keep one a
// judgement before deferred), and every second ask, answer and discharge
// waiver, oldest first. One arm per event type, each a literal so generic
// plans can prove the partial indexes' predicates
// (events_run_checks_settled_idx, _asked_idx, _decided_idx) and never scan
// a Run's every event.
const deferredRead = `WITH r AS (SELECT id, phase, COALESCE(project_id, '') AS project_id FROM runs WHERE task_id = $1
		AND attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1)))
	SELECT e.event_type, e.cursor, e.run_id, r.project_id, e.payload, false FROM r JOIN events e ON e.run_id = r.id
		WHERE e.event_type = 'run.checks.judged' AND r.phase IN ('implement','fix','simplify')
	UNION ALL
	SELECT e.event_type, e.cursor, e.run_id, r.project_id, e.payload, COALESCE((e.payload->>'deadline')::timestamptz <= now(), true)
		FROM r JOIN events e ON e.run_id = r.id WHERE e.event_type = 'run.checks.asked' AND e.payload ? 'discharge'
	UNION ALL
	SELECT e.event_type, e.cursor, e.run_id, r.project_id, e.payload, false FROM r JOIN events e ON e.run_id = r.id
		WHERE e.event_type = 'run.checks.conducted' AND e.payload ? 'discharge'
	UNION ALL
	SELECT e.event_type, e.cursor, e.run_id, r.project_id, e.payload, false FROM r JOIN events e ON e.run_id = r.id
		WHERE e.event_type = 'run.checks.decided' AND e.payload ? 'discharge' AND e.payload->>'action' = '` + ActionWaive + `'
	ORDER BY 2`

// readDeferredTx reads where the attempt's deferred checks stand; conducted
// says a live conductor decides the delivery.
func readDeferredTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, conducted bool) (*deferredState, error) {
	rows, err := tx.Query(ctx, deferredRead, taskID, attempt)
	if err != nil {
		return nil, err
	}
	s := newDeferredState()
	s.conducted = conducted
	at := map[string]int{}
	var typ, runID, projectID string
	var cursor int64
	var raw []byte
	var overdue bool
	_, err = pgx.ForEachRow(rows, []any{&typ, &cursor, &runID, &projectID, &raw, &overdue}, func() error {
		var p struct {
			Turn     int64     `json:"turn"`
			Deadline time.Time `json:"deadline"`
			// An ask's and an answer's ChecksDischarge; a waiver's true.
			Discharge json.RawMessage `json:"discharge"`
			Checks    []struct {
				Command, Result, Reason, Verdict, Evidence string
			} `json:"checks"`
			AskCursor int64    `json:"askCursor"`
			Commands  []string `json:"commands"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil
		}
		var dis ChecksDischarge
		_ = json.Unmarshal(p.Discharge, &dis)
		heads := headsID(dis.Heads)
		switch typ {
		case EvRunChecksJudged:
			for _, c := range p.Checks {
				command := strings.TrimSpace(c.Command)
				switch c.Result {
				case CheckDeferred:
					// A deferral is a new promise: what kept an older one
					// does not keep it.
					s.drop(at, command)
					at[command] = len(s.checks)
					s.checks = append(s.checks, deferredCheck{RunID: runID, ProjectID: projectID, Turn: p.Turn, Command: command, Reason: c.Reason})
				case CheckPass, CheckPreexisting, CheckWaived:
					// A newer judgement that keeps the command (run and
					// passed, as the gate passes any) ends its promise.
					s.drop(at, command)
				}
				// Any other result, or a judgement that does not list the
				// command, changes nothing.
			}
		case EvRunChecksAsked:
			s.asks[cursor] = &dischargeAsk{Key: dis.Key, Deadline: p.Deadline, Overdue: overdue}
			for _, c := range p.Checks {
				command := strings.TrimSpace(c.Command)
				if s.asked[command] == nil {
					s.asked[command] = map[string]int64{}
				}
				s.asked[command][heads] = cursor
			}
		case EvRunChecksConducted:
			if a := s.asks[p.AskCursor]; a != nil {
				a.Answered = true
			}
			for _, v := range p.Checks {
				command := strings.TrimSpace(v.Command)
				s.latest[command] = dischargeAnswer{v.Verdict, v.Reason}
				if v.Verdict == VerdictBacked {
					if s.backed[command] == nil {
						s.backed[command] = map[string]bool{}
					}
					s.backed[command][heads] = true
				}
			}
		case EvRunChecksDecided:
			for _, c := range p.Commands {
				s.waived[strings.TrimSpace(c)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// drop ends command's promise, if any (at: each command's index in
// s.checks), and forgets what was said about it since its deferral.
func (s *deferredState) drop(at map[string]int, command string) {
	delete(s.latest, command)
	delete(s.backed, command)
	delete(s.asked, command)
	delete(s.waived, command)
	i, ok := at[command]
	if !ok {
		return
	}
	s.checks = slices.Delete(s.checks, i, i+1)
	delete(at, command)
	for k, j := range at {
		if j > i {
			at[k] = j - 1
		}
	}
}

// Where an outstanding deferred check stands at the heads read.
const (
	deferredKept     = "kept"
	deferredRejected = "rejected"
	// deferredOverdue: asked at these heads, unanswered past the ask's
	// deadline.
	deferredOverdue = "overdue"
	// deferredPerson: no conductor decides the delivery to keep it.
	deferredPerson = "person"
	deferredAsked  = "asked"
	deferredToAsk  = "to ask"
)

// standing is where check stands at heads.
func (s *deferredState) standing(c deferredCheck, heads string) string {
	last, answered := s.latest[c.Command]
	switch {
	case s.waived[c.Command]:
		// A person waived it at a discharge escalation.
		return deferredKept
	case answered && last.Verdict == VerdictReject:
		return deferredRejected
	case s.backed[c.Command][heads]:
		return deferredKept
	case !s.conducted:
		// Whatever was asked before the delivery left its conductor,
		// no conductor will answer it now.
		return deferredPerson
	}
	if cursor, ok := s.asked[c.Command][heads]; ok {
		if a := s.asks[cursor]; a != nil && a.Overdue && !a.Answered {
			return deferredOverdue
		}
		return deferredAsked
	}
	return deferredToAsk
}

// deadline is the earliest deadline of an ask at heads still waiting on
// the conductor's answer; zero for none.
func (s *deferredState) deadline(heads string) time.Time {
	var at time.Time
	for _, c := range s.checks {
		if s.standing(c, heads) != deferredAsked {
			continue
		}
		if a := s.asks[s.asked[c.Command][heads]]; a != nil && !a.Deadline.IsZero() && (at.IsZero() || a.Deadline.Before(at)) {
			at = a.Deadline
		}
	}
	return at
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

// deferredItem is the deferred item from s at the heads named id: no
// deferred check of the attempt outstanding there. Its detail names each
// one: its command, the conductor's reason for deferring it, and where it
// stands.
func deferredItem(s *deferredState, id string) GateItem {
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
		switch s.standing(c, id) {
		case deferredRejected:
			stands = "the conductor rejected it (" + clip(oneLine(cmp.Or(s.latest[c.Command].Reason, "no reason given")), deferredItemMax) +
				"): a person settles it"
		case deferredOverdue:
			stands = "the conductor did not answer by the ask's deadline: a person settles it"
		case deferredPerson:
			stands = "no conductor decides this delivery: a person settles it"
		case deferredAsked:
			stands = "the conductor is asked to keep it at this head"
			if a := s.asks[s.asked[c.Command][id]]; a != nil && a.Key != "" {
				stands += " (ask " + a.Key + ")"
			}
		default:
			stands = "the conductor is asked once CI has reported at the head"
		}
		parts[i] = fmt.Sprintf("%q (deferred: %s) — %s", c.Command, clip(oneLine(cmp.Or(c.Reason, "no reason given")), deferredItemMax), stands)
	}
	return GateItem{Key: ReadyDeferred, Detail: fmt.Sprintf("%d deferred check(s) not kept: %s", len(open), strings.Join(parts, "; "))}
}

var checksDeferredWake = mustTemplate("wakes/checks-deferred")

// askDeferredTx asks the task's conductor, once per attempt and heads, to
// keep the deferred checks s has still to ask about at heads (the open
// pull requests' heads, each with CI reported): a second checks ask on the
// newest Run that deferred one, its wake keyed DischargeKey and naming
// that key, which the answer cites. A check the conductor rejected, or
// already asked about at these heads, is not asked again. It marks s
// asked. Returns whether it asked now.
func askDeferredTx(ctx context.Context, tx pgx.Tx, org, taskID string, attempt int, heads map[string]string, s *deferredState) (bool, error) {
	if !s.conducted {
		return false, nil
	}
	id := headsID(heads)
	var ask []deferredCheck
	var anchor deferredCheck
	for _, c := range s.checks {
		if s.standing(c, id) == deferredToAsk {
			ask = append(ask, c)
			anchor = c // checks are oldest deferral first: the newest is last
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
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = 'run.checks.asked'
		AND payload->'discharge'->>'key' = $2)`, anchor.RunID, key).Scan(&exists); err != nil || exists {
		return false, err
	}
	asked := ChecksAsked{Turn: anchor.Turn, Discharge: &ChecksDischarge{Attempt: attempt, Heads: heads, Key: key}}
	for _, c := range ask {
		asked.Check = append(asked.Check, Check{Command: c.Command, Result: CheckDeferred, Evidence: c.Reason})
	}
	// The first ask's wait: unanswered past it, a person is asked
	// (deferredEscalation).
	if err := tx.QueryRow(ctx, `SELECT now() + make_interval(secs => $1)`, ChecksAnswerWait.Seconds()).Scan(&asked.Deadline); err != nil {
		return false, err
	}
	raw, _ := json.Marshal(asked)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	ref := RunRef{Org: org, ProjectID: anchor.ProjectID, TaskID: taskID, RunID: anchor.RunID}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunChecksAsked, ledger.ActorSystem, payload)); err != nil {
		return false, err
	}
	if _, err := RecordWakeTx(ctx, tx, org, taskID, WakeChecks, key, deferredWakeLine(anchor, asked)); err != nil {
		return false, err
	}
	// Marked asked, as the next read would read it (cursor -1: this ask).
	s.asks[-1] = &dischargeAsk{Key: key, Deadline: asked.Deadline}
	for _, c := range ask {
		if s.asked[c.Command] == nil {
			s.asked[c.Command] = map[string]int64{}
		}
		s.asked[c.Command][id] = -1
	}
	return true, nil
}

// deferredWakeLine is the second ask's wake: the Run, turn and ask to
// answer, the heads, and one line per check with the conductor's own
// reason for deferring it.
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
		RunID, Heads, Ask, Wait string
		Turn                    int64
		Checks                  []check
	}{anchor.RunID, strings.Join(heads, ", "), asked.Discharge.Key, ChecksWaitWords(), anchor.Turn, checks})
}

// deliveryHeadsTx is the task's delivery (nil for none) and the heads of
// its open or draft pull requests (State.PullRequestIDs) by repository,
// the last in created_at, id order winning, as the weigh reads them
// (gateHeads): the heads a second ask must be at to be answered. Another
// pull request of the task, say an earlier attempt's left open, is not
// the delivery's.
func deliveryHeadsTx(ctx context.Context, tx pgx.Tx, taskID string) (*Delivery, map[string]string, error) {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil {
		return d, map[string]string{}, err
	}
	rows, err := tx.Query(ctx, `SELECT r.name, COALESCE(pr.head_sha, '') FROM pull_requests pr
		JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.id = ANY($1) AND pr.state IN ('open', 'draft') ORDER BY pr.created_at, pr.id`, d.State.PullRequestIDs)
	if err != nil {
		return nil, nil, err
	}
	heads := map[string]string{}
	var repo, head string
	_, err = pgx.ForEachRow(rows, []any{&repo, &head}, func() error {
		heads[repo] = head
		return nil
	})
	return d, heads, err
}

// openDischargeTx is the second ask about runID's turn the conductor has
// not answered, the one at the pull requests' heads first (the one an
// answer is taken for), else the newest; nil for none.
func openDischargeTx(ctx context.Context, tx pgx.Tx, taskID, runID string, turn int64) (*ChecksAsked, error) {
	_, heads, err := deliveryHeadsTx(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	at, _ := json.Marshal(heads)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT a.payload FROM events a WHERE a.run_id = $1 AND a.event_type = 'run.checks.asked'
		AND (a.payload->>'turn')::bigint = $2 AND a.payload ? 'discharge'
		AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = a.run_id AND v.event_type = 'run.checks.conducted'
			AND (v.payload->>'askCursor')::bigint = a.cursor)
		ORDER BY a.payload->'discharge'->'heads' = $3::jsonb DESC, a.cursor DESC LIMIT 1`, runID, turn, string(at)).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	asked := &ChecksAsked{}
	return asked, json.Unmarshal(raw, asked)
}

// conductDischargeTx records the conductor's answer to the second ask its
// key names (in.Ask): each deferred check backed (kept at that ask's
// heads, and no others) or rejected. An ask is answerable while its heads
// are the gate's heads, the open pull requests' (gateHeads); one at other
// heads is superseded and refused: its evidence keeps nothing at the
// heads the pull requests are at now. Heads that moved away and back (H1,
// H2, H1) make the ask at H1 answerable again: no new ask is made there,
// the same key's. The delivery weighs readiness again; a rejected check
// holds the gate until a person settles it.
func conductDischargeTx(ctx context.Context, tx pgx.Tx, ref RunRef, in DecideChecks, projectID string) (int64, error) {
	var raw []byte
	var askCursor int64
	var answered bool
	err := tx.QueryRow(ctx, `SELECT a.payload, a.cursor,
			EXISTS (SELECT 1 FROM events v WHERE v.run_id = a.run_id AND v.event_type = 'run.checks.conducted'
				AND (v.payload->>'askCursor')::bigint = a.cursor)
		FROM events a WHERE a.run_id = $1 AND a.event_type = 'run.checks.asked' AND a.payload->'discharge'->>'key' = $2
			AND (a.payload->>'turn')::bigint = $3`,
		in.RunID, strings.TrimSpace(in.Ask), in.Turn).Scan(&raw, &askCursor, &answered)
	if err == pgx.ErrNoRows {
		return 0, refusef(rChecksNoSuchAsk, in.Ask)
	}
	if err != nil {
		return 0, err
	}
	asked := &ChecksAsked{}
	if err := json.Unmarshal(raw, asked); err != nil {
		return 0, err
	}
	d, heads, err := deliveryHeadsTx(ctx, tx, ref.TaskID)
	if err != nil {
		return 0, err
	}
	switch {
	case !maps.Equal(heads, asked.Discharge.Heads):
		return 0, refusef(rChecksAskSuperseded, in.Ask)
	case answered:
		return 0, refuseText(rChecksNotWaiting)
	}
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
	if err := tx.QueryRow(ctx, `SELECT max(cursor) FROM events WHERE run_id = $1 AND event_type = 'run.checks.conducted'`, in.RunID).Scan(&cursor); err != nil {
		return 0, err
	}
	// A late answer still counts: a discharge escalation raised at the
	// ask's deadline is weighed again at once (decide, dischargeKeptTx),
	// and settled if nothing is left for a person.
	if waitsAtDecide(d) && d.State.Escalation.discharge() {
		if err := workflow.SignalTx(ctx, tx, ref.Org, d.WorkflowID, SignalHumanDecision, nil, fmt.Sprintf("deferred:%d", cursor)); err != nil {
			return 0, err
		}
	}
	return cursor, GateItemSettledTx(ctx, tx, ref.Org, ref.TaskID, fmt.Sprintf("deferred:%d", cursor))
}

// dischargeKeptTx says the open discharge escalation needs no person any
// more: read from the stored gate facts as the weigh reads them (the
// delivery's pull requests prIDs still open), every deferred check of the
// attempt is kept at their heads (a backed answer there, however late).
// why says so, for the record.
func dischargeKeptTx(ctx context.Context, tx pgx.Tx, taskID string, prIDs []string) (kept bool, why string, err error) {
	if len(prIDs) == 0 {
		// None of the delivery's: nil would read every pull request of the task.
		return false, "", nil
	}
	c, err := CheckReadyGateTx(ctx, tx, taskID, prIDs)
	// Still open, as weighReadiness weighs them.
	open := slices.DeleteFunc(c.PRs, func(p GateFacts) bool { return p.State == forge.StateMerged || p.State == forge.StateClosed })
	if err != nil || c.deferred == nil || len(open) == 0 {
		return false, "", err
	}
	id := headsID(gateHeads(open))
	if len(c.deferred.outstanding(id)) > 0 {
		return false, "", nil
	}
	if len(c.deferred.checks) == 0 {
		return true, "Resolved: no deferred check is outstanding any more (a later turn ran it and the checks gate passed it), so no person need settle it.", nil
	}
	return true, fmt.Sprintf("Resolved by the conductor's answer: every deferred check (%s) is kept at the pull requests' heads, so no person need settle it.",
		strings.Join(deferredCommands(c.deferred.checks), ", ")), nil
}

// deferredCommands are the checks' commands, quoted.
func deferredCommands(checks []deferredCheck) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = fmt.Sprintf("%q", clip(oneLine(c.Command), escalatedItemMax))
	}
	return out
}

// deferredEscalation is the escalation's detail for the deferred checks
// only a person can settle now at heads (id): the conductor rejected them,
// left the ask unanswered past its deadline, or no conductor decides the
// delivery (once due: the heads recorded and CI reported there); nil for
// none. The escalation is checks_unverified, as a report held after the
// rounds: a person waives (or stops); there is no writer's turn to send
// back, so no retry (Escalation.discharge). Once waived, the checks are
// kept, so it is raised once.
func deferredEscalation(s *deferredState, id string, due bool) map[string]any {
	var anchor deferredCheck
	var commands []string
	var unverified []UnverifiedCheck
	var lines []string
	for _, c := range s.checks {
		var why, line string
		switch s.standing(c, id) {
		case deferredRejected:
			why = clip(oneLine(cmp.Or(s.latest[c.Command].Reason, "no reason given")), escalatedItemMax)
			line = "deferred, then rejected by the conductor once the pull request was open: " + why
		case deferredOverdue:
			why = "the conductor did not answer the ask to keep it by its deadline"
			line = "deferred, then " + why
		case deferredPerson:
			if !due {
				continue
			}
			why = "no conductor decides this delivery to keep it"
			line = "deferred (" + clip(oneLine(cmp.Or(c.Reason, "no reason given")), escalatedItemMax) + "), and " + why
		default:
			continue
		}
		anchor = c
		commands = append(commands, c.Command)
		unverified = append(unverified, UnverifiedCheck{clip(oneLine(c.Command), escalatedItemMax), why})
		lines = append(lines, fmt.Sprintf("- %s: %s", c.Command, line))
	}
	if len(commands) == 0 {
		return nil
	}
	return map[string]any{"runId": anchor.RunID, "rounds": 0, "discharge": true, "commands": commands, "unverified": unverified,
		"message": "Deferred checks not kept:\n" + strings.Join(lines, "\n")}
}

// discharge says a checks_unverified escalation is of deferred checks only
// a person can settle once the pull request is open (deferredEscalation),
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
