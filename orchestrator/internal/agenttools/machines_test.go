package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// poolsLux reports two pools: the default, and highmem.
type poolsLux struct{ lux.Client }

func (poolsLux) Get(context.Context, string) (lux.Run, error) {
	return lux.Run{ID: "live", State: "running"}, nil
}

func (poolsLux) Pools(context.Context) ([]lux.Pool, error) {
	return []lux.Pool{{ID: "pool_default", Name: "general", IsDefault: true}, {ID: "pool_high", Name: "highmem"}}, nil
}

// machineRun is a running reviewer on Standard, in an organization with
// Large in its pool and XL in highmem, served by a server with poolsLux.
func machineRun(t *testing.T, validate func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error) (*fixture, string) {
	t.Helper()
	return machineRunOn(t, poolsLux{}, validate)
}

// machineRunOn is machineRun served with the lux given.
func machineRunOn(t *testing.T, l lux.Client, validate func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error) (*fixture, string) {
	t.Helper()
	f := setup(t)
	token := f.run(t, "run_mach", "reviewer", "running")
	mustExec(t, f.owner, `DELETE FROM machine_sizes WHERE organization_id = $1`, f.org)
	mustExec(t, f.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default) VALUES
		('ms_std', $1, 'Standard', 2, 8192, 20, NULL, true), ('ms_large', $1, 'Large', 4, 16384, 40, NULL, false),
		('ms_xl', $1, 'XL', 8, 32768, 80, 'pool_high', false)`, f.org)
	mustExec(t, f.owner, `UPDATE runs SET machine = '{"sizeId":"ms_std","name":"Standard","cpus":2,"memoryMiB":8192,"diskGiB":20,"from":"default"}', lux_run_id = 'live' WHERE id = 'run_mach'`)
	srv := httptest.NewServer((&agenttools.Server{DB: f.app, Log: slog.New(slog.DiscardHandler), Lux: l, ValidateMachine: validate}).Handler())
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f, token
}

// getTools is the CLI's `dude tools`: each tool's description by name.
func (f *fixture) getTools(t *testing.T, token string) map[string]string {
	t.Helper()
	req, _ := http.NewRequest("GET", f.url+"/tools", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Tools []struct{ Name, Description string }
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, tool := range body.Tools {
		out[tool.Name] = tool.Description
	}
	return out
}

func (f *fixture) str(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func toolDescriptions(t *testing.T, f *fixture, token string) map[string]string {
	t.Helper()
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, tool := range list.Tools {
		out[tool.Name] = tool.Description
	}
	return out
}

// The tool's description lists the sizes this Run could move to, with
// their pools, over MCP and the CLI's list alike.
func TestTheMachineToolsDescribeTheSizesThisRunMayMoveTo(t *testing.T) {
	f, token := machineRun(t, nil)
	sizes := "Large (4 · 16), XL (8 · 32, highmem pool)"
	got := toolDescriptions(t, f, token)
	if d := got["request_machine"]; !strings.Contains(d, "You run on Standard (2 CPUs · 8 GiB · 20 GiB). Larger sizes you may move to: "+sizes+".") ||
		!strings.Contains(d, "running processes and /tmp are not kept") || !strings.Contains(d, "ends your turn") {
		t.Errorf("request_machine: %q", d)
	}
	if d := got["list_machine_sizes"]; d != "The larger machine sizes this Run may move to, with their pools: "+sizes+"." {
		t.Errorf("list_machine_sizes: %q", d)
	}
	listed := f.getTools(t, token)
	if listed["request_machine"] != got["request_machine"] {
		t.Errorf("CLI's list: %q", listed["request_machine"])
	}
	status, out := f.post(t, token, "list_machine_sizes", `{}`)
	if status != 200 {
		t.Fatalf("list: %d %v", status, out)
	}

	// At the organization's largest: told so, offered nothing, and a
	// request is refused with the same words, before anything changes.
	mustExec(t, f.owner, `UPDATE runs SET machine = '{"sizeId":"ms_xl","name":"XL","cpus":8,"memoryMiB":32768,"diskGiB":80,"poolId":"pool_high","from":"task"}' WHERE id = 'run_mach'`)
	largest := "You run on XL (8 CPUs · 32 GiB · 80 GiB), Org's largest size: there is none larger to ask for. " +
		"Use fewer workers or less data, and say in your work what did not fit."
	org := f.str(t, `SELECT name FROM organizations WHERE id = $1`, f.org)
	largest = strings.Replace(largest, "Org's", org+"'s", 1)
	got = toolDescriptions(t, f, token)
	if got["request_machine"] != "Move to a larger machine. "+largest || !strings.HasSuffix(got["list_machine_sizes"], "none. "+largest) {
		t.Errorf("at the largest: %q / %q", got["request_machine"], got["list_machine_sizes"])
	}
	if status, out := f.post(t, token, "request_machine", `{"size":"Large","reason":"oom"}`); status != 422 || out["error"] != largest {
		t.Errorf("request at the largest: %d %v", status, out)
	}
	if n := f.count(t, `SELECT count(*) FROM machine_moves`); n != 0 {
		t.Errorf("%d moves", n)
	}
}

// MCP gives lux's refusal code beside dude's words for it, as the CLI's
// JSON does.
func TestMCPGivesLuxsRefusalCode(t *testing.T) {
	f, token := machineRun(t, func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error {
		return &lux.Error{Status: 422, Code: "platform_mismatch", Message: "pool highmem runs linux/arm64; this Run's image offers only linux/amd64"}
	})
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_machine", Arguments: map[string]any{"size": "XL", "reason": "duckdb oom"}})
	if err != nil || !res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	got, _ := res.StructuredContent.(map[string]any)
	if got["code"] != "platform_mismatch" || got["error"] != "XL is in highmem, whose machines cannot run this agent's image. Ask for another size, or ask a person to add an image for them." {
		t.Errorf("structured: %#v", res.StructuredContent)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; text != got["error"] {
		t.Errorf("text %q", text)
	}
	status, out := f.post(t, token, "request_machine", `{"size":"XL","reason":"duckdb oom"}`)
	if status != 422 || out["code"] != "platform_mismatch" || out["error"] != got["error"] {
		t.Errorf("JSON: %d %v", status, out)
	}
	// A refusal of dude's own with a code: not larger.
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_machine", Arguments: map[string]any{"size": "Standard", "reason": "oom"}})
	if got, _ := res.StructuredContent.(map[string]any); !res.IsError || got["code"] != "not_larger" ||
		got["error"] != "you run on Standard (2 CPUs · 8 GiB · 20 GiB); ask for one larger: Large, XL" {
		t.Errorf("not larger: %#v", res.StructuredContent)
	}
	// An unknown name lists what it may move to.
	if status, out := f.post(t, token, "request_machine", `{"size":"XXL","reason":"oom"}`); status != 422 ||
		out["error"] != "no size named XXL; you may move to: Large (4 · 16), XL (8 · 32, highmem pool)" {
		t.Errorf("unknown: %d %v", status, out)
	}
}

// factsLux is lux with what a refusal's sentence names: highmem runs
// linux/arm64 and has no nested containers; the Run's image is amd64 only
// and needs nested containers.
type factsLux struct{ lux.Client }

func (factsLux) Get(context.Context, string) (lux.Run, error) {
	run := lux.Run{ID: "live", State: "running", PoolID: "pool_default", Placements: []lux.Placement{{ImagePlatforms: []string{"linux/amd64"}}}}
	run.Spec.Sandbox = &lux.Sandbox{NestedContainers: true}
	return run, nil
}

func (factsLux) Pools(context.Context) ([]lux.Pool, error) {
	no := false
	return []lux.Pool{{ID: "pool_default", Name: "general", IsDefault: true},
		{ID: "pool_high", Name: "highmem", Platforms: []string{"linux/arm64"}, Capabilities: &lux.PoolCapabilities{NestedContainers: &no}}}, nil
}

// Every lux refusal of a move reaches the agent in the design's sentences,
// sizes and pools by name; lux's own words (pool ids) are never shown, and
// the refusal is recorded with the same words.
func TestEachLuxRefusalIsToldInTheDesignsWords(t *testing.T) {
	for _, tc := range []struct {
		status        int
		code, size    string
		luxSaid, want string
	}{
		{422, "platform_mismatch", "XL", "pool pool_high runs linux/arm64; this Run's image offers only linux/amd64",
			"XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only. Ask for another size, or ask a person to add an arm64 image."},
		{422, "pool_unfit", "XL", "pool pool_high cannot take this Run: none of its hosts offers nested containers (lux-runner --nested)",
			"XL is in highmem, which can't run nested containers, and this agent's image needs them. Ask for another size."},
		{422, "pool_unfit", "Large", "pool pool_default cannot take this Run: it needs 4 cpus; its largest host has 2",
			"Large (4 CPUs · 16 GiB) is larger than any machine in general. Ask for another size."},
		{422, "unknown_pool", "XL", "no pool with id pool_high", "XL's pool has no host it fits; ask for another size"},
		{422, "invalid_spec", "Large", "resources.disk must not be negative",
			"lux cannot give this agent Large as the size is set up. Ask for another size, or ask a person to check Large in Machines."},
		{409, "not_resumable", "Large", "run is resuming already with other resources",
			"lux was already resuming this Run, so it could not move it to Large. Ask again once you are running."},
	} {
		t.Run(tc.code+" "+tc.size, func(t *testing.T) {
			f, token := machineRunOn(t, factsLux{}, func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error {
				return &lux.Error{Status: tc.status, Code: tc.code, Message: tc.luxSaid}
			})
			status, out := f.post(t, token, "request_machine", `{"size":"`+tc.size+`","reason":"oom"}`)
			if status != 422 || out["code"] != tc.code || out["error"] != tc.want {
				t.Fatalf("%d %v\nwant %q", status, out, tc.want)
			}
			if n := f.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_mach' AND event_type = 'machine.requested' AND payload->>'message' = $1 AND payload->>'refused' = $2`, tc.want, tc.code); n != 1 {
				t.Errorf("%d machine.requested with the words", n)
			}
		})
	}
}

