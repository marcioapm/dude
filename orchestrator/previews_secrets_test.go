package orchestrator_test

// A project's preview secrets (migration 081) through the orchestrator's
// real code: every preview Run declares them as env secrets and records
// their names; a resume or wake sends the current value of each recorded
// name and nothing added since (lux v0.1.11 ignores an undeclared name and
// requires every declared one); a recorded name removed since replaces
// the Run, which is no failed start. An agent Run gets none of them.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// secret sets a project secret, as the API's add or replace leaves it.
func (w *world) secret(name, value string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ($1, $2, $3, $4, right($4, 4))
		ON CONFLICT (project_id, name) DO UPDATE SET value = EXCLUDED.value, hint = EXCLUDED.hint, updated_at = now()`,
		w.project, w.org, name, value)
}

func (w *world) removeSecret(name string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `DELETE FROM project_secrets WHERE project_id = $1 AND name = $2`, w.project, name)
}

// recorded is the secret names dude recorded for the preview's lux Run.
func (w *world) recorded(runID string) []string {
	w.t.Helper()
	var names []string
	if err := w.owner.QueryRow(context.Background(), `SELECT preview_secrets FROM runs WHERE id = $1`, runID).Scan(&names); err != nil {
		w.t.Fatal(err)
	}
	return names
}

// envSecrets are a spec's env secrets, name to value.
func envSecrets(spec lux.Spec) map[string]string {
	out := map[string]string{}
	for _, s := range spec.Secrets {
		if s.As == "env" {
			out[s.Name] = s.Value
		}
	}
	return out
}

func secretsByName(list []lux.Secret) map[string]string {
	out := map[string]string{}
	for _, s := range list {
		out[s.Name] = s.Value
	}
	return out
}

const (
	seedKey    = "sk-test-SEED-3f9a"
	seedKeyNew = "sk-test-SEED-ROTATED-e2b8"
	stripeKey  = "sk_test_STRIPE_x7Qb"
	pem        = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n"
)

// A preview on the eager path: submitted with every secret as an env
// secret beside GIT_TOKEN, the names recorded, and its server's process
// has them. Parked, then a value changes and one is added: the resume
// sends the recorded names' current values only.
func TestAPreviewRunGetsTheProjectsSecretsAndAResumeTheirCurrentValues(t *testing.T) {
	w := newWorld(t)
	w.previews.Minute = time.Hour // parked only when the test says
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.secret("SEED_LLM_KEY", seedKey)
	w.secret("SIGNING_KEY_PEM", pem)
	task := w.task()
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("web ready", func() bool {
		runs := w.luxRuns()
		return len(runs) == 1 && w.lux.ServerStates(runs[0].ID)["web"] == "ready"
	})
	r := w.luxRuns()[0]
	spec := submitted(t, r)
	if got := envSecrets(spec); len(got) != 2 || got["SEED_LLM_KEY"] != seedKey || got["SIGNING_KEY_PEM"] != pem {
		t.Fatalf("env secrets submitted = %v", got)
	}
	if !slices.ContainsFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "GIT_TOKEN" && s.As == "" }) {
		t.Errorf("GIT_TOKEN not declared as before: %+v", spec.Secrets)
	}
	if got := w.recorded(runID); !slices.Equal(got, []string{"SEED_LLM_KEY", "SIGNING_KEY_PEM"}) {
		t.Errorf("recorded %v", got)
	}
	env := w.lux.ServerEnv(r.ID, "web")
	if env["SEED_LLM_KEY"] != seedKey || env["SIGNING_KEY_PEM"] != pem || env["PORT"] != "1" {
		t.Errorf("web's environment = %v", env)
	}
	if _, ok := env["GIT_TOKEN"]; ok {
		t.Error("the git credential reached the server's environment")
	}

	// Parked; a value replaced and a secret added meanwhile.
	mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
	w.previews.Minute = time.Millisecond
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	w.secret("SEED_LLM_KEY", seedKeyNew)
	w.secret("STRIPE_TEST_KEY", stripeKey)
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code >= 300 {
		t.Fatalf("start web on the parked preview = %d %v", code, body)
	}
	w.until("web ready again", func() bool { return r.Resumed == 1 && w.lux.ServerStates(r.ID)["web"] == "ready" })
	sent := secretsByName(r.ResumeSecrets[0])
	if sent["SEED_LLM_KEY"] != seedKeyNew || sent["SIGNING_KEY_PEM"] != pem || sent["GIT_TOKEN"] == "" {
		t.Errorf("resume sent %v", sent)
	}
	if _, ok := sent["STRIPE_TEST_KEY"]; ok {
		t.Error("the resume sent a secret added after the Run was submitted")
	}
	if !strings.Contains(string(r.ResumeSecretsRaw[0]), `"as":"env"`) {
		t.Errorf("resume secrets as sent: %s", r.ResumeSecretsRaw[0])
	}
	if env := w.lux.ServerEnv(r.ID, "web"); env["SEED_LLM_KEY"] != seedKeyNew || env["STRIPE_TEST_KEY"] != "" {
		t.Errorf("web's environment after the resume = %v", env)
	}
	if len(w.luxRuns()) != 1 {
		t.Errorf("%d lux runs; want the one resumed", len(w.luxRuns()))
	}
}

// A secret the eager preview's Run was submitted with, removed while it
// was parked: lux would refuse a resume without it, so a new Run is
// submitted with the project's secrets now, the old one cancelled.
func TestAParkedPreviewWhoseSecretWasRemovedStartsANewRun(t *testing.T) {
	w := newWorld(t)
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.secret("SEED_LLM_KEY", seedKey)
	w.secret("STRIPE_TEST_KEY", stripeKey)
	task := w.task()
	_, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("web ready", func() bool {
		runs := w.luxRuns()
		return len(runs) == 1 && w.lux.ServerStates(runs[0].ID)["web"] == "ready"
	})
	old := w.luxRuns()[0]
	mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
	w.previews.Minute = time.Millisecond
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	w.removeSecret("STRIPE_TEST_KEY")
	if code, body := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code >= 300 {
		t.Fatalf("start web = %d %v", code, body)
	}
	w.until("a new Run serving", func() bool {
		runs := w.luxRuns()
		return len(runs) == 2 && w.lux.ServerStates(runs[1].ID)["web"] == "ready" &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id = $2`, runID, runs[1].ID) == 1
	})
	if old.Resumed != 0 || !slices.Contains(w.lux.CallsOf(old.ID), "cancel") {
		t.Errorf("old Run resumed %d, calls %v; want it cancelled, not resumed", old.Resumed, w.lux.CallsOf(old.ID))
	}
	next := w.luxRuns()[1]
	if got := envSecrets(submitted(t, next)); len(got) != 1 || got["SEED_LLM_KEY"] != seedKey {
		t.Errorf("the new Run's env secrets = %v", got)
	}
	if got := w.recorded(runID); !slices.Equal(got, []string{"SEED_LLM_KEY"}) {
		t.Errorf("recorded %v", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 0 AND error IS NULL`, runID); n != 1 {
		t.Errorf("the replacement was counted as a failure: %s", w.preview(runID))
	}
}

// A wakeable preview: its Run, submitted at the first wake, declares every
// secret; asleep, a value changes and a secret is added; the next wake
// resumes it with the recorded names' current values only, and its
// server's process has the new value.
func TestAWakeableRunGetsTheSecretsAndAWakeTheirCurrentValues(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.secret("SEED_LLM_KEY", seedKey)
	w.secret("SIGNING_KEY_PEM", pem)
	runID, web := w.asleepPreview()
	r := w.luxRuns()[0]
	if got := envSecrets(submitted(t, r)); len(got) != 2 || got["SEED_LLM_KEY"] != seedKey || got["SIGNING_KEY_PEM"] != pem {
		t.Fatalf("env secrets submitted = %v", got)
	}
	if got := w.recorded(runID); !slices.Equal(got, []string{"SEED_LLM_KEY", "SIGNING_KEY_PEM"}) {
		t.Errorf("recorded %v", got)
	}

	w.secret("SEED_LLM_KEY", seedKeyNew)
	w.secret("STRIPE_TEST_KEY", stripeKey)
	w.open(web)
	if r.Resumed != 1 || len(w.luxRuns()) != 1 {
		t.Fatalf("resumed %d, %d lux runs; want the one Run resumed", r.Resumed, len(w.luxRuns()))
	}
	sent := secretsByName(r.ResumeSecrets[0])
	if sent["SEED_LLM_KEY"] != seedKeyNew || sent["SIGNING_KEY_PEM"] != pem || sent["GIT_TOKEN"] == "" {
		t.Errorf("wake sent %v", sent)
	}
	if _, ok := sent["STRIPE_TEST_KEY"]; ok {
		t.Error("the wake sent a secret added after the Run was submitted")
	}
	if env := w.lux.ServerEnv(r.ID, "web"); env["SEED_LLM_KEY"] != seedKeyNew || env["SIGNING_KEY_PEM"] != pem {
		t.Errorf("web's environment after the wake = %v", env)
	}
	if got := w.recorded(runID); !slices.Equal(got, []string{"SEED_LLM_KEY", "SIGNING_KEY_PEM"}) {
		t.Errorf("recorded after the wake %v; a resume declares nothing new", got)
	}
}

// Asleep, a secret its Run was submitted with is removed: the next wake
// cancels that Run and submits a new one on the same wake, with no
// backoff, no failed start counted, nothing said to the person; it serves.
func TestAWakeAfterASecretWasRemovedSubmitsANewRunNotCountedAsAFailure(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.secret("SEED_LLM_KEY", seedKey)
	w.secret("STRIPE_TEST_KEY", stripeKey)
	runID, web := w.asleepPreview()
	old := w.luxRuns()[0]
	// Two failed starts already: a third counted would stop dude trying.
	mustExec(t, w.owner, `UPDATE runs SET start_failures = 2 WHERE id = $1`, runID)
	w.removeSecret("STRIPE_TEST_KEY")
	// One request: the wake it makes must bring the new Run by itself.
	w.lux.RequestServer(web, "/")
	w.untilPreview(runID, "a second lux Run", func() bool { return len(w.luxRuns()) == 2 })
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND start_failures = 2 AND error IS NULL`, runID); n != 1 {
		t.Errorf("the replacement was counted as a failed start: %s", w.preview(runID))
	}
	runs := w.luxRuns()
	if len(runs) != 2 || old.Resumed != 0 || !slices.Contains(w.lux.CallsOf(old.ID), "cancel") {
		t.Fatalf("%d lux runs, old resumed %d, calls %v; want the old cancelled and a new one\n%s",
			len(runs), old.Resumed, w.lux.CallsOf(old.ID), w.preview(runID))
	}
	if got := envSecrets(submitted(t, runs[1])); len(got) != 1 || got["SEED_LLM_KEY"] != seedKey {
		t.Errorf("the new Run's env secrets = %v", got)
	}
	if got := w.recorded(runID); !slices.Equal(got, []string{"SEED_LLM_KEY"}) {
		t.Errorf("recorded %v", got)
	}
	for _, c := range w.changes(runID) {
		if e, _ := c["error"].(string); strings.Contains(e, "failed to start") {
			t.Errorf("a failed start was said: %v", c)
		}
	}
	w.untilPreview(runID, "running on the new Run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id = $2 AND start_failures = 0 AND error IS NULL`,
			runID, runs[1].ID) == 1
	})
	w.open(web)
	if env := w.lux.ServerEnv(runs[1].ID, "web"); env["SEED_LLM_KEY"] != seedKey || env["STRIPE_TEST_KEY"] != "" {
		t.Errorf("web's environment on the new Run = %v", env)
	}
}

