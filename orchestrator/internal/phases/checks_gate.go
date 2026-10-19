package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

const (
	checksJudgedEvent  = delivery.EvRunChecksJudged
	checksDecidedEvent = delivery.EvRunChecksDecided
)

var checksText = delivery.MustText("notices/checks")
var backedQuestion = delivery.MustText("notices/checks/backed")
var missingQuestion = delivery.MustText("notices/checks/missing")
var checksSkip = delivery.MustText("notices/checks/skip")

type checksVerdict struct {
	Command  string `json:"command"`
	Result   string `json:"result,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason"`
	CallID   string `json:"callId,omitempty"`
}

type checksDecision struct {
	Turn     int64           `json:"turn"`
	Round    int             `json:"round"`
	Verified bool            `json:"verified"`
	Skipped  bool            `json:"skipped,omitempty"`
	Verdicts []checksVerdict `json:"checks"`
	Message  string          `json:"message"`
}

// checksBeforePush judges the report the Run's latest turn ended with,
// once per turn, before anything leaves the Run. pass: the turn's report
// was accepted, the judgement was skipped for want of a judgement tier, or
// a person waived it; acted: it recorded a judgement this time. The model
// is asked with no transaction held; its verdict, and the send-back or
// escalation it leads to, are written in one transaction, only if the
// Run's latest turn is still the one judged. A restart before that commit
// judges the turn again; one after it reads the recorded verdict.
func (s *Syncer) checksBeforePush(ctx context.Context, r phaseRun) (pass, acted bool, err error) {
	var turn int64
	var previous *checksDecision
	var rounds int
	var rawSettings []byte
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor),0) FROM events WHERE run_id=$1 AND event_type='agent.session.stopped'`, r.ID).Scan(&turn); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE run_id=$1 AND event_type=$2 AND (payload->>'turn')::bigint=$3 ORDER BY cursor DESC LIMIT 1`, r.ID, checksJudgedEvent, turn).Scan(&raw)
		if err == nil {
			previous = &checksDecision{}
			return json.Unmarshal(raw, previous)
		}
		if !db.IsNotFound(err) {
			return err
		}
		// Rounds are the send-backs since a person last decided on this
		// Run's checks: a retry gives the agent the configured rounds again.
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE run_id=$1 AND event_type=$2 AND payload->>'verified'='false'
			AND cursor > COALESCE((SELECT max(cursor) FROM events WHERE run_id=$1 AND event_type=$3), 0)`,
			r.ID, checksJudgedEvent, checksDecidedEvent).Scan(&rounds); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT settings FROM forge_credentials WHERE forge='github' LIMIT 1`).Scan(&rawSettings)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	if err != nil {
		return false, false, err
	}
	if previous != nil {
		return previous.Verified, false, nil
	}
	d, err := s.judgeChecks(ctx, r)
	if err != nil {
		return false, true, err
	}
	d.Turn, d.Round = turn, rounds
	maxRounds := forge.ReadSettings(rawSettings).ChecksFixRounds
	if !d.Verified {
		d.Message = checksText.Render(d.Verdicts)
	}
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor),0) FROM events WHERE run_id=$1 AND event_type='agent.session.stopped'`,
			r.ID).Scan(&current); err != nil {
			return err
		}
		if current != turn {
			return errRetry
		}
		raw, _ := json.Marshal(d)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		if d.Verified {
			return s.event(ctx, tx, r, checksJudgedEvent, ledger.ActorSystem, payload)
		}
		if rounds >= maxRounds {
			payload["escalated"] = true
		}
		if err := s.event(ctx, tx, r, checksJudgedEvent, ledger.ActorSystem, payload); err != nil {
			return err
		}
		if rounds < maxRounds {
			_, _, err := delivery.QueueDirective(ctx, tx, r.ref(), delivery.Directive{Text: d.Message, Scope: "run"})
			return err
		}
		return delivery.EscalateChecksTx(ctx, tx, r.Org, r.TaskID, r.ID, d.Message, rounds)
	})
	if errors.Is(err, errRetry) {
		return false, true, nil
	}
	return d.Verified, true, err
}

func (s *Syncer) judgeChecks(ctx context.Context, r phaseRun) (checksDecision, error) {
	d := checksDecision{Verdicts: []checksVerdict{}}
	var reply string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var err error
		reply, err = delivery.RunReplyTailTx(ctx, tx, r.ID, checksReplyMax)
		return err
	}); err != nil {
		return d, err
	}
	checks, ok := delivery.ParseChecks(reply)
	if !ok {
		d.Verdicts = append(d.Verdicts, checksVerdict{Command: "checks:", Reason: "missing or unparseable checks YAML block"})
		return d, nil
	}
	for _, c := range checks {
		if c.Result != delivery.CheckPass {
			d.Verdicts = append(d.Verdicts, checksVerdict{Command: c.Command, Result: c.Result, Reason: c.Evidence})
		}
	}
	if len(d.Verdicts) > 0 {
		return d, nil
	}
	text, err := s.checksContext(ctx, r, checks)
	if err != nil {
		return d, err
	}
	svc := s.Judge
	if svc == nil {
		svc = &judge.Service{DB: s.DB}
	}
	asks := []string{}
	for _, c := range checks {
		asks = append(asks, backedQuestion.Render(c))
	}
	asks = append(asks, missingQuestion.Render(nil))
	for i, q := range asks {
		name := "missing checks"
		if i < len(checks) {
			name = checks[i].Command
		}
		j, err := svc.Judge(ctx, r.Org, judge.Ask{Asker: "phases.checks", About: r.ID, TaskID: &r.TaskID, Question: q, Context: text})
		if errors.Is(err, judge.ErrOff) || errors.Is(err, judge.ErrNoModel) {
			d.Verified, d.Skipped = true, true
			d.Message = checksSkip.Render(nil)
			return d, nil
		}
		if err != nil && ctx.Err() != nil {
			// The orchestrator is stopping: no judgement was made, and the
			// next process judges the turn again.
			return d, err
		}
		v := checksVerdict{Command: name, Decision: j.Decision, Reason: j.Reasoning, CallID: j.CallID}
		if err != nil {
			v.Reason = err.Error()
		}
		// An absent CallID also fails closed, including a zero Judgement.
		accepted := err == nil && j.CallID != "" && ((i < len(checks) && j.Decision == judge.Yes) || (i == len(checks) && j.Decision == judge.No))
		if !accepted && v.Reason == "" {
			v.Reason = "the judgement did not establish verification"
		}
		if accepted {
			v.Result = "pass"
		} else {
			v.Result = "unverified"
		}
		d.Verdicts = append(d.Verdicts, v)
	}
	d.Verified = true
	for _, v := range d.Verdicts {
		if v.Result != "pass" {
			d.Verified = false
		}
	}
	return d, nil
}

// The judge's context is bounded, so a long Run's still fits the model it
// is sent to: the report and the project's notes whole up to their caps,
// what changed up to checksChangesMax, and the recorded calls newest first
// up to checksCallsMax, whole calls only, saying how many earlier ones were
// left out. Each call's output is already capped in the ledger
// (capOutput: its first and last 2 KiB, so a command's last lines, where
// it says how it ended, are kept).
const (
	// checksReplyMax is how much of the end of the agent's reply is read
	// for its last report: far more than a report, whose own cap in the
	// context is checksReportMax.
	checksReplyMax   = 256 << 10
	checksReportMax  = 16 << 10
	checksNotesMax   = 8 << 10
	checksChangesMax = 16 << 10
	checksCallsMax   = 80 << 10
)

var (
	checksContextText = delivery.MustText("notices/checks/context")
	checksCallText    = delivery.MustText("notices/checks/call")
)

// checksCall is one recorded tool call and its completion, as the judge
// reads it.
type checksCall struct {
	Tool, Input, Status string
	Exit                *int
	Streams             []checksStream
}

type checksStream struct {
	Name, Head, Tail string
	Omitted, Bytes   int
	Withheld         bool
}

// checksContext is what the judge reads: the report, the project's notes
// for the Run's role, what the Run changed and the tool calls it recorded,
// each with how it ended, in order (checksReportMax and its neighbours).
func (s *Syncer) checksContext(ctx context.Context, r phaseRun, checks []delivery.Check) (string, error) {
	report, _ := json.Marshal(checks)
	var notes string
	// Changes and calls are read only up to what their caps can show.
	var changes strings.Builder
	var shown []string
	var total int
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var p, o []byte
		if err := tx.QueryRow(ctx, `SELECT p.agent_models, o.default_agent_models FROM projects p
			JOIN organizations o ON o.id = p.organization_id WHERE p.id = $1`, r.ProjectID).Scan(&p, &o); err != nil && !db.IsNotFound(err) {
			return err
		}
		notes = delivery.ResolveRole(delivery.PromptRoleFor(r.Phase, r.Role), p, o).Context
		// A row renders to at least checksChangeMinRow bytes, so this many
		// rows exceed checksChangesMax whenever all of them would.
		rows, err := tx.Query(ctx, `SELECT COALESCE(f->>'path', ''), COALESCE(f->>'additions', '?'), COALESCE(f->>'deletions', '?')
			FROM run_diffs d CROSS JOIN LATERAL jsonb_array_elements(d.files) f WHERE d.run_id = $1 LIMIT $2`,
			r.ID, checksChangesMax/checksChangeMinRow+1)
		if err != nil {
			return err
		}
		for rows.Next() && changes.Len() <= checksChangesMax {
			var path, added, removed string
			if err := rows.Scan(&path, &added, &removed); err != nil {
				rows.Close()
				return err
			}
			fmt.Fprintf(&changes, "%s +%s -%s\n", path, added, removed)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		shown, total, err = newestCalls(ctx, tx, r.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	return checksContextText.Render(struct {
		Report, Notes, Changes string
		Calls                  []string
		Omitted                int
	}{boundHead(string(report), checksReportMax), boundHead(notes, checksNotesMax), boundHead(changes.String(), checksChangesMax),
		shown, total - len(shown)}), nil
}

// checksChangeMinRow is the length of the shortest rendered change row,
// " +? -?\n"; checksCallsPage is how many calls newestCalls reads at once.
const (
	checksChangeMinRow = 7
	checksCallsPage    = 32
)

// newestCalls renders the Run's newest whole calls up to checksCallsMax,
// in the order they were made, reading checksCallsPage calls at a time
// newest first, and counts every call the Run recorded.
func newestCalls(ctx context.Context, tx pgx.Tx, runID string) (shown []string, total int, err error) {
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT `+checksCallKey+`) FROM events WHERE run_id = $1 AND event_type IN ($2, $3)`,
		runID, evToolCalled, evToolCompleted).Scan(&total); err != nil {
		return nil, 0, err
	}
	size := 0
	before := int64(-1)
	for len(shown) < total {
		calls, first, err := recordedCalls(ctx, tx, runID, before)
		if err != nil || len(calls) == 0 {
			return nil, 0, err
		}
		for _, c := range calls {
			text := checksCallText.Render(c)
			if size+len(text) > checksCallsMax && len(shown) > 0 {
				slices.Reverse(shown)
				return shown, total, nil
			}
			shown = append(shown, text)
			size += len(text)
		}
		before = first
	}
	slices.Reverse(shown)
	return shown, total, nil
}

