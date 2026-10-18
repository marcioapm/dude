package phases

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
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
