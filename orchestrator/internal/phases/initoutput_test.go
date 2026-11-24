package phases

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func initEvent(epoch int, phase string, code int) lux.Frame {
	data, _ := json.Marshal(map[string]any{"phase": phase, "exitCode": code})
	return lux.Frame{Kind: "record", Epoch: epoch, Event: &lux.RecordEvent{Type: "lux.init", Data: data}}
}

func output(epoch int, ch, data string) lux.Frame {
	return lux.Frame{Kind: "record", Epoch: epoch, Channel: ch, Data: data}
}

// What a Run's records say of its init: the repository whose script
// failed, its code, what it printed, and the sentence people read.
func TestInitOutputReadsTheSetupThatFailed(t *testing.T) {
	big := strings.Repeat("x", initOutputMax)
	agent, _ := json.Marshal(map[string]any{"sessionUpdate": "agent_message_chunk"})
	for _, tc := range []struct {
		name   string
		frames []lux.Frame
		// nil: no setup failure read
		want       *SetupFailure
		wantReason string
	}{{
		name: "ordinary failure",
		frames: []lux.Frame{
			initEvent(2, "start", 0),
			output(2, "stderr", setupRunningPrefix+"api\n"),
			output(2, "stdout", "npm ci\n"),
			output(2, "stderr", "npm ERR! missing lockfile\n"),
			output(2, "stderr", setupFailedPrefix+"1 api\n"),
			initEvent(2, "done", 1),
		},
		want:       &SetupFailure{Repository: "api", ExitCode: 1, Epoch: 2, Output: setupRunningPrefix + "api\nnpm ci\nnpm ERR! missing lockfile"},
		wantReason: "The setup script of api failed with exit code 1: npm ERR! missing lockfile",
	}, {
		name: "timeout",
		frames: []lux.Frame{
			initEvent(1, "start", 0),
			output(1, "stderr", setupRunningPrefix+"web\n"+setupFailedPrefix+"124 web\n"),
			initEvent(1, "done", 124),
		},
		want:       &SetupFailure{Repository: "web", ExitCode: 124, Epoch: 1, Output: setupRunningPrefix + "web"},
		wantReason: "The setup script of web did not finish within 15m0s.",
	}, {
		name: "no failure line",
		frames: []lux.Frame{
			initEvent(1, "start", 0),
			output(1, "stderr", "sh: something else broke\n"),
			initEvent(1, "done", 2),
		},
	}, {
		name: "failure line not at a line's start",
		frames: []lux.Frame{
			initEvent(1, "start", 0),
			output(1, "stderr", "echo "+setupFailedPrefix+"1 api\n"),
			initEvent(1, "done", 1),
		},
	}, {
		name: "failure line without a name",
		frames: []lux.Frame{
			initEvent(1, "start", 0),
			output(1, "stderr", setupFailedPrefix+"1\n"),
			initEvent(1, "done", 1),
		},
	}, {
		name: "success",
		frames: []lux.Frame{
			initEvent(1, "start", 0),
			output(1, "stderr", setupFailedPrefix+"1 api\n"),
			initEvent(1, "done", 0),
		},
	}, {
		name: "output over the limit keeps its end",
		frames: []lux.Frame{
			initEvent(1, "start", 0),
			output(1, "stdout", "the start\n"+big),
			output(1, "stderr", "\nlast words\n"+setupFailedPrefix+"7 api\n"),
			initEvent(1, "done", 7),
		},
		want: &SetupFailure{Repository: "api", ExitCode: 7, Epoch: 1,
			Output: strings.Repeat("x", initOutputMax-len("\nlast words\n"+setupFailedPrefix+"7 api\n")) + "\nlast words"},
		wantReason: "The setup script of api failed with exit code 7: last words",
	}, {
		name: "other records are not init's",
		frames: []lux.Frame{
			output(1, "stdout", "before init\n"),
			initEvent(1, "start", 0),
			output(1, "stdout", "inside\n"),
			{Kind: "record", Epoch: 1, Event: &lux.RecordEvent{Type: "acp", Data: agent}},
			output(1, "pty", "terminal noise\n"),
			{Kind: "lux", EventType: "state", EventData: json.RawMessage(`{}`)},
			output(1, "stderr", setupFailedPrefix+"3 docs\n"),
			initEvent(1, "done", 3),
			output(1, "stdout", "after init\n"),
		},
		want:       &SetupFailure{Repository: "docs", ExitCode: 3, Epoch: 1, Output: "inside"},
		wantReason: "The setup script of docs failed with exit code 3: inside",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var o InitOutput
			for _, f := range tc.frames {
				o.Record(f)
			}
			if o.Running || o.Tail != "" {
				t.Errorf("after done: running %v, tail %q", o.Running, o.Tail)
			}
			if tc.want == nil {
				if o.Failed != nil {
					t.Fatalf("read a failure %+v", *o.Failed)
				}
				return
			}
			if o.Failed == nil {
				t.Fatal("no failure read")
			}
			if *o.Failed != *tc.want {
				t.Errorf("got  %+v\nwant %+v", *o.Failed, *tc.want)
			}
			if got := o.Failed.Reason(); got != tc.wantReason {
				t.Errorf("reason %q, want %q", got, tc.wantReason)
			}
		})
	}
}

