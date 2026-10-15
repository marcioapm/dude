package orchestrator_test

// Redaction at dude's boundaries: an agent given a project secret prints
// it (raw and encoded) in a tool's output, and passes it to dude's tools,
// in a finding, in its reply and thoughts, and in a published text file.
// lux redacts an agent Run's stream (v0.1.11 shim); the fake lux mirrors
// that only for servers, so here dude's own redaction is all that stands
// between the value and the database: every stored copy, and every answer
// the API gives, shows [REDACTED:NAME].

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// leaks are the value in the forms dude must catch.
func leaks(v string) []string {
	return []string{v, base64.StdEncoding.EncodeToString([]byte(v)), hex.EncodeToString([]byte(v))}
}

// leakingReviewer makes the task's reviewer, as scripted otherwise, print
// value in every way an agent's text reaches dude.
func (w *world) leakingReviewer(value string) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] != "review" {
			return b
		}
		l := leaks(value)
		b.Tools = []string{"bash"}
		b.ToolOutput = map[string]string{"bash": "SNOWFLAKE_PASSWORD=" + l[0] + "\nb64 " + l[1] + "\nhex " + l[2]}
		b.Thought = "the password is " + value
		b.Reply = "Checked the connection with " + value + ".\n\n```yaml\n---\nseverity: blocking\ncategory: correctness\n" +
			"title: Password " + value + " is hard-coded\ndescription: Found " + l[1] + " in config.\n```\n"
		b.Publish = map[string]string{"notes.md": "connected with " + value + "\n"}
		return b
	}
}

// What an agent passes to dude's own tools reaches dude over lux's service
// proxy, not its redacted stream: the call's record, and what the tool
// writes (a question to a person), hold the marker.
func TestAnAgentsSecretPassedToDudesToolsIsRedacted(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	w.grant("IMPL_TOKEN", implToken, "implementer")
	_, runID, _ := w.liveImplementer()
	status, body := w.callTool(tools.URL, w.luxSpecOf("implement"), "ask_person",
		`{"question":"Is `+implToken+` the right token? (hex `+hex.EncodeToString([]byte(implToken))+`)"}`)
	if status >= 300 {
		t.Fatalf("ask_person: %d %s", status, body)
	}
	var prompt string
	_ = w.owner.QueryRow(context.Background(), `SELECT prompt FROM questions WHERE run_id = $1`, runID).Scan(&prompt)
	if prompt != "Is [REDACTED:IMPL_TOKEN] the right token? (hex [REDACTED:IMPL_TOKEN])" {
		t.Errorf("question stored as %q", prompt)
	}
	for _, l := range leaks(implToken) {
		if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%' || $2 || '%'`, runID, l); n != 0 {
			t.Errorf("%d events hold the value as %q", n, l)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.tool.dude'
		AND payload->'arguments'->>'question' LIKE '%[REDACTED:IMPL_TOKEN]%'`, runID); n != 1 {
		t.Errorf("%d tool call records show the marker", n)
	}
}

func TestAnAgentsSecretIsRedactedEverywhereDudeStoresOrServesIt(t *testing.T) {
	w := newWorld(t)
	w.grant("SNOWFLAKE_PASSWORD", snowflakePassword, "reviewer")
	w.leakingReviewer(snowflakePassword)
	task := w.task()
	w.deliver(task)
	var review string
	w.until("the reviewer's finding and notes recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at LIMIT 1`, task).Scan(&review)
		return review != "" && w.count(`SELECT count(*) FROM review_findings WHERE run_id = $1`, review) > 0 &&
			w.count(`SELECT count(*) FROM artifacts WHERE run_id = $1 AND name = 'notes.md'`, review) == 1
	})

	stored := map[string]string{}
	read := func(what, sql string, args ...any) {
		var s *string
		if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if s != nil {
			stored[what] = *s
		}
	}
	read("events", `SELECT string_agg(payload::text, E'\n') FROM events WHERE run_id = $1`, review)
	read("findings", `SELECT string_agg(title || ' ' || description, E'\n') FROM review_findings WHERE run_id = $1`, review)
	read("runs", `SELECT COALESCE(error, '') || harness_state::text FROM runs WHERE id = $1`, review)
	for what, text := range stored {
		for _, l := range leaks(snowflakePassword) {
			if strings.Contains(text, l) {
				t.Errorf("%s holds the value (as %q)", what, l)
			}
		}
	}
	for what, marker := range map[string]string{"events": "[REDACTED:SNOWFLAKE_PASSWORD]", "findings": "Password [REDACTED:SNOWFLAKE_PASSWORD] is hard-coded"} {
		if !strings.Contains(stored[what], marker) {
			t.Errorf("%s lacks %q", what, marker)
		}
	}
	for _, typ := range []string{"agent.tool.completed", "agent.message", "agent.thought"} {
		if w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload::text LIKE '%[REDACTED:SNOWFLAKE_PASSWORD]%'`, review, typ) == 0 {
			t.Errorf("no %s shows the marker", typ)
		}
	}

	// The published notes, served redacted, with lux's checksum dropped.
	var artifact string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM artifacts WHERE run_id = $1 AND name = 'notes.md'`, review).Scan(&artifact)
	req, _ := http.NewRequest("GET", w.api+"/internal/artifacts/"+artifact+"/content", nil)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "connected with [REDACTED:SNOWFLAKE_PASSWORD]\n" {
		t.Errorf("notes served %d %q", res.StatusCode, body)
	}
	if res.Header.Get("X-Content-SHA256") != "" || res.ContentLength != int64(len(body)) {
		t.Errorf("served with sha %q and length %d for %d bytes", res.Header.Get("X-Content-SHA256"), res.ContentLength, len(body))
	}
}

// The value rotated while the Run holds the old one: the old value, the
// one the Run was given, is still what dude redacts, from the Run's copy.
func TestARotationMidRunStillRedactsTheValueTheRunWasGiven(t *testing.T) {
	w := newWorld(t)
	w.grant("SNOWFLAKE_PASSWORD", snowflakePassword, "reviewer")
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			// Rotated as the reviewer starts, before it prints anything.
			mustExec(t, w.owner, `UPDATE project_secrets SET value = 'sf-test-ROTATED-password-999' WHERE project_id = $1`, w.project)
			b.Reply = "Connected with " + snowflakePassword + ". Nothing to add."
		}
		return b
	}
	task := w.task()
	w.deliver(task)
	var review string
	w.until("the reviewer's reply", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND turn_done_at IS NOT NULL LIMIT 1`, task).Scan(&review)
		return review != ""
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%' || $2 || '%'`, review, snowflakePassword); n != 0 {
		t.Errorf("%d events hold the value the Run was given before the rotation", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message' AND payload->>'text' LIKE 'Connected with [REDACTED:SNOWFLAKE_PASSWORD]%'`, review); n == 0 {
		t.Error("the reply does not show the marker")
	}
}
