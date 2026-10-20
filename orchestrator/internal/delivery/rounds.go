package delivery

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A reviewer's or tester's next round goes on from its previous one, the
// same phase and category in the same attempt (its predecessor):
//
//   - resumed: a new runs row takes over the predecessor's lux Run, its
//     agent's conversation and workspace, and is told what changed since
//     (the round message, a directive);
//   - replayed: when it cannot be resumed (not kept, its lux Run gone, its
//     model or harness changed), a fresh agent given the predecessor
//     chain's conversation from the ledger, reviewing what changed since;
//   - fresh: no predecessor, a delta over MaxResumeDeltaLines, or nothing
//     to replay: today's whole-branch review.

// RangeSinceLastRound is a review of what changed since the predecessor's
// round, by a fresh agent given that round's conversation (ReviewRange).
const RangeSinceLastRound = "since_last_round"

// MaxResumeDeltaLines is the most changed lines (additions and deletions,
// across repositories) a next round goes on from its predecessor over: a
// larger change gets fresh eyes on the whole branch.
const MaxResumeDeltaLines = 300

// Continue asks createPhaseRunTx to go on from Predecessor; DeltaLines is
// what changed since its round, over MaxResumeDeltaLines a fresh Run.
type Continue struct {
	Predecessor string
	DeltaLines  int
}

// How a Run goes on from its predecessor (runs.continues_how).
const (
	ContinuesResume = "resume"
	ContinuesReplay = "replay"
)

// EvRunContinues is a Run created to go on from its predecessor. Payload:
// {from, how}: how is ContinuesResume or ContinuesReplay.
const EvRunContinues = "run.continues"

// RoundContinues says whether a phase's next round goes on from its last:
// a reviewer's or tester's, outside an integration.
func RoundContinues(p Policy, phase string, integration bool) bool {
	return p.ResumeRounds && !integration && (phase == PhaseReview || phase == PhaseTest)
}

// notIntegration (SQL, over runs): not an integration review, which is
// another job than the branch's review and never a predecessor.
const notIntegration = `NOT (pr_feedback @> '[{"source":"integration"}]')`

// Predecessor is the Run a next round would go on from, and where its
// checkouts were.
type Predecessor struct {
	ID       string
	BaseRefs map[string]string
}

// PreviousRound is the latest completed Run of phase and category in the
// task's attempt (0: its highest), nil for none.
func (s *Store) PreviousRound(ctx context.Context, org, taskID string, attempt int, phase, category string) (*Predecessor, error) {
	var p *Predecessor
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var found Predecessor
		err := tx.QueryRow(ctx, `SELECT id, base_refs FROM runs
			WHERE task_id = $1 AND attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1), 1)
			  AND phase = $3::run_phase AND category IS NOT DISTINCT FROM $4 AND status = 'completed'
			  AND replaced_by IS NULL AND source_kind IS NULL AND `+notIntegration+`
			ORDER BY ended_at DESC, id DESC LIMIT 1`, taskID, attempt, phase, db.Nullable(category)).Scan(&found.ID, &found.BaseRefs)
		if db.IsNotFound(err) {
			return nil
		}
		p = &found
		return err
	})
	return p, err
}

// DeltaLines is how many lines changed (additions and deletions) from the
// predecessor's checkouts to heads, across the task's repositories, as
// GitHub compares them; a comparison GitHub cut short counts as over
// MaxResumeDeltaLines.
func (s *Store) DeltaLines(ctx context.Context, org, taskID string, gh *forge.GitHub, from, heads map[string]string) (int, error) {
	var repos []Repository
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		repos, err = TaskRepositories(ctx, tx, taskID)
		return err
	}); err != nil {
		return 0, err
	}
	lines := 0
	for _, repo := range repos {
		now := heads[repo.Name]
		if now == "" {
			continue
		}
		prev := from[repo.Name]
		if prev == "" {
			prev = repo.DefaultBranch
		}
		if prev == now {
			continue
		}
		if gh == nil {
			return MaxResumeDeltaLines + 1, nil
		}
		cmp, err := gh.Compare(ctx, forge.SlugFromURL(repo.URL), prev, now)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if forge.Transient(err) {
				return 0, forgeWait(err)
			}
			// Unmeasurable changes require fresh eyes on the whole branch.
			return MaxResumeDeltaLines + 1, nil
		}
		if cmp.Truncated() {
			return MaxResumeDeltaLines + 1, nil
		}
		for _, f := range cmp.Files {
			lines += f.Additions + f.Deletions
		}
	}
	return lines, nil
}

