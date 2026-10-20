package phases

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSessionChildSubmitDoesNotRecordProjectSecrets(t *testing.T) {
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
	w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object('implementer', jsonb_build_object('tier', (SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1)), 'investigator', jsonb_build_object('tier', (SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org)

	w.exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
		VALUES ($1, $2, 'SUBMIT_SECRET', 'submit-secret-value', 'test', '{implementer}')`, w.run.ProjectID, w.run.Org)
	w.exec(`UPDATE runs SET status='pending', lux_run_id=NULL, lux_state=NULL, phase='implement', role='implementer', source_kind=NULL WHERE id=$1`, w.run.ID)
	task := w.run
	task.Status, task.LuxRunID, task.LuxState = statusPending, "", ""
	task.Phase, task.Role, task.SourceKind = delivery.PhaseImplement, "implementer", ""
	if err := w.s.submit(w.ctx, task); err != nil {
		var reason string
		_ = w.owner.QueryRow(w.ctx, `SELECT coalesce(error, '') FROM runs WHERE id=$1`, task.ID).Scan(&reason)
		t.Fatalf("task submit: %v reason=%q", err, reason)
	}
	var taskState, taskLux, taskError string
	if err := w.owner.QueryRow(w.ctx, `SELECT status::text, coalesce(lux_run_id, ''), coalesce(error, '') FROM runs WHERE id=$1`, task.ID).Scan(&taskState, &taskLux, &taskError); err != nil {
		t.Fatal(err)
	}
	if len(fake.Runs()) != 1 {
		t.Fatalf("submitted runs: %d db status=%s lux=%s error=%s", len(fake.Runs()), taskState, taskLux, taskError)
	}
	var taskSpec lux.Spec
	if err := json.Unmarshal(fake.Runs()[0].Spec, &taskSpec); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(taskSpec.Secrets, func(s lux.Secret) bool { return s.Name == "SUBMIT_SECRET" && s.Value != "" }) {
		t.Fatalf("task submit did not carry the configured secret")
	}
	var rows int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM run_project_secrets WHERE run_id=$1`, task.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("task secret rows: %d %v", rows, err)
	}

	old := secretRecipient["investigator"]
	secretRecipient["investigator"] = "implementer"
	t.Cleanup(func() { secretRecipient["investigator"] = old })
	w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_submit_child', $1, 'Research')`, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role)
		VALUES ('run_brainstorm_submit', $1, 'ssn_submit_child', 1, 'running', 'agent', 'brainstorm')`, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, session_id, attempt, status, phase, role, source_kind,
		parent_run_id, parent_session_id, child_profile, child_capabilities, child_prompt)
		VALUES ('run_submit_child', $1, $2, 'ssn_submit_child', 1, 'pending', 'investigate', 'investigator', 'session',
		'run_brainstorm_submit', 'ssn_submit_child', 'investigator', '{}'::jsonb, 'read')`, w.run.Org, w.run.ProjectID)
	child := phaseRun{ID: "run_submit_child", Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, SessionID: "ssn_submit_child",
		Phase: delivery.PhaseInvestigate, Role: "investigator", SourceKind: string(delivery.ChildParentSession), Status: statusPending}
	if err := w.s.submit(w.ctx, child); err != nil {
		t.Fatal(err)
	}
	var childState, childError string
	if err := w.owner.QueryRow(w.ctx, `SELECT status::text, coalesce(error, '') FROM runs WHERE id=$1`, child.ID).Scan(&childState, &childError); err != nil {
		t.Fatal(err)
	}
	if len(fake.Runs()) != 2 {
		t.Fatalf("submitted runs after child: %d db status=%s error=%s", len(fake.Runs()), childState, childError)
	}
	var childSpec lux.Spec
	if err := json.Unmarshal(fake.Runs()[1].Spec, &childSpec); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(childSpec.Secrets, func(s lux.Secret) bool { return s.Name == "SUBMIT_SECRET" || s.Value != "" }) {
		t.Fatal("session child submit carried a project secret")
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM run_project_secrets WHERE run_id=$1`, child.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("session child secret rows: %d %v", rows, err)
	}
}

func childInstructions(t *testing.T, spec lux.Spec) string {
	t.Helper()
	if spec.Init == nil {
		t.Fatal("child spec has no instruction init")
	}
	dir := t.TempDir()
	script := strings.ReplaceAll(spec.Init.Script, "/workspace", dir)
	runInit(t, dir, script)
	agents, _ := readBack(t, dir)
	return agents
}

func assertChildContract(t *testing.T, agents string) {
	t.Helper()
	if !strings.Contains(agents, "## Independent Report Child\n") {
		t.Fatal("child instruction file lacks independent-child contract")
	}
	allowed := map[string]bool{}
	for _, tool := range delivery.ChildTools {
		allowed[tool] = true
	}
	forbiddenTools := map[string]bool{}
	for _, forbidden := range delivery.InstructionTools {
		if !allowed[forbidden] {
			forbiddenTools[forbidden] = true
		}
	}
	var forbiddenNames []string
	for name := range forbiddenTools {
		forbiddenNames = append(forbiddenNames, name)
	}
	slices.Sort(forbiddenNames)
	for _, forbidden := range forbiddenNames {
		if strings.Contains(agents, forbidden) {
			t.Errorf("child instruction file advertises %s", forbidden)
		}
	}
}

func TestChildRuntimeWriterFence(t *testing.T) {
	w := newResumeWorld(t)
	if err := dbtest.Reachable(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status,kind,source_kind)
		VALUES('run_parent',$1,$2,$3,1,'conductor','running','agent',NULL)`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`UPDATE tasks SET status='running' WHERE id=$1`, w.run.TaskID)
	w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES('wf_child_fence',$1,$2,'task.delivery','child-fence','waiting','awaitPullRequest',
		('{"attempt":1,"taskId":"' || $2 || '","projectId":"' || $3 || '"}')::jsonb)`, w.run.Org, w.run.TaskID, w.run.ProjectID)
	w.exec(`UPDATE runs SET source_kind = 'task_conductor', parent_run_id = 'run_parent',
		child_profile = 'implementer', child_capabilities = '{"publishCode":true}', phase='implement',
		role='implementer', status='running', control='none', child_stop_reason='parent-ended' WHERE id = $1`, w.run.ID)
	r := w.run
	r.SourceKind = "task_conductor"
	r.Phase = "implement"
	r.Role = "implementer"
	r.Status = "running"
	if err := w.s.childExecutionAllowed(w.ctx, r); err == nil || !strings.Contains(err.Error(), "stop request") {
		t.Fatalf("writer child was not refused for its persisted stop request: %v", err)
	}
	if err := w.s.submit(w.ctx, r); err == nil {
		t.Fatal("writer child reached submission instead of being held")
	}
	if _, _, err := w.s.publish(w.ctx, r); err == nil {
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
			assertChildContract(t, childInstructions(t, spec))
			for _, forbidden := range []string{"ask_person", "request_repository", "request_machine", "create_task"} {
				if strings.Contains(spec.Workload.Prompt, forbidden) {
					t.Fatalf("child prompt advertises %s", forbidden)
				}
			}
		})
	}
}

func TestChildWriterSpec(t *testing.T) {
	for _, profile := range []string{"implementer", "fixer", "simplifier"} {
		t.Run(profile, func(t *testing.T) {
			w := newResumeWorld(t)
			w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
			w.s.Agent.ToolsURL = "http://tools.test"
			w.s.Agent.ToolsService = true
			phase := delivery.PhaseForPromptRole(profile)
			w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status,kind,source_kind)
				VALUES('run_parent',$1,$2,$3,1,'conductor','running','agent',NULL)`, w.run.Org, w.run.ProjectID, w.run.TaskID)
			w.exec(`UPDATE tasks SET status='running' WHERE id=$1`, w.run.TaskID)
			w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
				VALUES('wf_child_writer',$1,$2,'task.delivery','child-writer','waiting','awaitPullRequest',
				jsonb_build_object('attempt',1,'taskId',$2::text,'projectId',$3::text))`, w.run.Org, w.run.TaskID, w.run.ProjectID)
			w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',phase=$2::run_phase,role=$3::agent_role,
			child_profile=$4,child_capabilities='{"publishCode":true}',child_prompt='Make the exact change.',lux_run_id=NULL WHERE id=$1`, w.run.ID, phase, delivery.RoleForPhase[phase], profile)
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
			if spec.Git == nil || spec.Git.Push == nil || spec.Git.Push.Branch != runBranch(r) {
				t.Fatalf("writer child lacks push authority to %s: %+v", runBranch(r), spec.Git)
			}
			contract := delivery.ChildWriterContract(profile, delivery.InstructionsInput{Child: true, Tools: true, CLI: true})
			if agents := childInstructions(t, spec); !strings.HasSuffix(agents, "\n\n"+contract+"\n") {
				t.Fatalf("instruction file does not end with the writer contract:\n%s", agents)
			}
		})
	}
}

