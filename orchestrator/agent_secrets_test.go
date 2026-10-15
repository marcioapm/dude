package orchestrator_test

// Project secrets given to agent Runs: each secret names the phases whose
// Runs receive it (project_secrets.recipients, migration 127). A Run of a
// ticked phase declares it as a lux env secret, records its name, keeps a
// copy of the exact value in run_project_secrets for redaction and push
// scanning, and writes one run.secrets.given naming it. The conductor and
// a session's brainstorm never receive one, whatever is ticked.

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

const (
	snowflakePassword = "sf-test-not-a-real-password-123"
	implToken         = "impl-test-not-a-real-token-456"
	everyoneKey       = "all-test-not-a-real-key-789"
)

// grant sets a project secret given to recipients.
func (w *world) grant(name, value string, recipients ...string) {
	w.t.Helper()
	w.secret(name, value)
	mustExec(w.t, w.owner, `UPDATE project_secrets SET recipients = $3 WHERE project_id = $1 AND name = $2`, w.project, name, recipients)
}

// projectSecretsOf is a lux Run's declared project secrets: its env
// secrets that are not dude's own.
func projectSecretsOf(spec lux.Spec) map[string]string {
	out := envSecrets(spec)
	for _, own := range []string{"DUDE_LLM_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_CONFIG"} {
		delete(out, own)
	}
	return out
}

