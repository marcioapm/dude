package delivery

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// Implemented, reviewed at its head, simplified with nothing to change,
// no CI in the repository, nothing open, no approval: every item holds,
// and the pull request is ready. Nobody approved it, and protection does
// not ask anyone to.
func TestTheReadyGatePassesWithNoApprovalProtectionDoesNotAskFor(t *testing.T) {
	f := newGateFixture(t)
	f.gh.SetChecks("none")
	f.ready()
	g := f.gate()
	for _, it := range g.Items {
		if !it.OK {
			t.Errorf("%s fails: %s", it.Key, it.Detail)
		}
	}
	if !g.Ready {
		t.Fatalf("not ready: %+v", g.Items)
	}
	holds(t, g, ReadyChecks, true, "no CI configured")
}

// 1. open: a draft is not ready.
func TestTheReadyGateOpenItem(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	holds(t, f.gate(), ReadyOpen, true, "open")
	p := f.gh.Pull(f.n)
	f.gh.Set(func(*fakegithub.Server) { p.Draft = true })
	holds(t, f.gate(), ReadyOpen, false, "draft")
}

// 2. head: a head GitHub shows that dude has not recorded is re-evaluated,
// never judged ready.
func TestTheReadyGateHeadItem(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	holds(t, f.gate(), ReadyHead, true, "at ")
	f.gh.CommitOnTop("feature", "A person's commit")
	holds(t, f.gate(), ReadyHead, false, "re-evaluate")
}

// 3. reviewed_through_head: reviewed at the head; or at R with only
// GitHub's pure merge of main since; not with a merge carrying an edit,
// nor a commit nobody reviewed, nor dude's push no review covered, nor a
// lens the paths require missing.
func TestTheReadyGateReviewedThroughHeadItem(t *testing.T) {
	t.Run("reviewed at the head", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		holds(t, f.gate(), ReadyReviewed, true, "reviewed at")
	})
	t.Run("a pure merge of main since the review", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.gh.CommitFiles("main", "Other work", map[string]string{"lib/other.go": "package lib\n"})
		if f.gh.MergeInto("feature", "main", nil) == "" {
			t.Fatal("no merge")
		}
		f.synced()
		holds(t, f.gate(), ReadyReviewed, true, "pure merges of main")
	})
	t.Run("GitHub's update-branch merge since the review", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.gh.CommitFiles("main", "Other work", map[string]string{"lib/other.go": "package lib\n"})
		if err := f.forge.UpdateBranch(f.ctx, "acme/repo", f.n, f.gh.SHA("feature")); err != nil {
			t.Fatal(err)
		}
		f.synced()
		holds(t, f.gate(), ReadyReviewed, true, "pure merges of main")
	})
	t.Run("an impure merge since the review", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.gh.CommitFiles("main", "Other work", map[string]string{"lib/other.go": "package lib\n"})
		merge := f.gh.MergeInto("feature", "main", map[string]string{"app/greet.go": "package app // slipped in\n"})
		f.synced()
		holds(t, f.gate(), ReadyReviewed, false, merge[:7]+" (a merge that is not GitHub's pure merge of main)")
	})
	t.Run("a person's commit since the review", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		c := f.gh.CommitFiles("feature", "Tweak", map[string]string{"app/greet.go": "package app // tweak\n"})
		f.synced()
		holds(t, f.gate(), ReadyReviewed, false, "unreviewed since: "+c[:7])
	})
	t.Run("dude's push no review covered", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		start := f.gh.SHA("feature")
		head := f.gh.CommitFiles("feature", "Fix", map[string]string{"app/greet.go": "package app // fixed\n"})
		f.pushed(f.run(PhaseFix, "", start, map[string]any{"repo": map[string]any{"sha": head, "base": start}}), start, head, "app/greet.go")
		f.synced()
		holds(t, f.gate(), ReadyReviewed, false, "unreviewed: "+head[:7])
	})
	t.Run("no review at all", func(t *testing.T) {
		f := newGateFixture(t)
		head := f.implemented(map[string]string{"app/greet.go": "package app\n"})
		f.simplifiedEmpty(head)
		f.synced()
		holds(t, f.gate(), ReadyReviewed, false, "")
	})
	t.Run("a lens the paths require never reviewed", func(t *testing.T) {
		f := newGateFixture(t)
		head := f.implemented(map[string]string{"db/migrations/001.sql": "SELECT 1;\n"})
		f.reviewed(head)
		f.simplifiedEmpty(head)
		f.synced()
		holds(t, f.gate(), ReadyReviewed, false, "no completed review by database")
		f.reviewed(head, "database")
		holds(t, f.gate(), ReadyReviewed, true, "reviewed at")
	})
}

