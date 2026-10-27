package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	checksText           = delivery.MustText("notices/checks")
	backedQuestion       = delivery.MustText("notices/checks/backed")
	missingQuestion      = delivery.MustText("notices/checks/missing")
	waivedQuestion       = delivery.MustText("notices/checks/waived")
	preexistingQuestion  = delivery.MustText("notices/checks/preexisting")
	checksWaivedContext  = delivery.MustText("notices/checks/waived-context")
	checksNotCovered     = delivery.MustText("notices/checks/not-covered")
	checksNotPreexisting = delivery.MustText("notices/checks/not-preexisting")
	checksSkip           = delivery.MustText("notices/checks/skip")
	checksMany           = delivery.MustText("notices/checks/many")
	checksUnparseable    = delivery.MustText("notices/checks/unparseable")
	checksUnestablished  = delivery.MustText("notices/checks/unestablished")
	checksUnchanged      = delivery.MustText("notices/checks/unchanged")
	checksNoTaskBase     = delivery.MustText("notices/checks/no-task-base")
	checksNoTier         = delivery.MustText("notices/checks/no-tier")
)

// errNoTaskBase rejects a reported failure without a judge call when no
// target-branch commit is known to measure it against.
var errNoTaskBase = errors.New(checksNoTaskBase.Render(nil))

// checksCountMax caps the checks one report may list, and so its judge
// calls, made in turn before anything is pushed: each check costs one,
// two when a carried waiver does not cover it, plus one for Missing.
const checksCountMax = 30

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
	// Why the judge did not pass a reported failure or not-run check as
	// pre-existing or deferred, beside the agent's own result and evidence.
	JudgeReason string `json:"judgeReason,omitempty"`
}

// passedUnbacked: a verdict that passes the turn without a backed pass:
// waived (delivery.CheckWaived), pre-existing or deferred.
func (v checksVerdict) passedUnbacked() bool {
	return v.Result == delivery.CheckWaived || v.Result == delivery.CheckPreexisting || v.Result == delivery.CheckDeferred
}

