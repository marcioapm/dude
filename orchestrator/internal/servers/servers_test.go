package servers

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestARecipeRunsThroughAShellAfterItsSetup(t *testing.T) {
	setup := "npm ci"
	for _, c := range []struct {
		setup *string
		want  []string
	}{
		{nil, []string{"sh", "-c", "npm run dev"}},
		{&setup, []string{"sh", "-c", "npm ci && npm run dev"}},
		{new(string), []string{"sh", "-c", "npm run dev"}},
	} {
		if got := ShellCommand(c.setup, "npm run dev"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ShellCommand(%v) = %q, want %q", c.setup, got, c.want)
		}
	}
}

// A command is a shell line as a person would type it: an assignment before
// it, a cd, a chain. None of them survives an `exec` in front.
func TestACommandIsAShellLineAsTyped(t *testing.T) {
	for _, line := range []string{"PORT=3000 npm start", "cd web && npm run dev", "npm run build && npm start"} {
		want := []string{"sh", "-c", line}
		if got := ShellCommand(nil, line); !reflect.DeepEqual(got, want) {
			t.Errorf("ShellCommand(%q) = %q, want %q", line, got, want)
		}
		if got, _ := (Recipe{Name: "web", Port: 3000, Command: line}).Input("app"); !reflect.DeepEqual(got.Command, want) {
			t.Errorf("recipe %q runs %q, want %q", line, got.Command, want)
		}
		cmd, _ := json.Marshal(line)
		in, err := ManualServer{Name: "web", Port: 3000, Command: cmd}.Input("app")
		if err != nil || !reflect.DeepEqual(in.Command, want) {
			t.Errorf("manual %q runs %q (%v), want %q", line, in.Command, err, want)
		}
	}
	setup := "npm ci"
	if got, want := ShellCommand(&setup, "PORT=3000 npm start"), []string{"sh", "-c", "npm ci && PORT=3000 npm start"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with setup: %q, want %q", got, want)
	}
}

func TestARecipesWorkdirIsUnderTheRepositorysCheckout(t *testing.T) {
	for _, c := range []struct{ repo, workdir, want string }{
		{"web", "apps/web", "/workspace/repos/web/apps/web"},
		{"web", "", "/workspace/repos/web"},
		{"", "tools", "/workspace/tools"},
	} {
		if got := Workdir(c.repo, c.workdir); got != c.want {
			t.Errorf("Workdir(%q, %q) = %q, want %q", c.repo, c.workdir, got, c.want)
		}
	}
	r := Recipe{Name: "web", Port: 3000, Command: "npm run dev", Workdir: "apps/web",
		Env: []EnvVar{{"VITE_API_URL", "http://localhost:8080"}}}
	in, err := r.Input("app")
	if err != nil || in.Workdir != "/workspace/repos/app/apps/web" || in.Env["VITE_API_URL"] != "http://localhost:8080" || in.Start != nil {
		t.Errorf("input = %+v (%v)", in, err)
	}
	// lux's own names are refused, as a person's typed ones are.
	if _, err := (Recipe{Name: "web", Port: 3000, Command: "x", Env: []EnvVar{{"LUX_TOKEN", "x"}}}).Input("app"); err == nil {
		t.Error("a recipe's env name with lux's reserved prefix was taken")
	}
}

func TestOnlyNamesLuxTakes(t *testing.T) {
	for name, ok := range map[string]bool{"web": true, "api-2": true, "a": true, "web-": false, "2web": false,
		"Web": false, "": false, "a234567890123456789012345678901": false, "a23456789012345678901234567890": true} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
	for w, ok := range map[string]bool{"": true, "apps/web": true, "/etc": false, "../x": false, "a/../../x": false, "a/./b": true} {
		if ValidWorkdir(w) != ok {
			t.Errorf("ValidWorkdir(%q) = %v", w, !ok)
		}
	}
}

func TestAServerAPersonTypesRunsAsTheyTypedIt(t *testing.T) {
	line, _ := ManualServer{Name: "x", Port: 1, Command: json.RawMessage(`"make serve"`)}.Input("app")
	if !reflect.DeepEqual(line.Command, []string{"sh", "-c", "make serve"}) || line.Workdir != "/workspace/repos/app" {
		t.Errorf("a line = %+v", line)
	}
	argv, _ := ManualServer{Name: "x", Port: 1, Command: json.RawMessage(`["python3","-m","http.server"]`),
		Env: json.RawMessage(`[{"name":"A","value":"1"}]`)}.Input("app")
	if !reflect.DeepEqual(argv.Command, []string{"python3", "-m", "http.server"}) || argv.Env["A"] != "1" {
		t.Errorf("argv = %+v", argv)
	}
	// Only a port: someone else serves it; no command, no workdir.
	port, _ := ManualServer{Name: "x", Port: 5173}.Input("app")
	if port.Command != nil || port.Workdir != "" {
		t.Errorf("a port = %+v", port)
	}
	if _, err := (ManualServer{Name: "x", Port: 1, Command: json.RawMessage(`3`)}).Input(""); err == nil {
		t.Error("a number as a command was taken")
	}
	if _, err := (ManualServer{Name: "x", Port: 1, Env: json.RawMessage(`{"LUX_TOKEN":"x"}`)}).Input(""); err == nil {
		t.Error("an env name with lux's reserved prefix was taken")
	}
}

