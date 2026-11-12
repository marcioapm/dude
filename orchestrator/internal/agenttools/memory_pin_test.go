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
	out, err := dude(f.conductor(t), "memory", "add", "--title", "The tools mirror goes down sometimes", "--content", "Retry once.", "--pin")
	if err != nil || !strings.Contains(out, `"pinned": true`) && !strings.Contains(out, `"pinned":true`) {
		t.Fatalf("the conductor's pin: %v\n%s", err, out)
	}
}