// runCopy is the per-Run copy of the values the Run was given.
func (w *world) runCopy(runID string) map[string]string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT name, value FROM run_project_secrets WHERE run_id = $1`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]string{}
	var name, value string
	if _, err := pgx.ForEachRow(rows, []any{&name, &value}, func() error {
		out[name] = value
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) givenNames(runID string) []string {
	w.t.Helper()
	var names []string
	if err := w.owner.QueryRow(context.Background(), `SELECT project_secret_names FROM runs WHERE id = $1`, runID).Scan(&names); err != nil {
		w.t.Fatal(err)
	}
	return names
}

// givenEvents are the payloads of the Run's run.secrets.given events, as stored.
func (w *world) givenEvents(runID string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload::text FROM events WHERE run_id = $1 AND event_type = 'run.secrets.given' ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) luxRunFor(runID string) *fakelux.Run {
	for _, r := range w.luxRuns() {
		if submitted(w.t, r).Labels["dude.run"] == runID {
			return r
		}
	}
	return nil
}

// Every phase of a delivery with the tester on gets exactly the secrets
// ticked for it: the tester its own, the implementer its own (not the
// fixer, though it runs on the implementer's settings), the reviewer, fixer
// and simplifier only the one ticked for everyone. Each Run records the
// names and writes one event naming them, never a value.
func TestEachPhaseRunReceivesTheSecretsTickedForIt(t *testing.T) {
	w := newWorld(t)
	w.grant("SNOWFLAKE_PASSWORD", snowflakePassword, "previews", "tester")
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.grant("EVERYONE_KEY", everyoneKey, "previews", "tester", "implementer", "fixer", "simplifier", "reviewer")
	task := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	want := map[string][]string{
		"implement": {"EVERYONE_KEY", "IMPL_TOKEN"},
		"review":    {"EVERYONE_KEY"},
		"fix":       {"EVERYONE_KEY"},
		"simplify":  {"EVERYONE_KEY"},
		"test":      {"EVERYONE_KEY", "SNOWFLAKE_PASSWORD"},
	}
	values := map[string]string{"SNOWFLAKE_PASSWORD": snowflakePassword, "IMPL_TOKEN": implToken, "EVERYONE_KEY": everyoneKey}
	rows, err := w.owner.Query(context.Background(), `SELECT id, phase::text FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL`, task)
	if err != nil {
		t.Fatal(err)
	}
	type phaseRun struct{ ID, Phase string }
	runs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[phaseRun])
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, pr := range runs {
		r := struct{ id, phase string }{pr.ID, pr.Phase}
		names, ok := want[r.phase]
		if !ok {
			continue
		}
		seen[r.phase] = true
		lr := w.luxRunFor(r.id)
		if lr == nil {
			t.Fatalf("no lux Run for %s", r.phase)
		}
		got := projectSecretsOf(submitted(t, lr))
		if !slices.Equal(slices.Sorted(maps.Keys(got)), names) {
			t.Errorf("%s declares %v, want %v", r.phase, slices.Sorted(maps.Keys(got)), names)
		}
		for _, n := range names {
			if got[n] != values[n] {
				t.Errorf("%s got %s with another value", r.phase, n)
			}
		}
		if rec := w.givenNames(r.id); !slices.Equal(rec, names) {
			t.Errorf("%s recorded %v, want %v", r.phase, rec, names)
		}
		evs := w.givenEvents(r.id)
		if len(evs) != 1 {
			t.Fatalf("%s wrote %d run.secrets.given, want 1", r.phase, len(evs))
		}
		var payload struct{ Names []string }
		_ = json.Unmarshal([]byte(evs[0]), &payload)
		if !slices.Equal(payload.Names, names) {
			t.Errorf("%s event names %v, want %v", r.phase, payload.Names, names)
		}
		for _, v := range values {
			if strings.Contains(evs[0], v) {
				t.Errorf("%s event carries a value: %s", r.phase, evs[0])
			}
		}
	}
	for phase := range want {
		if !seen[phase] {
			t.Errorf("no %s Run", phase)
		}
	}
}

// While the implementer runs, its copy holds exactly the values it was
// given: what dude redacts its output with and scans its push for.
func TestARunningAgentsCopyHoldsExactlyTheValuesItWasGiven(t *testing.T) {
	w := newWorld(t)
	w.grant("SNOWFLAKE_PASSWORD", snowflakePassword, "previews", "tester")
	w.grant("IMPL_TOKEN", implToken, "implementer")
	_, runID, _ := w.liveImplementer()
	if got := w.runCopy(runID); len(got) != 1 || got["IMPL_TOKEN"] != implToken {
		t.Errorf("copy = %v names, want IMPL_TOKEN only", slices.Sorted(maps.Keys(got)))
	}
	if got := projectSecretsOf(submitted(t, w.luxRunFor(runID))); len(got) != 1 || got["IMPL_TOKEN"] != implToken {
		t.Errorf("implementer declares %v", slices.Sorted(maps.Keys(got)))
	}
}

const implTokenRotated = "impl-test-not-a-real-token-ROTATED"

// Paused, the implementer's secret is rotated and another is ticked for it:
// the resume sends the recorded name's current value and nothing added
// since, and the Run's copy becomes exactly what the new process has.
func TestAResumeSuppliesCurrentValuesAndReplacesTheCopy(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	_, runID, _ := w.liveImplementer()
	w.pausePhase(runID)
	w.until("paused", func() bool { return w.personallyPaused(runID) })
	w.grant("IMPL_TOKEN", implTokenRotated, "implementer")
	w.grant("EVERYONE_KEY", everyoneKey, "implementer")
	w.resumePhase(runID)
	lr := w.luxRunFor(runID)
	if len(lr.ResumeSecrets) != 1 {
		t.Fatalf("%d resumes", len(lr.ResumeSecrets))
	}
	sent := secretsByName(lr.ResumeSecrets[0])
	if sent["IMPL_TOKEN"] != implTokenRotated {
		t.Errorf("the resume did not send IMPL_TOKEN's current value")
	}
	if _, ok := sent["EVERYONE_KEY"]; ok {
		t.Error("the resume sent a secret added after the Run was submitted")
	}
	if got := w.runCopy(runID); len(got) != 1 || got["IMPL_TOKEN"] != implTokenRotated {
		t.Errorf("copy after the resume has %v, want IMPL_TOKEN at its current value", slices.Sorted(maps.Keys(got)))
	}
	if got := w.givenNames(runID); !slices.Equal(got, []string{"IMPL_TOKEN"}) {
		t.Errorf("names after the resume = %v", got)
	}
}

// Paused, a secret the implementer was submitted with is removed, or no
// longer ticked for it: lux would refuse a resume without it, so the Run
// is retired and a fresh one takes its slot with the project's secrets now,
// as a preview is replaced; neither Run is failed.
func TestAResumeMissingARecordedSecretStartsAFreshRun(t *testing.T) {
	for _, how := range []string{"removed", "withdrawn"} {
		t.Run(how, func(t *testing.T) {
			w := newWorld(t)
			w.grant("IMPL_TOKEN", implToken, "implementer")
			w.grant("EVERYONE_KEY", everyoneKey, "implementer")
			task, runID, _ := w.liveImplementer()
			w.pausePhase(runID)
			w.until("paused", func() bool { return w.personallyPaused(runID) })
			if how == "removed" {
				w.removeSecret("IMPL_TOKEN")
			} else {
				w.grant("IMPL_TOKEN", implToken, "previews", "tester")
			}
			if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
				t.Fatalf("resume: %d %v", status, body)
			}
			var next string
			w.until("a fresh implementer running", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND id <> $2
					AND status = 'running'`, task, runID).Scan(&next)
				return next != ""
			})
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND replaced_by = $2`, runID, next); n != 1 {
				t.Errorf("the old Run was not retired for the fresh one")
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.failed'`, task); n != 0 {
				t.Errorf("%d run.failed; a retired Run is no failure", n)
			}
			if lr := w.luxRunFor(runID); lr.Resumed != 0 {
				t.Errorf("the old lux Run was resumed %d times", lr.Resumed)
			}
			if got := projectSecretsOf(submitted(t, w.luxRunFor(next))); len(got) != 1 || got["EVERYONE_KEY"] != everyoneKey {
				t.Errorf("fresh Run declares %v", slices.Sorted(maps.Keys(got)))
			}
			if got := w.runCopy(next); len(got) != 1 || got["EVERYONE_KEY"] != everyoneKey {
				t.Errorf("fresh Run's copy has %v", slices.Sorted(maps.Keys(got)))
			}
		})
	}
}

