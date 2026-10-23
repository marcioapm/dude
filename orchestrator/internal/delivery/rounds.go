package delivery

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A reviewer's or tester's next round goes on from its previous one, the
// same phase and category in the same attempt (its predecessor); a fixer's
// from the attempt's last writer (goesOnFrom):
//
//   - resumed: a new runs row takes over the predecessor's lux Run, its
//     agent's conversation and workspace, and is told what changed since
//     (the round message, a directive);
//   - replayed: when it cannot be resumed (not kept, its lux Run gone, its
//     model or harness changed, or too little running time left:
//     timeSpentTx), a fresh agent given the predecessor chain's
//     conversation from the ledger, reviewing what changed since;
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
// a reviewer's, tester's or fixer's, outside an integration.
func RoundContinues(p Policy, phase string, integration bool) bool {
	return p.ResumeRounds && !integration && len(goesOnFrom[phase]) > 0
}

// goesOnFrom is, per phase whose rounds go on, the phases its predecessor
// may be of: a fix goes on from the attempt's last writer, its previous
// fixer or else its implementer.
var goesOnFrom = map[string][]string{
	PhaseReview: {PhaseReview},
	PhaseTest:   {PhaseTest},
	PhaseFix:    {PhaseFix, PhaseImplement},
}

// notIntegration (SQL, over runs): not an integration review, which is
// another job than the branch's review and never a predecessor.
const notIntegration = `NOT (pr_feedback @> '[{"source":"integration"}]')`

// Predecessor is the Run a next round would go on from, and where its
// checkouts were when its round ended.
type Predecessor struct {
	ID       string
	BaseRefs map[string]string
}

// roundEnd (SQL, over runs r): where a Run's checkouts were when its round
// ended: where they started, each repository it published moved to its
// head.
const roundEnd = `(r.base_refs || COALESCE((SELECT jsonb_object_agg(h.key, h.value->>'sha')
	FROM jsonb_each(CASE WHEN jsonb_typeof(r.heads) = 'object' THEN r.heads ELSE '{}' END) h
	WHERE COALESCE(h.value->>'sha', '') <> ''), '{}'::jsonb))`