// A server a person added to a parked eager preview (not one of the
// project's autostart servers), then a declared secret removed: the new
// Run has that server too, and the start the person asked for is made on
// it once it runs.
func TestAReplacementForARemovedSecretKeepsAManuallyAddedServer(t *testing.T) {
	w := newWorld(t)
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.secret("SEED_LLM_KEY", seedKey)
	_, out := w.do("POST", "/internal/tasks/"+w.task()+"/preview", nil)
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("web ready", func() bool {
		return len(w.luxRuns()) == 1 && w.lux.ServerStates(w.luxRuns()[0].ID)["web"] == "ready"
	})
	mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
	w.previews.Minute = time.Millisecond
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	w.removeSecret("SEED_LLM_KEY")
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{
		"name": "probe", "port": 4100, "command": "npm run probe", "env": map[string]string{"PROBE_MODE": "deep"},
	}); code != 201 {
		t.Fatalf("add a server to the parked preview = %d %v", code, out)
	}
	w.until("the new Run running, the asked-for start made", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND pending_starts = '{}'`, runID) == 1
	})
	next := w.luxRuns()[1]
	w.until("probe ready on the new Run", func() bool { return w.lux.ServerStates(next.ID)["probe"] == "ready" })
	if env := w.lux.ServerEnv(next.ID, "probe"); env["PROBE_MODE"] != "deep" {
		t.Errorf("probe's environment on the new Run = %v", env)
	}
	if w.lux.ServerStates(next.ID)["web"] == "" {
		t.Errorf("the new Run's servers = %v", w.lux.ServerStates(next.ID))
	}
}

// A server a person added to a running wakeable preview stays its own
// across the replacements of its Run: one for a declared secret removed,
// and one for a start that failed before it ran (startFailed's new Run).
func TestAWakeableReplacementKeepsAManuallyAddedServer(t *testing.T) {
	for _, why := range []string{"secret removed", "failed start"} {
		t.Run(why, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.secret("SEED_LLM_KEY", seedKey)
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			web := w.serverID(runID, "web")
			w.open(web)
			w.running(runID, "web")
			old := w.luxRuns()[0]
			if code, out := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{
				"name": "probe", "port": 4100, "command": "npm run probe", "env": map[string]string{"PROBE_MODE": "deep"},
			}); code != 201 {
				t.Fatalf("add a server to the running preview = %d %v", code, out)
			}
			w.lux.Idle(web)
			w.until("parked", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
			})
			if why == "secret removed" {
				w.removeSecret("SEED_LLM_KEY")
			} else {
				w.lux.FailStarts("dude.preview="+runID, 1)
			}
			w.lux.RequestServer(web, "/")
			w.untilPreview(runID, "a new Run running", func() bool {
				runs := w.luxRuns()
				return len(runs) == 2 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id = $2`,
					runID, runs[1].ID) == 1
			})
			if !slices.Contains(w.lux.CallsOf(old.ID), "cancel") {
				t.Errorf("old Run's calls %v; want it cancelled", w.lux.CallsOf(old.ID))
			}
			next := w.luxRuns()[1]
			if _, ok := w.lux.ServerStates(next.ID)["probe"]; !ok {
				t.Fatalf("the new Run's servers = %v; want probe kept", w.lux.ServerStates(next.ID))
			}
			if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/probe/start", nil); code >= 300 {
				t.Fatalf("start probe = %d %v", code, out)
			}
			w.until("probe ready on the new Run", func() bool { return w.lux.ServerStates(next.ID)["probe"] == "ready" })
			if env := w.lux.ServerEnv(next.ID, "probe"); env["PROBE_MODE"] != "deep" {
				t.Errorf("probe's environment on the new Run = %v", env)
			}
		})
	}
}

