package automations

// Incoming webhooks: an automation's own URL. The control plane stores each
// request (incoming_deliveries, migration 211) after checking its secret,
// size, JSON and rate; this loop reads each accepted one: a later delivery
// of a live run's group goes to that run whatever the concurrency (it is
// not checked against the conditions), any other is checked against the
// conditions (all must hold) and fires the automation as a GitHub event
// does. Bodies are read by dot paths, an all-digit segment indexing an
// array; packages/domain/src/incoming.ts reads them the same way, and
// packages/domain/test/incoming-conditions.json holds the cases both pass.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// EventIncoming is an incoming delivery's Payload.Event.
const EventIncoming = "incoming"

// Condition is one row of an incoming trigger's conditions.
type Condition struct {
	Path   string   `json:"path"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// ConditionResult is whether a condition held, and what the body had at
// its path as text ("missing" for nothing).
type ConditionResult struct {
	OK     bool   `json:"ok"`
	Actual string `json:"actual"`
}

// PathValue is the raw JSON at a dot path of body; false when the body has
// nothing there.
func PathValue(body json.RawMessage, path string) (json.RawMessage, bool) {
	at := bytes.TrimSpace(body)
	for _, key := range strings.Split(path, ".") {
		switch {
		case len(at) > 0 && at[0] == '[':
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || strings.TrimLeft(key, "0123456789") != "" {
				return nil, false
			}
			var list []json.RawMessage
			if json.Unmarshal(at, &list) != nil || i >= len(list) {
				return nil, false
			}
			at = bytes.TrimSpace(list[i])
		case len(at) > 0 && at[0] == '{':
			var obj map[string]json.RawMessage
			if json.Unmarshal(at, &obj) != nil {
				return nil, false
			}
			v, ok := obj[key]
			if !ok {
				return nil, false
			}
			at = bytes.TrimSpace(v)
		default:
			return nil, false
		}
	}
	return at, len(at) > 0
}

// valueText is a JSON value as conditions compare it: a string as itself,
// anything else as its compact JSON.
func valueText(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// Holds says whether c holds for body. contains on an array asks whether an
// element is the value; on a string, whether it is a substring.
func Holds(c Condition, body json.RawMessage) ConditionResult {
	raw, found := PathValue(body, c.Path)
	text, actual := "", "missing"
	if found {
		text = valueText(raw)
		actual = text
	}
	first := ""
	if len(c.Values) > 0 {
		first = c.Values[0]
	}
	r := ConditionResult{Actual: actual}
	switch c.Op {
	case "exists":
		r.OK = found && string(raw) != "null"
	case "is":
		r.OK = found && text == first
	case "is_not":
		r.OK = !found || text != first
	case "one_of":
		r.OK = found && contains(c.Values, text)
	case "contains":
		switch {
		case found && raw[0] == '[':
			var list []json.RawMessage
			_ = json.Unmarshal(raw, &list)
			for _, x := range list {
				if valueText(x) == first {
					r.OK = true
				}
			}
		case found && raw[0] == '"':
			r.OK = strings.Contains(text, first)
		}
	}
	return r
}

// EvaluateConditions is every condition's result, in order.
func EvaluateConditions(conditions []Condition, body json.RawMessage) []ConditionResult {
	out := make([]ConditionResult, len(conditions))
	for i, c := range conditions {
		out[i] = Holds(c, body)
	}
	return out
}


// missActualRunes is how much of the body's value a skip reason quotes.
const missActualRunes = 200

// Miss is why a missed condition skips a delivery, as the Deliveries tab
// says it; the body's value is cut to missActualRunes characters and "…".
func Miss(c Condition, r ConditionResult) string {
	first := ""
	if len(c.Values) > 0 {
		first = c.Values[0]
	}
	actual := r.Actual
	if rs := []rune(actual); len(rs) > missActualRunes {
		actual = string(rs[:missActualRunes]) + "…"
	}
	switch c.Op {
	case "exists":
		return c.Path + " is missing"
	case "is":
		return c.Path + " is " + actual + ", not " + first
	case "is_not":
		return c.Path + " is " + actual
	case "one_of":
		return c.Path + " is " + actual + ", not one of " + strings.Join(c.Values, ", ")
	}
	return c.Path + " does not contain " + first
}

// FirstMiss is the first condition that does not hold, -1 when all do.
func FirstMiss(conditions []Condition, body json.RawMessage) (int, string) {
	for i, c := range conditions {
		if r := Holds(c, body); !r.OK {
			return i, Miss(c, r)
		}
	}
	return -1, ""
}

// GroupKey is a delivery's group: its group-by path's value as text; ""
// with no path, or when the body has nothing (or null) there.
func GroupKey(groupBy string, body json.RawMessage) string {
	if groupBy == "" {
		return ""
	}
	raw, ok := PathValue(body, groupBy)
	if !ok || string(raw) == "null" {
		return ""
	}
	return valueText(raw)
}

// IncomingSubstitute replaces an incoming automation's {{…}} names in text:
// payload.<path> and event.payload are the sender's words, quoted as
// external text; event.delivery_id and event.group_key are dude's. The
// last payload is the one a prompt or input is for.
func IncomingSubstitute(text string, payloads []Payload) string {
	if len(payloads) == 0 {
		return text
	}
	p := payloads[len(payloads)-1]
	quote := func(name, s string) string {
		sum := sha256.Sum256([]byte(p.DeliveryID + "\x00" + name))
		return delivery.ExternalText(hex.EncodeToString(sum[:4]), s)
	}
	return substituteWith(text, func(name string) (string, bool) {
		switch {
		case name == "event.delivery_id":
			return p.DeliveryID, true
		case name == "event.group_key":
			return p.GroupKey, true
		case name == "event.payload":
			return quote(name, valueText(p.Body)), true
		case strings.HasPrefix(name, "payload."):
			raw, ok := PathValue(p.Body, strings.TrimPrefix(name, "payload."))
			if !ok {
				return quote(name, ""), true
			}
			return quote(name, valueText(raw)), true
		}
		return "", false
	})
}

// incomingPrompt is an incoming automation's prompt for its payloads: the
// external-text note, then the text with its names substituted.
func incomingPrompt(text string, payloads []Payload) string {
	return delivery.IncomingPayloadNote + "\n\n" + IncomingSubstitute(text, payloads)
}

// substituteWith is Substitute with a lookup; a name it does not know is left as written.
func substituteWith(text string, lookup func(string) (string, bool)) string {
	var b strings.Builder
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		j := strings.Index(text[i:], "}}")
		if j < 0 {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:i])
		if v, ok := lookup(strings.TrimSpace(text[i+2 : i+j])); ok {
			b.WriteString(v)
		} else {
			b.WriteString(text[i : i+j+2])
		}
		text = text[i+j+2:]
	}
}

// ---------------------------------------------------------------------------
// The loop's step
// ---------------------------------------------------------------------------

// Delivery verdicts (incoming_deliveries.verdict, automation_github_verdicts.verdict).
const (
	VerdictStarted   = "started"
	VerdictSentToRun = "sent_to_run"
	VerdictSkipped   = "skipped"
)

type pendingIncoming struct {
	ID, Org, AutomationID string
	Payload               json.RawMessage
	Attempts              int
}

// matchIncoming reads the accepted incoming deliveries not yet read, oldest
// first, each in its own transaction. One that fails is backed off as a
// GitHub delivery is (matchBackoff), never blocking the rest, and after
// MatchAttempts tries is skipped, saying why.
func (r *Runner) matchIncoming(ctx context.Context) (int, error) {
	var batch []pendingIncoming
	if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id, automation_id, COALESCE(payload, 'null'), attempts
			FROM incoming_deliveries WHERE verdict IS NULL AND (next_at IS NULL OR next_at <= $1)
			ORDER BY received_at, id LIMIT 50`, r.now())
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[pendingIncoming])
		return err
	}); err != nil {
		return 0, err
	}
	var errs []error
	for _, d := range batch {
		var cancelled []Cancelled
		err := r.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) (err error) {
			cancelled, err = ArriveIncoming(ctx, tx, d.AutomationID, d.ID, d.Payload)
			return err
		})
		if err == nil {
			r.EndCancelled(ctx, cancelled)
			continue
		}
		r.Log.Warn("deciding an incoming delivery failed", "delivery", d.ID, "attempt", d.Attempts+1, "error", err)
		if berr := r.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
			return r.backOffIncoming(ctx, tx, d, err)
		}); berr != nil {
			errs = append(errs, fmt.Errorf("incoming delivery %s: %w", d.ID, berr))
		}
	}
	return len(batch), errors.Join(errs...)
}