// PreviousRound is the latest completed Run phase's next round goes on
// from (goesOnFrom), of category in the task's attempt (0: its highest),
// nil for none.
func (s *Store) PreviousRound(ctx context.Context, org, taskID string, attempt int, phase, category string) (*Predecessor, error) {
	var p *Predecessor
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var found Predecessor
		err := tx.QueryRow(ctx, `SELECT r.id, `+roundEnd+` FROM runs r
			WHERE r.task_id = $1 AND r.attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1), 1)
			  AND r.phase = ANY($3::run_phase[]) AND r.category IS NOT DISTINCT FROM $4 AND r.status = 'completed'
			  AND r.replaced_by IS NULL AND r.source_kind IS NULL AND `+notIntegration+`
			ORDER BY r.ended_at DESC, r.id DESC LIMIT 1`, taskID, attempt, goesOnFrom[phase], db.Nullable(category)).Scan(&found.ID, &found.BaseRefs)
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
	err := tx.QueryRow(ctx, `SELECT r.id, `+roundEnd+`, r.phase::text, COALESCE(r.category, ''),
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
	if db.IsNotFound(err) || err == nil && (!slices.Contains(goesOnFrom[in.Phase], p.Phase) || p.Category != in.Category) {
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
	moved, err := headMovedTx(ctx, tx, in, p)
	if err != nil {
		return "", err
	}
	spent := false
	if p.Kept || p.Collecting {
		if spent, err = timeSpentTx(ctx, tx, p.ID, in.timeLimit); err != nil {
			return "", err
		}
	}
	if sameSpec && !moved && !spent && in.Tier == "" {
		if p.Kept {
			return handOverTx(ctx, tx, org, in, p)
		}
		if p.Collecting {
			// Its last exit's files are still being collected onto it: the
			// handover waits for them, a little.
			return "", workflow.Wait{After: time.Second, Why: "the previous round's files are still being collected"}
		}
	}
	if err := releaseRunTx(ctx, tx, p.ID); err != nil {
		return "", err
	}
	switch {
	case !sameSpec:
		in.freshReason = "spec_changed"
	case moved:
		in.freshReason = "head_moved"
	case spent:
		in.freshReason = "time_budget"
	}
	in.Replays = p.ID
	return createPhaseRunTx(ctx, tx, org, in)
}

// timeSpentTx says whether the lux Run predecessor holds has too little of
// limit left for another round. lux times a Run's running time summed over
// all its placements, and a resume cannot give it more.
//
//   - Used: the active time (run_metrics: started to ended, less dude's
//     parks) of the rows that held that lux Run in turn, linked by resume.
//   - Enough: half the limit, or the chain's longest round if that is more
//     (a round that ran that long may run that long again), taken over
//     every round the chain continues, replays included, as its replay
//     (chainScope) covers.
//
// dude's clock can be a few minutes under lux's at each stop, so this is a
// heuristic; phases' holdTimedOut is its backstop. A zero limit is
// unknown: nothing is spent.
func timeSpentTx(ctx context.Context, tx pgx.Tx, predecessor string, limit time.Duration) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	// Under RLS the planner prices run_metrics' per-park subplan from the
	// organisation's parks, and past a few thousand JIT takes ~60 ms of a
	// ~1.5 ms query while the predecessor is locked. Off for this query
	// only: the caller's transaction goes on.
	var jitWas string
	if err := tx.QueryRow(ctx, `SELECT current_setting('jit'), set_config('jit', 'off', true)`).Scan(&jitWas, nil); err != nil {
		return false, err
	}
	if beforeTimeSpentQuery != nil {
		beforeTimeSpentQuery(ctx, tx)
	}
	var used, longest float64
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (
			SELECT id, continues_run_id, continues_how, true AS held FROM runs WHERE id = $1
			UNION ALL
			SELECT r.id, r.continues_run_id, r.continues_how, c.held AND c.continues_how = 'resume'
			FROM runs r JOIN chain c ON r.id = c.continues_run_id)
		SELECT COALESCE(sum(m.active_seconds) FILTER (WHERE chain.held), 0), COALESCE(max(m.active_seconds), 0)
		FROM chain, run_metrics(chain.id) m`, predecessor).Scan(&used, &longest)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('jit', $1, true)`, jitWas); err != nil {
		return false, err
	}
	seconds := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	return limit-seconds(used) < max(limit/2, seconds(longest)), nil
}

// beforeTimeSpentQuery, when set (by tests), runs in timeSpentTx's
// transaction just before its query.
var beforeTimeSpentQuery func(context.Context, pgx.Tx)

// headMovedTx says whether a writer predecessor's checkout cannot be
// fast-forwarded to the round's head: lux moves a checkout to a commit
// only when it is detached, and a lux Run first submitted with no commit
// for a repository (the implementer of new work) checked that repository
// out on its branch. Such a checkout is synced only when the head is still
// where the predecessor's round ended.
func headMovedTx(ctx context.Context, tx pgx.Tx, in PhaseRun, p predecessorRow) (bool, error) {
	if !Publishes[p.Phase] {
		return false, nil
	}
	// The resumed chain's first row is the one its lux Run was submitted for.
	var submitted map[string]string
	if err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (
			SELECT id, continues_run_id, continues_how, base_refs FROM runs WHERE id = $1
			UNION ALL
			SELECT r.id, r.continues_run_id, r.continues_how, r.base_refs FROM runs r JOIN chain c ON r.id = c.continues_run_id
			WHERE c.continues_how = 'resume')
		SELECT base_refs FROM chain WHERE continues_how IS DISTINCT FROM 'resume'`, p.ID).Scan(&submitted); err != nil {
		return false, err
	}
	for repo, head := range in.BaseRefs {
		if submitted[repo] == "" && head != "" && head != p.BaseRefs[repo] {
			return true, nil
		}
	}
	return false, nil
}

// SecretRecipients is the project-secret recipient a Run is, by the
// settings role it runs as; "" for a Run that is given no project secret.
var SecretRecipients = map[string]string{
	"implementer": "implementer",
	"fixer":       "fixer",
	"simplifier":  "simplifier",
	"reviewer":    "reviewer",
	"qa_browser":  "tester",
}