func TestAPreviewsEgressIsWhatItsSettingsAllow(t *testing.T) {
	n, refused := Egress([]string{"registry.npmjs.org", "10.0.0.5", "192.168.0.0/16", " ", "2001:db8::1"})
	want := []lux.EgressRule{{Host: "registry.npmjs.org"}, {CIDR: "10.0.0.5/32"}, {CIDR: "192.168.0.0/16"}, {CIDR: "2001:db8::1/128"}}
	if n.Unrestricted || !reflect.DeepEqual(n.Egress, want) || refused != nil {
		t.Errorf("egress = %+v, refused %q", n, refused)
	}
	if n, _ := Egress([]string{"a.com", "*"}); !n.Unrestricted {
		t.Error(`"*" does not turn filtering off`)
	}
	// Nothing allowed is nothing allowed, not everything.
	if n, _ := Egress(nil); n.Unrestricted || len(n.Egress) != 0 {
		t.Errorf("no egress = %+v", n)
	}
	// What lux would refuse, failing the whole preview, is left out; a
	// wildcard lux takes (lux#68) is kept.
	n, refused = Egress([]string{"*.github.com", "*.com", "10.0.0.0/33", "not a host", "10.0.0.0/8", "github.com"})
	if !reflect.DeepEqual(n.Egress, []lux.EgressRule{{Host: "*.github.com"}, {CIDR: "10.0.0.0/8"}, {Host: "github.com"}}) ||
		!reflect.DeepEqual(refused, []string{"*.com", "10.0.0.0/33", "not a host"}) {
		t.Errorf("egress = %+v, refused %q", n, refused)
	}
}

func srv(name, state string, fromSpec bool) lux.Server {
	return lux.Server{Name: name, State: state, FromSpec: fromSpec, Command: []string{"sh"}}
}

func TestAPreviewsStageFollowsLuxAndItsServers(t *testing.T) {
	setupOf := func(n string) bool { return n == "web" }
	stage := func(status, luxState string, list ...lux.Server) string {
		if s := Stage(status, luxState, list, setupOf); s != nil {
			return *s
		}
		return "none"
	}
	for _, c := range []struct {
		got, want string
	}{
		{stage("pending", ""), "scheduling"},
		{stage("scheduled", "submitted"), "scheduling"},
		{stage("running", "resuming"), "scheduling"},
		{stage("running", "starting"), "cloning"},
		{stage("running", "running", srv("web", "starting", true), srv("api", "ready", true)), "setup"},
		{stage("running", "running", srv("api", "starting", true), srv("web", "ready", true)), "starting"},
		{stage("running", "running", srv("api", "stopped", true)), "starting"},
		// A server a person added later does not hold it back.
		{stage("running", "running", srv("api", "ready", true), srv("docs", "stopped", false)), "ready"},
		{stage("running", "running"), "ready"},
		{stage("paused", "stopped"), "none"},
		{stage("completed", "cancelled"), "none"},
	} {
		if c.got != c.want {
			t.Errorf("stage = %s, want %s", c.got, c.want)
		}
	}
}

func TestPreviewProgress(t *testing.T) {
	setupOf := func(n string) bool { return n == "web" }
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	at := func(s int) *time.Time { v := t0.Add(time.Duration(s) * time.Second); return &v }
	ready := func(name string, s int, fromSpec bool) lux.Server {
		sv := srv(name, lux.ServerReady, fromSpec)
		sv.ReadySince, sv.Since = at(s), at(s)
		return sv
	}
	starting := func(name string, s int) lux.Server {
		sv := srv(name, lux.ServerStarting, true)
		sv.Since = at(s)
		return sv
	}
	stageRun := func(stage string) lux.Run { return lux.Run{Stage: stage, StageSince: at(-100)} }
	for _, c := range []struct {
		name          string
		status, state string
		run           lux.Run
		list          []lux.Server
		stage         string // "" for nil
		since         *time.Time
	}{
		{"a lux without stages is coarse with no timer", "running", "starting", lux.Run{}, nil, "cloning", nil},
		{"waiting", "scheduled", "submitted", stageRun("waiting"), nil, "scheduling", at(-100)},
		{"image", "running", "starting", stageRun("image"), nil, "image", at(-100)},
		{"volumes", "running", "starting", stageRun("volumes"), nil, "volumes", at(-100)},
		{"repositories is cloning", "running", "starting", stageRun("repositories"), nil, "cloning", at(-100)},
		{"container", "running", "starting", stageRun("container"), nil, "container", at(-100)},
		{"stopping while running", "running", "stopping", stageRun("stopping"), nil, "stopping", at(-100)},
		// A park: dude's status is paused, so the preview shows no stage at
		// all, even while lux is still stopping it.
		{"stopping while paused is nothing", "paused", "stopping", stageRun("stopping"), nil, "", nil},
		{"an unknown stage is coarse with no timer", "running", "starting", stageRun("draining"), []lux.Server{ready("web", 5, true)}, "cloning", nil},
		{"a resting stage while running is coarse with no timer", "running", "running", stageRun("failed"), []lux.Server{ready("web", 5, true)}, "ready", nil},
		{"stopped while running is coarse with no timer", "running", "stopped", stageRun("stopped"), nil, "scheduling", nil},
		{"running with no servers is ready with no timer", "running", "running", stageRun("running"), nil, "ready", nil},
		{"running before state running goes by servers", "running", "starting", stageRun("running"), []lux.Server{starting("api", 7)}, "starting", at(7)},
		{"setup times the setup server only", "running", "running", stageRun("running"), []lux.Server{starting("api", 9), starting("web", 4)}, "setup", at(4)},
		{"ready is the latest server's readySince", "running", "running", stageRun("running"),
			[]lux.Server{ready("a", 1, true), ready("b", 3, true), ready("c", 2, true)}, "ready", at(3)},
		{"a server not from the spec is ignored", "running", "running", stageRun("running"),
			[]lux.Server{ready("a", 1, true), ready("docs", 50, false)}, "ready", at(1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			stage, since := previewProgress(c.status, c.state, c.run, c.list, setupOf)
			got := ""
			if stage != nil {
				got = *stage
			}
			if got != c.stage || !reflect.DeepEqual(since, c.since) {
				t.Errorf("previewProgress = %q %v, want %q %v", got, since, c.stage, c.since)
			}
		})
	}
}