// predecessorRow is what continueOrCreateTx reads of the predecessor:
// Kept, its lux Run can be taken over now; Collecting, it can once its
// last exit is collected, within collectWait of the exit.
type predecessorRow struct {
	Predecessor
	Phase, Category              string
	Kept, Collecting             bool
	Model, Tier, Effort, Harness string
	ProjectModels, OrgModels     json.RawMessage
}

// collectWait bounds how long a next round waits for its predecessor's
// exit to be collected (phases' Artifacts) before it starts fresh instead.
const collectWait = 2 * time.Minute

// continueOrCreateTx is createPhaseRunTx's choice for a Run asked to go on
// (in.Continue): resumed, replayed or fresh. The caller holds no Run lock;
// the predecessor is locked here.
func continueOrCreateTx(ctx context.Context, tx pgx.Tx, org string, in PhaseRun) (string, error) {
	c := in.Continue
	in.Continue = nil
	var p predecessorRow
	err := tx.QueryRow(ctx, `SELECT r.id, r.base_refs, r.phase::text, COALESCE(r.category, ''),
			r.keep AND COALESCE(r.kept_until > now(), false) AND r.lux_run_id IS NOT NULL
			  AND r.lux_stop_reason IS DISTINCT FROM 'cancel' AND r.artifacts_due_at IS NULL,
			r.keep AND COALESCE(r.kept_until > now(), false) AND r.lux_run_id IS NOT NULL
			  AND r.lux_stop_reason IS DISTINCT FROM 'cancel' AND COALESCE(r.artifacts_due_at > now() - make_interval(secs => $3), false),
			COALESCE(r.model, ''), COALESCE(r.model_tier, ''), COALESCE(r.effort, ''), COALESCE(r.harness, ''),
			p.agent_models, o.default_agent_models
		FROM runs r JOIN projects p ON p.id = r.project_id JOIN organizations o ON o.id = r.organization_id
		WHERE r.id = $1 AND r.task_id = $2 AND r.status = 'completed' AND r.replaced_by IS NULL
		FOR UPDATE OF r`, c.Predecessor, in.TaskID, collectWait.Seconds()).
		Scan(&p.ID, &p.BaseRefs, &p.Phase, &p.Category, &p.Kept, &p.Collecting, &p.Model, &p.Tier, &p.Effort, &p.Harness,
			&p.ProjectModels, &p.OrgModels)
	if db.IsNotFound(err) || err == nil && (p.Phase != in.Phase || p.Category != in.Category) {
		return createPhaseRunTx(ctx, tx, org, in)
	}
	if err != nil {
		return "", err
	}
	if c.DeltaLines > MaxResumeDeltaLines {
		// Fresh eyes on the whole branch: the predecessor is let go and any
		// narrower range is no longer valid for this decision.
		if err := releaseRunTx(ctx, tx, p.ID); err != nil {
			return "", err
		}
		in.ReviewRange = nil
		in.freshReason = "large_delta"
		return createPhaseRunTx(ctx, tx, org, in)
	}
	sameSpec, err := sameSpecTx(ctx, tx, in, p)
	if err != nil {
		return "", err
	}
	if p.Kept && sameSpec && in.Tier == "" {
		return handOverTx(ctx, tx, org, in, p)
	}
	if p.Collecting && sameSpec && in.Tier == "" {
		// Its last exit's files are still being collected onto it: the
		// handover waits for them, a little.
		return "", workflow.Wait{After: time.Second, Why: "the previous round's files are still being collected"}
	}
	if err := releaseRunTx(ctx, tx, p.ID); err != nil {
		return "", err
	}
	in.Replays = p.ID
	return createPhaseRunTx(ctx, tx, org, in)
}