// sameSpecTx says whether the predecessor runs on what a fresh Run of the
// role would now: lux keeps a Run's model, tier, effort, harness and the
// names of its project secrets from its submit, and its instruction file.
// A fixer going on from the implementer runs on the implementer's: the
// same only while the fixer's settings resolve to it, its recipient is
// given the same secret names, and it has no saved prompt or notes of its
// own. Grants are not compared: capabilities.Resolve is the role's
// defaults alone, and a fixer's are the implementer's (toolRoleOf).
func sameSpecTx(ctx context.Context, tx pgx.Tx, in PhaseRun, p predecessorRow) (bool, error) {
	role := PromptRoleFor(in.Phase, RoleForPhase[in.Phase])
	settings := ResolveRole(role, p.ProjectModels, p.OrgModels)
	tier, noTier, err := TierFor(ctx, tx, role, settings)
	if err != nil {
		return false, err
	}
	if noTier != "" {
		return false, nil
	}
	harness := p.Harness
	if !IsHarness(harness) {
		// The scripted agent's label: it stands in for OpenCode.
		harness = HarnessOpenCode
	}
	if tier.Model != p.Model || tier.Name != p.Tier || tier.Effort != p.Effort || harness != settings.HarnessName() {
		return false, nil
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT s.name FROM project_secrets s WHERE s.project_id = r.project_id AND $2 = ANY(s.recipients) ORDER BY s.name)
			= ARRAY(SELECT n FROM unnest(r.project_secret_names) n ORDER BY n)
		FROM runs r WHERE r.id = $1`, p.ID, SecretRecipients[role]).Scan(&same); err != nil || !same {
		return false, err
	}
	was := PromptRoleFor(p.Phase, RoleForPhase[p.Phase])
	if was == role {
		return true, nil
	}
	if ResolveRole(was, p.ProjectModels, p.OrgModels).Context != settings.Context {
		return false, nil
	}
	// The fixer's current prompts, as LoadPrompts selects them: the latest
	// version of each layer. An empty org body is none (instructionFile
	// drops it), and so is a project's empty add.
	var saved bool
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT btrim(v.body) <> '' FROM prompt_versions v
			WHERE v.role = $2 AND v.project_id IS NULL ORDER BY v.created_at DESC, v.id DESC LIMIT 1), false)
		OR COALESCE((SELECT v.mode = 'replace' OR btrim(v.body) <> '' FROM prompt_versions v JOIN runs r ON r.id = $1
			WHERE v.role = $2 AND v.project_id = r.project_id ORDER BY v.created_at DESC, v.id DESC LIMIT 1), false)`,
		p.ID, role).Scan(&saved)
	return !saved, err
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
	text, err := roundMessageTx(ctx, tx, in, p, created)
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
// head; then, for a reviewer or tester, the findings it judges
// (in.FindingIDs, answered by position, as a fresh Run's prompt lists
// them) and how it reports; for a fixer, what it fixes (FixRoundMessage).
func roundMessageTx(ctx context.Context, tx pgx.Tx, in PhaseRun, p predecessorRow, created string) (string, error) {
	findings, err := findingsInOrderTx(ctx, tx, in.FindingIDs)
	if err != nil {
		return "", err
	}
	var message string
	if in.Phase == PhaseFix {
		people, err := FeedbackItemsTx(ctx, tx, in.TaskID, in.FeedbackItemIDs)
		if err != nil {
			return "", err
		}
		// The predecessor's tools are the new row's: a token was made for
		// it only when dude serves them (phases' toolsToken); the CLI is
		// there only when lux serves them in the container, as a fresh
		// fixer's prompt has it.
		var tools bool
		if err := tx.QueryRow(ctx, `SELECT mcp_token_hash IS NOT NULL FROM runs WHERE id = $1`, p.ID).Scan(&tools); err != nil {
			return "", err
		}
		message = FixRoundMessage(p.BaseRefs, in.BaseRefs, p.Phase != PhaseFix,
			PromptInput{Findings: findings, PRFeedback: in.PRFeedback, Feedback: people, Tools: tools, CLI: tools && in.toolsService})
		since, err := sinceLastRoundTx(ctx, tx, in.TaskID, p.ID)
		if err != nil {
			return "", err
		}
		if since != "" {
			message += "\n\n" + since
		}
	} else {
		reason := ""
		if in.ReviewRange != nil {
			reason = rangeReason[in.ReviewRange.Of]
		}
		var lastFix []FixRange
		var questions []EarlierQuestion
		if in.Phase == PhaseReview {
			if lastFix, err = LastFixTx(ctx, tx, in.ParentRunID); err != nil {
				return "", err
			}
			// The new row's own: it is a review, of its category, so it is
			// shown what that category asked.
			if questions, err = EarlierQuestionsTx(ctx, tx, created); err != nil {
				return "", err
			}
		}
		message = RoundMessage(in.Phase, p.BaseRefs, in.BaseRefs, reason, findings, questions, lastFix)
		if in.Phase == PhaseTest {
			checks, err := WriterChecksTx(ctx, tx, in.TaskID, in.Attempt)
			if err != nil {
				return "", err
			}
			if checks != "" {
				message += "\n\n" + checks
			}
		}
	}
	if note := strings.TrimSpace(in.ConductorNote); note != "" {
		message += "\n\n" + render(fromConductorTmpl, note)
	}
	return message, nil
}

