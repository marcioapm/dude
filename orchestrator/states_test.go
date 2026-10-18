package orchestrator_test

// Starting states written directly: what a delivery with the scripted agent
// leaves at a point a test starts from, without running its agents. Each is
// the rows and fake GitHub state the real path leaves there, built with the
// delivery's own types and, where it has exported code for a step (opening
// the pull requests, judging a test's verdicts), with that code. The tests
// whose subject is the path to the state keep driving it.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// reviewing is a new task with an open pull request waiting on people
// (prOpen with the world's policy).
func (w *world) reviewing() string {
	return w.prOpen(w.task(), prAt{})
}

// inReview is reviewing for a task the caller made.
func (w *world) inReview(wi string) string {
	return w.prOpen(wi, prAt{})
}

// prAt is what differs between the pull request states prOpen writes.
type prAt struct {
	// The policy's tester is on: a test Run follows the review loop, its
	// verdicts recorded, and the pull request's description verifies them.
	test bool
	// The task also changes acme/web, writable: the implementer commits in
	// both, and each gets a pull request.
	web bool
}

// stateRun is one phase Run prOpen writes.
type stateRun struct {
	id, phase, category, key string
	fixed                    bool
	step                     fakeagent.Step
	// Where it started and, for a publisher, what it published, by repository.
	starts    map[string]string
	published map[string]string
	paths     []string
	parent    *stateRun
}