// backOffIncoming counts a failed try at d. After the last it is skipped
// with why, under a savepoint: should that write fail too, d still backs
// off rather than being retried on every pass.
func (r *Runner) backOffIncoming(ctx context.Context, tx pgx.Tx, d pendingIncoming, cause error) error {
	if d.Attempts+1 >= MatchAttempts {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = sp.Exec(ctx, `UPDATE incoming_deliveries SET verdict = 'skipped', reason = left($2, 2000), reason_index = NULL,
			attempts = attempts + 1, next_at = NULL WHERE id = $1`, d.ID, "could not be matched: "+cause.Error())
		if err == nil {
			return sp.Commit(ctx)
		}
		r.Log.Warn("skipping an undecidable incoming delivery failed", "delivery", d.ID, "error", err)
		if err := sp.Rollback(ctx); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE incoming_deliveries SET attempts = attempts + 1, next_at = $2 WHERE id = $1`,
		d.ID, r.now().Add(matchBackoff(d.Attempts)))
	return err
}

// ArriveIncoming decides one accepted delivery in tx and records its
// verdict: sent to the live run of its group, skipped by a condition, or
// fired (started, merged or sent as the concurrency says, or skipped by it).
// It returns the lux Runs a cancel-and-restart firing ended, to stop after commit.
func ArriveIncoming(ctx context.Context, tx pgx.Tx, automationID, deliveryID string, body json.RawMessage) ([]Cancelled, error) {
	// Fire takes this lock too; taken first, the group's live run cannot end between the read and the send.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM automations WHERE id = $1 FOR UPDATE`, automationID); err != nil {
		return nil, err
	}
	a, err := Load(ctx, tx, automationID)
	if err != nil {
		return nil, err
	}
	verdict := func(v, reason string, index *int, group, run string) error {
		_, err := tx.Exec(ctx, `UPDATE incoming_deliveries SET verdict = $2, reason = left(NULLIF($3, ''), 2000), reason_index = $4,
			group_key = left(NULLIF($5, ''), 2000), run_id = NULLIF($6, '') WHERE id = $1`, deliveryID, v, reason, index, group, run)
		return err
	}
	if a.Trigger.Kind != "incoming" {
		return nil, verdict(VerdictSkipped, "the automation no longer runs on an incoming webhook", nil, "", "")
	}
	group := GroupKey(a.Trigger.GroupBy, body)
	if !a.Enabled {
		return nil, verdict(VerdictSkipped, "the automation was turned off", nil, group, "")
	}
	payloads := []Payload{{DeliveryID: deliveryID, Event: EventIncoming, GroupKey: group, Body: body}}
	if live, err := liveGroupRun(ctx, tx, a, group); err != nil {
		return nil, err
	} else if live != "" {
		raw, _ := json.Marshal(payloads)
		if _, err := tx.Exec(ctx, `UPDATE automation_runs SET payloads = payloads || $2::jsonb WHERE id = $1`, live, raw); err != nil {
			return nil, err
		}
		return nil, verdict(VerdictSentToRun, "", nil, group, live)
	}
	if i, why := FirstMiss(a.Trigger.Conditions, body); i >= 0 {
		return nil, verdict(VerdictSkipped, why, &i, group, "")
	}
	fired, err := Fire(ctx, tx, a, SourceEvent, payloads, "", nil)
	if err != nil {
		return nil, err
	}
	v, reason := firedVerdict(ctx, tx, fired)
	return fired.Cancelled, verdict(v, reason, nil, group, fired.Run)
}