type checksDecision struct {
	Turn     int64 `json:"turn"`
	Round    int   `json:"round"`
	Verified bool  `json:"verified"`
	Skipped  bool  `json:"skipped,omitempty"`
	// Some reported failure passed as one a person waived for the attempt.
	Carried bool `json:"carried,omitempty"`
	// Some reported failure or not-run check passed on the judge's word
	// alone: pre-existing at the task's base, or deferred to a later phase.
	JudgePassed bool            `json:"judgePassed,omitempty"`
	Verdicts    []checksVerdict `json:"checks"`
	Message     string          `json:"message"`
	// The commit of each repository's target branch the task builds on,
	// for the send-back.
	bases []taskBase
	// No judgement tier answered: only a person can pass what is held.
	untiered bool
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
		d.Message = d.sendBack()
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
			return delivery.EscalateChecksTx(ctx, tx, r.Org, r.TaskID, r.ID, d.Message, rounds, d.unverified())
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
		d.Verdicts = append(d.Verdicts, checksVerdict{Command: "checks:", Reason: checksUnparseable.Render(delivery.ChecksYAMLErrorOf(reply))})
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
	// A reported failure a person waived for the attempt passes once the
	// judge says it is that failure; any reported failure or not-run check
	// not passed so passes once the judge says it fails the same way at the
	// target branch's commit the task builds on, or is one a later phase
	// runs. Waivers are asked about first, and the first non-pass not passed
	// rejects the report.
	type held struct {
		verdict checksVerdict
		check   delivery.Check
		waiver  *delivery.CarriedWaiver
	}
	var nonPass []held
	var testPhase bool
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
			var unwaived []held
			for i, c := range slices.DeleteFunc(slices.Clone(checks), func(c delivery.Check) bool { return c.Result == delivery.CheckPass }) {
				h := held{d.Verdicts[i], c, candidates.For(c)}
				if h.waiver == nil {
					unwaived = append(unwaived, h)
				} else {
					nonPass = append(nonPass, h)
				}
			}
			nonPass = append(nonPass, unwaived...)
			testPhase, err = deliveryTests(ctx, tx, r.TaskID)
			return err
		}); err != nil {
			return d, err
		}
	}
	// Only a reported failure is measured against the task base.
	if slices.ContainsFunc(nonPass, func(h held) bool { return h.check.Result == delivery.CheckFail }) {
		var err error
		if d.bases, err = s.taskBases(ctx, r); err != nil {
			return d, err
		}
	}
	text, err := s.checksContext(ctx, r, checks, d.bases)
	if err != nil {
		return d, err
	}
	svc := s.Judge
	if svc == nil {
		svc = &judge.Service{DB: s.DB}
	}
	// A rejection for want of a tier records the reported non-passes.
	rejected := d
	d.Verdicts = []checksVerdict{}
	ask := func(question, more string) (judge.Judgement, error) {
		return svc.Judge(ctx, r.Org, judge.Ask{Asker: "phases.checks", About: r.ID, TaskID: &r.TaskID, Question: question, Context: text + more})
	}
	// answered: j is the judge's recorded answer want. An absent CallID
	// fails closed, including a zero Judgement.
	answered := func(j judge.Judgement, err error, want string) bool {
		return err == nil && j.CallID != "" && j.Decision == want
	}
	// preexisting asks the pre-existing (or deferred) question about h.
	preexisting := func(h held) (judge.Judgement, error) {
		if h.check.Result == delivery.CheckFail && len(d.bases) == 0 {
			// Nothing to measure a failure against: rejected unasked.
			return judge.Judgement{}, errNoTaskBase
		}
		return ask(preexistingQuestion.Render(struct {
			delivery.Check
			TestPhase bool
		}{h.check, testPhase}), "")
	}
	// notPassed is why j (or err) did not pass a check.
	notPassed := func(j judge.Judgement, err error) string {
		if err != nil {
			return err.Error()
		}
		return cmp.Or(j.Reasoning, checksUnestablished.Render(nil))
	}
	for i, h := range nonPass {
		v := h.verdict
		var j judge.Judgement
		var err error
		if h.waiver != nil {
			var waived string
			if waived, err = s.waivedContext(ctx, r, h.waiver); err != nil {
				return d, err
			}
			j, err = ask(waivedQuestion.Render(h.waiver.Covered), waived)
			if offTier(err) {
				return untiered(rejected), nil
			}
			if err != nil && ctx.Err() != nil {
				return d, err
			}
			if answered(j, err, judge.Yes) {
				d.Carried = true
				d.Verdicts = append(d.Verdicts, checksVerdict{Command: v.Command, Result: delivery.CheckWaived, Decision: j.Decision,
					Reason: j.Reasoning, CallID: j.CallID, Waiver: h.waiver})
				continue
			}
			// Not the waived failure: it may still predate the task.
			v.WaiverReason = checksNotCovered.Render(struct{ Role, Reason string }{h.waiver.Role, notPassed(j, err)})
		}
		j, err = preexisting(h)
		if offTier(err) {
			// No tier to judge the failure: it stays rejected, never the
			// skip a tier's absence gives passes.
			return untiered(rejected), nil
		}
		if err != nil && ctx.Err() != nil {
			return d, err
		}
		if answered(j, err, judge.Yes) {
			passed := checksVerdict{Command: v.Command, Decision: j.Decision, Reason: j.Reasoning, CallID: j.CallID, WaiverReason: v.WaiverReason}
			switch v.Result {
			case delivery.CheckFail:
				passed.Result = delivery.CheckPreexisting
			case delivery.CheckNotRun:
				passed.Result = delivery.CheckDeferred
			default:
				return d, fmt.Errorf("checks gate: the judge passed a %q check", v.Result)
			}
			d.JudgePassed = true
			d.Verdicts = append(d.Verdicts, passed)
			continue
		}
		v.Decision, v.CallID = j.Decision, j.CallID
		if errors.Is(err, errNoTaskBase) {
			v.JudgeReason = err.Error()
		} else {
			v.JudgeReason = checksNotPreexisting.Render(notPassed(j, err))
		}
		// The send-back names this and every non-pass not yet judged.
		d.Verdicts = append(d.Verdicts, v)
		for _, rest := range nonPass[i+1:] {
			d.Verdicts = append(d.Verdicts, rest.verdict)
		}
		d.Carried, d.JudgePassed = false, false
		return d, nil
	}
	passes := slices.DeleteFunc(slices.Clone(checks), func(c delivery.Check) bool { return c.Result != delivery.CheckPass })
	verified := true
	// One Backed question per check, answered yes to pass, then Missing,
	// answered no to pass.
	for i := range len(passes) + 1 {
		var name, q, want string
		if i < len(passes) {
			name, q, want = passes[i].Command, backedQuestion.Render(passes[i]), judge.Yes
		} else {
			name, q, want = delivery.MissingChecks, missingQuestion.Render(nil), judge.No
		}
		j, err := ask(q, "")
		if offTier(err) {
			// A non-pass the judge passed is never let through by a skip.
			if len(nonPass) > 0 {
				return untiered(rejected), nil
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
		if answered(j, err, want) {
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

// offTier says a judgement could not be asked for want of a tier.
func offTier(err error) bool { return errors.Is(err, judge.ErrOff) || errors.Is(err, judge.ErrNoModel) }

// untiered is a rejection for want of a judgement tier: each reported
// failure or not-run check says only a person can pass it, and the
// send-back does not say how the judge would.
func untiered(d checksDecision) checksDecision {
	d.Verdicts = slices.Clone(d.Verdicts)
	for i := range d.Verdicts {
		d.Verdicts[i].JudgeReason = checksNoTier.Render(nil)
	}
	d.untiered = true
	return d
}

// sendBack is the send-back of a rejected report: only the verdicts that
// did not pass, and, when one is a reported failure, a judgement tier
// answered and a task base is known, how a failure that predates the task
// passes, naming the commit of each target branch the task builds on.
func (d checksDecision) sendBack() string {
	failed := d.unpassed()
	// Each field one line: evidence and reasons are free text, and the
	// send-back's lines are one per check.
	for i, v := range failed {
		failed[i].Command, failed[i].Reason = oneLine(v.Command), oneLine(v.Reason)
		failed[i].WaiverReason, failed[i].JudgeReason = oneLine(v.WaiverReason), oneLine(v.JudgeReason)
	}
	passable := !d.untiered && len(d.bases) > 0
	return checksText.Render(struct {
		Checks []checksVerdict
		Failed bool
		Bases  string
	}{failed, passable && slices.ContainsFunc(failed, func(v checksVerdict) bool { return v.Result == delivery.CheckFail }), joinBases(d.bases)})
}

// unpassed are the verdicts that did not pass.
func (d checksDecision) unpassed() []checksVerdict {
	return slices.DeleteFunc(slices.Clone(d.Verdicts), func(v checksVerdict) bool { return v.Result == delivery.CheckPass || v.passedUnbacked() })
}

// unverified is what the escalation of a held report keeps of each check
// it did not pass: its command, and its reason with why it was not passed.
func (d checksDecision) unverified() []delivery.UnverifiedCheck {
	var out []delivery.UnverifiedCheck
	for _, v := range d.unpassed() {
		why := slices.DeleteFunc([]string{v.Reason, v.WaiverReason, v.JudgeReason}, func(s string) bool { return s == "" })
		out = append(out, delivery.UnverifiedCheck{Command: v.Command, Reason: strings.Join(why, " — ")})
	}
	return out
}

// taskBase is the commit of a repository's target branch the task builds
// on, rendered "repo: branch at sha".
type taskBase struct{ Repo, Branch, SHA string }

func (b taskBase) String() string { return b.Repo + ": " + b.Branch + " at " + b.SHA }

func joinBases(bases []taskBase) string {
	out := make([]string, len(bases))
	for i, b := range bases {
		out[i] = b.String()
	}
	return strings.Join(out, ", ")
}

// taskBases are, per repository the task may change that the Run's
// checkout holds, the commit of its target branch the task builds on:
// GitHub's merge base of that branch and the commit the Run started from
// (runs.base_shas). The Run's start holds the task's earlier work and any
// merge of the target branch made before the Run started, so the base is
// the newest such merged commit, never one of the task's own. A merge made
// during the Run is not seen: a failure it brought in is measured against
// the older base, where it does not show, and is rejected. A repository
// whose start was not recorded, or that GitHub refuses to compare, has
// none: no failure in it passes as pre-existing. Sorted by repository.
func (s *Syncer) taskBases(ctx context.Context, r phaseRun) ([]taskBase, error) {
	var starts map[string]string
	var tb delivery.TaskBranch
	var repos []delivery.Repository
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		if err = tx.QueryRow(ctx, `SELECT base_shas FROM runs WHERE id = $1`, r.ID).Scan(&starts); err != nil {
			return err
		}
		if tb, err = delivery.TaskBranchTx(ctx, tx, r.TaskID, r.Attempt); err != nil {
			return err
		}
		repos, err = delivery.TaskRepositories(ctx, tx, r.TaskID)
		return err
	}); err != nil || len(starts) == 0 || s.Forges == nil {
		return nil, err
	}
	gh, err := s.Forges.For(ctx, r.Org)
	if err != nil || gh == nil {
		return nil, err
	}
	byName := repositoriesBySpecName(repos)
	var out []taskBase
	for _, name := range slices.Sorted(maps.Keys(starts)) {
		repo, ok := byName[name]
		slug := forge.SlugFromURL(repo.URL)
		if !ok || repo.Access != "write" || !isSHA(starts[name]) || slug == "" {
			continue
		}
		branch := baseBranch(tb, repo)
		c, err := gh.Compare(ctx, slug, branch, starts[name])
		if err != nil && forge.Transient(err) {
			return nil, err
		}
		if err != nil || !isSHA(c.MergeBase.SHA) {
			continue
		}
		out = append(out, taskBase{Repo: repo.Name, Branch: branch, SHA: c.MergeBase.SHA})
	}
	return out, nil
}

// deliveryTests says the task's delivery runs a test phase: its attempt's
// policy (workflow_runs.state->'policy'), not the settings as they are now.
func deliveryTests(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	d, err := delivery.ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil {
		return false, err
	}
	return d.State.Policy.Test, nil
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
// each with how it ended, in order (checksReportMax and its neighbours);
// with bases, the target-branch commits the task builds on (taskBases).
func (s *Syncer) checksContext(ctx context.Context, r phaseRun, checks []delivery.Check, bases []taskBase) (string, error) {
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
		Bases                  []taskBase
		Calls                  []string
		Omitted                int
	}{boundHead(string(report), checksReportMax), boundHead(notes, checksNotesMax), boundHead(changes.String(), checksChangesMax),
		bases, shown, total - len(shown)}), nil
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
