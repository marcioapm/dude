package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestChildProfileAdmission(t *testing.T) {
	for _, profile := range []string{"", "unknown", "conductor", "brainstorm"} {
		t.Run(profile, func(t *testing.T) {
			if _, err := childPhase(profile); !IsRefusal(err) {
				t.Fatalf("profile %q: want refusal, got %v", profile, err)
			}
		})
	}
	for _, profile := range []string{"implementer", "fixer", "reviewer", "simplifier", "qa_browser", "investigator"} {
		t.Run(profile, func(t *testing.T) {
			phase, err := childPhase(profile)
			if err != nil || PromptRoleForPhase[phase] != profile {
				t.Fatalf("profile %q: phase %q, error %v", profile, phase, err)
			}
		})
	}
}

func TestStartChildAdmission(t *testing.T) {
	ctx := context.Background()
	if err := dbtest.Reachable(ctx); err != nil {
		t.Fatal(err)
	}
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_child', $1, 'P', 'p', 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		VALUES ('wi_child', $1, 'prj_child', 1, 'Child', 'Investigate', 'running'),
		('wi_other', $1, 'prj_child', 2, 'Other', 'Other', 'running')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, base_refs)
		VALUES ('run_parent', $1, 'prj_child', 'wi_child', 3, 'running', 'conductor', '{"repo":"base"}')`, org)
	exec(`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_child', $1, 'wi_child', 'task.delivery', 'child-fixture', 'waiting', 'awaitPullRequest',
		'{"taskId":"wi_child","projectId":"prj_child","attempt":3,"heads":{"repo":"stored"},"headRunId":"run_parent","pendingRunIds":["run_parent"],"reviewRunIds":["run_parent"],"pullRequestIds":["pr_preserved"]}')`, org)
	ref := RunRef{Org: org, ProjectID: "prj_child", TaskID: "wi_child", RunID: "run_parent"}
	in := StartChildInput{Parent: ChildParent{Kind: ChildParentTaskConductor, Run: ref}, Profile: "investigator", Key: "logical", Capabilities: ChildCapabilities{}}
	s := &Store{DB: app}
	start := func(in StartChildInput) (string, error) { return s.StartChild(ctx, in) }
	snapshot := func() map[string]any {
		t.Helper()
		var raw []byte
		if err := owner.QueryRow(ctx, `SELECT jsonb_build_object('task', to_jsonb(t), 'workflow', to_jsonb(w),
			'prs', (SELECT jsonb_agg(to_jsonb(p)) FROM pull_requests p WHERE p.task_id = t.id),
			'signals', (SELECT jsonb_agg(to_jsonb(s)) FROM workflow_signals s WHERE s.workflow_run_id = w.id))
			FROM tasks t JOIN workflow_runs w ON w.task_id = t.id WHERE t.id = 'wi_child'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	for _, profile := range []string{"", "unknown", "conductor", "brainstorm"} {
		bad := in
		bad.Profile = profile
		if _, err := start(bad); !IsRefusal(err) {
			t.Fatalf("profile %q: %v", profile, err)
		}
	}
	for _, change := range []func(*StartChildInput){
		func(v *StartChildInput) { v.Parent.Run.TaskID = "wi_other" },
		func(v *StartChildInput) { v.Parent.Run.ProjectID = "prj_other" },
		func(v *StartChildInput) { v.Parent.Run.Org = dbtest.Org(t, owner) },
		func(v *StartChildInput) { v.Parent.Kind = "session" },
		func(v *StartChildInput) { v.Key = "" },
		func(v *StartChildInput) { v.Capabilities.PublishCode = true },
	} {
		bad := in
		change(&bad)
		if _, err := start(bad); !IsRefusal(err) {
			t.Fatalf("unauthorized request %+v: %v", bad, err)
		}
	}
	for _, update := range []string{
		`UPDATE runs SET status = 'completed' WHERE id = 'run_parent'`,
		`UPDATE runs SET replaced_by = 'run_parent' WHERE id = 'run_parent'`,
		`UPDATE runs SET control = 'abort' WHERE id = 'run_parent'`,
		`UPDATE runs SET lux_state = 'terminated' WHERE id = 'run_parent'`,
		`UPDATE tasks SET status = 'done' WHERE id = 'wi_child'`,
	} {
		exec(update)
		if _, err := start(in); !IsRefusal(err) {
			t.Fatalf("ended/replaced parent: %s: %v", update, err)
		}
		exec(`UPDATE runs SET status = 'running', replaced_by = NULL, control = 'none', lux_state = NULL WHERE id = 'run_parent'`)
		exec(`UPDATE tasks SET status = 'running' WHERE id = 'wi_child'`)
	}
	// Take a fresh snapshot after the fixture's deliberate status changes.
	before = snapshot()
	id, err := start(in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := start(in)
	if err != nil || again != id {
		t.Fatalf("retry: %s %v, want %s", again, err, id)
	}
	var parent, source, profile, role, phase string
	var attempt int
	var bases map[string]string
	if err := owner.QueryRow(ctx, `SELECT parent_run_id, source_kind, child_profile, role::text, phase::text, attempt, base_refs
		FROM runs WHERE id = $1`, id).Scan(&parent, &source, &profile, &role, &phase, &attempt, &bases); err != nil {
		t.Fatal(err)
	}
	if parent != ref.RunID || source != string(ChildParentTaskConductor) || profile != "investigator" || role != "investigator" || phase != PhaseInvestigate || attempt != 3 || bases["repo"] != "stored" {
		t.Fatalf("child metadata: %s %s %s %s %s %d %v", parent, source, profile, role, phase, attempt, bases)
	}
	// Even a child whose role is forged to conductor cannot recurse.
	exec(`UPDATE runs SET role = 'investigator' WHERE id = 'run_parent'`)
	exec(`UPDATE runs SET role = 'conductor', phase = NULL WHERE id = $1`, id)
	bad := in
	bad.Parent.Run.RunID = id
	bad.Key = "recursive"
	if _, err := start(bad); !IsRefusal(err) {
		t.Fatalf("recursive child: %v", err)
	}
	exec(`UPDATE runs SET role = 'investigator', phase = 'investigate' WHERE id = $1`, id)
	exec(`UPDATE runs SET role = 'conductor' WHERE id = 'run_parent'`)
	// Separate Store values and transactions exercise database serialization, not an in-memory mutex.
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 32)
	for i := 0; i < 32; i++ {
		go func(i int) {
			request := in
			if i >= 16 {
				request.Key = fmt.Sprintf("child-%d", i)
			}
			run, err := (&Store{DB: app}).StartChild(ctx, request)
			results <- result{run, err}
		}(i)
	}
	for i := 0; i < 32; i++ {
		if r := <-results; r.err != nil {
			t.Fatal(r.err)
		}
	}
	var count, events int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM runs WHERE source_kind = 'task_conductor'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE event_type = 'run.child_started'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if count != 17 || events != 17 {
		t.Fatalf("children %d, starts %d; want 17 each", count, events)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("workflow changed: before %v after %v", before, after)
	}
	// Prompt/note are persisted privately, never copied into the start event.
	private := in
	private.Key = "private"
	private.Prompt = "private prompt"
	private.Note = "private note"
	privateID, err := start(private)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := owner.QueryRow(ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = 'run.child_started'`, privateID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"parentRunId": ref.RunID, "profile": "investigator", "sourceKind": "task_conductor"}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("unsafe start event: %v", payload)
	}
	var prompt, note string
	if err := owner.QueryRow(ctx, `SELECT child_prompt, conductor_note FROM runs WHERE id = $1`, privateID).Scan(&prompt, &note); err != nil {
		t.Fatal(err)
	}
	if prompt != private.Prompt || note != private.Note {
		t.Fatalf("lost overrides: %q %q", prompt, note)
	}
	exec(`INSERT INTO model_tiers (id, organization_id, name, model) VALUES ('tier_child', $1, 'Child', 'fake/scripted')`, org)
	for _, profile := range []string{"implementer", "fixer", "reviewer", "simplifier", "qa_browser", "investigator"} {
		request := in
		request.Profile, request.Key, request.Tier = profile, profile, "Child"
		phase := PhaseForPromptRole(profile)
		request.Capabilities.PublishCode = Publishes[phase]
		run, err := start(request)
		if err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		var gotPhase, gotRole, tier string
		var capabilities ChildCapabilities
		if err := owner.QueryRow(ctx, `SELECT phase::text, role::text, tier_override, child_capabilities FROM runs WHERE id = $1`, run).
			Scan(&gotPhase, &gotRole, &tier, &capabilities); err != nil {
			t.Fatal(err)
		}
		if gotPhase != phase || gotRole != RoleForPhase[phase] || tier != "tier_child" || capabilities != request.Capabilities {
			t.Fatalf("%s resolved to %s/%s/%s/%+v", profile, gotPhase, gotRole, tier, capabilities)
		}
	}
	bad = in
	bad.Key, bad.Tier = "bad-tier", "missing"
	if _, err := start(bad); !IsRefusal(err) {
		t.Fatalf("unknown tier: %v", err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("profile admission changed workflow")
	}
}