// A server a person added to a parked preview keeps its own definition
// when, before the Run is replaced, a recipe that starts in previews takes
// its name: the replacement has the servers the old Run had, and the old
// one never had that recipe. The next fresh preview gets the recipe.
func TestACarriedServerKeepsItsDefinitionWhenARecipeTakesItsName(t *testing.T) {
	w := newWorld(t)
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.secret("SEED_LLM_KEY", seedKey)
	_, out := w.do("POST", "/internal/tasks/"+w.task()+"/preview", nil)
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("web ready", func() bool {
		return len(w.luxRuns()) == 1 && w.lux.ServerStates(w.luxRuns()[0].ID)["web"] == "ready"
	})
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{
		"name": "probe", "port": 4100, "command": "npm run manual-probe", "env": map[string]string{"PROBE_MODE": "manual"},
	}); code != 201 {
		t.Fatalf("add a server = %d %v", code, out)
	}
	mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
	w.previews.Minute = time.Millisecond
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	w.recipe("probe", 4200, "npm run recipe-probe", "", nil, true)
	w.removeSecret("SEED_LLM_KEY")
	mustExec(t, w.owner, `UPDATE runs SET pending_starts = ARRAY['probe'] WHERE id = $1`, runID)
	w.until("the new Run running, the asked-for start made", func() bool {
		return len(w.luxRuns()) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND pending_starts = '{}'`, runID) == 1
	})
	next := w.luxRuns()[1]
	w.until("probe ready on the new Run", func() bool { return w.lux.ServerStates(next.ID)["probe"] == "ready" })
	list, err := w.previews.Lux.Servers(context.Background(), next.ID)
	if err != nil {
		t.Fatal(err)
	}
	var probe *lux.Server
	for i := range list {
		if list[i].Name == "probe" {
			probe = &list[i]
		}
	}
	if probe == nil || probe.Port != 4100 || probe.FromSpec || !slices.Equal(probe.Command, []string{"sh", "-c", "npm run manual-probe"}) {
		t.Fatalf("probe on the new Run = %+v; want the person's server, port 4100", probe)
	}
	if env := w.lux.ServerEnv(next.ID, "probe"); env["PROBE_MODE"] != "manual" {
		t.Errorf("probe's environment on the new Run = %v", env)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND carried_servers = '[]'`, runID); n != 1 {
		t.Errorf("the carry was not cleared once restored:\n%s", w.preview(runID))
	}
	if spec := submitted(t, next); slices.ContainsFunc(spec.Workload.Servers, func(s lux.ServerInput) bool { return s.Name == "probe" }) {
		t.Errorf("the replacement's spec declares the recipe probe: %+v", spec.Workload.Servers)
	}
}