// firedVerdict says what a firing did, as a delivery's verdict: a run of
// its own (started, queued included), added to another run (merged or
// sent), or skipped by the concurrency, with its reason.
func firedVerdict(ctx context.Context, tx pgx.Tx, fired Fired) (string, string) {
	var status, reason string
	var first string
	_ = tx.QueryRow(ctx, `SELECT status, COALESCE(reason, ''), COALESCE(payloads->0->>'deliveryId', '') FROM automation_runs WHERE id = $1`,
		fired.Run).Scan(&status, &reason, &first)
	switch {
	case status == "skipped":
		return VerdictSkipped, reason
	case fired.Action == MergeInto || fired.Action == SendInto:
		return VerdictSentToRun, ""
	}
	return VerdictStarted, ""
}

// liveGroupRun is the automation's queued or running run whose first
// delivery is of group, "" when none or the delivery has no group. Only an
// agent or a server takes a delivery while it runs; a test run is its own.
func liveGroupRun(ctx context.Context, tx pgx.Tx, a Automation, group string) (string, error) {
	if group == "" || (a.Kind != "agent" && a.Kind != "server") {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM automation_runs WHERE automation_id = $1 AND status IN ('queued', 'running')
		AND source <> 'test' AND payloads->0->>'groupKey' = $2 ORDER BY created_at DESC, id DESC LIMIT 1`, a.ID, group).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil
	}
	return id, err
}

// inProjects says an organisation incoming automation covers a project's
// repository: one of its projects. Any other automation covers every project.
func inProjects(a Automation, project string) bool {
	return a.ProjectID != "" || a.Trigger.Kind != "incoming" || contains(a.ProjectIDs, project)
}

// incomingRepos are what an organisation incoming automation's run checks
// out: its projects' repositories, all of them, none, or the ids its repos names.
func incomingRepos(ctx context.Context, tx pgx.Tx, a Automation) ([]phases.AutomationRepo, error) {
	type repo struct{ ID, Name, URL, Branch, Setup string }
	rows, err := tx.Query(ctx, `SELECT id, name, url, default_branch, COALESCE(setup_script, '') FROM repositories
		WHERE project_id = ANY($1) ORDER BY array_position($1, project_id), name`, a.ProjectIDs)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[repo])
	if err != nil {
		return nil, err
	}
	var which any
	_ = json.Unmarshal(a.Repos, &which)
	out := []phases.AutomationRepo{}
	seen := map[string]bool{}
	for _, r := range all {
		keep := which == "all"
		if ids, ok := which.([]any); ok {
			keep = slices.Contains(ids, any(r.ID))
		}
		if !keep || seen[lux.SpecName(r.Name)] {
			continue
		}
		seen[lux.SpecName(r.Name)] = true
		out = append(out, phases.AutomationRepo{Name: r.Name, URL: r.URL, Ref: r.Branch, Setup: r.Setup})
	}
	return out, nil
}

// runNowIncoming is Run now on an incoming webhook: a stored delivery's
// body, or a written one, as a test run of its own whatever its conditions
// say (the dialog shows them first) and whatever runs.
func runNowIncoming(ctx context.Context, tx pgx.Tx, a Automation, person, deliveryID string, test *Payload) (string, Verdict, error) {
	var p Payload
	switch {
	case test != nil:
		p = Payload{DeliveryID: cmp.Or(test.DeliveryID, "test-"+ids.New("t")[2:]), Event: EventIncoming, Body: test.Body}
	case deliveryID != "":
		var body []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM incoming_deliveries WHERE id = $1 AND automation_id = $2`, deliveryID, a.ID).Scan(&body)
		if db.IsNotFound(err) {
			return "", Verdict{}, fmt.Errorf("%w: no delivery %s of this automation", ErrNotFound, deliveryID)
		}
		if err != nil {
			return "", Verdict{}, err
		}
		if body == nil {
			return "", Verdict{}, fmt.Errorf("%w: delivery %s keeps no body (refused, or older than 30 days)", ErrBadRequest, deliveryID)
		}
		p = Payload{DeliveryID: deliveryID, Event: EventIncoming, Body: body}
	default:
		return "", Verdict{}, fmt.Errorf("%w: an incoming webhook runs on a past delivery or a written body", ErrBadRequest)
	}
	p.GroupKey = GroupKey(a.Trigger.GroupBy, p.Body)
	fired, err := Fire(ctx, tx, a, SourceTest, []Payload{p}, person, nil)
	return fired.Run, Verdict{Matches: true}, err
}