// 4. simplified_after_last_change: the simplifier ran from the last change
// and committed nothing; or it made the last change and that was
// reviewed; off in the policy, not required.
func TestTheReadyGateSimplifiedAfterLastChangeItem(t *testing.T) {
	t.Run("ran empty from the last change", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		holds(t, f.gate(), ReadySimplified, true, "found nothing to simplify")
	})
	t.Run("never ran", func(t *testing.T) {
		f := newGateFixture(t)
		head := f.implemented(map[string]string{"app/greet.go": "package app\n"})
		f.reviewed(head)
		f.synced()
		holds(t, f.gate(), ReadySimplified, false, "start the simplifier")
	})
	t.Run("ran before a later change", func(t *testing.T) {
		f := newGateFixture(t)
		head := f.ready()
		start := head
		fix := f.gh.CommitFiles("feature", "Fix", map[string]string{"app/greet.go": "package app // fixed\n"})
		f.pushed(f.run(PhaseFix, "", start, map[string]any{"repo": map[string]any{"sha": fix, "base": start}}), start, fix, "app/greet.go")
		f.reviewed(fix)
		f.synced()
		holds(t, f.gate(), ReadySimplified, false, "has not run since "+fix[:7])
	})
	t.Run("committed, and reviewed", func(t *testing.T) {
		f := newGateFixture(t)
		head := f.implemented(map[string]string{"app/greet.go": "package app\n"})
		f.reviewed(head)
		s := f.simplified()
		f.reviewed(s)
		f.synced()
		holds(t, f.gate(), ReadySimplified, true, "the simplifier made "+s[:7]+", the last change, and it was reviewed")
		holds(t, f.gate(), ReadyReviewed, true, "reviewed at "+s[:7])
	})
	t.Run("committed, not reviewed", func(t *testing.T) {
		f := newGateFixture(t)
		head := f.implemented(map[string]string{"app/greet.go": "package app\n"})
		f.reviewed(head)
		s := f.simplified()
		f.synced()
		holds(t, f.gate(), ReadySimplified, false, "the simplifier made "+s[:7])
		holds(t, f.gate(), ReadyReviewed, false, s[:7])
	})
	t.Run("a pure merge after the empty simplifier", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.gh.CommitFiles("main", "Other work", map[string]string{"lib/other.go": "package lib\n"})
		f.gh.MergeInto("feature", "main", nil)
		f.synced()
		holds(t, f.gate(), ReadySimplified, true, "found nothing to simplify")
	})
	t.Run("off in the policy", func(t *testing.T) {
		f := newGateFixture(t)
		f.st.Policy.Simplify = false
		f.saveState()
		head := f.implemented(map[string]string{"app/greet.go": "package app\n"})
		f.reviewed(head)
		f.synced()
		holds(t, f.gate(), ReadySimplified, true, "simplifier off")
	})
}

// 5. findings: an open finding at the fix bar fails it; a low one, or one
// resolved, does not.
func TestTheReadyGateFindingsItem(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	review := "run_g02"
	f.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status)
		VALUES ('fnd_low', $1, 'wi_g', $2, 'correctness', 'low', 'A nit', 'open')`, review)
	holds(t, f.gate(), ReadyFindings, true, "no open finding")
	f.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status)
		VALUES ('fnd_high', $1, 'wi_g', $2, 'correctness', 'high', 'It crashes', 'open')`, review)
	holds(t, f.gate(), ReadyFindings, false, "fnd_high (high) It crashes")
	f.exec(`UPDATE review_findings SET status = 'resolved' WHERE id = 'fnd_high' AND organization_id = $1`)
	holds(t, f.gate(), ReadyFindings, true, "no open finding")
}