// sameSpecTx says whether the predecessor runs on what a fresh Run of the
// role would now: lux keeps a Run's model, tier, effort and harness from
// its submit. Its project secrets are checked at its resume (phases'
// roundSecrets), where the recipient a phase is given them as is known.
func sameSpecTx(ctx context.Context, tx pgx.Tx, in PhaseRun, p predecessorRow) (bool, error) {
	role := PromptRoleFor(in.Phase, RoleForPhase[in.Phase])
	settings := ResolveRole(role, p.ProjectModels, p.OrgModels)
	tier, noTier, err := TierFor(ctx, tx, role, settings)
	if err != nil || noTier != "" {
		return false, err
	}
	harness := p.Harness
	if !IsHarness(harness) {
		// The scripted agent's label: it stands in for OpenCode.
		harness = HarnessOpenCode
	}
	return tier.Model == p.Model && tier.Name == p.Tier && tier.Effort == p.Effort && harness == settings.HarnessName(), nil
}

// releaseRunTx lets a kept Run go: RetireCompleted, or the syncer's end,
// terminates its lux Run on its next pass.
func releaseRunTx(ctx context.Context, tx pgx.Tx, runID string) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET keep = false, kept_until = now() WHERE id = $1 AND keep`, runID)
	return err
}

// handOverTx creates the next round as a new row that takes the
// predecessor's lux Run over: its stream cursor and translator state, so
// the new row reads only what comes after, and its costs so far as the
// new row's base. Paused, to be resumed (phases.Syncer.whilePaused),
// moved to the new heads, with the round message as its next input. The
// predecessor keeps its record and its cost to date, and no lux Run.
func handOverTx(ctx context.Context, tx pgx.Tx, org string, in PhaseRun, p predecessorRow) (string, error) {
	created, err := createPhaseRunTx(ctx, tx, org, in)
	if err != nil {
		return "", err
	}
	// The lux Run is the new row's now (runs.lux_run_id is unique): its cost
	// is read there, and nothing retires it from here.
	var luxRunID string
	if err := tx.QueryRow(ctx, `UPDATE runs o SET lux_run_id = NULL, keep = false, kept_until = NULL, lux_cost_next_at = NULL
		FROM (SELECT id, lux_run_id FROM runs WHERE id = $1) was WHERE o.id = was.id RETURNING was.lux_run_id`, p.ID).Scan(&luxRunID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs n SET status = 'paused', control = 'resume', control_requested_at = now(),
			control_reason = 'next round', lux_stop_reason = 'pause', continues_run_id = o.id, continues_how = 'resume',
			lux_run_id = $3, lux_state = o.lux_state, lux_repositories = o.lux_repositories, lux_pushes = o.lux_pushes,
			push_branch = o.push_branch, lux_cursor = o.lux_cursor, lux_after_event = o.lux_after_event,
			agent_session_epoch = o.agent_session_epoch, harness_state = o.harness_state - 'brokenTurn',
			context_tokens = o.context_tokens, base_shas = o.base_shas, synced_bases = o.synced_bases,
			harness = o.harness, model = o.model, model_tier = o.model_tier, effort = o.effort, image = o.image,
			machine = o.machine, network = o.network, can_run_containers = o.can_run_containers,
			machine_usd_per_hour = o.machine_usd_per_hour, project_secret_names = o.project_secret_names,
			prompt_version_id = o.prompt_version_id, project_prompt_version_id = o.project_prompt_version_id,
			tool_starts = o.tool_starts, started_at = now(),
			agent_cost_base = o.agent_cost_base + o.agent_cost_usd,
			lux_ai_base = o.lux_ai_base + COALESCE(o.lux_ai_usd, 0),
			lux_compute_base = o.lux_compute_base + COALESCE(o.lux_compute_usd, 0)
		FROM runs o WHERE n.id = $1 AND o.id = $2`, created, p.ID, luxRunID); err != nil {
		return "", err
	}
	text, err := roundMessageTx(ctx, tx, in, p.BaseRefs)
	if err != nil {
		return "", err
	}
	ref, err := runRefTx(ctx, tx, org, created)
	if err != nil {
		return "", err
	}
	if _, _, err := QueueDirective(ctx, tx, ref, Directive{Text: text, Scope: "run"}); err != nil {
		return "", err
	}
	return created, continuesEventTx(ctx, tx, ref, p.ID, ContinuesResume)
}