// filterWords is a skipping filter as the Deliveries tab says it: "Author: it is by …".
func filterWords(v Verdict) string {
	if v.Filter == "" {
		return v.Because
	}
	return strings.ToUpper(v.Filter[:1]) + v.Filter[1:] + ": " + v.Because
}

// recordGitHubVerdict keeps what matching a GitHub delivery did for one
// automation, for its Deliveries tab.
func recordGitHubVerdict(ctx context.Context, tx pgx.Tx, a Automation, deliveryID, verdict, reason, run string) error {
	_, err := tx.Exec(ctx, `INSERT INTO automation_github_verdicts (organization_id, automation_id, delivery_id, verdict, reason, run_id)
		VALUES ($1, $2, $3, $4, left(NULLIF($5, ''), 2000), NULLIF($6, ''))
		ON CONFLICT (automation_id, delivery_id) DO UPDATE SET verdict = EXCLUDED.verdict, reason = EXCLUDED.reason,
		  run_id = EXCLUDED.run_id, at = now()`, a.Org, a.ID, deliveryID, verdict, reason, run)
	return err
}

// IncomingVerdict is a written body against an incoming trigger, for the
// test dialog: each condition's result, its group key, and the live run of
// that group a real delivery would be sent to.
type IncomingVerdict struct {
	Matches   bool              `json:"matches"`
	Results   []ConditionResult `json:"results"`
	GroupKey  string            `json:"groupKey"`
	LiveRunID *string           `json:"liveRunId"`
}

// EvaluateIncoming checks body against a's trigger without storing anything.
func EvaluateIncoming(ctx context.Context, tx pgx.Tx, a Automation, body json.RawMessage) (IncomingVerdict, error) {
	v := IncomingVerdict{Results: EvaluateConditions(a.Trigger.Conditions, body), GroupKey: GroupKey(a.Trigger.GroupBy, body)}
	v.Matches = true
	for _, r := range v.Results {
		v.Matches = v.Matches && r.OK
	}
	live, err := liveGroupRun(ctx, tx, a, v.GroupKey)
	if live != "" {
		v.LiveRunID = &live
	}
	return v, err
}