func TestSessionChildSpecUsesReadOnlyLinkedRepositoriesAndNoSecrets(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_spec', $1, 'Research')`, w.run.Org)
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_linked', $1, 'Linked', 'linked', 'L')`, w.run.Org)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ('repo_linked', $1, 'prj_linked', 'linked-repo', 'https://github.com/test/linked', 'main')`, w.run.Org)
	w.exec(`INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ssn_spec', 'prj_linked', $1)`, w.run.Org)
	w.exec(`INSERT INTO session_repositories (session_id, repository_id, organization_id) VALUES ('ssn_spec', 'repo_linked', $1)`, w.run.Org)
	w.exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients) VALUES ('prj_' || $1, $1, 'INVESTIGATOR_SECRET', 'secret-value-not-for-child', 'child', '{reviewer}')`, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role) VALUES ('run_brainstorm', $1, 'ssn_spec', 1, 'running', 'agent', 'brainstorm')`, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role, source_kind, parent_run_id, parent_session_id, child_profile, child_capabilities, child_prompt)
		VALUES ('run_session_child', $1, 'ssn_spec', 1, 'running', 'investigate', 'investigator', 'session', 'run_brainstorm', 'ssn_spec', 'investigator', '{}', 'read the linked repository')`, w.run.Org)
	w.exec(`UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org)
	w.exec(`UPDATE organizations SET agent_egress='{api.example.test}' WHERE id=$1`, w.run.Org)
	w.exec(`UPDATE machine_sizes SET is_default=false WHERE organization_id=$1`, w.run.Org)
	w.exec(`INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, is_default) VALUES ('machine_session', $1, 'session-medium', 4, 8192, 80, true)`, w.run.Org)
	w.exec(`INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, is_default) VALUES ('machine_raised', $1, 'session-large', 8, 16384, 160, false)`, w.run.Org)
	w.exec(`UPDATE sessions SET agent_machine=jsonb_build_object('sizeId','machine_raised','moveId','move_session','askedBy','run_brainstorm','reason','OOM','at','2026-10-19T00:00:00Z') WHERE id='ssn_spec'`)
	w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object('investigator', jsonb_build_object('tier', (SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org)
	w.run = phaseRun{ID: "run_session_child", Org: w.run.Org, SessionID: "ssn_spec", Phase: delivery.PhaseInvestigate, Role: delivery.RoleForPhase[delivery.PhaseInvestigate], SourceKind: string(delivery.ChildParentSession), Status: statusRunning}
	w.s.Agent.ToolsURL = "http://tools.test"
	w.s.Agent.ToolsService = true
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
	sp, _, _, secrets, err := w.s.spec(w.ctx, w.run, nil, chosenImage{Ref: "test/image:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 0 {
		t.Fatalf("session child received project secrets: %v", secrets)
	}
	for _, secret := range sp.Secrets {
		if secret.Name == "INVESTIGATOR_SECRET" || secret.Value == "secret-value-not-for-child" {
			t.Fatalf("project secret reached submitted spec: %v", secret.Name)
		}
	}
	if sp.Git == nil || len(sp.Git.Repositories) != 1 || sp.Git.Repositories[0].Push == nil || *sp.Git.Repositories[0].Push || !strings.HasPrefix(sp.Git.Repositories[0].Name, "l-linked-repo") {
		t.Fatalf("linked read-only repositories: %+v", sp.Git)
	}
	var rows int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM run_project_secrets WHERE run_id='run_session_child'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("session child secret rows: %d", rows)
	}
	if sp.Network == nil || !slices.Contains(sp.Network.Egress, lux.EgressRule{Host: "api.example.test"}) {
		t.Fatalf("session child egress: %v", sp.Network)
	}
	if sp.Resources == nil || sp.Resources.CPUs != 8 || sp.Resources.Memory != 16384<<20 || sp.Resources.Disk != 160<<30 {
		t.Fatalf("session child machine: %+v", sp.Resources)
	}
	if sp.Labels["dude.harness"] != delivery.HarnessOpenCode || sp.Labels["dude.model"] != "gpt-6-sol" {
		t.Fatalf("session child runtime labels: %v", sp.Labels)
	}
}

func TestSessionChildArtifactsAreSessionOwnedAndVersioned(t *testing.T) {
	w := newResumeWorld(t)
	session := "ssn_artifacts_" + w.run.Org
	brainstorm := "run_brainstorm_art_" + w.run.Org
	first, second, task := "run_art_one_"+w.run.Org, "run_art_two_"+w.run.Org, "run_task_art_"+w.run.Org
	w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ($1, $2, 'Research')`, session, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role) VALUES ($1, $2, $3, $4, 1, 'running', 'agent', 'brainstorm')`, brainstorm, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role, source_kind, parent_run_id, parent_session_id, child_profile, child_capabilities)
		SELECT id, $1, $2, 1, 'running', 'investigate', 'investigator', 'session', $3, $2, 'investigator', '{}'::jsonb FROM unnest($4::text[]) id`, w.run.Org, session, brainstorm, []string{first, second})
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role) VALUES ($1, $2, $3, $4, 1, 'completed', 'investigate', 'investigator')`, task, w.run.Org, w.run.ProjectID, w.run.TaskID)
	// Isolate recordArtifact's owner attribution from the legacy run-derived trigger.
	w.exec(`ALTER TABLE events DISABLE TRIGGER events_session_of_run`)
	record := func(run, storage, sha string) {
		t.Helper()
		if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
			return recordArtifact(w.ctx, tx, artifactRun{Org: w.run.Org, SessionID: session, ID: run}, lux.Artifact{ID: storage, Path: lux.PublishedPrefix + "design.md", SHA256: sha, Available: true})
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(first, "artifact_one_"+w.run.Org, "sha-one")
	record(second, "artifact_two_"+w.run.Org, "sha-two")
	var eventSession *string
	var correlation string
	if err := w.owner.QueryRow(w.ctx, `SELECT session_id, correlation_id FROM events WHERE event_type=$1 AND run_id=$2 ORDER BY cursor DESC LIMIT 1`, ArtifactEventType, first).Scan(&eventSession, &correlation); err != nil {
		t.Fatal(err)
	}
	if eventSession == nil || *eventSession != session || correlation != session {
		t.Fatalf("session artifact event attribution: session=%v correlation=%q", eventSession, correlation)
	}
	var count int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE r.session_id=$1 AND a.name='design.md'`, session).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("session artifact copies: %d", count)
	}
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		return recordArtifact(w.ctx, tx, artifactRun{Org: w.run.Org, ProjectID: w.run.ProjectID, TaskID: w.run.TaskID, ID: task}, lux.Artifact{ID: "task_storage_" + w.run.Org, Path: lux.PublishedPrefix + "design.md", SHA256: "task-sha", Available: true})
	}); err != nil {
		t.Fatal(err)
	}
	var artifactSessionID, artifactTaskID *string
	if err := w.owner.QueryRow(w.ctx, `SELECT a.session_id, r.task_id FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE a.storage_key=$1`, "task_storage_"+w.run.Org).Scan(&artifactSessionID, &artifactTaskID); err != nil {
		t.Fatal(err)
	}
	if artifactSessionID != nil || artifactTaskID == nil || *artifactTaskID != w.run.TaskID {
		t.Fatalf("task artifact ownership: session=%v task=%v", artifactSessionID, artifactTaskID)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT session_id, correlation_id FROM events WHERE event_type=$1 AND run_id=$2 ORDER BY cursor DESC LIMIT 1`, ArtifactEventType, task).Scan(&eventSession, &correlation); err != nil {
		t.Fatal(err)
	}
	if eventSession != nil || correlation != w.run.TaskID {
		t.Fatalf("task artifact event attribution: session=%v correlation=%q", eventSession, correlation)
	}
}

