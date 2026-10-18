package phases

import (
	"context"
	"errors"
	"strings"
	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"testing"
	"time"
)

func TestChildRuntimeWriterFence(t *testing.T) {
	w := newResumeWorld(t)
	if err := dbtest.Reachable(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET source_kind = 'task_conductor', parent_run_id = id,
		child_profile = 'implementer', child_capabilities = '{"publishCode":true}' WHERE id = $1`, w.run.ID)
	if err := w.s.submit(w.ctx, w.run); err == nil {
		t.Fatal("writer child reached submission instead of being held")
	}
	if _, _, err := w.s.publish(w.ctx, w.run); err == nil {
		t.Fatal("writer child reached publication")
	}
}

func TestChildReplacementWriterWaitsForLivePredecessor(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status)
		VALUES('run_parent',$1,$2,$3,1,'conductor','running')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status,phase)
		VALUES('run_predecessor',$1,$2,$3,1,'implementer','running','implement')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',child_replaces='run_predecessor',
		child_profile='implementer',child_capabilities='{"publishCode":true}',phase='implement',role='implementer',status='running' WHERE id=$1`, w.run.ID)
	r := w.run
	r.SourceKind = "task_conductor"
	r.Phase = "implement"
	r.Role = "implementer"
	r.Status = "running"
	if err := w.s.childExecutionAllowed(w.ctx, r); err == nil || !strings.Contains(err.Error(), "predecessor is not terminal") {
		t.Fatal("replacement writer ran while predecessor was live")
	}
}

func TestChildParentEndCancellation(t *testing.T) {
	for _, ending := range []string{"task", "conductor"} {
		t.Run(ending, func(t *testing.T) {
			w := newResumeWorld(t)
			w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status,lux_state) VALUES('run_parent',$1,$2,$3,1,'conductor','running','terminated')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
			w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',child_profile='investigator',phase='investigate',role='investigator',child_capabilities='{}',status='running',control='none' WHERE id=$1`, w.run.ID)
			fake := w.onCallLux(nil)
			if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
				if err := delivery.LockChat(w.ctx, tx, w.run.TaskID); err != nil {
					return err
				}
				if ending == "task" {
					_, err := delivery.SetTaskStatusTx(w.ctx, tx, w.run.Org, w.run.ProjectID, w.run.TaskID, "", "done", "ended")
					return err
				}
				return delivery.EndConductor(w.ctx, tx, delivery.RunRef{Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, RunID: "run_parent"}, "ended")
			}); err != nil {
				t.Fatal(err)
			}
			if len(fake.Calls()) != 0 {
				t.Fatal("parent-end transaction called lux")
			}
			r := w.run
			r.SourceKind = "task_conductor"
			r.Status = "running"
			r.Phase = "investigate"
			r.Control = "abort"
			var reason string
			if err := w.owner.QueryRow(w.ctx, `SELECT child_stop_reason FROM runs WHERE id=$1`, r.ID).Scan(&reason); err != nil || reason != "parent-ended" {
				t.Fatalf("end marker %s %v", reason, err)
			}
			if _, err := w.s.advance(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if len(fake.Calls()) != 1 {
				t.Fatalf("cancel calls %v", fake.Calls())
			}
		})
	}
}

func TestChildCancelReleasesLocks(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}',control='abort',status='running' WHERE id=$1`, w.run.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	fake := w.onCallLux(nil)
	fake.onCancel = func() { close(entered); <-release }
	r := w.run
	r.SourceKind = "task_conductor"
	r.Status = "running"
	r.Control = "abort"
	done := make(chan error, 1)
	go func() { _, err := w.s.advance(w.ctx, r); done <- err }()
	<-entered
	probe, err := w.owner.Begin(w.ctx)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	_, probeErr := probe.Exec(w.ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE NOWAIT`, r.ID)
	_, parentErr := probe.Exec(w.ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE NOWAIT`, "run_parent")
	_, taskErr := probe.Exec(w.ctx, `SELECT id FROM tasks WHERE id=$1 FOR UPDATE NOWAIT`, r.TaskID)
	var chatOK bool
	if err := probe.QueryRow(w.ctx, `SELECT pg_try_advisory_xact_lock(hashtext('chat:'||$1))`, r.TaskID).Scan(&chatOK); err != nil {
		t.Fatal(err)
	}
	_ = probe.Rollback(w.ctx)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if probeErr != nil {
		t.Fatalf("cancel held child DB lock: %v", probeErr)
	}
	if parentErr != nil || taskErr != nil || !chatOK {
		t.Fatalf("cancel held parent/task/chat lock: %v %v %v", parentErr, taskErr, chatOK)
	}
}