// A new start forgets what an earlier placement's init printed and failed.
func TestInitOutputStartsOverOnEachStart(t *testing.T) {
	var o InitOutput
	for _, f := range []lux.Frame{initEvent(1, "start", 0), output(1, "stderr", setupFailedPrefix+"1 api\n"), initEvent(1, "done", 1),
		initEvent(2, "start", 0), output(2, "stdout", "partial")} {
		o.Record(f)
	}
	if o.Failed != nil || !o.Running || o.Tail != "partial" {
		t.Errorf("%+v", o)
	}
}

// One frame of several MiB keeps only its end, cut on a rune.
func TestInitOutputKeepsTheEndOfAHugeFrame(t *testing.T) {
	var o InitOutput
	o.Record(initEvent(1, "start", 0))
	o.Record(output(1, "stdout", "earlier\n"))
	huge := output(1, "stdout", strings.Repeat("é", 3<<20)+"the end")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	o.Record(huge)
	runtime.ReadMemStats(&after)
	if copied := after.TotalAlloc - before.TotalAlloc; copied > 4*initOutputMax {
		t.Errorf("allocated %d bytes to keep %d", copied, initOutputMax)
	}
	if len(o.Tail) > initOutputMax || !strings.HasSuffix(o.Tail, "the end") || !utf8.ValidString(o.Tail) {
		t.Fatalf("tail of %d bytes, ends %q, valid %v", len(o.Tail), o.Tail[max(0, len(o.Tail)-10):], utf8.ValidString(o.Tail))
	}
	if strings.Contains(o.Tail, "earlier") {
		t.Error("kept what came before a frame larger than the limit")
	}
	o.Record(output(1, "stderr", "é and more"))
	if len(o.Tail) > initOutputMax || !strings.HasSuffix(o.Tail, "the endé and more") || !utf8.ValidString(o.Tail) {
		t.Errorf("tail of %d bytes after a small frame, valid %v", len(o.Tail), utf8.ValidString(o.Tail))
	}
}