// 6. questions: the conductor's open question to a person, an open review
// question, an open escalation, and a person's feedback still to handle
// each fail it.
func TestTheReadyGateQuestionsItem(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	holds(t, f.gate(), ReadyQuestions, true, "nothing open")

	f.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_cond', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
	f.exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, addressee) VALUES ('q_g', $1, 'wi_g', 'run_cond', 'Ship it?', 'person')`)
	holds(t, f.gate(), ReadyQuestions, false, "1 question(s) to a person open")
	f.exec(`UPDATE questions SET status = 'answered', answer = 'yes' WHERE id = 'q_g' AND organization_id = $1`)
	holds(t, f.gate(), ReadyQuestions, true, "nothing open")

	f.exec(`INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question)
		VALUES ('rq_g', $1, 'wi_g', 'run_g02', 1, 'correctness', 'Is the token rotated?')`)
	holds(t, f.gate(), ReadyQuestions, false, "1 review question(s) open")
	f.exec(`UPDATE review_questions SET status = 'withdrawn' WHERE id = 'rq_g' AND organization_id = $1`)

	f.st.Escalation = &Escalation{Reason: "ci_stuck"}
	f.saveState()
	holds(t, f.gate(), ReadyQuestions, false, "escalation ci_stuck waits on a decision")
	f.st.Escalation = nil
	f.saveState()

	f.exec(`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		VALUES ('fbt_g', $1, 'wi_g', 1, 'per_g_'||$1, 'k')`)
	f.exec(`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor, status, submitted_at)
		VALUES ('fbi_g', $1, 'wi_g', 1, 'fbt_g', 'per_g_'||$1, 'Rename it', 'general', '{"kind":"general"}', 'open', now())`)
	holds(t, f.gate(), ReadyQuestions, false, "1 feedback item(s)")
}

// 7. threads: an unresolved review thread is a person's open feedback.
func TestTheReadyGateThreadsItem(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	holds(t, f.gate(), ReadyThreads, true, "no unresolved")
	f.gh.Set(func(s *fakegithub.Server) { s.Unresolved[f.n] = 2 })
	holds(t, f.gate(), ReadyThreads, false, "2 review thread(s) unresolved")
}

// 8. mergeable, from GitHub's mergeable_state: behind, dirty and blocked
// fail, naming what to do; unstable is judged by the checks; unknown is
// retried.
func TestTheReadyGateMergeableItem(t *testing.T) {
	for _, c := range []struct {
		state string
		ok    bool
		want  string
	}{
		{"clean", true, "clean"},
		{"unstable", true, "see checks"},
		{"behind", false, "behind main: update the branch"},
		{"dirty", false, "conflicts with main"},
		{"blocked", false, "GitHub branch protection blocks it"},
		{"unknown", false, "retry later"},
	} {
		t.Run(c.state, func(t *testing.T) {
			f := newGateFixture(t)
			f.ready()
			f.gh.Set(func(s *fakegithub.Server) {
				switch c.state {
				case "dirty":
					s.Conflicting[f.n] = true
				case "unknown":
					s.MergeableUnknown[f.n] = true
				default:
					s.MergeableStates = map[int]string{f.n: c.state}
				}
			})
			holds(t, f.gate(), ReadyMergeable, c.ok, c.want)
		})
	}
	t.Run("blocked for a required approval", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.gh.Set(func(s *fakegithub.Server) { s.RequiredApprovals = map[string]int{"main": 1} })
		holds(t, f.gate(), ReadyMergeable, false, "it requires 1 approving review(s), and has 0")
		f.gh.Review(f.n, "alice", "APPROVED")
		holds(t, f.gate(), ReadyMergeable, true, "clean")
	})
	t.Run("main moved ahead though GitHub says clean", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.gh.AdvanceBase("main", "Other work")
		holds(t, f.gate(), ReadyMergeable, false, "behind main by 1 commit(s): update the branch")
	})
}

// 9. checks, at the exact head: green passes; failing names the failing;
// pending says so; workflows with nothing reported yet are pending; no
// workflow and no required check is no CI; checks unreadable is not ok.
func TestTheReadyGateChecksItem(t *testing.T) {
	for _, c := range []struct {
		name, checks string
		workflows    string
		seen         string
		required     []string
		forbidden    bool
		ok           bool
		want         string
	}{
		{name: "green status", checks: "success", ok: true, want: "1 check(s) passed"},
		{name: "green Actions", checks: "run:success", ok: true, want: "1 check(s) passed"},
		{name: "neutral", checks: "run:neutral", ok: true, want: "passed"},
		{name: "skipped", checks: "run:skipped", ok: true, want: "passed"},
		{name: "failing status", checks: "failure", want: "failing: ci"},
		{name: "failing Actions", checks: "run:failure", want: "failing: e2e"},
		{name: "pending", checks: "run:pending", want: "pending: e2e"},
		{name: "no CI", checks: "none", ok: true, want: "no CI configured"},
		{name: "workflows on pull requests, nothing reported yet", checks: "none", workflows: "on: pull_request\n", want: "pending: no checks have reported at the head yet"},
		{name: "workflows on pull requests, nothing reported past the grace", checks: "none", workflows: "on: [pull_request]\n", seen: "1 hour", ok: true, want: "no CI for this head: nothing reported within the grace"},
		{name: "workflows only on push to main", checks: "none", workflows: "on:\n  push:\n    branches: [main]\n  schedule:\n    - cron: '0 0 * * *'\n", ok: true, want: "none of which runs on pull requests"},
		{name: "an unparseable workflow, within the grace", checks: "none", workflows: "on: [\n", want: "pending: no checks have reported at the head yet"},
		{name: "an external status before Actions, within the grace", checks: "success", workflows: "on:\n  pull_request:\n", want: "Actions has not reported yet"},
		{name: "an external status before Actions, past the grace", checks: "success", workflows: "on:\n  pull_request:\n", seen: "1 hour", ok: true, want: "1 check(s) passed"},
		{name: "an external status, workflows only on push", checks: "success", workflows: "on: push\n", ok: true, want: "1 check(s) passed"},
		{name: "a required check, nothing reported", checks: "none", required: []string{"build"}, want: "branch protection requires build"},
		{name: "checks unreadable", checks: "success", forbidden: true, want: "checks unreadable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGateFixture(t)
			files := map[string]string{"app/greet.go": "package app\n"}
			if c.workflows != "" {
				files[".github/workflows/ci.yml"] = c.workflows
			}
			head := f.implemented(files)
			f.reviewed(head)
			f.simplifiedEmpty(head)
			f.synced()
			if c.seen != "" {
				f.exec(`UPDATE pull_requests SET head_seen_at = now() - $2::interval WHERE id = 'pr_g' AND organization_id = $1`, c.seen)
			} else {
				f.exec(`UPDATE pull_requests SET head_seen_at = now() WHERE id = 'pr_g' AND organization_id = $1`)
			}
			f.gh.SetChecks(c.checks)
			f.gh.Set(func(s *fakegithub.Server) {
				s.CheckRunsForbidden = c.forbidden
				if c.required != nil {
					s.RequiredChecks = map[string][]string{"main": c.required}
				}
			})
			g := f.gate()
			holds(t, g, ReadyChecks, c.ok, c.want)
			if c.ok != g.Ready {
				t.Errorf("ready %v with checks ok %v: %+v", g.Ready, c.ok, g.Items)
			}
		})
	}
}

// GitHub not connected: the GitHub items are unreadable, never ready, and
// dude's own items are still judged.
func TestTheReadyGateWithoutGitHubIsNotReady(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	gates, err := f.store.ReadyGateFor(f.ctx, f.org, nil, "wi_g", nil)
	if err != nil || len(gates) != 1 || gates[0].Ready || len(gates[0].Items) != len(ReadyKeys) {
		t.Fatalf("gates %+v (%v)", gates, err)
	}
	holds(t, gates[0], ReadyChecks, false, "unreadable")
	holds(t, gates[0], ReadyFindings, true, "no open finding")
}

// dude's push recorded with no paths ("changedPaths": null, a nil list
// marshalled) is read as none, never failing the gate's read.
func TestTheReadyGateReadsAPushRecordedWithNoPaths(t *testing.T) {
	f := newGateFixture(t)
	head := f.ready()
	f.exec(`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_nullpaths', $1, 'git.commit_created', 'run_g01', 'wi_g', 'agent', 'run_g01', 'test',
		jsonb_build_object('repo', 'repo', 'headSha', $2::text, 'changedPaths', 'null'::jsonb))`, head)
	if !f.gate().Ready {
		t.Fatalf("not ready: %+v", f.gate().Items)
	}
}

// The checks item agrees with the syncer's CI grace: on a pull request that
// has had CI, a head nothing reported on is pending while the syncer says
// pending (within its grace), and no CI for this head once it records
// unknown (past it), whatever the workflows directory holds.
func TestTheReadyGateChecksItemFollowsTheSyncersGrace(t *testing.T) {
	f := newGateFixture(t)
	f.gh.SetChecks("none")
	f.ready()
	f.exec(`UPDATE pull_requests SET had_ci = true, checks = 'pending', head_seen_at = now() - interval '1 hour'
		WHERE id = 'pr_g' AND organization_id = $1`)
	holds(t, f.gate(), ReadyChecks, false, "pending: no checks have reported at the head yet")
	f.exec(`UPDATE pull_requests SET checks = 'unknown' WHERE id = 'pr_g' AND organization_id = $1`)
	g := f.gate()
	holds(t, g, ReadyChecks, true, "no CI for this head")
	if !g.Ready {
		t.Fatalf("not ready past the grace: %+v", g.Items)
	}
}