type childTerminalLux struct {
	*sweepLux
	frames    []lux.Frame
	output    func(context.Context) error
	submitted []lux.Spec
	inputs    []lux.InputRequest
}

func (f *childTerminalLux) Submit(_ context.Context, spec lux.Spec, key string) (lux.Run, error) {
	f.submitted = append(f.submitted, spec)
	return lux.Run{ID: "lux_" + key, State: "running"}, nil
}
func (f *childTerminalLux) Stop(context.Context, string) error { return nil }
func (f *childTerminalLux) Input(_ context.Context, _ string, in lux.InputRequest) error {
	f.inputs = append(f.inputs, in)
	return nil
}

func (f *childTerminalLux) Output(ctx context.Context, _, _ string, _ int64, fn func(lux.Frame) error) error {
	if f.output != nil {
		return f.output(ctx)
	}
	for _, frame := range f.frames {
		if err := fn(frame); err != nil {
			return err
		}
	}
	return nil
}

func TestChildTerminalDrainBound(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}',status='completed',ended_at=now(),artifacts_due_at=now()-interval '2 days',artifacts_next_at=now() WHERE id=$1`, w.run.ID)
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	bounded := make(chan bool, 1)
	fake := &childTerminalLux{sweepLux: &sweepLux{}, output: func(ctx context.Context) error {
		_, ok := ctx.Deadline()
		bounded <- ok
		if ok {
			return context.DeadlineExceeded
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { _, err := (&Artifacts{DB: w.s.DB, Lux: fake, Log: w.s.Log}).Sweep(ctx); done <- err }()
	if !<-bounded {
		cancel()
		<-done
		t.Fatal("terminal drain has no independent deadline")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("drain error %v", err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("bounded drain hung artifact sweep")
	}
	var pending bool
	if err := w.owner.QueryRow(w.ctx, `SELECT artifacts_due_at IS NOT NULL FROM runs WHERE id=$1`, w.run.ID).Scan(&pending); err != nil || !pending {
		t.Fatalf("unfinished drain lost pending exit: %v %v", pending, err)
	}
}

func TestChildTerminalGoneOutput(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}',status='completed',ended_at=now() WHERE id=$1`, w.run.ID)
	fake := &childTerminalLux{sweepLux: &sweepLux{}, output: func(context.Context) error { return &lux.Error{Status: 404, Code: "not_found"} }}
	if _, err := (&Artifacts{DB: w.s.DB, Lux: fake, Log: w.s.Log}).Sweep(w.ctx); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := w.owner.QueryRow(w.ctx, `SELECT artifacts_due_at IS NOT NULL FROM runs WHERE id=$1`, w.run.ID).Scan(&pending); err != nil || pending {
		t.Fatalf("gone output did not settle: %v %v", pending, err)
	}
}

func TestChildOldFollowerCannotAppendAfterDrain(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}',phase='investigate',role='investigator' WHERE id=$1`, w.run.ID)
	oldOpened, release := make(chan struct{}), make(chan struct{})
	old := newStreamLux(w.lux)
	oldSync := &Syncer{DB: w.s.DB, Lux: old, Log: w.s.Log}
	r := w.run
	r.SourceKind = "task_conductor"
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	oldDone := make(chan error, 1)
	go func() { close(oldOpened); oldDone <- oldSync.followOutput(ctx, r) }()
	<-oldOpened
	<-old.opened
	fresh := &childTerminalLux{sweepLux: &sweepLux{}, frames: []lux.Frame{spoke(1), record(1, "lux.activity", map[string]any{"activity": "idle"})}}
	newSync := &Syncer{DB: w.s.DB, Lux: fresh, Log: w.s.Log}
	if err := newSync.followOutput(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	go func() { old.frames <- cursorFrame(spoke(1), "stale"); close(release) }()
	<-release
	// EOF is deliberately absent from the old stream; its stale batch must end it.
	select {
	case err := <-oldDone:
		if err == nil {
			t.Fatal("stale follower succeeded")
		}
	case <-time.After(time.Second):
		cancel()
		<-oldDone
		t.Fatal("stale follower remained active after replacement")
	}
	var cursor string
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(lux_cursor,'') FROM runs WHERE id=$1`, r.ID).Scan(&cursor); err != nil || cursor == "stale" {
		t.Fatalf("stale cursor committed %q %v", cursor, err)
	}
}
func (f *childTerminalLux) Cost(context.Context, string) (lux.RunCost, error) {
	return lux.RunCost{}, &lux.Error{Status: 404, Code: "not_found"}
}

func TestChildTerminalCollection(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('run_parent',$1,$2,$3,1,'conductor','running')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_terminal',$1,$2,'task.delivery','terminal','waiting','awaitPullRequest','{"attempt":1}')`, w.run.Org, w.run.TaskID)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',phase='investigate',role='investigator',child_profile='investigator',child_capabilities='{}',status='completed',ended_at=now() WHERE id=$1`, w.run.ID)
	store := &delivery.Store{DB: w.s.DB}
	if n, err := settleCollectedChildren(w.ctx, store); err != nil || n != 0 {
		t.Fatalf("settled before late collection %d %v", n, err)
	}
	fake := &childTerminalLux{sweepLux: &sweepLux{listed: []lux.Artifact{{ID: "art_child_terminal", Path: lux.PublishedPrefix + "report.md", Available: true, Version: 1, Epoch: 1, SHA256: "abc", Size: 10}}}, frames: []lux.Frame{spoke(1), record(1, "lux.activity", map[string]any{"activity": "idle"})}}
	a := &Artifacts{DB: w.s.DB, Lux: fake, Log: w.s.Log}
	if n, err := a.Sweep(w.ctx); err != nil || n != 1 {
		t.Fatalf("collect %d %v", n, err)
	}
	if n, err := settleCollectedChildren(w.ctx, store); err != nil || n != 1 {
		t.Fatalf("settlement %d %v", n, err)
	}
	var artifacts []string
	if err := w.owner.QueryRow(w.ctx, `SELECT artifact_ids FROM child_results WHERE child_run_id=$1`, w.run.ID).Scan(&artifacts); err != nil || len(artifacts) != 1 {
		t.Fatalf("late artifact %v %v", artifacts, err)
	}
	var reply string
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) (err error) { reply, err = delivery.RunReplyTx(w.ctx, tx, w.run.ID); return err }); err != nil || !strings.Contains(reply, "On it.") {
		t.Fatalf("late report %q %v", reply, err)
	}
	if n, err := store.EnqueueChildResults(w.ctx); err != nil || n != 1 {
		t.Fatalf("parent directive %d %v", n, err)
	}
}

