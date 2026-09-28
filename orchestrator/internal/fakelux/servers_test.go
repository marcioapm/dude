package fakelux_test

// The fake's servers, through dude's real lux client: the states a server
// moves through, as lux reports them, and what a move does to them. These
// pin the fake to lux's contract, so the tests that use it test dude
// against something as awkward as lux.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func started(t *testing.T, spec lux.Spec) (*fakelux.Server, *lux.HTTPClient, string) {
	t.Helper()
	fake := fakelux.New(t.TempDir(), "k", nil)
	fake.PreviewDomain = "lux.test"
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	c := lux.New(srv.URL, "k")
	run, err := c.Submit(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to run", func() bool {
		r, _ := c.Get(context.Background(), run.ID)
		return r.State == "running"
	})
	return fake, c, run.ID
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func serverState(c *lux.HTTPClient, run, name string) lux.Server {
	list, _ := c.Servers(context.Background(), run)
	for _, s := range list {
		if s.Name == name {
			return s
		}
	}
	return lux.Server{}
}

var preview = lux.Spec{
	Image:    lux.Image{Ref: "node:22"},
	Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}, Servers: []lux.ServerInput{{Name: "web", Port: 3000, Command: []string{"sh", "-c", "exec npm run dev"}}}},
}

func TestAServerStartsThenIsReadyAndSaysSoOnTheStream(t *testing.T) {
	_, c, run := started(t, preview)
	ctx := context.Background()
	// The spec's server started with the Run.
	waitFor(t, "web ready", func() bool { return serverState(c, run, "web").State == "ready" })
	web := serverState(c, run, "web")
	var view map[string]any
	_ = json.Unmarshal(web.Raw, &view)
	if !web.FromSpec || web.ReadySince == nil || view["url"] != "https://web-1.lux.test" {
		t.Errorf("web = %s", web.Raw)
	}

	// One added at runtime starts at once (it has a command) …
	if _, err := c.AddServer(ctx, run, lux.ServerInput{Name: "api", Port: 8080, Command: []string{"sh", "-c", "exec api"}}); err != nil {
		t.Fatal(err)
	}
	if st := serverState(c, run, "api").State; st != "starting" {
		t.Errorf("api = %s, want starting", st)
	}
	waitFor(t, "api ready", func() bool { return serverState(c, run, "api").State == "ready" })
	// … and a name is taken once.
	if _, err := c.AddServer(ctx, run, lux.ServerInput{Name: "api", Port: 1}); err == nil {
		t.Error("a second api was added")
	} else if le, _ := lux.AsError(err); le.Code != "name_taken" {
		t.Errorf("err = %v", err)
	}

	// One that exits says how, and its log has it.
	if _, err := c.AddServer(ctx, run, lux.ServerInput{Name: "bad", Port: 9, Command: []string{"sh", "-c", "fakelux-exit=2"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bad to exit", func() bool { return serverState(c, run, "bad").State == "exited" })
	var bad map[string]any
	_ = json.Unmarshal(serverState(c, run, "bad").Raw, &bad)
	if bad["exitCode"] != float64(2) || bad["error"] == "" {
		t.Errorf("bad = %v", bad)
	}
	log, err := c.ServerLog(ctx, run, "bad", 1)
	if err != nil {
		t.Fatal(err)
	}
	var lines struct {
		Lines []struct {
			T            int64
			Stream, Text string
		}
	}
	_ = json.Unmarshal(log, &lines)
	if len(lines.Lines) != 1 || lines.Lines[0].Stream != "stderr" {
		t.Errorf("log = %s", log)
	}

	// Stopped by a person, it says so; removed, it is gone.
	s, err := c.ServerAction(ctx, run, "api", "stop")
	if err != nil || s.State != "stopped" || s.StopReason == nil || *s.StopReason != "stopped" {
		t.Errorf("stopped api = %+v, %v", s, err)
	}
	if err := c.RemoveServer(ctx, run, "api"); err != nil {
		t.Fatal(err)
	}
	if serverState(c, run, "api").Name != "" {
		t.Error("api is still there")
	}

	// Every change was a server.* event on the Run's stream, after the fact.
	types := map[string]int{}
	ctx2, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = c.Output(ctx2, run, "", 0, func(f lux.Frame) error {
		if f.Kind == "lux" {
			types[f.EventType]++
		}
		return nil
	})
	if types["server.added"] != 2 || types["server.removed"] != 1 || types["server.state"] < 6 {
		t.Errorf("events = %v", types)
	}
}

func TestAMoveStopsEveryServerAndTheSpecsStartAgain(t *testing.T) {
	fake, c, run := started(t, preview)
	ctx := context.Background()
	waitFor(t, "web ready", func() bool { return serverState(c, run, "web").State == "ready" })
	if _, err := c.AddServer(ctx, run, lux.ServerInput{Name: "api", Port: 8080, Command: []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "api ready", func() bool { return serverState(c, run, "api").State == "ready" })

	fake.Migrate(run)
	waitFor(t, "the run to run again", func() bool {
		r, _ := c.Get(ctx, run)
		return r.State == "running" && r.Epoch == 2
	})
	api := serverState(c, run, "api")
	if api.State != "stopped" || *api.StopReason != "migrated" || *api.StoppedEpoch != 1 {
		t.Errorf("api after the move = %s", api.Raw)
	}
	// The spec's server starts on every start of the Run; the one added
	// later waits for a person.
	waitFor(t, "web ready again", func() bool { return serverState(c, run, "web").State == "ready" })
	if web := serverState(c, run, "web"); web.Epoch != 2 {
		t.Errorf("web after the move = %s", web.Raw)
	}
	r, _ := c.Get(ctx, run)
	if r.Host != "host-2" || r.Placements[0].HostName != "host-1" {
		t.Errorf("hosts = %q, %+v", r.Host, r.Placements)
	}

	// A stop is not a move: its servers stopped with the Run, and nothing
	// can start one until it runs.
	if err := c.Stop(ctx, run); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { r, _ := c.Get(ctx, run); return r.State == "stopped" })
	if web := serverState(c, run, "web"); *web.StopReason != "run stopped" {
		t.Errorf("web after a stop = %s", web.Raw)
	}
	if _, err := c.ServerAction(ctx, run, "web", "start"); err == nil {
		t.Error("started a server on a stopped run")
	} else if le, _ := lux.AsError(err); le.Code != "not_running" {
		t.Errorf("err = %v", err)
	}
	// Adding works while it is stopped.
	if _, err := c.AddServer(ctx, run, lux.ServerInput{Name: "docs", Port: 4000}); err != nil {
		t.Errorf("adding to a stopped run: %v", err)
	}
}

func TestAPortSomeoneElseServesIsReadyWhenItOpens(t *testing.T) {
	fake, c, run := started(t, preview)
	ctx := context.Background()
	if _, err := c.AddServer(ctx, run, lux.ServerInput{Name: "vite", Port: 5173}); err != nil {
		t.Fatal(err)
	}
	if st := serverState(c, run, "vite").State; st != "stopped" {
		t.Errorf("a port with no command = %s, want stopped", st)
	}
	fake.OpenPort(run, "vite")
	if st := serverState(c, run, "vite").State; st != "ready" {
		t.Errorf("an open port = %s, want ready", st)
	}
	if _, err := c.ServerAction(ctx, run, "vite", "restart"); err == nil {
		t.Error("restarted a server with no command")
	}
}