// A Run whose init a setup script failed fails with the sentence naming
// the repository, in runs.error and run.failed, the script's output in the
// event's setup; a project secret it printed is redacted in both. Each
// frame is a batch of its own: what init printed outlives a batch.
func TestASetupFailureFailsTheRunNamingItsRepository(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const value = "setup-test-not-a-real-token-1"
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state, project_secret_names)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lrun_1', 'running', '{TOKEN}')`,
		`INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_'||$1, $1, 'TOKEN', '` + value + `')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	r := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "implement", Status: statusRunning, LuxState: "running"}
	failed, _ := json.Marshal(map[string]any{"state": "failed", "reason": "exit code 1"})
	for _, f := range []lux.Frame{
		initEvent(1, "start", 0),
		output(1, "stderr", setupRunningPrefix+"api\n"),
		output(1, "stdout", "logging in with "+value+"\n"),
		output(1, "stderr", "npm ERR! 401 for "+value+"\n"+setupFailedPrefix+"1 api\n"),
		initEvent(1, "done", 1),
		{Kind: "lux", Epoch: 1, EventID: 9, EventType: "state", EventData: failed},
	} {
		tr := &translator{run: r}
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error { return tr.load(ctx, tx) }); err != nil {
			t.Fatal(err)
		}
		if err := s.applyBatch(ctx, tr, []lux.Frame{f}); err != nil {
			t.Fatal(err)
		}
	}
	const want = "The setup script of api failed with exit code 1: npm ERR! 401 for [REDACTED:TOKEN]"
	var status, runErr string
	if err := owner.QueryRow(ctx, `SELECT status::text, error FROM runs WHERE id = $1`, r.ID).Scan(&status, &runErr); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || runErr != want {
		t.Errorf("run %s, error %q\nwant %q", status, runErr, want)
	}
	var payload map[string]any
	var raw []byte
	if err := owner.QueryRow(ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, r.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &payload)
	setup, _ := payload["setup"].(map[string]any)
	wantOutput := setupRunningPrefix + "api\nlogging in with [REDACTED:TOKEN]\nnpm ERR! 401 for [REDACTED:TOKEN]"
	if payload["error"] != want || setup["repository"] != "api" || setup["exitCode"] != float64(1) || setup["output"] != wantOutput {
		t.Errorf("run.failed %s", raw)
	}
	if strings.Contains(string(raw), value) {
		t.Error("run.failed holds the secret")
	}
}

// setupStageRun is a running implementer Run given the project secret
// value, for the setup stage's tests.
func setupStageRun(t *testing.T, value string) (*Syncer, phaseRun, *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state, project_secret_names)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lrun_1', 'running', '{TOKEN}')`,
		`INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_'||$1, $1, 'TOKEN', '` + value + `')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	r := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "implement", Status: statusRunning, LuxState: "running"}
	return &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, r, owner
}

// applyEach applies each frame as a batch of its own, at the time at gives it.
func applyEach(t *testing.T, s *Syncer, r phaseRun, frames []lux.Frame, at []time.Time) {
	t.Helper()
	ctx := context.Background()
	for i, f := range frames {
		s.Now = func() time.Time { return at[i] }
		tr := &translator{run: r}
		if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error { return tr.load(ctx, tx) }); err != nil {
			t.Fatal(err)
		}
		if err := s.applyBatch(ctx, tr, []lux.Frame{f}); err != nil {
			t.Fatal(err)
		}
	}
}

type stageEvent struct {
	Type    string
	Payload map[string]any
}

func stageEvents(t *testing.T, owner *pgx.Conn, runID string) []stageEvent {
	t.Helper()
	rows, err := owner.Query(context.Background(), `SELECT event_type, payload FROM events WHERE run_id = $1
		AND event_type IN ('run.stage', 'run.setup.progress') ORDER BY cursor`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []stageEvent
	for rows.Next() {
		var e stageEvent
		var raw []byte
		if err := rows.Scan(&e.Type, &raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &e.Payload)
		out = append(out, e)
	}
	return out
}

// A setup script that runs is a start stage: setup, naming the repository
// from init's marker line, then its latest line no more often than every
// 3 s and redacted, then the agent's stage once init is done.
func TestASetupScriptIsAStartStageWithItsLatestLine(t *testing.T) {
	const value = "setup-stage-not-a-real-token-1"
	s, r, owner := setupStageRun(t, value)
	t0 := time.Date(2026, 11, 24, 12, 2, 8, 0, time.UTC)
	sec := func(n float64) time.Time { return t0.Add(time.Duration(n * float64(time.Second))) }
	frames := []lux.Frame{
		initEvent(1, "start", 0),
		output(1, "stderr", setupRunningPrefix+"abs\n"),
		output(1, "stdout", "abs setup: npm ci (backend)\n"),
		output(1, "stdout", "abs setup: npm ci (frontend)\n"),
		output(1, "stdout", "npm notice using "+value+"\n"),
		output(1, "stdout", "\x1b[32mabs setup: prisma generate\x1b[0m\n\n"),
		output(1, "stdout", "abs setup: "+strings.Repeat("y", 300)+"\n"),
		output(1, "stdout", "abs setup: generate:api\n"),
		initEvent(1, "done", 0),
	}
	at := []time.Time{sec(0), sec(0.5), sec(1), sec(2), sec(4.5), sec(5), sec(8), sec(9), sec(276)}
	applyEach(t, s, r, frames, at)
	got := stageEvents(t, owner, r.ID)
	var lines []string
	for _, e := range got {
		if e.Type == EvRunSetupProgress {
			lines = append(lines, e.Payload["line"].(string))
			if e.Payload["repository"] != "abs" {
				t.Errorf("progress names %v", e.Payload["repository"])
			}
		}
	}
	if len(got) < 2 || got[0].Type != EvRunStage || got[0].Payload["stage"] != "setup" || got[0].Payload["repository"] != "abs" {
		t.Fatalf("first events %+v", got)
	}
	if since, _ := got[0].Payload["since"].(string); !strings.HasPrefix(since, "2026-11-24T12:02:08.5") {
		t.Errorf("setup since %q", since)
	}
	last := got[len(got)-1]
	if last.Type != EvRunStage || last.Payload["stage"] != "running" || last.Payload["setup"] != true {
		t.Errorf("last event %+v", last)
	}
	// 1 s: the first line; 2 s: within 3 s, held; 4.5 s: the redacted token line; 5 s: held; 8 s: the long line cut.
	want := []string{"abs setup: npm ci (backend)", "npm notice using [REDACTED:TOKEN]", "abs setup: " + strings.Repeat("y", setupLineMax-len("abs setup: ")-1) + "…"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines\n%q\nwant\n%q", lines, want)
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > setupLineMax {
			t.Errorf("line of %d characters", utf8.RuneCountInString(l))
		}
	}
	var raw string
	if err := owner.QueryRow(context.Background(), `SELECT string_agg(payload::text, ' ') FROM events WHERE run_id = $1`, r.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, value) {
		t.Error("an event holds the secret")
	}
	// The API keeps reporting the start, though lux says running, until the agent's first record.
	stageNow := func() string {
		var stage *string
		if err := owner.QueryRow(context.Background(), `SELECT `+StartStageJSON+`->>'stage' FROM runs r WHERE r.id = $1`, r.ID).Scan(&stage); err != nil {
			t.Fatal(err)
		}
		if stage == nil {
			return ""
		}
		return *stage
	}
	if got := stageNow(); got != "running" {
		t.Errorf("start stage after setup %q, want running", got)
	}
	if err := s.DB.InOrg(context.Background(), r.Org, func(tx pgx.Tx) error {
		return s.event(context.Background(), tx, r, evSessionStarted, "agent", map[string]any{"role": "implement"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := stageNow(); got != "" {
		t.Errorf("start stage once the agent is up %q", got)
	}
}

// An init that runs no script writes no stage: no manifest, a resume whose
// marker skips the script, or a repository with no checkout.
func TestAnInitThatRunsNoScriptWritesNoSetupStage(t *testing.T) {
	for name, frames := range map[string][]lux.Frame{
		"no manifest":           {initEvent(1, "start", 0), initEvent(1, "done", 0)},
		"skipped by its marker": {initEvent(2, "start", 0), output(2, "stdout", "instructions written\n"), initEvent(2, "done", 0)},
		"no checkout":           {initEvent(1, "start", 0), output(1, "stderr", "dude: abs has no checkout at abs; its setup script is skipped\n"), initEvent(1, "done", 0)},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, owner := setupStageRun(t, "setup-stage-not-a-real-token-2")
			at := make([]time.Time, len(frames))
			for i := range at {
				at[i] = time.Date(2026, 11, 24, 12, 0, i*5, 0, time.UTC)
			}
			applyEach(t, s, r, frames, at)
			if got := stageEvents(t, owner, r.ID); len(got) != 0 {
				t.Errorf("wrote %+v", got)
			}
		})
	}
}

// A setup script that fails keeps the failure path: no agent stage after
// it, the Run failed naming the repository.
func TestAFailedSetupWritesNoAgentStage(t *testing.T) {
	s, r, owner := setupStageRun(t, "setup-stage-not-a-real-token-3")
	failed, _ := json.Marshal(map[string]any{"state": "failed", "reason": "exit code 1"})
	frames := []lux.Frame{
		initEvent(1, "start", 0),
		output(1, "stderr", setupRunningPrefix+"abs\n"),
		output(1, "stderr", "npm ERR! boom\n"+setupFailedPrefix+"1 abs\n"),
		initEvent(1, "done", 1),
		{Kind: "lux", Epoch: 1, EventID: 9, EventType: "state", EventData: failed},
	}
	at := make([]time.Time, len(frames))
	for i := range at {
		at[i] = time.Date(2026, 11, 24, 12, 0, i*5, 0, time.UTC)
	}
	applyEach(t, s, r, frames, at)
	got := stageEvents(t, owner, r.ID)
	if len(got) == 0 || got[0].Payload["stage"] != "setup" {
		t.Fatalf("events %+v", got)
	}
	for _, e := range got {
		if e.Type == EvRunStage && e.Payload["stage"] == "running" {
			t.Errorf("the agent's stage after a failed setup: %+v", e)
		}
	}
	var runErr string
	if err := owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, r.ID).Scan(&runErr); err != nil {
		t.Fatal(err)
	}
	if runErr != "The setup script of abs failed with exit code 1: npm ERR! boom" {
		t.Errorf("error %q", runErr)
	}
}

// The latest line: after the marker, as a terminal shows it, never one cut at the tail's start.
func TestSetupLine(t *testing.T) {
	for _, tc := range []struct {
		tail string
		cut  bool
		want string
	}{
		{setupRunningPrefix + "abs\n", false, ""},
		{"earlier\n" + setupRunningPrefix + "abs\nnpm ci\n\n  \n", false, "npm ci"},
		{setupRunningPrefix + "abs\nprogress 10%\rprogress 90%\n", false, "progress 90%"},
		{"half a lin", true, ""},
		{"half a line\nwhole line\n", true, "whole line"},
		{setupRunningPrefix + "api\nfirst\n" + setupRunningPrefix + "web\n", false, ""},
		// A line still being written is not whole: a value in it may go on in the next frame.
		{setupRunningPrefix + "abs\nnpm ci\nnpm notice auth s3cr3t-token-v", false, "npm ci"},
		{setupRunningPrefix + "abs\nnpm notice auth s3cr3t-token-v", false, ""},
		{setupRunningPrefix + "abs\nnpm ci\nprogress 10%\rprogress 9", false, "progress 10%"},
		{"half a line\nwhole line\nhalf", true, "whole line"},
	} {
		if got := setupLine(tc.tail, tc.cut); got != tc.want {
			t.Errorf("setupLine(%q) = %q, want %q", tc.tail, got, tc.want)
		}
	}
	if got := setupRunning("x\n" + setupRunningPrefix + "api\n" + setupRunningPrefix + "web\nout\n"); got != "web" {
		t.Errorf("setupRunning %q", got)
	}
	if got := setupRunning("echo " + setupRunningPrefix + "api\n"); got != "" {
		t.Errorf("setupRunning mid-line %q", got)
	}
}

// assertNoSecretPrefix fails when a setup line written for runID, in an
// event or in harness_state's init, holds the first min or more bytes of
// value.
func assertNoSecretPrefix(t *testing.T, owner *pgx.Conn, runID, value string, min int) {
	t.Helper()
	var lines []string
	rows, err := owner.Query(context.Background(), `SELECT payload::text FROM events WHERE run_id = $1
		UNION ALL SELECT COALESCE(harness_state->'init'->>'line', '') FROM runs WHERE id = $1`, runID)
	if err != nil {
		t.Fatal(err)
	}
	lines, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if strings.Contains(l, value[:min]) {
			t.Errorf("holds a prefix of the secret: %s", l)
		}
	}
}

// A value split across two frames is never whole in the first: the line
// it is in is not written until its end comes, then redacted whole.
func TestASecretSplitAcrossFramesNeverReachesTheSetupLine(t *testing.T) {
	const value = "s3cr3t-token-value-not-real-1"
	s, r, owner := setupStageRun(t, value)
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	frames := []lux.Frame{
		initEvent(1, "start", 0),
		output(1, "stderr", setupRunningPrefix+"abs\n"),
		output(1, "stdout", "npm ci\nnpm notice auth "+value[:14]),
		output(1, "stdout", value[14:]+"\n"),
	}
	for i, f := range frames {
		applyEach(t, s, r, []lux.Frame{f}, []time.Time{t0.Add(time.Duration(i*4) * time.Second)})
		assertNoSecretPrefix(t, owner, r.ID, value, 6)
	}
	var lines []string
	for _, e := range stageEvents(t, owner, r.ID) {
		if e.Type == EvRunSetupProgress {
			lines = append(lines, e.Payload["line"].(string))
		}
	}
	if want := "npm ci|npm notice auth [REDACTED:TOKEN]"; strings.Join(lines, "|") != want {
		t.Errorf("lines %q, want %q", lines, want)
	}
}

// A tail cut inside a secret's rune must not publish its incomplete head line.
func TestASecretCutInsideARuneNeverReachesTheSetupLine(t *testing.T) {
	const suffix = "rd-not-real-secret-77"
	for _, runeText := range []string{"ö", "o", "\U0001F600"} {
		t.Run(runeText, func(t *testing.T) {
			value := "pässw" + runeText + suffix
			s, r, owner := setupStageRun(t, value)
			// Cut after the rune's first byte; UTF-8 rounding drops its remainder.
			dropped := len(runeText) - 1
			blanks := initOutputMax - dropped - len(suffix) - 1
			frames := []lux.Frame{
				initEvent(1, "start", 0),
				output(1, "stderr", setupRunningPrefix+"abs\n"),
				output(1, "stdout", "auth "+value+"\n"+strings.Repeat("\n", blanks)),
			}
			t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
			applyEach(t, s, r, frames, []time.Time{t0, t0.Add(time.Second), t0.Add(5 * time.Second)})
			var tail, line string
			if err := owner.QueryRow(context.Background(), `SELECT harness_state->'init'->>'tail',
				COALESCE(harness_state->'init'->>'line', '') FROM runs WHERE id = $1`, r.ID).Scan(&tail, &line); err != nil {
				t.Fatal(err)
			}
			if len(tail) != initOutputMax-dropped || !strings.HasPrefix(tail, suffix+"\n") {
				t.Fatalf("cut tail: %d bytes, want %d starting with %q", len(tail), initOutputMax-dropped, suffix)
			}
			if strings.Contains(line, suffix) {
				t.Errorf("persisted init.line holds secret suffix: %q", line)
			}
			if line != "" {
				t.Errorf("persisted incomplete line: %q", line)
			}
			for _, e := range stageEvents(t, owner, r.ID) {
				if e.Type == EvRunSetupProgress {
					raw, err := json.Marshal(e.Payload)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(raw), suffix) {
						t.Errorf("progress payload holds secret suffix: %s", raw)
					}
					t.Errorf("published incomplete line: %s", raw)
				}
			}
		})
	}
}

// A value that runs past setupLineMax is redacted before the line is
// shortened: no part of it is left to see. The Run's own redactor is the
// only one that knows it, and the ledger's redaction of the whole payload
// cannot match the part a shortening leaves, so the line written is what
// setupProgress made of it.
func TestASecretAtTheSetupLinesLimitIsRedactedBeforeItIsCut(t *testing.T) {
	const value = "s3cr3t-token-value-not-real-2"
	s, r, owner := setupStageRun(t, value)
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	frames := []lux.Frame{
		initEvent(1, "start", 0),
		output(1, "stderr", setupRunningPrefix+"abs\n"),
		output(1, "stdout", strings.Repeat("y", 185)+value+"\n"),
	}
	for i, f := range frames {
		applyEach(t, s, r, []lux.Frame{f}, []time.Time{t0.Add(time.Duration(i*4) * time.Second)})
		assertNoSecretPrefix(t, owner, r.ID, value, 10)
	}
	var line string
	if err := owner.QueryRow(context.Background(), `SELECT COALESCE(harness_state->'init'->>'line', '') FROM runs WHERE id = $1`, r.ID).Scan(&line); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, strings.Repeat("y", 185)+"[REDACTED:") || utf8.RuneCountInString(line) != setupLineMax {
		t.Errorf("line %q", line)
	}
}