// checksCallKey (SQL, over events) is the call a tool event belongs to:
// its string callId, else "".
const checksCallKey = `CASE WHEN jsonb_typeof(payload->'callId') = 'string' THEN payload->>'callId' ELSE '' END`

// recordedCalls are up to checksCallsPage of the Run's tool calls, each
// with its completion, newest first by their first event, made before the
// one whose first event is at cursor before (-1: from the newest); first
// is the oldest returned call's first cursor.
func recordedCalls(ctx context.Context, tx pgx.Tx, runID string, before int64) (calls []checksCall, first int64, err error) {
	rows, err := tx.Query(ctx, `WITH c AS (
			SELECT `+checksCallKey+` AS id, min(cursor) AS first FROM events
			WHERE run_id = $1 AND event_type IN ($2, $3) GROUP BY 1),
		page AS (SELECT id, first FROM c WHERE $4 < 0 OR first < $4 ORDER BY first DESC LIMIT $5)
		SELECT page.first, e.event_type, e.payload FROM page
		JOIN events e ON e.run_id = $1 AND e.event_type IN ($2, $3) AND `+strings.ReplaceAll(checksCallKey, "payload", "e.payload")+` = page.id
		ORDER BY page.first DESC, e.cursor`,
		runID, evToolCalled, evToolCompleted, before, checksCallsPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	byFirst := map[int64]int{}
	for rows.Next() {
		var typ string
		var p map[string]any
		var at int64
		if err := rows.Scan(&at, &typ, &p); err != nil {
			return nil, 0, err
		}
		i, seen := byFirst[at]
		if !seen {
			calls = append(calls, checksCall{})
			i = len(calls) - 1
			byFirst[at] = i
			first = at
		}
		c := &calls[i]
		if tool, _ := p["tool"].(string); tool != "" && (c.Tool == "" || typ == evToolCalled) {
			c.Tool = tool
		}
		if typ == evToolCalled {
			if in, ok := p["input"]; ok {
				raw, _ := json.Marshal(in)
				c.Input = string(raw)
			}
			continue
		}
		c.Status, _ = p["status"].(string)
		if exit, ok := p["exitCode"].(float64); ok {
			n := int(exit)
			c.Exit = &n
		}
		for _, name := range []string{"output", "stdout", "stderr"} {
			out, ok := p[name].(map[string]any)
			if !ok {
				continue
			}
			st := checksStream{Name: name}
			st.Head, _ = out["head"].(string)
			st.Tail, _ = out["tail"].(string)
			st.Omitted, st.Bytes = byteCount(out["omittedBytes"]), byteCount(out["bytes"])
			st.Withheld = out["withheld"] == true
			c.Streams = append(c.Streams, st)
		}
	}
	return calls, first, rows.Err()
}

// boundHead is s up to n bytes, saying so when it was cut.
func boundHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutRunes(s[:n], false) + "\n[… the rest left out for length]"
}
