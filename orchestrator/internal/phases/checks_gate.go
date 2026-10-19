package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

const checksJudgedEvent = "run.checks.judged"

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

func (s *Syncer) checksBeforePush(ctx context.Context, r phaseRun) (bool, error) {
	var turn int64
	var previous *checksDecision
	var rounds int
	var rawSettings []byte
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
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
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE run_id=$1 AND event_type=$2 AND payload->>'verified'='false'`, r.ID, checksJudgedEvent).Scan(&rounds); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT settings FROM forge_credentials WHERE forge='github' LIMIT 1`).Scan(&rawSettings)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	if err != nil {
		return false, err
	}
	if previous != nil {
		return previous.Verified, nil
	}
	d, err := s.judgeChecks(ctx, r)
	if err != nil {
		return false, err
	}
	d.Turn, d.Round = turn, rounds
	maxRounds := forge.ReadSettings(rawSettings).ChecksFixRounds
	if !d.Verified {
		d.Message = checksText.Render(d.Verdicts)
	}
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Recheck the exact turn after the model's latency, before recording an
		// acceptance or queuing input. A fresh turn must be judged independently.
		var current int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor),0) FROM events WHERE run_id=$1 AND event_type='agent.session.stopped'`, r.ID).Scan(&current); err != nil {
			return err
		}
		if current != turn {
			return errRetry
		}
		raw, _ := json.Marshal(d)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		if err := s.event(ctx, tx, r, checksJudgedEvent, ledger.ActorSystem, payload); err != nil {
			return err
		}
		if d.Verified {
			return nil
		}
		if rounds < maxRounds {
			_, _, err := delivery.QueueDirective(ctx, tx, r.ref(), delivery.Directive{Text: d.Message, Scope: "run"})
			return err
		}
		return delivery.EscalateChecksTx(ctx, tx, r.Org, r.TaskID, r.ID, d.Message, rounds)
	})
	return d.Verified, err
}

func (s *Syncer) judgeChecks(ctx context.Context, r phaseRun) (checksDecision, error) {
	d := checksDecision{Verdicts: []checksVerdict{}}
	var reply string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error { var err error; reply, err = delivery.RunReplyTx(ctx, tx, r.ID); return err }); err != nil {
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

// Each question gets at most 128 KiB: report 16 KiB, project notes 8 KiB,
// changed paths/stat 16 KiB, and tool records 80 KiB. Oversize records
// retain their tails; omitted evidence is explicitly marked for the model.
func (s *Syncer) checksContext(ctx context.Context, r phaseRun, checks []delivery.Check) (string, error) {
	report, _ := json.Marshal(checks)
	var notes, changes string
	var records strings.Builder
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var p, o []byte
		if err := tx.QueryRow(ctx, `SELECT p.agent_models,o.default_agent_models FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE p.id=$1`, r.ProjectID).Scan(&p, &o); err != nil {
			return err
		}
		notes = delivery.ResolveRole(delivery.PromptRoleFor(r.Phase, r.Role), p, o).Context
		rows, err := tx.Query(ctx, `SELECT f->>'path',f->>'additions',f->>'deletions' FROM run_diffs d CROSS JOIN LATERAL jsonb_array_elements(d.files) f WHERE d.run_id=$1`, r.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var path, a, d *string
			if err := rows.Scan(&path, &a, &d); err != nil {
				rows.Close()
				return err
			}
			fmt.Fprintf(&records, "%v +%v -%v\n", value(path), value(a), value(d))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		changes = records.String()
		records.Reset()
		rows, err = tx.Query(ctx, `SELECT event_type,payload FROM events WHERE run_id=$1 AND event_type IN ('agent.tool.called','agent.tool.completed') ORDER BY cursor`, r.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			var raw []byte
			if err := rows.Scan(&typ, &raw); err != nil {
				return err
			}
			fmt.Fprintf(&records, "%s %s\n", typ, raw)
		}
		return rows.Err()
	})
	if err != nil {
		return "", err
	}
	return "Report:\n" + checksBound(string(report), 16<<10) + "\nProject notes:\n" + checksBound(notes, 8<<10) + "\nChange paths/stat (recorded snapshot):\n" + checksBound(changes, 16<<10) + "\nRecorded calls and completions in order:\n" + checksBound(records.String(), 80<<10), nil
}
func value(s *string) string {
	if s == nil {
		return "unknown"
	}
	return *s
}
func checksBound(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "[earlier context omitted; unavailable evidence must not be assumed]\n" + s[len(s)-n:]
}