// lux added a carried server and its answer was lost; the retry is
// answered name_taken by the server that add made. That is the carry
// restored: the definitions match. A server of that name with another
// definition is not: the carry is kept, the restore fails to be retried,
// and the log names the server, never a value.
func TestARestoredServersNameTakenCountsOnlyForItsOwnDefinition(t *testing.T) {
	for _, same := range []bool{true, false} {
		name := "a lost answer, replayed"
		if !same {
			name = "another server of the name"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.previews.Minute = time.Hour
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.secret("SEED_LLM_KEY", seedKey)
			_, out := w.do("POST", "/internal/tasks/"+w.task()+"/preview", nil)
			runID := out["run"].(map[string]any)["id"].(string)
			w.until("web ready", func() bool {
				return len(w.luxRuns()) == 1 && w.lux.ServerStates(w.luxRuns()[0].ID)["web"] == "ready"
			})
			if code, out := w.do("POST", "/internal/runs/"+runID+"/servers", map[string]any{
				"name": "probe", "port": 4100, "command": "npm run probe", "env": map[string]string{"PROBE_MODE": seedKey},
			}); code != 201 {
				t.Fatalf("add a server = %d %v", code, out)
			}
			mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
			w.previews.Minute = time.Millisecond
			w.until("parked", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
			})
			w.previews.Minute = time.Hour
			lost := &lostAddAnswer{Client: w.previews.Lux, name: "probe"}
			if !same {
				lost.replace = &lux.ServerInput{Name: "probe", Port: 4300, Command: []string{"sh", "-c", "other"}}
			}
			w.previews.Lux = lost
			var logs lockedBuffer
			w.previews.Log = slog.New(slog.NewTextHandler(&logs, nil))
			w.removeSecret("SEED_LLM_KEY")
			mustExec(t, w.owner, `UPDATE runs SET pending_starts = ARRAY['probe'] WHERE id = $1`, runID)
			if same {
				w.until("the new Run running with the carry restored", func() bool {
					return len(w.luxRuns()) == 2 &&
						w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND carried_servers = '[]'`, runID) == 1
				})
				return
			}
			w.until("the restore refused", func() bool { return lost.Retried() })
			if _, err := w.previews.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NULL AND carried_servers <> '[]'`, runID); n != 1 {
				t.Errorf("the carry was dropped, or the new Run recorded, though another server holds its name:\n%s", w.preview(runID))
			}
			if got := logs.String(); !strings.Contains(got, "server=probe") || strings.Contains(got, seedKey) {
				t.Errorf("the log should name the server and no value:\n%s", got)
			}
		})
	}
}

