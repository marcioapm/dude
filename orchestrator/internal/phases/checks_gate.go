package phases

import (
	"cmp"
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

var (
	checksText          = delivery.MustText("notices/checks")
	backedQuestion      = delivery.MustText("notices/checks/backed")
	missingQuestion     = delivery.MustText("notices/checks/missing")
	waivedQuestion      = delivery.MustText("notices/checks/waived")
	checksWaivedContext = delivery.MustText("notices/checks/waived-context")
	checksNotCovered    = delivery.MustText("notices/checks/not-covered")
	checksSkip          = delivery.MustText("notices/checks/skip")
	checksMany          = delivery.MustText("notices/checks/many")
	checksUnparseable   = delivery.MustText("notices/checks/unparseable")
	checksUnestablished = delivery.MustText("notices/checks/unestablished")
	checksUnchanged     = delivery.MustText("notices/checks/unchanged")
)

// checksCountMax bounds the judge calls one report costs, one per check
// plus one, made in turn before anything is pushed.
const checksCountMax = 30

// checksWaived is the result of a reported failure the judge found to be
// one a person waived for the attempt: carried, never backed.
const checksWaived = "waived"

type checksVerdict struct {
	Command  string `json:"command"`
	Result   string `json:"result,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason"`
	CallID   string `json:"callId,omitempty"`
	// The person's waiver a waived result carries.
	Waiver *delivery.CarriedWaiver `json:"waiver,omitempty"`
	// Why an attempt waiver does not cover a reported failure, beside the
	// agent's own result and evidence, which a person may still waive.
	WaiverReason string `json:"waiverReason,omitempty"`
}

type checksDecision struct {
	Turn     int64 `json:"turn"`
	Round    int   `json:"round"`
	Verified bool  `json:"verified"`
	Skipped  bool  `json:"skipped,omitempty"`
	// Some reported failure passed as one a person waived for the attempt.
	Carried  bool            `json:"carried,omitempty"`
	Verdicts []checksVerdict `json:"checks"`
	Message  string          `json:"message"`
	// Unchanged: a fresh inspection at this turn found every repository the
	// Run may push at its starting commit with nothing staged, unstaged or
	// untracked (unchangedBeforePush), so no report was judged. Epoch is
	// the agent session placement it was inspected in; Heads each
	// repository's HEAD then.
	Unchanged bool              `json:"unchanged,omitempty"`
	Epoch     int               `json:"epoch,omitempty"`
	Heads     map[string]string `json:"heads,omitempty"`
}

// checksBeforePush judges the report the Run's latest turn ended with,
// once per turn, before anything leaves the Run. pass: the turn's report
// was accepted, the judgement was skipped for want of a judgement tier, or
// a person waived it; acted: it recorded a judgement this time. The model
// is asked with no transaction held; its verdict, and the send-back or
// escalation it leads to, are written in one transaction, only if the
// Run's latest turn is still the one judged. A restart before that commit
// judges the turn again; one after it reads the recorded verdict.
//
// A turn that changed nothing the Run may push (unchangedBeforePush) is
// recorded as such and passes without a report or a judge: there is no
// work of its own for checks to cover. That decision is reused only in the
// agent session placement it was inspected in.
func (s *Syncer) checksBeforePush(ctx context.Context, r phaseRun) (pass, acted bool, err error) {
	var turn int64
	var epoch int
	var previous *checksDecision
	var rounds int
	var rawSettings []byte
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var err error
		if turn, err = delivery.LatestTurnTx(ctx, tx, r.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT agent_session_epoch FROM runs WHERE id = $1`, r.ID).Scan(&epoch); err != nil {
			return err
		}
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT payload FROM events WHERE run_id=$1 AND event_type=$2 AND (payload->>'turn')::bigint=$3 ORDER BY cursor DESC LIMIT 1`, r.ID, delivery.EvRunChecksJudged, turn).Scan(&raw)
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
			r.ID, delivery.EvRunChecksJudged, delivery.EvRunChecksDecided).Scan(&rounds); err != nil {
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
	if previous != nil && (!previous.Unchanged || previous.Epoch == epoch) {
		return previous.Verified, false, nil
	}
	heads, unchanged, err := s.unchangedBeforePush(ctx, r)
	if err != nil {
		return false, true, err
	}
	var d checksDecision
	if unchanged {
		d = checksDecision{Verified: true, Unchanged: true, Epoch: epoch, Heads: heads, Verdicts: []checksVerdict{},
			Message: checksUnchanged.Render(nil)}
	} else if d, err = s.judgeChecks(ctx, r); err != nil {
		return false, true, err
	}
	d.Turn, d.Round = turn, rounds
	maxRounds := forge.ReadSettings(rawSettings).ChecksFixRounds
	if !d.Verified {
		// The send-back names only what the agent must fix; d.Verdicts,
		// recorded below, keeps the passed checks too.
		failed := slices.DeleteFunc(slices.Clone(d.Verdicts), func(v checksVerdict) bool { return v.Result == "pass" || v.Result == checksWaived })
		d.Message = checksText.Render(failed)
	}
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		current, err := delivery.LatestTurnTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		var nowEpoch int
		if err := tx.QueryRow(ctx, `SELECT agent_session_epoch FROM runs WHERE id = $1`, r.ID).Scan(&nowEpoch); err != nil {
			return err
		}
		if current != turn || d.Unchanged && nowEpoch != epoch {
			return errRetry
		}
		raw, _ := json.Marshal(d)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		escalate := !d.Verified && rounds >= maxRounds
		if escalate {
			payload["escalated"] = true
		}
		if !d.Verified && !escalate {
			// The judgement names its send-back, so its readers can tell
			// when the agent received the words.
			id, _, err := delivery.QueueDirective(ctx, tx, r.ref(), delivery.Directive{Text: d.Message, Scope: "run"})
			if err != nil {
				return err
			}
			payload["directiveId"] = id
		}
		if err := s.event(ctx, tx, r, delivery.EvRunChecksJudged, ledger.ActorSystem, payload); err != nil {
			return err
		}
		if escalate {
			return delivery.EscalateChecksTx(ctx, tx, r.Org, r.TaskID, r.ID, d.Message, rounds)
		}
		return nil
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
		d.Verdicts = append(d.Verdicts, checksVerdict{Command: "checks:", Reason: checksUnparseable.Render(delivery.ChecksYAMLError(reply))})
		return d, nil
	}
	if len(checks) > checksCountMax {
		d.Verdicts = append(d.Verdicts, checksVerdict{Command: "checks:",
			Reason: checksMany.Render(struct{ Count, Max int }{len(checks), checksCountMax})})
		return d, nil
	}
	for _, c := range checks {
		if c.Result != delivery.CheckPass {
			d.Verdicts = append(d.Verdicts, checksVerdict{Command: c.Command, Result: c.Result, Reason: c.Evidence})
		}
	}
	// A reported failure passes only as one a person waived for the
	// attempt, and only once the judge says it is that failure: any
	// other non-pass, or one no waiver names, is rejected unasked.
	rejected := d
	var waivers []*delivery.CarriedWaiver
	if len(d.Verdicts) > 0 {
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			turn, err := delivery.LatestTurnTx(ctx, tx, r.ID)
			if err != nil {
				return err
			}
			candidates, err := delivery.AttemptWaiversTx(ctx, tx, r.ID, turn)
			if err != nil {
				return err
			}
			for _, c := range checks {
				if c.Result == delivery.CheckPass {
					continue
				}
				w := candidates.For(c)
				if w == nil {
					waivers = nil
					return nil
				}
				waivers = append(waivers, w)
			}
			return nil
		}); err != nil || len(waivers) == 0 {
			return rejected, err
		}
	}
	text, err := s.checksContext(ctx, r, checks)
	if err != nil {
		return d, err
	}
	svc := s.Judge
	if svc == nil {
		svc = &judge.Service{DB: s.DB}
	}
	d.Verdicts = []checksVerdict{}
	for i, w := range waivers {
		waived, err := s.waivedContext(ctx, r, w)
		if err != nil {
			return d, err
		}
		j, err := svc.Judge(ctx, r.Org, judge.Ask{Asker: "phases.checks", About: r.ID, TaskID: &r.TaskID,
			Question: waivedQuestion.Render(w.Covered), Context: text + waived})
		if errors.Is(err, judge.ErrOff) || errors.Is(err, judge.ErrNoModel) {
			// No tier judges the failure the same: it stays rejected,
			// never the skip a tier's absence gives passes.
			return rejected, nil
		}
		if err != nil && ctx.Err() != nil {
			return d, err
		}
		held := rejected.Verdicts[i]
		if err == nil && j.CallID != "" && j.Decision == judge.Yes {
			d.Verdicts = append(d.Verdicts, checksVerdict{Command: held.Command, Result: checksWaived, Decision: j.Decision,
				Reason: j.Reasoning, CallID: j.CallID, Waiver: w})
			continue
		}
		reason := j.Reasoning
		if err != nil {
			reason = err.Error()
		}
		held.Decision, held.CallID = j.Decision, j.CallID
		held.WaiverReason = checksNotCovered.Render(struct{ Role, Reason string }{w.Role, cmp.Or(reason, checksUnestablished.Render(nil))})
		// The send-back names this and every failure not yet judged.
		d.Verdicts = append(append(d.Verdicts, held), rejected.Verdicts[i+1:]...)
		return d, nil
	}
	d.Carried = len(waivers) > 0
	passes := slices.DeleteFunc(slices.Clone(checks), func(c delivery.Check) bool { return c.Result != delivery.CheckPass })
	verified := true
	// One Backed question per check, answered yes to pass, then Missing,
	// answered no to pass.
	for i := range len(passes) + 1 {
		var name, q, want string
		if i < len(passes) {
			name, q, want = passes[i].Command, backedQuestion.Render(passes[i]), judge.Yes
		} else {
			name, q, want = "missing checks", missingQuestion.Render(nil), judge.No
		}
		j, err := svc.Judge(ctx, r.Org, judge.Ask{Asker: "phases.checks", About: r.ID, TaskID: &r.TaskID, Question: q, Context: text})
		if errors.Is(err, judge.ErrOff) || errors.Is(err, judge.ErrNoModel) {
			if d.Carried {
				return rejected, nil
			}
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
		if err == nil && j.CallID != "" && j.Decision == want {
			v.Result = "pass"
		} else {
			verified = false
			v.Result = "unverified"
			if v.Reason == "" {
				v.Reason = checksUnestablished.Render(nil)
			}
		}
		d.Verdicts = append(d.Verdicts, v)
	}
	d.Verified = verified
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
	// checksWaivedCallsMax is the waiver's source Run's share, beside the
	// judged Run's own calls.
	checksWaivedCallsMax = 32 << 10
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
		shown, total, err = newestCalls(ctx, tx, r.ID, 0, checksCallsMax)
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

// waivedContext is what the judge reads beside checksContext about a
// waiver's source: the person's note, the failure that Run reported and
// its calls up to the waiver, under checksWaivedCallsMax. Its own Run's
// earlier turn when the waiver is the judged Run's own.
func (s *Syncer) waivedContext(ctx context.Context, r phaseRun, w *delivery.CarriedWaiver) (string, error) {
	var shown []string
	var total int
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		shown, total, err = newestCalls(ctx, tx, w.RunID, w.Cursor, checksWaivedCallsMax)
		return err
	})
	return checksWaivedContext.Render(struct {
		Role, RunID, Note, Command, Evidence string
		Calls                                []string
		Omitted                              int
	}{w.Role, w.RunID, boundHead(w.Note, checksNotesMax), w.Covered.Command, boundHead(w.Covered.Evidence, checksReportMax),
		shown, total - len(shown)}), err
}

