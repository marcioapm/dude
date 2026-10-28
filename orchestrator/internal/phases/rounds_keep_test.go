package phases

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Replacement before the unread round directive arrives must carry the
// current owner note, not replay that obsolete directive into a fresh round.
func TestOwnerNoteSurvivesRoundReplacement(t *testing.T) {
	for _, why := range []string{"unresumable session", "secret change", "removed secret"} {
		t.Run(why, func(t *testing.T) {
			w := timeLimitWorld(t, "resume", true)
			ctx := context.Background()
			note := "The task's owner, Ola Nordmann, said:\n\nUse this exact config:\n  backend: versitygw\nDo not use minio."
			if _, err := w.owner.Exec(ctx, `UPDATE runs SET person_note=$2 WHERE id=$1`, w.run.ID, note); err != nil {
				t.Fatal(err)
			}
			if _, err := w.owner.Exec(ctx, `INSERT INTO directives(id,organization_id,run_id,text) VALUES('dir_owner',$1,$2,$3)`, w.run.Org, w.run.ID, note+"\nOBSOLETE ROUND BRIEF"); err != nil {
				t.Fatal(err)
			}
			if why == "secret change" {
				if _, err := w.owner.Exec(ctx, `INSERT INTO project_secrets(project_id,organization_id,name,value,hint,recipients) VALUES($1,$2,'NEW_TOKEN','secret','test','{tester}')`, w.run.ProjectID, w.run.Org); err != nil {
					t.Fatal(err)
				}
				var cannot errCannotResume
				if err := w.s.roundSecrets(ctx, w.run); !errors.As(err, &cannot) {
					t.Fatalf("secret change: %v", err)
				}
			}
			var replaced bool
			var replaceErr error
			if why == "removed secret" {
				replaced, replaceErr = w.s.retireForSecrets(ctx, w.run, errSecretsGone{"OLD_TOKEN"})
			} else {
				replaced, replaceErr = w.s.replaceRound(ctx, w.run, why)
			}
			if replaceErr != nil || !replaced {
				t.Fatalf("replace: %v %v", replaced, replaceErr)
			}
			var id string
			if err := w.owner.QueryRow(ctx, `SELECT replaced_by FROM runs WHERE id=$1`, w.run.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
			w.s.Agent.ToolsURL = "http://tools.test"
			w.s.Agent.ToolsService = true
			if _, err := w.owner.Exec(ctx, `UPDATE model_tiers SET model='gpt-6-sol' WHERE organization_id=$1`, w.run.Org); err != nil {
				t.Fatal(err)
			}
			if _, err := w.owner.Exec(ctx, `UPDATE organizations SET default_agent_models=jsonb_build_object('qa_browser',jsonb_build_object('tier',(SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org); err != nil {
				t.Fatal(err)
			}
			r := w.run
			r.ID, r.Phase, r.Role = id, delivery.PhaseTest, "qa_browser"
			r.LuxRunID = ""
			spec, _, _, _, err := w.s.spec(ctx, r, nil, chosenImage{Ref: "test/image:latest"})
			if err != nil {
				t.Fatal(err)
			}
			input := spec.Workload.Prompt
			if strings.Count(input, note) != 1 || strings.Contains(input, "OBSOLETE ROUND BRIEF") {
				t.Fatalf("replacement input lost/duplicated note or copied stale directive: %q", input)
			}
			var failed bool
			if err := w.owner.QueryRow(ctx, `SELECT failed_at IS NOT NULL AND delivered_at IS NULL FROM directives WHERE id='dir_owner'`).Scan(&failed); err != nil {
				t.Fatal(err)
			}
			if !failed {
				t.Fatal("abandoned directive remains deliverable")
			}
		})
	}
}

// A continuing fixer whose phase was given a project secret after its row
// was made (the implementer's lux Run has none of it) is refused its first
// resume, naming the secret; a later resume (finishes > 0) is a person's,
// and goes on.
func TestAContinuingFixerGivenANewSecretCannotResume(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status, ended_at)
		VALUES ('run_im_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'implementer', 'completed', now())`, w.run.Org)
	w.exec(`UPDATE runs SET phase = 'fix', role = 'implementer', continues_run_id = 'run_im_'||organization_id, continues_how = 'resume',
		project_secret_names = '{}', finishes = 0 WHERE id = $1`, w.run.ID)
	w.run.Phase, w.run.Role = "fix", "implementer"
	if err := w.s.roundSecrets(w.ctx, w.run); err != nil {
		t.Fatalf("no secret added: %v", err)
	}
	w.exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
		VALUES ($1, $2, 'FIX_TOKEN', 'fix-token-value', 'test', '{fixer}')`, w.run.ProjectID, w.run.Org)
	var cannot errCannotResume
	if err := w.s.roundSecrets(w.ctx, w.run); !errors.As(err, &cannot) || !strings.Contains(err.Error(), "its phase is now given FIX_TOKEN") {
		t.Fatalf("a secret added for fixers: %v, want errCannotResume naming FIX_TOKEN", err)
	}
	w.exec(`UPDATE runs SET finishes = 1 WHERE id = $1`, w.run.ID)
	if err := w.s.roundSecrets(w.ctx, w.run); err != nil {
		t.Fatalf("a later resume: %v, want nil", err)
	}
}

// The same for an organisation secret ticked for fixers after the row was
// made (migration 210): it is in force for the project, so the first resume
// is refused naming it; one the project holds of its own is the project's.
func TestAContinuingFixerGivenANewOrganisationSecretCannotResume(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status, ended_at)
		VALUES ('run_im_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'implementer', 'completed', now())`, w.run.Org)
	w.exec(`UPDATE runs SET phase = 'fix', role = 'implementer', continues_run_id = 'run_im_'||organization_id, continues_how = 'resume',
		project_secret_names = '{GH_TOKEN}', finishes = 0 WHERE id = $1`, w.run.ID)
	w.run.Phase, w.run.Role = "fix", "implementer"
	// The recorded GH_TOKEN is the project's; the organisation's of that name is not in force here.
	w.exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
		VALUES ($1, $2, 'GH_TOKEN', 'project-gh', 'p-gh', '{fixer}')`, w.run.ProjectID, w.run.Org)
	w.exec(`INSERT INTO organization_secrets (organization_id, name, value, hint, recipients)
		VALUES ($1, 'GH_TOKEN', 'org-gh', 'o-gh', '{fixer}')`, w.run.Org)
	if err := w.s.roundSecrets(w.ctx, w.run); err != nil {
		t.Fatalf("only an overridden organisation name added: %v", err)
	}
	w.exec(`INSERT INTO organization_secrets (organization_id, name, value, hint, recipients)
		VALUES ($1, 'NPM_TOKEN', 'org-npm', 'onpm', '{fixer}')`, w.run.Org)
	var cannot errCannotResume
	if err := w.s.roundSecrets(w.ctx, w.run); !errors.As(err, &cannot) || !strings.Contains(err.Error(), "its phase is now given NPM_TOKEN") {
		t.Fatalf("an organisation secret added for fixers: %v, want errCannotResume naming NPM_TOKEN", err)
	}
}

// A completed reviewer or tester is kept for its next round on a project
// that goes on from the previous one; an integration's reviewer never is,
// since no round goes on from it.
func TestAnIntegrationReviewerIsNotKeptForANextRound(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE projects SET delivery_policy = '{"resumeRounds":true}' WHERE id = $1`, w.run.ProjectID)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, ended_at, lux_run_id, pr_feedback)
		VALUES ('run_rev_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', now(), 'lrun_rev', '[]'),
		       ('run_int_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', now(), 'lrun_int', '[{"source":"integration"}]')`, w.run.Org)
	kept := func(id string) bool {
		t.Helper()
		var k bool
		if err := w.owner.QueryRow(w.ctx, `SELECT `+keptForNextRound+` FROM runs r WHERE id = $1 || $2`, id, w.run.Org).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	if !kept("run_rev_") {
		t.Error("a completed reviewer of a project that resumes rounds is not kept")
	}
	if kept("run_int_") {
		t.Error("an integration's completed reviewer is kept for a next round")
	}
}