func TestMovedIsSaidOnlyWhenTheMoveStoppedEveryServer(t *testing.T) {
	at := time.Date(2026, 9, 28, 14, 32, 0, 0, time.UTC)
	migrated, byHand := "migrated", "stopped"
	three := 3
	stoppedBy := func(name string, reason *string) lux.Server {
		return lux.Server{Name: name, State: "stopped", StopReason: reason, StoppedEpoch: &three, Since: &at}
	}
	run := lux.Run{Epoch: 4, Host: "lux-c9", Placements: []lux.Placement{{Epoch: 3, HostName: "lux-c7"}, {Epoch: 4, HostName: "lux-c9"}}}

	m := moved([]lux.Server{stoppedBy("web", &migrated), stoppedBy("api", &migrated),
		{Name: "storybook", State: "stopped"}}, run)
	if m == nil || !m.At.Equal(at) || *m.FromHost != "lux-c7" || *m.ToHost != "lux-c9" {
		t.Fatalf("moved = %+v", m)
	}
	// One started again, or one a person stopped: no longer the move's doing.
	if moved([]lux.Server{stoppedBy("web", &migrated), srv("api", "ready", false)}, run) != nil {
		t.Error("moved with a server serving")
	}
	if moved([]lux.Server{stoppedBy("web", &migrated), stoppedBy("api", &byHand)}, run) != nil {
		t.Error("moved with a server a person stopped")
	}
	// Still stopping, on the placement they stopped in.
	if moved([]lux.Server{stoppedBy("web", &migrated)}, lux.Run{Epoch: 3}) != nil {
		t.Error("moved before the Run started again")
	}
	if moved([]lux.Server{{Name: "web", State: "stopped"}}, run) != nil {
		t.Error("moved with nothing ever started")
	}
}

// lux's times are decoded in lux's own offset; what the API passes on of
// them (a preview's stage timer, when its Run moved) is UTC.
func TestLuxsTimesAreSentInUTC(t *testing.T) {
	var run lux.Run
	if err := json.Unmarshal([]byte(`{"stage":"image","stageSince":"2026-10-09T17:30:00+05:30","epoch":4,"host":"lux-c9",
		"placements":[{"epoch":3,"hostName":"lux-c7"},{"epoch":4,"hostName":"lux-c9"}]}`), &run); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	encoded := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	_, since := previewProgress("running", "starting", run, nil, func(string) bool { return false })
	if since == nil || encoded(since) != `"2026-10-09T12:00:00Z"` || !since.Equal(want) {
		t.Errorf("stage since %s, want %q", encoded(since), "2026-10-09T12:00:00Z")
	}

	var stopped lux.Server
	if err := json.Unmarshal([]byte(`{"name":"web","state":"stopped","stopReason":"migrated","stoppedEpoch":3,
		"since":"2026-10-09T17:30:00+05:30"}`), &stopped); err != nil {
		t.Fatal(err)
	}
	m := moved([]lux.Server{stopped}, run)
	if m == nil || encoded(m.At) != `"2026-10-09T12:00:00Z"` {
		t.Errorf("moved at %s, want %q", encoded(m), "2026-10-09T12:00:00Z")
	}
}

func TestLuxsServerIsPassedOnAsLuxSentIt(t *testing.T) {
	raw := `{"name":"web","port":3000,"state":"ready","url":"https://web-x.lux.test","somethingNew":1}`
	var s lux.Server
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "web" || s.State != "ready" {
		t.Errorf("read %+v", s)
	}
	out, _ := json.Marshal([]lux.Server{s})
	if string(out) != "["+raw+"]" {
		t.Errorf("passed on %s", out)
	}
}