// checksChangeMinRow is the length of the shortest rendered change row,
// " +? -?\n".
const checksChangeMinRow = 7

// newestCalls renders the Run's newest whole calls recorded before cursor
// before (0: all) up to maxBytes, in the order they were made, and counts
// every such call. One query groups the calls once and streams them newest
// first; reading stops at the first call that would not fit.
func newestCalls(ctx context.Context, tx pgx.Tx, runID string, before int64, maxBytes int) (shown []string, total int, err error) {
	rows, err := tx.Query(ctx, `WITH c AS (
			SELECT `+checksCallKey+` AS id, min(cursor) AS first, count(*) OVER () AS total FROM events
			WHERE run_id = $1 AND event_type IN ($2, $3) AND ($4::bigint = 0 OR cursor < $4::bigint) GROUP BY 1)
		SELECT c.first, c.total, e.event_type, e.payload FROM c
		JOIN events e ON e.run_id = $1 AND e.event_type IN ($2, $3) AND ($4::bigint = 0 OR e.cursor < $4::bigint)
			AND `+strings.ReplaceAll(checksCallKey, "payload", "e.payload")+` = c.id
		ORDER BY c.first DESC, e.cursor`,
		runID, evToolCalled, evToolCompleted, before)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	size := 0
	// fits renders a finished call and keeps it if it fits; the newest
	// call is kept whatever its size.
	fits := func(c *checksCall) bool {
		text := checksCallText.Render(*c)
		if size+len(text) > maxBytes && len(shown) > 0 {
			return false
		}
		shown = append(shown, text)
		size += len(text)
		return true
	}
	var call *checksCall
	first := int64(-1)
	for rows.Next() {
		var typ string
		var p map[string]any
		var at int64
		if err := rows.Scan(&at, &total, &typ, &p); err != nil {
			return nil, 0, err
		}
		if at != first {
			if call != nil && !fits(call) {
				slices.Reverse(shown)
				return shown, total, nil
			}
			call, first = &checksCall{}, at
		}
		addCallEvent(call, typ, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if call != nil {
		fits(call)
	}
	slices.Reverse(shown)
	return shown, total, nil
}

// checksCallKey (SQL, over events) is the call a tool event belongs to:
// its string callId, else "".
const checksCallKey = `CASE WHEN jsonb_typeof(payload->'callId') = 'string' THEN payload->>'callId' ELSE '' END`

// addCallEvent folds one of a call's tool events, read in cursor order,
// into c.
func addCallEvent(c *checksCall, typ string, p map[string]any) {
	if tool, _ := p["tool"].(string); tool != "" && (c.Tool == "" || typ == evToolCalled) {
		c.Tool = tool
	}
	if typ == evToolCalled {
		if in, ok := p["input"]; ok {
			raw, _ := json.Marshal(in)
			c.Input = string(raw)
		}
		return
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

// boundHead is s up to n bytes, saying so when it was cut.
func boundHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutRunes(s[:n], false) + "\n[… the rest left out for length]"
}
