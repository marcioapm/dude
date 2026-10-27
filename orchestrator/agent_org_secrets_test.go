package orchestrator_test

// Organisation secrets (migration 210) reach agent Runs as project secrets
// do: a Run of a phase gets each organisation secret ticked for it, in every
// project, unless the project holds a secret of the same name, whose value
// it gets instead. The conductor and a session's brainstorm never get one.
// dude redacts an organisation value and refuses to push it exactly as a
// project value.

import (
	"context"
	"encoding/hex"
	"maps"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
)

const (
	orgNpmToken    = "org-npm-not-a-real-token-314"
	orgGhToken     = "org-gh-not-a-real-token-271"
	projectGhToken = "project-gh-not-a-real-token-161"
)

// orgGrant sets an organisation secret given to recipients.
func (w *world) orgGrant(name, value string, recipients ...string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO organization_secrets (organization_id, name, value, hint, recipients)
		VALUES ($1, $2, $3, right($3, 4), $4)
		ON CONFLICT (organization_id, name) DO UPDATE SET value = EXCLUDED.value, hint = EXCLUDED.hint, recipients = EXCLUDED.recipients`,
		w.org, name, value, recipients)
}

// A phase ticked at organisation level gets the secret; one not ticked
// does not; a name the project also holds is the project's value.
func TestOrganisationSecretsReachThePhasesTickedForThem(t *testing.T) {
	w := newWorld(t)
	w.orgGrant("NPM_TOKEN", orgNpmToken, "implementer", "tester")
	w.orgGrant("GH_TOKEN", orgGhToken, "implementer", "reviewer")
	w.grant("GH_TOKEN", projectGhToken, "implementer")
	task := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	want := map[string]map[string]string{
		"implement": {"GH_TOKEN": projectGhToken, "NPM_TOKEN": orgNpmToken},
		"test":      {"NPM_TOKEN": orgNpmToken},
		// The project's GH_TOKEN is the implementer's alone; the
		// organisation's is not given in its place.
		"review":   {},
		"simplify": {},
	}
	seen := map[string]bool{}
	rows, err := w.owner.Query(context.Background(), `SELECT id, phase::text FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL`, task)
	if err != nil {
		t.Fatal(err)
	}
	type phaseRun struct{ ID, Phase string }
	var runs []phaseRun
	for rows.Next() {
		var r phaseRun
		if err := rows.Scan(&r.ID, &r.Phase); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	for _, r := range runs {
		expected, ok := want[r.Phase]
		if !ok {
			continue
		}
		seen[r.Phase] = true
		got := projectSecretsOf(submitted(t, w.luxRunFor(r.ID)))
		if !maps.Equal(got, expected) {
			t.Errorf("%s declares %v, want %v", r.Phase, slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(expected)))
		}
		if rec := w.givenNames(r.ID); !slices.Equal(rec, slices.Sorted(maps.Keys(expected))) {
			t.Errorf("%s recorded %v", r.Phase, rec)
		}
	}
	for phase := range want {
		if !seen[phase] {
			t.Errorf("no %s Run", phase)
		}
	}
}

// An organisation secret ticked for every recipient never reaches the
// conductor or a session's brainstorm.
func TestTheConductorAndBrainstormNeverReceiveAnOrganisationSecret(t *testing.T) {
	all := []string{"previews", "tester", "implementer", "fixer", "simplifier", "reviewer"}
	t.Run("conductor", func(t *testing.T) {
		w := conductorWorld(t)
		w.orgGrant("EVERYONE_KEY", everyoneKey, all...)
		task := w.task()
		if status, out := w.chat(task, "what is this task about?"); status != 201 {
			t.Fatalf("chat: %d %v", status, out)
		}
		var id string
		w.until("the conductor in lux", func() bool {
			id, _, _ = w.conductor(task)
			return id != "" && w.luxRunFor(id) != nil
		})
		assertNoProjectSecret(t, w, id, string(w.luxRunFor(id).Spec))
	})
	t.Run("brainstorm", func(t *testing.T) {
		s := newSessionWorld(t)
		s.orgGrant("EVERYONE_KEY", everyoneKey, all...)
		session := s.session()
		s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "hello"})
		id := s.started(session)
		assertNoProjectSecret(t, s.world, id, s.luxRun(id).raw)
	})
}

// An organisation value passed to dude's tools is stored redacted, as a
// project value is.
func TestAnOrganisationSecretPassedToDudesToolsIsRedacted(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: w.log}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	w.orgGrant("NPM_TOKEN", orgNpmToken, "implementer")
	_, runID, _ := w.liveImplementer()
	// The live Run's copy holds the organisation value: what it is redacted with.
	if c := w.runCopy(runID); len(c) != 1 || c["NPM_TOKEN"] != orgNpmToken {
		t.Errorf("the implementer's copy has %v", slices.Sorted(maps.Keys(c)))
	}
	status, body := w.callTool(tools.URL, w.luxSpecOf("implement"), "ask_person",
		`{"question":"Is `+orgNpmToken+` right? (hex `+hex.EncodeToString([]byte(orgNpmToken))+`)"}`)
	if status >= 300 {
		t.Fatalf("ask_person: %d %s", status, body)
	}
	var prompt string
	_ = w.owner.QueryRow(context.Background(), `SELECT prompt FROM questions WHERE run_id = $1`, runID).Scan(&prompt)
	if prompt != "Is [REDACTED:NPM_TOKEN] right? (hex [REDACTED:NPM_TOKEN])" {
		t.Errorf("question stored as %q", prompt)
	}
	for _, l := range leaks(orgNpmToken) {
		if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%' || $2 || '%'`, runID, l); n != 0 {
			t.Errorf("%d events hold the value as %q", n, l)
		}
	}
}

