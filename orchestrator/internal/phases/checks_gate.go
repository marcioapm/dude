package phases

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

var (
	checksText        = delivery.MustText("notices/checks")
	checksSkip        = delivery.MustText("notices/checks/skip")
	checksMany        = delivery.MustText("notices/checks/many")
	checksUnparseable = delivery.MustText("notices/checks/unparseable")
	checksUnchanged   = delivery.MustText("notices/checks/unchanged")
)

// checksCountMax caps the checks one report may list: each is a verdict
// the conductor gives before anything is pushed.
const checksCountMax = 30

type checksVerdict struct {
	Command  string `json:"command"`
	Result   string `json:"result,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason"`
	CallID   string `json:"callId,omitempty"`
	// A conducted verdict's record: the run.checks.conducted event's
	// cursor, and who decided ("conductor").
	DecisionCursor int64  `json:"decisionCursor,omitempty"`
	By             string `json:"by,omitempty"`
	// The person's waiver a waived result carries.
	Waiver *delivery.CarriedWaiver `json:"waiver,omitempty"`
	// Why an attempt waiver does not cover a reported failure, beside the
	// agent's own result and evidence, which a person may still waive.
	WaiverReason string `json:"waiverReason,omitempty"`
	// Why the judge did not pass a reported failure or not-run check as
	// pre-existing or deferred, beside the agent's own result and evidence.
	JudgeReason string `json:"judgeReason,omitempty"`
}

type checksDecision struct {
	Turn     int64 `json:"turn"`
	Round    int   `json:"round"`
	Verified bool  `json:"verified"`
	Skipped  bool  `json:"skipped,omitempty"`
	// Some reported failure passed as one a person waived for the attempt.
	Carried bool `json:"carried,omitempty"`
	// Some reported failure or not-run check passed on the conductor's
	// word alone: pre-existing at the task's base, or deferred to a later
	// phase. Its JSON name is the judge's, so older rows read the same.
	ConductorPassed bool            `json:"judgePassed,omitempty"`
	Verdicts        []checksVerdict `json:"checks"`
	Message         string          `json:"message"`
	// The commit of each repository's target branch the task builds on,
	// for the send-back.
	bases []taskBase
	// No conductor decided: only a person can pass what is held.
	untiered bool
	// pending: the conductor was asked about this turn and has not
	// decided; nothing is recorded and nothing is pushed.
	pending bool
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
	var pending bool
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
		// Asked about this turn and not yet decided, within the wait, with
		// the conductor still there: pending as before. The ask was made
		// only once the turn was found changed (unchangedBeforePush), so
		// nothing is inspected again while it is open.
		if pending, err = checksAskOpenTx(ctx, tx, r, turn); err != nil || pending {
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
	if pending {
		return false, false, nil
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
	} else if d.pending {
		// Held for the conductor's verdict (askConductor): nothing is
		// recorded; the verdict, or its deadline, resumes the gate.
		return false, false, nil
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

// judgeChecks decides the Run's latest report: a conducted delivery's
// conductor decides it (askConductor); with no conductor, only a person
// passes a non-pass (noConductor). The judgement tier is never asked.
func (s *Syncer) judgeChecks(ctx context.Context, r phaseRun) (checksDecision, error) {
	d, checks, done, err := s.parsedChecks(ctx, r)
	if err != nil || done {
		return d, err
	}
	conducted, err := s.conductedDelivery(ctx, r)
	if err != nil {
		return d, err
	}
	if !conducted {
		// No conductor decides this delivery: only a person can pass a
		// reported failure, and passes go with the skip notice.
		return noConductor(d, checksNoConductor, checksSkip), nil
	}
	return s.askConductor(ctx, r, d, checks)
}

// parsedChecks reads the Run's report: done when it is unparseable or too
// long, d then its rejection; otherwise d holds a verdict per non-pass.
func (s *Syncer) parsedChecks(ctx context.Context, r phaseRun) (d checksDecision, checks []delivery.Check, done bool, err error) {
	d = checksDecision{Verdicts: []checksVerdict{}}
	var reply string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var err error
		reply, err = delivery.RunReplyTailTx(ctx, tx, r.ID, checksReplyMax)
		return err
	}); err != nil {
		return d, nil, true, err
	}
	checks, ok := delivery.ParseChecks(reply)
	if !ok {
		d.Verdicts = append(d.Verdicts, checksVerdict{Command: "checks:", Reason: checksUnparseable.Render(delivery.ChecksYAMLErrorOf(reply))})
		return d, nil, true, nil
	}
	if len(checks) > checksCountMax {
		d.Verdicts = append(d.Verdicts, checksVerdict{Command: "checks:",
			Reason: checksMany.Render(struct{ Count, Max int }{len(checks), checksCountMax})})
		return d, nil, true, nil
	}
	for _, c := range checks {
		if c.Result != delivery.CheckPass {
			d.Verdicts = append(d.Verdicts, checksVerdict{Command: c.Command, Result: c.Result, Reason: c.Evidence})
		}
	}
	return d, checks, false, nil
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

// unpassed are the verdicts that did not pass: neither backed, waived,
// pre-existing nor deferred.
func (d checksDecision) unpassed() []checksVerdict {
	passed := []string{delivery.CheckPass, delivery.CheckWaived, delivery.CheckPreexisting, delivery.CheckDeferred}
	return slices.DeleteFunc(slices.Clone(d.Verdicts), func(v checksVerdict) bool { return slices.Contains(passed, v.Result) })
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

// checksReplyMax is how much of the end of the agent's reply is read for
// its last report: far more than a report.
const checksReplyMax = 256 << 10
