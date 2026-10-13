package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// liveLux is lux as the CLI's calls see it: the Run live in the default
// pool, with highmem beside it.
type liveLux struct{ lux.Client }

func (liveLux) Get(context.Context, string) (lux.Run, error) {
	return lux.Run{ID: "lrun_cli", State: "running"}, nil
}

func (liveLux) Pools(context.Context) ([]lux.Pool, error) {
	return []lux.Pool{{ID: "pool_default", Name: "general", IsDefault: true}, {ID: "pool_high", Name: "highmem"}}, nil
}

// machineCLI is a running implementer on Standard, reached as the dude CLI
// in its container reaches dude's tools (DUDE_TOOLS_URL), with lux's dry
// run answering refuse (nil accepts). It returns the owner's connection
// and the Run's id.
func machineCLI(t *testing.T, refuse *lux.Error) (*pgx.Conn, string) {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	project, task, run := "prj_"+org, "wi_"+org, "run_cli_"+org
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'P', $1, 'CLI')`, project, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES ($1, $2, $3, 1, 'T', 'running')`, task, org, project)
	exec(`DELETE FROM machine_sizes WHERE organization_id = $1`, org)
	exec(`INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default) VALUES
		('ms_std_'||$1, $1, 'Standard', 2, 8192, 20, NULL, true), ('ms_large_'||$1, $1, 'Large', 4, 16384, 40, NULL, false),
		('ms_xl_'||$1, $1, 'XL', 8, 32768, 80, 'pool_high', false)`, org)
	token, hash := agenttools.NewToken()
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash, lux_run_id, machine)
		VALUES ($1, $2, $3, $4, 1, 'running', 'implement', 'implementer', $5, 'lrun_cli',
		jsonb_build_object('sizeId', 'ms_std_'||$2, 'name', 'Standard', 'cpus', 2, 'memoryMiB', 8192, 'diskGiB', 20, 'from', 'default'))`,
		run, org, project, task, hash)
	srv := httptest.NewServer((&agenttools.Server{DB: app, Lux: liveLux{}, ValidateMachine: func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error {
		if refuse != nil {
			return refuse
		}
		return nil
	}}).Handler())
	t.Cleanup(srv.Close)
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	t.Setenv("DUDE_TOOLS_TOKEN", token)
	return owner, run
}

func count(t *testing.T, owner *pgx.Conn, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// dude machine sizes lists what the Run may move to; dude machine request
// prints the tool's JSON, the move made.
func TestDudeMachineSizesAndRequest(t *testing.T) {
	owner, run := machineCLI(t, nil)
	var out bytes.Buffer
	if err := runCLI(&out, "machine", "sizes"); err != nil {
		t.Fatal(err)
	}
	var sizes []struct {
		Name string  `json:"name"`
		CPUs float64 `json:"cpus"`
		Pool *string `json:"pool"`
	}
	if err := json.Unmarshal(out.Bytes(), &sizes); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if len(sizes) != 2 || sizes[0].Name != "Large" || sizes[1].Name != "XL" || sizes[1].Pool == nil || *sizes[1].Pool != "highmem" {
		t.Fatalf("sizes: %s", out.String())
	}
	out.Reset()
	if err := runCLI(&out, "machine", "request", "XL", "--reason", "duckdb: Out of Memory Error at 8 threads"); err != nil {
		t.Fatal(err)
	}
	var answer struct{ Size, Pool, Answer string }
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if answer.Size != "XL" || answer.Pool != "highmem" || !strings.HasPrefix(answer.Answer, "Moved: you run on XL (8 CPUs · 32 GiB · 80 GiB) in the highmem pool") {
		t.Fatalf("answer: %s", out.String())
	}
	if count(t, owner, `SELECT count(*) FROM machine_moves WHERE run_id = $1 AND reason = 'duckdb: Out of Memory Error at 8 threads'`, run) != 1 ||
		count(t, owner, `SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful' AND dude_pause = 'machine'`, run) != 1 {
		t.Fatal("the request did not park the Run for its move")
	}
}

// A refusal is the CLI's error, in the tool's words, and moves nothing:
// lux's dry run, a size that is not larger, and a missing reason (refused
// by the CLI itself, before dude is asked).
func TestDudeMachineRequestRefused(t *testing.T) {
	owner, run := machineCLI(t, &lux.Error{Status: 422, Code: "platform_mismatch", Message: "pool highmem runs linux/arm64; this Run's image offers only linux/amd64"})
	var out bytes.Buffer
	err := runCLI(&out, "machine", "request", "XL", "--reason", "oom")
	if err == nil || err.Error() != "pool highmem runs linux/arm64; this Run's image offers only linux/amd64" || out.Len() != 0 {
		t.Fatalf("lux's refusal: %v %q", err, out.String())
	}
	err = runCLI(&out, "machine", "request", "Standard", "--reason", "oom")
	if err == nil || err.Error() != "you run on Standard (2 CPUs · 8 GiB · 20 GiB); ask for one larger: Large, XL" {
		t.Fatalf("not larger: %v", err)
	}
	if err := runCLI(&out, "machine", "request", "XL"); err == nil || !strings.HasPrefix(err.Error(), "usage: dude machine request SIZE") {
		t.Fatalf("no reason: %v", err)
	}
	if count(t, owner, `SELECT count(*) FROM machine_moves`) != 0 || count(t, owner, `SELECT count(*) FROM runs WHERE id = $1 AND control = 'none'`, run) != 1 {
		t.Fatal("a refused request changed the Run")
	}
	if count(t, owner, `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'machine.requested' AND payload->>'refused' IN ('platform_mismatch', 'not_larger')`, run) != 2 {
		t.Fatal("refusals not recorded")
	}
}

func runCLI(out *bytes.Buffer, args ...string) error { return run(args, out) }