func TestChildResumeSpecReturnsNoInitAndTheChildPromptOverride(t *testing.T) {
	w := newResumeWorld(t)
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
	w.s.Agent.ToolsURL = "http://tools.test"
	w.s.Agent.ToolsService = true
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,phase='investigate',role='investigator',child_profile='investigator',child_capabilities='{}',child_prompt='Investigate the exact regression.',lux_run_id=NULL WHERE id=$1`, w.run.ID)
	w.exec(`UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org)
	w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object('investigator',jsonb_build_object('tier',(SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org)
	r := w.run
	r.SourceKind, r.Phase, r.Role, r.Attempt = "task_conductor", "investigate", "investigator", 1
	stored := lux.StoredSpec{Image: lux.Image{Ref: "test/image:latest"}}
	resumed, _, _, _, err := w.s.spec(w.ctx, r, &stored, chosenImage{})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Init != nil {
		t.Fatal("resume sent a replacement init instead of retaining the stored spec")
	}
	if !strings.Contains(resumed.Workload.Prompt, "Investigate the exact regression.") {
		t.Fatal("resume spec lacks child prompt override")
	}
}

func TestRestartedChildKeepsItsInstructionContractAndPrompt(t *testing.T) {
	w := newResumeWorld(t)
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
	w.s.Agent.ToolsURL = "http://tools.test"
	w.s.Agent.ToolsService = true
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('run_parent',$1,$2,$3,1,'conductor','running')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_child_restart',$1,$2,'task.delivery','child-restart','waiting','conductorDecision','{"attempt":1}')`, w.run.Org, w.run.TaskID)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',phase='investigate',role='investigator',child_profile='investigator',child_capabilities='{}',child_prompt='Investigate the exact regression.',lux_run_id=NULL WHERE id=$1`, w.run.ID)
	w.exec(`UPDATE runs SET status='completed',ended_at=now() WHERE id=$1`, w.run.ID)
	w.exec(`UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org)
	w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object('investigator',jsonb_build_object('tier',(SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org)
	var restarted string
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		var err error
		restarted, err = delivery.RestartChildTx(w.ctx, tx, w.run.Org, w.run.TaskID, w.run.ID, "restart-key")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := w.s.due(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var r phaseRun
	for _, loaded := range runs {
		if loaded.ID == restarted {
			r = loaded
			break
		}
	}
	if r.ID == "" {
		t.Fatalf("replacement %s absent from production run loader", restarted)
	}
	spec, _, _, _, err := w.s.spec(w.ctx, r, nil, chosenImage{Ref: "test/image:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Workload.Prompt, "Investigate the exact regression.") {
		t.Fatal("restarted child prompt override absent")
	}
	assertChildContract(t, childInstructions(t, spec))
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
