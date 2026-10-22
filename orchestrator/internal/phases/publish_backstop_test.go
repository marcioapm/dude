package phases

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

const backstopRefusal = "an open pull request's branch moves only by promotion"

// backstop is a writer Run of w whose push of one commit to "target" has
// landed, finished against a fake GitHub over a; the task branch exists
// on GitHub at the commit the Run started from.
type backstop struct {
	w      *resumeWorld
	a      *accountingRun
	gh     *fakegithub.Server
	branch string
}

func newBackstop(t *testing.T, w *resumeWorld) *backstop {
	t.Helper()
	a := newAccountingRun(t)
	a.commit(map[string]string{"fix.txt": "fixed\n"}, "Fix")
	a.push()
	branch := delivery.BranchFor(w.run.TaskID, 1)
	git(t, a.work, "push", "-q", a.origin, a.start+":refs/heads/"+branch)
	fake := fakegithub.New(a.origin, "acme/target")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_x", APIBaseURL: srv.URL})
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return gh, nil })
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_' || $1, $1, $2, 'target', 'https://github.com/acme/target.git', 'main')
		ON CONFLICT (id) DO UPDATE SET url = EXCLUDED.url, default_branch = 'main'`, w.run.Org, w.run.ProjectID)
	w.exec(`INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, 'repo_' || $1, 'write')`, w.run.Org, w.run.TaskID)
	return &backstop{w: w, a: a, gh: fake, branch: branch}
}

// pullRequest records a pull request in state on head, as the pull
// request syncer would.
func (b *backstop) pullRequest(head, state string) {
	b.w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_' || $1, $1, $2, $3, 'repo_' || $1, 1, 'https://github.com/acme/target/pull/1', $4, 'main', $5, 'T', $6::pull_request_state)`,
		b.w.run.Org, b.w.run.ProjectID, b.w.run.TaskID, head, b.a.start, state)
}

// finish is the Run's push result landing: finish publishes it.
func (b *backstop) finish(r phaseRun) {
	b.w.t.Helper()
	r.Attempt, r.Status = 1, statusRunning
	r.PushBranch, r.HoldsPushable = "dude/run", true
	r.BaseSHAs = map[string]string{"target": b.a.start}
	r.PushResult, _ = json.Marshal(map[string]any{"results": []map[string]string{{"repo": "target", "branch": "dude/run", "commit": b.a.head(), "status": "pushed"}}})
	if _, err := b.w.s.finish(b.w.ctx, r); err != nil {
		b.w.t.Fatal(err)
	}
}

func (b *backstop) outcome() (status, reason string) {
	b.w.t.Helper()
	if err := b.w.owner.QueryRow(b.w.ctx, `SELECT status::text, COALESCE(error, '') FROM runs WHERE id = $1`, b.w.run.ID).Scan(&status, &reason); err != nil {
		b.w.t.Fatal(err)
	}
	return status, reason
}

// refused asserts the Run failed with the backstop's reason and that
// GitHub never moved branch.
func (b *backstop) refused(branch string) {
	b.w.t.Helper()
	if status, reason := b.outcome(); status != "failed" || !strings.Contains(reason, backstopRefusal) {
		b.w.t.Errorf("the Run: %s %q, want failed saying %q", status, reason, backstopRefusal)
	}
	if got := b.gh.BranchLog(branch); len(got) != 0 {
		b.w.t.Errorf("GitHub moved %s to %v", branch, got)
	}
	if got := b.gh.SHA(branch); got != b.a.start {
		b.w.t.Errorf("%s is at %s, want %s where it was", branch, got, b.a.start)
	}
}

// A writer Run still targeting a task branch whose pull request is open
// (or a draft) fails at publish and moves nothing: as a workflow fix
// (runs.branch NULL, the task branch resolved) and as a conductor's child
// (runs.branch set to the task branch).
func TestAPublishToAnOpenPullRequestsBranchFailsTheRun(t *testing.T) {
	for _, state := range []string{"open", "draft"} {
		t.Run("fix/"+state, func(t *testing.T) {
			w := newResumeWorld(t)
			b := newBackstop(t, w)
			w.exec(`UPDATE runs SET phase = 'fix', role = 'implementer', status = 'running', branch = NULL, lux_run_id = NULL WHERE id = $1`, w.run.ID)
			b.pullRequest(b.branch, state)
			r := w.run
			r.Phase, r.Role, r.LuxRunID, r.LuxState = delivery.PhaseFix, "implementer", "", ""
			b.finish(r)
			b.refused(b.branch)
		})
		t.Run("own-branch/"+state, func(t *testing.T) {
			w := newResumeWorld(t)
			b := newBackstop(t, w)
			w.exec(`UPDATE runs SET phase = 'implement', role = 'implementer', status = 'running', branch = $2, lux_run_id = NULL WHERE id = $1`, w.run.ID, b.branch)
			b.pullRequest(b.branch, state)
			r := w.run
			r.Phase, r.Role, r.LuxRunID, r.LuxState = delivery.PhaseImplement, "implementer", "", ""
			b.finish(r)
			b.refused(b.branch)
		})
	}
}