// lostAddAnswer adds a server named name to lux, then answers as if the
// answer was lost; with replace, what lux adds is replace instead (another
// server took the name). Its retry gets lux's own answer.
type lostAddAnswer struct {
	lux.Client
	name    string
	replace *lux.ServerInput
	mu      sync.Mutex
	adds    int
}

func (c *lostAddAnswer) AddServer(ctx context.Context, runID string, in lux.ServerInput) (lux.Server, error) {
	c.mu.Lock()
	c.adds++
	first := c.adds == 1
	c.mu.Unlock()
	if in.Name != c.name || !first {
		return c.Client.AddServer(ctx, runID, in)
	}
	if c.replace != nil {
		in = *c.replace
	}
	if _, err := c.Client.AddServer(ctx, runID, in); err != nil {
		return lux.Server{}, err
	}
	return lux.Server{}, errors.New("the add's answer was lost after lux took it")
}

func (c *lostAddAnswer) Retried() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.adds > 1
}

// lostSubmitAnswer passes a submit to lux, then, once, loses lux's answer
// after running after: what changed before dude's retry.
type lostSubmitAnswer struct {
	lux.Client
	after func()
}

func (c *lostSubmitAnswer) Submit(ctx context.Context, spec lux.Spec, key string) (lux.Run, error) {
	r, err := c.Client.Submit(ctx, spec, key)
	if err == nil && c.after != nil {
		after := c.after
		c.after = nil
		after()
		return lux.Run{}, errors.New("the submit's answer was lost after lux accepted it")
	}
	return r, err
}