// A reviewer given an organisation secret prints it in a tool's output,
// its reply and its thoughts: the ledger holds only the marker.
func TestAnOrganisationSecretIsRedactedFromTheLedger(t *testing.T) {
	w := newWorld(t)
	w.orgGrant("SNOWFLAKE_PASSWORD", snowflakePassword, "reviewer")
	w.leakingReviewer(snowflakePassword)
	task := w.task()
	w.deliver(task)
	var review string
	w.until("the reviewer's finding recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at LIMIT 1`, task).Scan(&review)
		return review != "" && w.count(`SELECT count(*) FROM review_findings WHERE run_id = $1`, review) > 0
	})
	var events string
	_ = w.owner.QueryRow(context.Background(), `SELECT string_agg(payload::text, E'\n') FROM events WHERE run_id = $1`, review).Scan(&events)
	for _, l := range leaks(snowflakePassword) {
		if strings.Contains(events, l) {
			t.Errorf("the ledger holds the value (as %q)", l)
		}
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.tool.completed' AND payload::text LIKE '%[REDACTED:SNOWFLAKE_PASSWORD]%'`, review) == 0 {
		t.Error("no tool output shows the marker")
	}
}

// A preview gets the organisation's secrets ticked for Previews, the
// project's winning a name, and not one ticked only for an agent.
func TestPreviewsReceiveOrganisationSecretsTickedForPreviews(t *testing.T) {
	w := newWorld(t)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.orgGrant("NPM_TOKEN", orgNpmToken, "previews")
	w.orgGrant("GH_TOKEN", orgGhToken, "previews")
	w.orgGrant("TESTER_ONLY", everyoneKey, "tester")
	w.secret("GH_TOKEN", projectGhToken)
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
	got := envSecrets(submitted(t, w.luxRuns()[0]))
	if want := map[string]string{"GH_TOKEN": projectGhToken, "NPM_TOKEN": orgNpmToken}; !maps.Equal(got, want) {
		t.Errorf("the preview got %v", slices.Sorted(maps.Keys(got)))
	}
	if names := w.recorded(runID); !slices.Equal(names, []string{"GH_TOKEN", "NPM_TOKEN"}) {
		t.Errorf("recorded names = %v", names)
	}
}

// An implementer committing an organisation value is refused before the push.
func TestAnImplementerCommittingAnOrganisationSecretIsRefused(t *testing.T) {
	w := newWorld(t)
	w.orgGrant("NPM_TOKEN", orgNpmToken, "implementer")
	w.scriptedImplementer(writeCall(".npmrc", "//registry.example.com/:_authToken="+orgNpmToken), commitCall("Add npmrc"))
	task := w.task()
	w.deliver(task)
	w.finishOnceBased(task)
	var runID string
	w.until("the implementer failed", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, task).Scan(&runID)
		return runID != ""
	})
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 {
		t.Errorf("lux was asked to push %d times", len(lr.PushCredentials))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["NPM_TOKEN"]' AND payload->'files' = '[".npmrc"]'`, runID); n != 1 {
		t.Errorf("%d run.push.refused naming NPM_TOKEN in .npmrc", n)
	}
	w.assertTaskHoldsNo(task, orgNpmToken)
}