func TestChildStitchedRoleRoundTrip(t *testing.T) {
	for _, profile := range []string{"reviewer", "qa_browser"} {
		t.Run(profile, func(t *testing.T) {
			w := newResumeWorld(t)
			w.exec(`UPDATE tasks SET status='running',acceptance_criteria='["It works"]' WHERE id=$1`, w.run.TaskID)
			w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('run_round_parent',$1,$2,$3,1,'conductor','running')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
			w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_round',$1,$2,'task.delivery','round','waiting','awaitPullRequest',jsonb_build_object('attempt',1,'taskId',$2::text,'projectId',$3::text))`, w.run.Org, w.run.TaskID, w.run.ProjectID)
			w.exec(`UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org)
			w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object($2::text,jsonb_build_object('tier',(SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org, profile)
			store := &delivery.Store{DB: w.s.DB}
			id, err := store.StartChild(w.ctx, delivery.StartChildInput{Parent: delivery.ChildParent{Kind: delivery.ChildParentTaskConductor, Run: delivery.RunRef{Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, RunID: "run_round_parent"}}, Profile: profile, Key: "round"})
			if err != nil {
				t.Fatal(err)
			}
			fake := &childTerminalLux{sweepLux: &sweepLux{}}
			w.s.Lux = fake
			w.s.Agent.DefaultImage = "test/image:latest"
			w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
			r := phaseRun{ID: id, Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, Phase: delivery.PhaseForPromptRole(profile), Role: delivery.RoleForPhase[delivery.PhaseForPromptRole(profile)], SourceKind: "task_conductor", Status: "pending", Control: "none", Attempt: 1}
			if _, err := w.s.advance(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if len(fake.submitted) != 1 {
				t.Fatalf("dispatcher submissions %d", len(fake.submitted))
			}
			r.LuxRunID = "lux_" + id
			r.LuxState = "running"
			r.Status = "running"
			w.exec(`UPDATE runs SET status='running' WHERE id=$1`, id)
			report := "```yaml\nseverity: high\ncategory: correctness\ntitle: Reported defect\ndescription: Reproduced defect\n```"
			if profile == "qa_browser" {
				report = "```yaml\ncriterion: 1\nstatus: pass\nevidence: evidence.txt\nobserved: It works\n```"
			}
			frame := spoke(1)
			frame = record(1, "acp.agent_message_chunk", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": report}})
			fake.frames = []lux.Frame{frame, record(1, "lux.activity", map[string]any{"activity": "idle"})}
			if err := w.s.followOutput(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if _, err := w.s.finish(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if n, err := settleCollectedChildren(w.ctx, store); err != nil || n != 0 {
				t.Fatalf("early settlement %d %v", n, err)
			}
			fake.frames = nil
			fake.listed = []lux.Artifact{{ID: "art_round", Path: lux.PublishedPrefix + "evidence.txt", Available: true, Epoch: 1, Version: 1, SHA256: "abc", Size: 3}}
			if _, err := (&Artifacts{DB: w.s.DB, Lux: fake, Log: w.s.Log}).Sweep(w.ctx); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				n   int
				err error
			}
			settled := make(chan outcome, 2)
			for i := 0; i < 2; i++ {
				go func() {
					n, err := NotifyFinished(w.ctx, w.s.DB, func(context.Context, string, string, string, string, string) error {
						return errors.New("child reached workflow notifier")
					})
					settled <- outcome{n, err}
				}()
			}
			wins := 0
			for i := 0; i < 2; i++ {
				result := <-settled
				if result.err != nil {
					t.Fatal(result.err)
				}
				wins += result.n
			}
			if wins != 1 {
				t.Fatalf("settlement winners %d", wins)
			}
			var findings, verdicts int
			if err := w.owner.QueryRow(w.ctx, `SELECT findings,verdicts FROM child_results WHERE child_run_id=$1`, id).Scan(&findings, &verdicts); err != nil {
				t.Fatal(err)
			}
			if profile == "reviewer" && findings != 1 || profile == "qa_browser" && verdicts != 1 {
				t.Fatalf("role results %d/%d", findings, verdicts)
			}
			for i := 0; i < 2; i++ {
				go func() {
					worker := &Syncer{DB: w.s.DB}
					n, err := (&delivery.Store{DB: worker.DB}).EnqueueChildResults(w.ctx)
					settled <- outcome{n, err}
				}()
			}
			wins = 0
			for i := 0; i < 2; i++ {
				result := <-settled
				if result.err != nil {
					t.Fatal(result.err)
				}
				wins += result.n
			}
			if wins != 1 {
				t.Fatalf("enqueue winners %d", wins)
			}
			var directive string
			if err := w.owner.QueryRow(w.ctx, `SELECT directive_id FROM child_result_outbox WHERE child_run_id=$1`, id).Scan(&directive); err != nil {
				t.Fatal(err)
			}
			parent := phaseRun{ID: "run_round_parent", Org: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, Role: "conductor", Status: "running", LuxState: "running", LuxRunID: "lux_parent"}
			// The parent is mid-turn; a restarted sender uses the already queued
			// directive, which lux accepts for the next step, not an interrupt.
			sender := &Syncer{DB: w.s.DB, Lux: fake, Log: w.s.Log}
			if _, err := sender.deliverDirectives(w.ctx, parent); err != nil {
				t.Fatal(err)
			}
			if len(fake.inputs) != 1 {
				t.Fatalf("parent inputs %d", len(fake.inputs))
			}
			if _, err := sender.deliverDirectives(w.ctx, parent); err != nil {
				t.Fatal(err)
			}
			if len(fake.inputs) != 1 {
				t.Fatal("restart replay sent another directive")
			}
			tr := &translator{run: parent}
			if err := w.s.DB.InOrg(w.ctx, r.Org, func(tx pgx.Tx) error {
				typ, data := accepted(directive, true, "next_step")
				if err := tr.shimEvent(w.ctx, tx, w.s, typ, data, 1); err != nil {
					return err
				}
				typ, data = consumed(directive)
				return tr.shimEvent(w.ctx, tx, w.s, typ, data, 1)
			}); err != nil {
				t.Fatal(err)
			}
			var delivered bool
			if err := w.owner.QueryRow(w.ctx, `SELECT delivered_at IS NOT NULL FROM directives WHERE id=$1`, directive).Scan(&delivered); err != nil || !delivered {
				t.Fatalf("receipt %v %v", delivered, err)
			}
		})
	}
}

func TestChildReportSpec(t *testing.T) {
	for _, profile := range []string{"investigator", "reviewer", "qa_browser"} {
		t.Run(profile, func(t *testing.T) {
			w := newResumeWorld(t)
			w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
			w.s.Agent.ToolsURL = "http://tools.test"
			w.s.Agent.ToolsService = true
			phase := delivery.PhaseForPromptRole(profile)
			w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,phase=$2::run_phase,role=$3::agent_role,
			child_profile=$4,child_capabilities='{}',child_prompt='Investigate the exact regression.',lux_run_id=NULL WHERE id=$1`, w.run.ID, phase, delivery.RoleForPhase[phase], profile)
			w.exec(`UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org)
			w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object($2::text,jsonb_build_object('tier',(SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org, profile)
			r := w.run
			r.SourceKind = "task_conductor"
			r.Phase = phase
			r.Role = delivery.RoleForPhase[phase]
			r.Attempt = 1
			r.LuxRunID = ""
			spec, _, _, _, err := w.s.spec(w.ctx, r, nil, chosenImage{Ref: "test/image:latest"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(spec.Workload.Prompt, "Investigate the exact regression.") {
				t.Fatalf("override absent: %s", spec.Workload.Prompt)
			}
			if spec.Git != nil && spec.Git.Push != nil {
				t.Fatal("report child got push authority")
			}
			for _, forbidden := range []string{"ask_person", "request_repository", "request_machine", "create_task"} {
				if strings.Contains(spec.Workload.Prompt, forbidden) {
					t.Fatalf("child prompt advertises %s", forbidden)
				}
			}
		})
	}
}

func TestChildRuntimeIsolation(t *testing.T) {
	for _, action := range []string{"finish", "fail", "idle", "resume", "abort"} {
		t.Run(action, func(t *testing.T) {
			w := newResumeWorld(t)
			w.exec(`UPDATE tasks SET status='running' WHERE id=$1`, w.run.TaskID)
			w.exec(`INSERT INTO runs (id,organization_id,project_id,task_id,attempt,role,status) VALUES ('run_parent',$1,$2,$3,1,'conductor','running')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
			w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_child',$1,$2,'task.delivery','child','waiting','awaitPullRequest',jsonb_build_object('taskId',$2::text,'projectId',$3::text,'attempt',1,'heads',jsonb_build_object('repo','preserved'),'pendingRunIds',jsonb_build_array($4::text)))`, w.run.Org, w.run.TaskID, w.run.ProjectID, w.run.ID)
			w.exec(`INSERT INTO repositories(id,organization_id,project_id,name,url) VALUES('repo_isolation',$1,$2,'repo','https://github.com/test/repo')`, w.run.Org, w.run.ProjectID)
			w.exec(`INSERT INTO pull_requests(id,organization_id,project_id,task_id,repository_id,number,url,head_branch,base_branch,head_sha,title,checks,review) VALUES('pr_isolation',$1,$2,$3,'repo_isolation',1,'https://github.com/test/repo/pull/1','work','main','preserved','PR','passing','approved')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
			w.exec(`UPDATE workflow_runs SET state=state || '{"headRunId":"run_parent","reviewRunIds":["run_parent"],"pullRequestIds":["pr_isolation"]}' WHERE id='wf_child'`)
			w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',phase='investigate',role='investigator',child_profile='investigator',child_capabilities='{}',status='running',control='none',lux_state='running',lux_stop_reason=NULL,turn_done_at=now() WHERE id=$1`, w.run.ID)
			r := w.run
			r.SourceKind = "task_conductor"
			r.Phase = "investigate"
			r.Role = "investigator"
			r.Status = "running"
			r.LuxState = "running"
			r.DudePause = ""
			r.TurnDone = true
			fake := w.onCallLux(nil)
			snapshot := func() string {
				var raw string
				if err := w.owner.QueryRow(w.ctx, `SELECT jsonb_build_object('t',to_jsonb(t),'w',to_jsonb(w),'pr',(SELECT to_jsonb(p) FROM pull_requests p WHERE id='pr_isolation'))::text FROM tasks t JOIN workflow_runs w ON w.task_id=t.id WHERE t.id=$1`, r.TaskID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			switch action {
			case "finish":
				if _, err := w.s.finish(w.ctx, r); err != nil {
					t.Fatal(err)
				}
			case "fail":
				if err := w.s.fail(w.ctx, r, "child failure"); err != nil {
					t.Fatal(err)
				}
			case "idle":
				w.exec(`UPDATE runs SET turn_done_at=NULL,agent_active_at=now() WHERE id=$1`, r.ID)
				if err := w.owner.QueryRow(w.ctx, `SELECT agent_active_at FROM runs WHERE id=$1`, r.ID).Scan(&r.QuietSince); err != nil {
					t.Fatal(err)
				}
				if err := w.s.requestPause(w.ctx, r, "idle", "quiet"); err != nil {
					t.Fatal(err)
				}
			case "resume":
				w.exec(`UPDATE runs SET turn_done_at=NULL,agent_active_at=now() WHERE id=$1`, r.ID)
				if err := w.owner.QueryRow(w.ctx, `SELECT agent_active_at FROM runs WHERE id=$1`, r.ID).Scan(&r.QuietSince); err != nil {
					t.Fatal(err)
				}
				if err := w.s.requestPause(w.ctx, r, "idle", "quiet"); err != nil {
					t.Fatal(err)
				}
				w.exec(`UPDATE runs SET status='paused',control='resume',dude_pause='idle',lux_state='stopped',model='gpt-6-sol',harness='opencode' WHERE id=$1`, r.ID)
				r.Status = "paused"
				r.Control = "resume"
				r.DudePause = "idle"
				r.Resumable = true
				r.LuxState = "stopped"
				w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
				w.s.Lux = &childResumeLux{callLux: fake}
				if _, err := w.s.whilePaused(w.ctx, r); err != nil {
					t.Fatal(err)
				}
				var status string
				if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM runs WHERE id=$1`, r.ID).Scan(&status); err != nil || status != "running" {
					t.Fatalf("child resume %s %v", status, err)
				}
			case "abort":
				if err := (&delivery.Store{DB: w.s.DB}).AbortChild(w.ctx, r.Org, r.TaskID, r.ID); err != nil {
					t.Fatal(err)
				}
				r.Control = "abort"
				if _, err := w.s.advance(w.ctx, r); err != nil {
					t.Fatal(err)
				}
				if len(fake.Calls()) != 1 {
					t.Fatalf("cancellation calls %v", fake.Calls())
				}
			}
			if snapshot() != before {
				t.Fatal("child mutated task/workflow")
			}
			if action != "idle" && action != "resume" {
				signals := 0
				if _, err := NotifyFinished(w.ctx, w.s.DB, func(_ context.Context, _, _, _, _, _ string) error { signals++; return nil }); err != nil {
					t.Fatal(err)
				}
				if signals != 0 {
					t.Fatal("child signalled workflow")
				}
				if err := w.s.DB.InOrg(w.ctx, r.Org, func(tx pgx.Tx) error {
					var n int
					return tx.QueryRow(w.ctx, `SELECT count(*) FROM child_results`).Scan(&n)
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type childResumeLux struct{ *callLux }

func (f *childResumeLux) Get(context.Context, string) (lux.Run, error) {
	return lux.Run{ID: "lrun_1", State: "stopped", Epoch: 1, Spec: lux.StoredSpec{Image: lux.Image{Ref: "test/image:latest"}}}, nil
}
func (f *childResumeLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	return lux.Run{ID: "lrun_1", State: "resuming", Epoch: 2}, nil
}
