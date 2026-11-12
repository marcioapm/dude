package agenttools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The conductor keeps the project's memory: remember with pin pins the
// fact it saves, as the conductor, and the ledger records it.
func TestAConductorPinsWhatItRemembers(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	status, body := f.raw(t, token, "remember", `{"title": "TestMemo is flaky on main", "content": "Rerun it once before blaming the change.", "pin": true}`)
	if status != 200 {
		t.Fatalf("remember pin: %d %s", status, body)
	}
	var out struct {
		ID     string
		Pinned bool
	}
	_ = json.Unmarshal([]byte(body), &out)
	if !out.Pinned {
		t.Errorf("answer %s does not say pinned", body)
	}
	var by string
	var pinnedEvents int
	if err := f.owner.QueryRow(context.Background(), `SELECT coalesce(m.pinned_by_run_id, ''),
			(SELECT count(*) FROM events e WHERE e.event_type = 'memory.pinned' AND e.payload->>'memoryId' = m.id
			   AND e.actor_type = 'agent' AND e.actor_id = 'run_cond' AND e.run_id = 'run_cond')
		FROM memories m WHERE m.id = $1 AND m.pinned_at IS NOT NULL`, out.ID).Scan(&by, &pinnedEvents); err != nil {
		t.Fatalf("not pinned: %v", err)
	}
	if by != "run_cond" || pinnedEvents != 1 {
		t.Errorf("pinned by %q, %d memory.pinned events by the conductor", by, pinnedEvents)
	}
}