// A Run moves at most 3 times. A move lux did not apply does not count,
// refused for good or kept on another size, nor does a Reset: a Run with
// three settled raises, one of them not applied, or two raises and a
// Reset, may still move.
func TestARunMovesAtMostThreeTimesAndRefusalsDoNotCount(t *testing.T) {
	f, token := machineRun(t, func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error { return nil })
	type move struct{ kind, refused, message string }
	moves := func(ms ...move) {
		mustExec(t, f.owner, `DELETE FROM machine_moves WHERE run_id = 'run_mach'`)
		mustExec(t, f.owner, `UPDATE runs SET control = 'none', dude_pause = NULL WHERE id = 'run_mach'`)
		for i, m := range ms {
			mustExec(t, f.owner, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply, refused, refused_message, settled_at)
				VALUES ($1, $2, $3, 'run_mach', 'reviewer', $4, '{}', 'ms_large', 'resize', NULLIF($5, ''), NULLIF($6, ''), now())`, fmt.Sprintf("mmv_%d", i), f.org, f.item, m.kind, m.refused, m.message)
		}
	}
	raise, reset := move{kind: "raise"}, move{kind: "reset"}
	moves(raise, raise, raise)
	if status, out := f.post(t, token, "request_machine", `{"size":"Large","reason":"oom"}`); status != 422 ||
		out["error"] != "This Run has moved 3 times, the limit for one Run; a Reset does not give a move back. Carry on on Standard, or say in your work what did not fit." {
		t.Fatalf("fourth move: %d %v", status, out)
	}
	for name, ms := range map[string][]move{
		"refused":         {raise, {"raise", "pool_unfit", "no host"}, raise},
		"kept":            {raise, {"raise", "", "Its settings now name Large, but this resume kept Standard (lux said: resuming)."}, raise},
		"two and a Reset": {raise, raise, reset},
	} {
		moves(ms...)
		if status, out := f.post(t, token, "request_machine", `{"size":"Large","reason":"oom"}`); status != 200 {
			t.Fatalf("%s counted: %d %v", name, status, out)
		}
	}
}

// One move of a Run at a time: a Run running again with its move not yet
// settled (lux's outcome unknown) is refused another, and the database
// holds the same line should two calls race past the check.
func TestOneUnsettledMoveAtATime(t *testing.T) {
	f, token := machineRun(t, func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error { return nil })
	insert := func(id string) error {
		_, err := f.owner.Exec(context.Background(), `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply)
			VALUES ($1, $2, $3, 'run_mach', 'reviewer', 'raise', '{}', 'ms_xl', 'resize')`, id, f.org, f.item)
		return err
	}
	if err := insert("mmv_unsettled"); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, token, "request_machine", `{"size":"Large","reason":"oom"}`)
	if status != 422 || out["error"] != "you are being moved to XL now; ask again once you are there, if it is still too small" {
		t.Fatalf("second move: %d %v", status, out)
	}
	if n := f.count(t, `SELECT count(*) FROM machine_moves WHERE run_id = 'run_mach'`); n != 1 {
		t.Fatalf("%d moves", n)
	}
	if err := insert("mmv_second"); err == nil {
		t.Fatal("a second unsettled move of the Run was stored")
	}
}

// An accepted request answers as the design words it, the pool named when
// the move changes pools.
func TestAnAcceptedMachineRequestSaysWhatIsKept(t *testing.T) {
	f, token := machineRun(t, func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error { return nil })
	status, out := f.post(t, token, "request_machine", `{"size":"XL","reason":"duckdb out of memory at 8 threads"}`)
	want := "Moved: you run on XL (8 CPUs · 32 GiB · 80 GiB) in the highmem pool from your next step. Your conversation and " +
		"your checkout are kept, committed or not, with everything else on /workspace and your home. Running processes and " +
		"/tmp are not: start your servers and watchers again. Later reviewer Runs in this task run on XL too. End your turn " +
		"now; you are resumed on XL."
	if status != 200 || out["answer"] != want || out["pool"] != "highmem" || out["size"] != "XL" {
		t.Fatalf("answer: %d %v", status, out)
	}
	if status, out := f.post(t, token, "request_machine", `{"size":"XL","reason":"again"}`); status != 422 ||
		out["error"] != "you are being moved to XL now; ask again once you are there, if it is still too small" {
		t.Fatalf("pending: %d %v", status, out)
	}
}