// A project secret ticked for every recipient never reaches the task's
// conductor: not in its spec, its record, its copy or its events.
func TestTheConductorNeverReceivesAProjectSecret(t *testing.T) {
	w := conductorWorld(t)
	w.grant("EVERYONE_KEY", everyoneKey, "previews", "tester", "implementer", "fixer", "simplifier", "reviewer")
	task := w.task()
	if status, out := w.chat(task, "what is this task about?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	var id string
	w.until("the conductor in lux", func() bool {
		id, _, _ = w.conductor(task)
		return id != "" && w.luxRunFor(id) != nil
	})
	assertNoProjectSecret(t, w.world(), id, string(w.luxRunFor(id).Spec))
}

// Nor a session's brainstorm, whose session names the project.
func TestABrainstormNeverReceivesAProjectSecret(t *testing.T) {
	s := newSessionWorld(t)
	s.grant("EVERYONE_KEY", everyoneKey, "previews", "tester", "implementer", "fixer", "simplifier", "reviewer")
	session := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "hello"})
	id := s.started(session)
	assertNoProjectSecret(t, s.world, id, s.luxRun(id).raw)
}

func (w *world) world() *world { return w }

func assertNoProjectSecret(t *testing.T, w *world, runID, rawSpec string) {
	t.Helper()
	if strings.Contains(rawSpec, "EVERYONE_KEY") || strings.Contains(rawSpec, everyoneKey) {
		t.Errorf("Run %s was given the project secret", runID)
	}
	if got := w.givenNames(runID); len(got) != 0 {
		t.Errorf("Run %s recorded %v", runID, got)
	}
	if got := w.runCopy(runID); len(got) != 0 {
		t.Errorf("Run %s has a copy of %d values", runID, len(got))
	}
	if n := len(w.givenEvents(runID)); n != 0 {
		t.Errorf("Run %s wrote %d run.secrets.given", runID, n)
	}
}