// sinceLastRoundTx is what a resumed writer was not told and a fresh one's
// prompt carries (its task section): the task as it is now, when its
// title, goal or criteria were edited after the predecessor's round ended
// (as ResumeKeptTx tells it), and what people decided since; "" for
// neither.
func sinceLastRoundTx(ctx context.Context, tx pgx.Tx, taskID, predecessor string) (string, error) {
	var title, goal string
	var criteria []byte
	var edited bool
	if err := tx.QueryRow(ctx, `SELECT title, goal, acceptance_criteria, EXISTS (SELECT 1 FROM events e
			WHERE e.task_id = t.id AND e.event_type = 'task.updated'
			  AND (e.payload ? 'title' OR e.payload ? 'goal' OR e.payload ? 'acceptanceCriteria')
			  AND e.occurred_at > (SELECT ended_at FROM runs WHERE id = $2))
		FROM tasks t WHERE t.id = $1`, taskID, predecessor).Scan(&title, &goal, &criteria, &edited); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT q.prompt, q.answer FROM questions q
		WHERE q.task_id = $1 AND q.status = 'answered' AND q.answer IS NOT NULL
		  AND q.answered_at > (SELECT ended_at FROM runs WHERE id = $2) ORDER BY q.answered_at`, taskID, predecessor)
	if err != nil {
		return "", err
	}
	decisions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Decision])
	if err != nil {
		return "", err
	}
	var parts []string
	if edited {
		parts = append(parts, changedTask(title, goal, criteria))
	}
	if len(decisions) > 0 {
		// The task section's decisions alone.
		parts = append(parts, strings.TrimSpace(PromptInput{Decisions: decisions}.task()))
	}
	return strings.Join(parts, "\n\n"), nil
}

// FixRoundMessage renders contracts/resume/fix-round: from and to are each
// repository's commit at the writer's last push and now; contract, a
// writer that was not a fixer (the implementer), whose instruction file
// lacks the fix contract; in, what the round fixes, as Prompt(PhaseFix)
// gives it.
func FixRoundMessage(from, to map[string]string, contract bool, in PromptInput) string {
	c := ""
	if contract {
		c = fixContract
	}
	return render(fixRoundTmpl, struct {
		Ranges   []roundRange
		Contract string
		Sections []string
	}{roundRanges(from, to), c, fixSections(in)})
}

type roundRange struct {
	Repo, From string
	Named      bool
}

// roundRanges are the repositories whose commit moved from from to to.
func roundRanges(from, to map[string]string) []roundRange {
	var ranges []roundRange
	for _, repo := range slices.Sorted(maps.Keys(to)) {
		if from[repo] == "" || from[repo] == to[repo] {
			continue
		}
		ranges = append(ranges, roundRange{repo, from[repo], len(to) > 1})
	}
	return ranges
}

// RoundMessage renders contracts/resume/round: from and to are each
// repository's commit at the last round and now; questions, what earlier
// reviewers of a review's category asked; lastFix, the commits of the fix
// this round follows (LastFixTx).
func RoundMessage(phase string, from, to map[string]string, reason string, findings []Finding, questions []EarlierQuestion,
	lastFix []FixRange) string {
	list := ""
	if len(findings) > 0 {
		list = earlierFindings(findings)
	}
	return render(roundTmpl, struct {
		Test                                 bool
		Ranges                               []roundRange
		Reason, Findings, Questions, LastFix string
		Format, Summary                      string
	}{phase == PhaseTest, roundRanges(from, to), reason, list, earlierQuestions(questions), lastFixNote(lastFix), findingFormat, reviewSummary})
}

// FixRange is what a fix Run published in one repository: its commits are
// Base..Head.
type FixRange struct{ Repo, Base, Head string }

// LastFixTx is what the fix Run parent published, per repository, in name
// order; none when parent is no fix (a first review, a simplifier's range)
// or published nothing. A reviewer reads these commits to say whether a new
// finding is in code the fix wrote: dude does not infer it from the paths
// the fix touched (earlierFindings).
func LastFixTx(ctx context.Context, tx pgx.Tx, parent string) ([]FixRange, error) {
	if parent == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT h.key, COALESCE(NULLIF(h.value->>'base', ''), r.base_refs->>h.key, ''), h.value->>'sha'
		FROM runs r, jsonb_each(r.heads) h
		WHERE r.id = $1 AND r.phase = 'fix' AND jsonb_typeof(h.value) = 'object' AND COALESCE(h.value->>'sha', '') <> ''
		ORDER BY h.key`, parent)
	if err != nil {
		return nil, err
	}
	ranges, err := pgx.CollectRows(rows, pgx.RowToStructByPos[FixRange])
	return slices.DeleteFunc(ranges, func(r FixRange) bool { return r.Base == "" }), err
}

// lastFixNote renders contracts/last-fix, each range under its repository's
// name: the ranges list only repositories the fix published, so their count
// cannot tell a one-checkout Run from a several-checkout one. "" for no range.
func lastFixNote(ranges []FixRange) string {
	if len(ranges) == 0 {
		return ""
	}
	return render(lastFixTmpl, ranges)
}

func findingsInOrderTx(ctx context.Context, tx pgx.Tx, ids []string) ([]Finding, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT severity::text, category, title, description, suggested_fix,
			COALESCE(repo, ''), COALESCE(file, ''), COALESCE(line, 0), COALESCE(acceptance, ''), caused_by_fix
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