// prOpen writes what a delivery of wi with the scripted agent leaves once
// its pull requests open: the agent's commits on the task branch (the
// implementer's in every repository, the fixer's and simplifier's in
// target), its Runs (implement; review with one finding; fix; re-review;
// simplify; a review of the simplifier's diff; with at.test, the test),
// the finding resolved, the workflow's and the Runs' outcome events in the
// real order, the task in review and the delivery parked on
// awaitPullRequest. Each Run's model and tier are its role's, resolved as
// the syncer resolves them. The pull requests are opened by
// delivery.Store.OpenPullRequests and, with a tester, the verdicts judged
// by delivery.Store.RecordVerdict.
//
// Left out: the fake lux holds no Runs (each lux_run_id is one it never
// issues), the implementer's NOTES.md, the agents' lifecycle and tool
// events (agent.message is kept: briefings and the verdict read it), the
// consumed phase.finished signals, a Run's recorded network, and the fake
// GitHub's BranchLog and request log of the publishes.
func (w *world) prOpen(wi string, at prAt) string {
	w.t.Helper()
	ctx := context.Background()
	policy := scriptedPolicy()
	policy.Test = at.test
	repos := []struct {
		name string
		gh   *fakegithub.Server
	}{{"target", w.gh}}
	if at.web {
		w.addWeb(wi, "write")
		repos = append(repos, struct {
			name string
			gh   *fakegithub.Server
		}{"web", w.web})
	} else if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(ctx, tx, wi)
	}); err != nil {
		w.t.Fatal(err)
	}
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.name
	}
	branch := delivery.BranchFor(wi, 1)
	wfr := ids.New(ids.WorkflowRun)
	runs := []*stateRun{
		{phase: delivery.PhaseImplement, key: "implement"},
		{phase: delivery.PhaseReview, category: "correctness", key: "review:0:correctness"},
		{phase: delivery.PhaseFix, key: "fix:1"},
		{phase: delivery.PhaseReview, category: "correctness", key: "review:1:correctness", fixed: true},
		{phase: delivery.PhaseSimplify, key: "simplify"},
		{phase: delivery.PhaseReview, category: "correctness", key: "review:2:correctness", fixed: true},
	}
	if at.test {
		runs = append(runs, &stateRun{phase: delivery.PhaseTest, key: "test"})
	}
	for _, r := range runs {
		r.id = ids.New(ids.Run)
		r.step = fakeagent.For(r.phase, "fake/scripted", r.id, r.fixed)
	}
	implement, review0, fix, review1, simplify, review2 := runs[0], runs[1], runs[2], runs[3], runs[4], runs[5]
	var test *stateRun
	if at.test {
		test = runs[6]
	}
	review0.parent, fix.parent, review1.parent, simplify.parent, review2.parent = implement, implement, fix, fix, simplify
	if test != nil {
		test.parent = simplify
	}

	// The publishers' commits, as the fake lux writes them (fakelux.commit):
	// the implementer's in every repository, then the fixer's and the
	// simplifier's in target, each repository's in one git process.
	heads := map[string]string{}
	for _, repo := range repos {
		publishers := []*stateRun{implement}
		if repo.name == "target" {
			publishers = append(publishers, fix, simplify)
		}
		head, err := exec.Command("git", "-C", repo.gh.Repo, "rev-parse", "main").Output()
		if err != nil {
			w.t.Fatal(err)
		}
		heads[repo.name] = strings.TrimSpace(string(head))
		var stream strings.Builder
		now := time.Now().Unix()
		for i, r := range publishers {
			fmt.Fprintf(&stream, "commit refs/heads/%s\nauthor agent <a@x> %d +0000\ncommitter agent <a@x> %d +0000\n", branch, now, now)
			fmt.Fprintf(&stream, "data %d\n%s\n\n", len(r.step.Message)+1, r.step.Message)
			if i == 0 {
				stream.WriteString("from refs/heads/main\n")
			}
			for path, content := range r.step.Commit {
				// "*:FACTORY.md" is every repository's FACTORY.md.
				if _, file, ok := strings.Cut(path, ":"); ok {
					path = file
				}
				fmt.Fprintf(&stream, "M 100644 inline %s\ndata %d\n%s\n", path, len(content), content)
			}
		}
		importer := exec.Command("git", "-C", repo.gh.Repo, "fast-import", "--quiet")
		importer.Stdin = strings.NewReader(stream.String())
		if out, err := importer.CombinedOutput(); err != nil {
			w.t.Fatalf("git fast-import: %v\n%s", err, out)
		}
		for i, r := range publishers {
			sha, err := exec.Command("git", "-C", repo.gh.Repo, "rev-parse", fmt.Sprintf("%s~%d", branch, len(publishers)-1-i)).Output()
			if err != nil {
				w.t.Fatal(err)
			}
			if r.published == nil {
				r.published = map[string]string{}
			}
			r.published[repo.name] = strings.TrimSpace(string(sha))
		}
	}
	// What each Run started from: the heads as they stood when it was
	// made, every repository at main before the implementer.
	for _, r := range runs {
		r.starts = maps.Clone(heads)
		for name, sha := range r.published {
			heads[name] = sha
		}
	}
	implement.paths, fix.paths, simplify.paths = []string{"FACTORY.md"}, []string{fakeagent.FixedFile}, []string{"SIMPLE.md"}
	finding := delivery.ParseFindings(review0.step.Reply)[0]
	findingID := ids.New(ids.Finding)
	st := delivery.State{TaskID: wi, ProjectID: w.project, Policy: policy, Branch: branch,
		HeadRunID: simplify.id, Heads: maps.Clone(heads), ChangedPaths: []string{"target/SIMPLE.md"}, Iteration: 3}

	clock := time.Now().Add(-30 * time.Second)
	tick := func() time.Time { clock = clock.Add(10 * time.Millisecond); return clock }
	ev := func(tx pgx.Tx, typ, runID, actorType, actorID, source string, payload map[string]any) error {
		_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: w.org, ProjectID: w.project, TaskID: wi,
			RunID: runID, ActorType: actorType, ActorID: actorID, Source: source, CorrelationID: wi, OccurredAt: tick(),
			Payload: payload})
		return err
	}
	byWorkflow := func(tx pgx.Tx, typ, runID string, payload map[string]any) error {
		return ev(tx, typ, runID, ledger.ActorSystem, "workflow", ledger.SourceOrchestrator, payload)
	}
	byAgent := func(tx pgx.Tx, typ string, r *stateRun, payload map[string]any) error {
		return ev(tx, typ, r.id, ledger.ActorAgent, r.id, ledger.SourceRunner, payload)
	}

	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		var projectModels, orgModels json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT p.agent_models, o.default_agent_models FROM projects p
			JOIN organizations o ON o.id = p.organization_id WHERE p.id = $1`, w.project).Scan(&projectModels, &orgModels); err != nil {
			return err
		}
		for i, r := range runs {
			role := delivery.RoleForPhase[r.phase]
			settings := delivery.ResolveRole(delivery.PromptRoleFor(r.phase, role), projectModels, orgModels)
			tier, noTier, err := delivery.TierFor(ctx, tx, delivery.PromptRoleFor(r.phase, role), settings)
			if err != nil || noTier != "" {
				return fmt.Errorf("%s's tier: %v %s", r.phase, err, noTier)
			}
			harness := delivery.HarnessOpenCode
			if fakeagent.Is(tier.Model) {
				harness = "scripted"
			}
			var parent, category, reviewRange, own, pushBranch, pushRequest, pushResult any
			var findings, blocking []string
			baseRefs := map[string]string{}
			if r != implement {
				baseRefs, parent = r.starts, r.parent.id
			}
			published := map[string]any{}
			if r.published != nil {
				own, pushBranch, pushRequest = branch, "dude/"+wi+"/run-"+r.id, "push-"+r.id
				var results []map[string]any
				for _, name := range names {
					status := "up-to-date"
					if sha, ok := r.published[name]; ok {
						status = "pushed"
						published[name] = map[string]any{"base": r.starts[name], "sha": sha, "changedPaths": r.paths}
					}
					commit := r.starts[name]
					if sha, ok := r.published[name]; ok {
						commit = sha
					}
					results = append(results, map[string]any{"repo": name, "branch": pushBranch, "commit": commit, "status": status})
				}
				raw, _ := json.Marshal(map[string]any{"requestId": pushRequest, "results": results})
				pushResult = string(raw)
			}
			if r.phase == delivery.PhaseReview || r == test {
				blocking = policy.BlockingSeverities
			}
			if r.phase == delivery.PhaseReview {
				category = r.category
			}
			if r == fix || r == review1 {
				findings = []string{findingID}
			}
			if r == review2 {
				raw, _ := json.Marshal(delivery.ReviewRange{Of: delivery.RangeSimplify, Base: map[string]string{"target": fix.published["target"]}})
				reviewRange = string(raw)
			}
			bases, _ := json.Marshal(baseRefs)
			shas, _ := json.Marshal(r.starts)
			headsRaw, _ := json.Marshal(published)
			created, busy, ended := tick(), tick(), tick()
			if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role,
				parent_run_id, base_refs, base_shas, category, creation_key, finding_ids, blocking_severities, branch, heads, review_range,
				lux_run_id, lux_state, lux_stop_reason, lux_pushes, lux_repositories, push_branch, push_request_id, push_result,
				harness, harness_state, model, model_tier, effort, machine, agent_session_epoch, can_run_containers,
				agent_cost_usd, context_tokens, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
				created_at, started_at, agent_busy_at, agent_active_at, turn_done_at, files_changed_at, ended_at, phase_notified_at)
				SELECT $1, $2, $3, $4, 1, 'completed', $5::run_phase, $6::agent_role,
				$7, $8::jsonb, $9::jsonb, $10, $11, $12, $13, $14, $15::jsonb, $16::jsonb,
				$17, 'running', 'cancel', $18, $18, $19, $20, $21::jsonb,
				$22, jsonb_build_object('sessionId', 'ses_' || $17), $23, $24, NULLIF($25, ''),
				jsonb_build_object('name', m.name, 'sizeId', m.id, 'cpus', m.cpus, 'memoryMiB', m.memory_mib, 'diskGiB', m.disk_gib,
					'pool', NULL, 'poolId', m.pool_id, 'from', 'default'), 1, false,
				0.01, 1000, 12, 34, 900, 100,
				$26, $27, $27, $27, $27, $28, $28, $28
				FROM machine_sizes m WHERE m.organization_id = $2 AND m.is_default`,
				r.id, w.org, w.project, wi, r.phase, role,
				parent, string(bases), string(shas), category, wfr+":"+r.key,
				db.NonNil(findings), db.NonNil(blocking), own, string(headsRaw), reviewRange,
				"lrun_prepared_"+r.id, names, pushBranch, pushRequest, pushResult,
				harness, tier.Model, tier.Name, tier.Effort,
				created, busy, ended); err != nil {
				return fmt.Errorf("run %d: %w", i, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, file, line,
			title, description, suggested_fix, status, resolved_by_run_id, resolution_note, fix_attempts)
			VALUES ($1, $2, $3, $4, $5, $6::finding_severity, $7, $8, $9, $10, $11, 'resolved', $12, 'judged fixed by the re-review', 1)`,
			findingID, w.org, wi, review0.id, finding.Category, finding.Severity, finding.File, finding.Line,
			finding.Title, finding.Description, finding.SuggestedFix, review1.id); err != nil {
			return err
		}

		// The ledger, in the order the real delivery writes it.
		statusBefore := map[*stateRun][2]string{implement: {"running", "implementing"}, fix: {"running", "fixing review findings"},
			simplify: {"running", "simplifying"}}
		round := 0
		for _, r := range runs {
			if s, ok := statusBefore[r]; ok {
				if err := byWorkflow(tx, delivery.EvTaskStatusChanged, "", map[string]any{"status": s[0], "reason": s[1]}); err != nil {
					return err
				}
			}
			if r.phase == delivery.PhaseReview {
				if err := byWorkflow(tx, delivery.EvTaskStatusChanged, "", map[string]any{"status": "review", "reason": "agent review"}); err != nil {
					return err
				}
			}
			payload := map[string]any{"attempt": 1, "phase": r.phase, "role": delivery.RoleForPhase[r.phase],
				"publishes": delivery.Publishes[r.phase], "baseRefs": map[string]string{}}
			if r != implement {
				payload["baseRefs"] = r.starts
			}
			if r.category != "" {
				payload["category"] = r.category
			}
			if r == fix || r == review1 {
				payload["findingIds"] = []string{findingID}
			}
			if err := byWorkflow(tx, delivery.EvRunCreated, r.id, payload); err != nil {
				return err
			}
			if r.phase == delivery.PhaseReview {
				if err := byWorkflow(tx, delivery.EvRunCreated, "", map[string]any{"phase": "review",
					"reviewers": []string{"correctness"}, "iteration": round}); err != nil {
					return err
				}
				round++
			}
			reply := r.step.Reply
			if r == test {
				var criteria []string
				if err := tx.QueryRow(ctx, `SELECT acceptance_criteria FROM tasks WHERE id = $1`, wi).Scan(&criteria); err != nil {
					return err
				}
				r.step = fakeagent.Tester(tierModel(ctx, tx, "qa_browser", projectModels, orgModels), len(criteria), 1)
				reply = r.step.Reply
			}
			if err := byAgent(tx, "agent.message", r, map[string]any{"contextTokens": 1000, "text": reply}); err != nil {
				return err
			}
			for _, name := range names {
				sha, ok := r.published[name]
				if !ok {
					continue
				}
				if err := byAgent(tx, delivery.EvGitCommitCreated, r, map[string]any{"repo": name, "branch": branch,
					"baseSha": r.starts[name], "headSha": sha, "changedPaths": r.paths}); err != nil {
					return err
				}
			}
			if r == test {
				for _, name := range slices.Sorted(maps.Keys(r.step.Publish)) {
					if err := w.publishedArtifact(ctx, tx, wi, r.id, name, r.step.Publish[name]); err != nil {
						return err
					}
				}
			}
			if r.phase == delivery.PhaseReview || r == test {
				count, by := 0, map[string]int{}
				if r == review0 {
					count, by = 1, map[string]int{finding.Severity: 1}
				}
				if err := byAgent(tx, delivery.EvReviewCompleted, r, map[string]any{"phase": r.phase, "count": count, "bySeverity": by}); err != nil {
					return err
				}
			}
			if err := ev(tx, "run.completed", r.id, ledger.ActorSystem, r.id, ledger.SourceRunner, map[string]any{"status": "completed"}); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE tasks SET status = 'review', updated_at = now() WHERE id = $1`, wi)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}

	store := &delivery.Store{DB: w.app}
	if test != nil {
		st.TestRunID = test.id
		if _, err := store.RecordVerdict(ctx, w.org, &st, test.id); err != nil {
			w.t.Fatalf("record the test's verdicts: %v", err)
		}
	}
	// The pull requests, by the publish's own code: rendered from the rows
	// above, opened on the fake GitHub, recorded with their events.
	prIDs, err := store.OpenPullRequests(ctx, w.org, &st, forge.Resolver{DB: w.app})
	if err != nil || len(prIDs) != len(repos) {
		w.t.Fatalf("open the pull requests: %v %v", prIDs, err)
	}
	st.PullRequestIDs = prIDs
	state, _ := json.Marshal(st)
	signals, _ := json.Marshal([]string{delivery.SignalPRFeedback, delivery.SignalHumanDecision, delivery.SignalHumanFeedback})
	mustExec(w.t, w.owner, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state,
		awaiting_signals, task_id) VALUES ($1, $2, $3, $4, 'waiting', 'awaitPullRequest', $5::jsonb, $6::jsonb, $7)`,
		wfr, w.org, delivery.WorkflowType, "delivery:"+wi, state, signals, wi)
	return wi
}

// tierModel is the model role's Runs request, as the syncer resolves it.
func tierModel(ctx context.Context, tx pgx.Tx, role string, layers ...json.RawMessage) string {
	t, _, _ := delivery.TierFor(ctx, tx, role, delivery.ResolveRole(role, layers...))
	return t.Model
}

// publishedArtifact records a file a Run published, as the artifact
// collector does (phases.recordArtifact), under a storage key the fake lux
// never issues: its bytes are not on lux.
func (w *world) publishedArtifact(ctx context.Context, tx pgx.Tx, wi, runID, name, content string) error {
	ctype := "text/markdown; charset=utf-8"
	if strings.HasSuffix(name, ".webm") {
		ctype = "video/webm"
	}
	id := ids.New(ids.Artifact)
	if _, err := tx.Exec(ctx, `INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type,
			size_bytes, storage_key, sha256, epoch, description)
		VALUES ($1, $2, $3, 'published', $4, $5, $6, $7, '', 1, $8)`,
		id, w.org, runID, name, ctype, len(content), "prepared_"+id, fakeagent.Description(name)); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: "artifact.created", OrganizationID: w.org, ProjectID: w.project,
		TaskID: wi, RunID: runID, ActorType: ledger.ActorAgent, ActorID: runID, Source: ledger.SourceRunner, CorrelationID: wi,
		Payload: map[string]any{"artifactId": id, "name": name, "contentType": ctype, "sizeBytes": len(content),
			"sha256": "", "description": fakeagent.Description(name)}})
	return err
}