// Controls: a candidate Run publishes to its candidate while the pull
// request is open, and a task branch whose pull request was closed or
// merged is moved as before.
func TestAPublishBesideAnOpenPullRequestMovesItsBranch(t *testing.T) {
	cases := []struct{ name, own, state string }{
		{"candidate", "-candidate", "open"},
		{"closed", "", "closed"},
		{"merged", "", "merged"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			b := newBackstop(t, w)
			target := b.branch + c.own
			own := any(nil)
			if c.own != "" {
				own = target
			}
			w.exec(`UPDATE runs SET phase = 'fix', role = 'implementer', status = 'running', branch = $2, lux_run_id = NULL WHERE id = $1`, w.run.ID, own)
			b.pullRequest(b.branch, c.state)
			r := w.run
			r.Phase, r.Role, r.LuxRunID, r.LuxState = delivery.PhaseFix, "implementer", "", ""
			b.finish(r)
			if status, reason := b.outcome(); status != "completed" {
				t.Fatalf("the Run: %s %q, want completed", status, reason)
			}
			if got := b.gh.BranchLog(target); len(got) != 1 || got[0] != b.a.head() {
				t.Errorf("%s moved to %v, want [%s]", target, got, b.a.head())
			}
			if c.own != "" && len(b.gh.BranchLog(b.branch)) != 0 {
				t.Errorf("the pull request's branch moved: %v", b.gh.BranchLog(b.branch))
			}
		})
	}
}

// A conductor's writer child admitted and started while no pull request
// was open, whose push lands after one opened on the task branch, fails at
// publish: the pull request's branch does not move, and its conductor's
// child result says why.
func TestAWriterChildWhosePushLandsAfterAPullRequestOpenedIsRefused(t *testing.T) {
	w := newResumeWorld(t)
	fake := fakelux.New("", "k", func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(fake.Close)
	w.s.Lux = lux.New(srv.URL, "k")
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
	w.s.Agent.DefaultImage = "test/image:latest"
	w.exec(`UPDATE organizations SET default_image_id=NULL WHERE id=$1`, w.run.Org)
	w.exec(`UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org)
	w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object('implementer', jsonb_build_object('tier', (SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status,kind)
		VALUES('run_parent',$1,$2,$3,1,'conductor','running','agent')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`UPDATE tasks SET status='review' WHERE id=$1`, w.run.TaskID)
	w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES('wf_child_pr',$1,$2,'task.delivery','child-pr','waiting','awaitPullRequest',
		('{"attempt":1,"taskId":"' || $2 || '","projectId":"' || $3 || '","branch":"dude/' || $2 || '/attempt-1"}')::jsonb)`,
		w.run.Org, w.run.TaskID, w.run.ProjectID)
	w.exec(`UPDATE runs SET source_kind='task_conductor', parent_run_id='run_parent', child_profile='implementer',
		child_capabilities='{"publishCode":true}', phase='implement', role='implementer', status='pending', control='none',
		lux_run_id=NULL, lux_state=NULL, dude_pause=NULL, branch='dude/' || task_id || '/attempt-1' WHERE id=$1`, w.run.ID)
	r := w.run
	r.SourceKind, r.Phase, r.Role = string(delivery.ChildParentTaskConductor), delivery.PhaseImplement, "implementer"
	r.Status, r.LuxRunID, r.LuxState, r.DudePause = statusPending, "", "", ""
	// Admitted and submitted with no pull request open: it reaches lux.
	if err := w.s.submit(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(fake.Runs()) != 1 {
		status, reason := (&backstop{w: w}).outcome()
		t.Fatalf("with no pull request open: %d lux Runs, status %s %q", len(fake.Runs()), status, reason)
	}
	// The pull request opens while it works; then its push lands.
	b := newBackstop(t, w)
	b.pullRequest(b.branch, "open")
	w.exec(`UPDATE runs SET status='running' WHERE id=$1`, r.ID)
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(lux_run_id, '') FROM runs WHERE id=$1`, r.ID).Scan(&r.LuxRunID); err != nil {
		t.Fatal(err)
	}
	r.LuxState = "running"
	b.finish(r)
	b.refused(b.branch)

	// Its exit collected by the artifact sweep, with nothing to collect.
	w.exec(`UPDATE runs SET artifacts_due_at = NULL, artifacts_next_at = NULL WHERE id = $1`, r.ID)
	store := &delivery.Store{DB: w.s.DB}
	if n, err := settleCollectedChildren(w.ctx, store); err != nil || n != 1 {
		t.Fatalf("settled %d %v", n, err)
	}
	if n, err := store.EnqueueChildResults(w.ctx); err != nil || n != 1 {
		t.Fatalf("delivered %d %v", n, err)
	}
	var text string
	if err := w.owner.QueryRow(w.ctx, `SELECT d.text FROM child_result_outbox o JOIN directives d ON d.id = o.directive_id
		WHERE o.child_run_id = $1`, r.ID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"status": "failed"`) || !strings.Contains(text, backstopRefusal) {
		t.Errorf("the conductor's result: %s", text)
	}
}