func runRefTx(ctx context.Context, tx pgx.Tx, org, runID string) (RunRef, error) {
	ref := RunRef{Org: org, RunID: runID}
	err := tx.QueryRow(ctx, `SELECT project_id, task_id FROM runs WHERE id = $1`, runID).Scan(&ref.ProjectID, &ref.TaskID)
	return ref, err
}

func continuesEventTx(ctx context.Context, tx pgx.Tx, ref RunRef, from, how string) error {
	_, err := ledger.Append(ctx, tx, ref.Event(EvRunContinues, ledger.ActorSystem, map[string]any{"from": from, "how": how}))
	return err
}

// roundMessageTx is a resumed Run's next input: what changed since its
// last round, per repository from its predecessor's checkout to the new
// head; the findings it judges (in.FindingIDs, answered by position, as a
// fresh Run's prompt lists them); and, for a reviewer, how it reports.
func roundMessageTx(ctx context.Context, tx pgx.Tx, in PhaseRun, from map[string]string) (string, error) {
	findings, err := findingsInOrderTx(ctx, tx, in.FindingIDs)
	if err != nil {
		return "", err
	}
	reason := ""
	if in.ReviewRange != nil {
		reason = rangeReason[in.ReviewRange.Of]
	}
	return RoundMessage(in.Phase, from, in.BaseRefs, reason, findings), nil
}

// RoundMessage renders contracts/resume/round: from and to are each
// repository's commit at the last round and now.
func RoundMessage(phase string, from, to map[string]string, reason string, findings []Finding) string {
	type rng struct {
		Repo, From string
		Named      bool
	}
	var ranges []rng
	for _, repo := range slices.Sorted(maps.Keys(to)) {
		if from[repo] == "" || from[repo] == to[repo] {
			continue
		}
		ranges = append(ranges, rng{repo, from[repo], len(to) > 1})
	}
	list := ""
	if len(findings) > 0 {
		list = earlierFindings(findings)
	}
	return render(roundTmpl, struct {
		Test             bool
		Ranges           []rng
		Reason, Findings string
		Format, Summary  string
	}{phase == PhaseTest, ranges, reason, list, findingFormat, reviewSummary})
}

func findingsInOrderTx(ctx context.Context, tx pgx.Tx, ids []string) ([]Finding, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT severity::text, category, title, description, suggested_fix,
			COALESCE(repo, ''), COALESCE(file, ''), COALESCE(line, 0)
		FROM review_findings f JOIN unnest($1::text[]) WITH ORDINALITY AS chosen(id, n) ON chosen.id = f.id
		ORDER BY chosen.n`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Finding])
}

// replayingTx settles a Run that replays predecessor (createPhaseRunTx):
// one with a conversation to replay reviews what changed since, the
// predecessor's checkouts its range unless a range is set already; one
// with none reviews as a fresh Run does.
func replayingTx(ctx context.Context, tx pgx.Tx, in *PhaseRun, predecessor string) (bool, error) {
	text, err := Replay(ctx, tx, Talker{RunID: predecessor})
	if err != nil || text == "" {
		in.freshReason = "nothing_to_replay"
		return false, err
	}
	if in.ReviewRange == nil && in.Phase == PhaseReview {
		var base map[string]string
		if err := tx.QueryRow(ctx, `SELECT base_refs FROM runs WHERE id = $1`, predecessor).Scan(&base); err != nil {
			return false, err
		}
		in.ReviewRange = &ReviewRange{Of: RangeSinceLastRound, Base: base}
	}
	return true, nil
}