// What the pin tool refuses: a Run without conduct (an implementer), and
// a fact a conductor may not pin (about a task, a note, the organization's).
// Nothing is saved by a refused call.
func TestRememberRefusesAPinItMayNotMake(t *testing.T) {
	f := setup(t)
	impl := f.run(t, "run_mem", "implementer", "running")
	if status, body := f.raw(t, impl, "remember", `{"title": "t", "content": "x", "pin": true}`); status != 422 || !strings.Contains(body, "pin is the conductor's") {
		t.Errorf("an implementer pins: %d %s", status, body)
	}
	cond := f.conductor(t)
	for args, want := range map[string]string{
		`{"title": "t", "content": "x", "pin": true, "about": ["TEXT-1"]}`:     "about a task is not pinned",
		`{"title": "t", "content": "x", "pin": true, "kind": "note"}`:          "only a fact is pinned",
		`{"title": "t", "content": "x", "pin": true, "scope": "organization"}`: "only a project's memory is pinned",
	} {
		if status, body := f.raw(t, cond, "remember", args); status != 422 || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s, want a refusal saying %q", args, status, body, want)
		}
	}
	var n int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM memories`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d memories saved by refused calls (%v)", n, err)
	}
}

// From the shell: `dude memory add --pin` pins as the conductor, and is
// refused an implementer.
func TestTheCLIPinsAsTheConductor(t *testing.T) {
	f := setup(t)
	bin := cli(t)
	dude := func(token string, args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	impl := f.run(t, "run_sh", "implementer", "running")
	if out, err := dude(impl, "memory", "add", "--title", "t", "--content", "x", "--pin"); err == nil || !strings.Contains(out, "pin is the conductor's") {
		t.Errorf("an implementer's pin: %v\n%s", err, out)
	}
	cond := f.conductor(t)
	out, err := dude(cond, "memory", "add", "--title", "The tools mirror goes down sometimes", "--content", "Retry once.", "--pin")
	if err != nil || !strings.Contains(out, `"pinned": true`) && !strings.Contains(out, `"pinned":true`) {
		t.Fatalf("the conductor's pin: %v\n%s", err, out)
	}
	// And unpins it, and pins it again, by id.
	var saved struct{ ID string }
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &saved); err != nil || saved.ID == "" {
		t.Fatalf("no id in %s (%v)", out, err)
	}
	if out, err := dude(cond, "memory", "unpin", saved.ID); err != nil || strings.Contains(out, "pinned") {
		t.Errorf("the conductor's unpin: %v\n%s", err, out)
	}
	if out, err := dude(impl, "memory", "pin", saved.ID); err == nil || !strings.Contains(out, "pin is the conductor's") {
		t.Errorf("an implementer's pin by id: %v\n%s", err, out)
	}
}

// The hand-off rPinNotGranted asks for ends in one memory, pinned: a phase
// Run is refused the pin and saves nothing; told, the conductor remembers
// and pins it once. Or the phase Run saved it, and the conductor pins its
// memory by id: still one.
func TestTheHandOffToTheConductorLeavesOneFact(t *testing.T) {
	f := setup(t)
	impl := f.run(t, "run_mem", "implementer", "running")
	cond := f.conductor(t)
	fact := `"title": "The tools mirror goes down sometimes", "content": "Retry once."`
	if status, body := f.raw(t, impl, "remember", `{`+fact+`, "pin": true}`); status != 422 || !strings.Contains(body, "don't save it; tell your conductor") {
		t.Fatalf("an implementer's pin: %d %s", status, body)
	}
	if status, body := f.raw(t, cond, "remember", `{`+fact+`, "pin": true}`); status != 200 {
		t.Fatalf("the conductor's pin: %d %s", status, body)
	}
	count := func(where string) (n int) {
		t.Helper()
		if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM memories WHERE `+where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if all, pinned := count("true"), count("pinned_at IS NOT NULL"); all != 1 || pinned != 1 {
		t.Errorf("after the hand-off: %d memories, %d pinned; want one, pinned", all, pinned)
	}

	// The other way: the phase Run saved it unpinned; the conductor pins it by id.
	status, body := f.raw(t, impl, "remember", `{"title": "Deploys freeze on Fridays", "content": "x"}`)
	var saved struct{ ID string }
	if status != 200 || json.Unmarshal([]byte(body), &saved) != nil {
		t.Fatalf("remember: %d %s", status, body)
	}
	if status, body := f.raw(t, impl, "remember", `{"id": "`+saved.ID+`", "pin": true}`); status != 422 || !strings.Contains(body, "pin is the conductor's") {
		t.Errorf("an implementer pins by id: %d %s", status, body)
	}
	status, body = f.raw(t, cond, "remember", `{"id": "`+saved.ID+`", "pin": true}`)
	if status != 200 || !strings.Contains(body, `"pinned":true`) {
		t.Fatalf("the conductor pins by id: %d %s", status, body)
	}
	if all, pinned := count("true"), count("pinned_at IS NOT NULL AND pinned_by_run_id = 'run_cond'"); all != 2 || pinned != 2 {
		t.Errorf("%d memories, %d pinned by the conductor; want 2 and 2", all, pinned)
	}
	var events int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = 'memory.pinned'
		AND payload->>'memoryId' = $1 AND actor_type = 'agent' AND actor_id = 'run_cond'`, saved.ID).Scan(&events); err != nil || events != 1 {
		t.Errorf("%d memory.pinned events by the conductor (%v)", events, err)
	}
}

// The conductor unpins a fact an agent pinned, by id, recorded; a
// person's pin is theirs, and is refused it. What pin by id refuses: a
// memory that may not be pinned, one it cannot read, other fields beside.
func TestTheConductorUnpinsAnAgentsPinNotAPersons(t *testing.T) {
	f := setup(t)
	cond := f.conductor(t)
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ('per_ana', $1, 'Ana', 'member')`, f.org)
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role)
		VALUES ('run_old', $1, $2, $3, 1, 'completed', 'conductor')`, f.org, f.project, f.item)
	mustExec(t, f.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason, pinned_at, pinned_by_run_id, pinned_by_person_id)
		VALUES ('mem_agent', $1, $2, 'Stale: the old mirror', 'x', 'system', 'test', now(), 'run_old', NULL),
		       ('mem_person', $1, $2, 'Ana''s rule', 'x', 'system', 'test', now(), NULL, 'per_ana'),
		       ('mem_note', $1, $2, 'A note', 'x', 'system', 'test', NULL, NULL, NULL)`, f.org, f.project)
	mustExec(t, f.owner, `UPDATE memories SET kind = 'note' WHERE id = 'mem_note'`)

	status, body := f.raw(t, cond, "remember", `{"id": "mem_agent", "pin": false}`)
	if status != 200 || strings.Contains(body, `"pinned":true`) {
		t.Errorf("the conductor unpins an agent's pin: %d %s", status, body)
	}
	if status, body := f.raw(t, cond, "remember", `{"id": "mem_person", "pin": false}`); status != 422 || !strings.Contains(body, "pinned by Ana: a person's pin is theirs") {
		t.Errorf("the conductor unpins Ana's pin: %d %s", status, body)
	}
	for args, want := range map[string]string{
		`{"id": "mem_note", "pin": true}`:                     "only a fact is pinned",
		`{"id": "mem_nothing", "pin": true}`:                  "no memory mem_nothing",
		`{"id": "mem_agent"}`:                                 "give pin true or false, and nothing else",
		`{"id": "mem_agent", "pin": true, "title": "Edited"}`: "give pin true or false, and nothing else",
		`{"content": "x"}`:                                    "a title is required",
	} {
		if status, body := f.raw(t, cond, "remember", args); status != 422 || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s, want a refusal saying %q", args, status, body, want)
		}
	}
	var state string
	if err := f.owner.QueryRow(context.Background(), `SELECT string_agg(id || '=' || (pinned_at IS NOT NULL)::text, ',' ORDER BY id) FROM memories`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if want := "mem_agent=false,mem_note=false,mem_person=true"; state != want {
		t.Errorf("pins %s, want %s", state, want)
	}
	var events int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = 'memory.unpinned'
		AND payload->>'memoryId' = 'mem_agent' AND actor_id = 'run_cond'`).Scan(&events); err != nil || events != 1 {
		t.Errorf("%d memory.unpinned events (%v)", events, err)
	}
}