// lux accepts a preview's submit and its answer is lost; a secret is
// removed and another added before dude retries. The retry's key gets the
// Run lux accepted, which declares the removed one: that is what is
// recorded, so the next resume replaces the Run rather than failing it.
func TestARetriedSubmitRecordsTheSecretsOfTheRunLuxAccepted(t *testing.T) {
	for _, wakeable := range []bool{false, true} {
		name := "eager"
		if wakeable {
			name = "wakeable"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.previews.Minute = time.Hour
			if wakeable {
				w.wakeable()
			}
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.secret("ORIGINAL_KEY", seedKey)
			w.previews.Lux = &lostSubmitAnswer{Client: w.previews.Lux, after: func() {
				w.removeSecret("ORIGINAL_KEY")
				w.secret("ADDED_KEY", stripeKey)
			}}
			_, out := w.do("POST", "/internal/tasks/"+w.task()+"/preview", nil)
			runID := out["run"].(map[string]any)["id"].(string)
			if wakeable {
				w.until("server declared", func() bool { return w.serverID(runID, "web") != "" })
				w.lux.RequestServer(w.serverID(runID, "web"), "/")
			}
			w.until("the submit accepted, its answer lost", func() bool { return len(w.luxRuns()) == 1 })
			mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL, wake_claimed_at = NULL WHERE id = $1`, runID)
			w.until("the retry recorded", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id IS NOT NULL`, runID) == 1
			})
			if got := w.recorded(runID); !slices.Equal(got, []string{"ORIGINAL_KEY"}) {
				t.Errorf("recorded %v; the Run lux accepted declares ORIGINAL_KEY", got)
			}
			first := w.luxRuns()[0]
			w.until("web ready", func() bool { return w.lux.ServerStates(first.ID)["web"] == "ready" })
			if wakeable {
				w.lux.Idle(w.serverID(runID, "web"))
			} else {
				mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
				w.previews.Minute = time.Millisecond
			}
			w.until("parked", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
			})
			w.previews.Minute = time.Hour
			if wakeable {
				w.open(w.serverID(runID, "web"))
			} else if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code >= 300 {
				t.Fatalf("start = %d %v", code, out)
			}
			w.untilPreview(runID, "a new Run serving", func() bool {
				runs := w.luxRuns()
				return len(runs) == 2 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id = $2`,
					runID, runs[1].ID) == 1
			})
			if first.Resumed != 0 || !slices.Contains(w.lux.CallsOf(first.ID), "cancel") {
				t.Errorf("first Run resumed %d, calls %v; want it cancelled and replaced", first.Resumed, w.lux.CallsOf(first.ID))
			}
			if got := w.recorded(runID); !slices.Equal(got, []string{"ADDED_KEY"}) {
				t.Errorf("recorded for the new Run %v", got)
			}
		})
	}
}

// An agent's Run, in a project with secrets, declares none of them, and
// their values are nowhere in what lux was asked to run.
func TestAnAgentRunGetsNoPreviewSecret(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = hang
	w.secret("SEED_LLM_KEY", seedKey)
	w.secret("SIGNING_KEY_PEM", pem)
	task := w.task()
	w.deliver(task)
	w.until("the implementer to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND status = 'running'`, task) == 1
	})
	agents := slices.DeleteFunc(w.luxRuns(), func(r *fakelux.Run) bool { return r == nil })
	if len(agents) == 0 {
		t.Fatal("no agent Run in lux")
	}
	for _, r := range agents {
		spec := submitted(t, r)
		for _, s := range spec.Secrets {
			if s.Name == "SEED_LLM_KEY" || s.Name == "SIGNING_KEY_PEM" {
				t.Errorf("agent Run %s declares %s", r.ID, s.Name)
			}
		}
		raw, _ := json.Marshal(spec)
		if strings.Contains(string(raw), "SEED-3f9a") || strings.Contains(string(raw), "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC") {
			t.Errorf("agent Run %s carries a secret's value", r.ID)
		}
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND preview_secrets <> '{}'`, task); n != 0 {
		t.Errorf("%d agent Runs recorded preview secrets", n)
	}
}
