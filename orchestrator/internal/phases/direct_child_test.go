package phases

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// In a delivery whose fixes reach its pull request directly, a writer
// child admitted earlier and only now due to start fails at submit while
// the pull request shows a head dude pushed that no review has covered;
// once a review started from that head, it starts.
func TestDirectAWriterChildIsRefusedAtSubmitWhileThePullRequestsLastPushIsUnreviewed(t *testing.T) {
	for _, reviewed := range []bool{false, true} {
		name := "unreviewed"
		if reviewed {
			name = "reviewed"
		}
		t.Run(name, func(t *testing.T) {
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
				('{"attempt":1,"taskId":"' || $2 || '","projectId":"' || $3 || '","branch":"dude/' || $2 || '/attempt-1","policy":{"pullRequestFixes":"direct"}}')::jsonb)`,
				w.run.Org, w.run.TaskID, w.run.ProjectID)
			w.exec(`UPDATE runs SET source_kind='task_conductor', parent_run_id='run_parent', child_profile='implementer',
				child_capabilities='{"publishCode":true}', phase='implement', role='implementer', status='pending', control='none',
				lux_run_id=NULL, lux_state=NULL, dude_pause=NULL WHERE id=$1`, w.run.ID)
			w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_pr', $1, $2, 'target', 'https://github.com/acme/target')`,
				w.run.Org, w.run.ProjectID)
			w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
				VALUES ('pr_child', $1, $2, $3, 'repo_pr', 1, 'https://github.com/acme/target/pull/1', 'dude/' || $3 || '/attempt-1', 'main', 'aaa', 'T', 'open')`,
				w.run.Org, w.run.ProjectID, w.run.TaskID)
			// dude pushed aaa: a fix's publish.
			w.exec(`INSERT INTO events (id, organization_id, project_id, task_id, event_type, actor_type, actor_id, source, payload)
				VALUES ('evt_pushed', $1, $2, $3, 'git.commit_created', 'agent', 'run_x', 'orchestrator', '{"repo":"target","headSha":"aaa"}')`,
				w.run.Org, w.run.ProjectID, w.run.TaskID)
			if reviewed {
				w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status,phase,base_refs)
					VALUES('run_review',$1,$2,$3,1,'reviewer','completed','review','{"target":"aaa"}')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
			}
			r := w.run
			r.SourceKind, r.Phase, r.Role = string(delivery.ChildParentTaskConductor), delivery.PhaseImplement, "implementer"
			r.Status, r.LuxRunID, r.LuxState, r.DudePause = statusPending, "", "", ""
			if err := w.s.submit(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			var status, reason string
			if err := w.owner.QueryRow(w.ctx, `SELECT status::text, COALESCE(error, '') FROM runs WHERE id=$1`, r.ID).Scan(&status, &reason); err != nil {
				t.Fatal(err)
			}
			if reviewed {
				if len(fake.Runs()) != 1 {
					t.Fatalf("reviewed: %d lux Runs, status %s %q", len(fake.Runs()), status, reason)
				}
				return
			}
			if len(fake.Runs()) != 0 || status != "failed" || !strings.Contains(reason, "no review has covered yet") || !strings.Contains(reason, "start_phase review") {
				t.Errorf("the writer child: %s %q, %d lux Runs; want failed saying the push is unreviewed", status, reason, len(fake.Runs()))
			}
		})
	}
}
