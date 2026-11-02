package delivery

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The voice is written from outside the steps (the hand-back, a person's
// message in Chat): a step that is running keeps the row's.
func TestTheVoiceIsOwnedByTheRow(t *testing.T) {
	if def := Workflow(&Store{}, nil); !slices.Contains(def.Owned, "voice") {
		t.Errorf("Owned %v lacks voice", def.Owned)
	}
}

// Quiet is the conductor's alone, and Deliver's rules hold its gate as
// they hold one no conductor decides.
func TestQuietAndWhoseRulesOpen(t *testing.T) {
	for _, c := range []struct {
		decider, voice      string
		quiet, deliverOpens bool
	}{
		{"", "", false, true},
		{DeciderPolicy, VoiceQuiet, false, true},
		{DeciderConductor, "", false, false},
		{DeciderConductor, VoiceChatty, false, false},
		{DeciderConductor, VoiceQuiet, true, true},
	} {
		st := &State{Decider: c.decider, Voice: c.voice}
		if st.quiet() != c.quiet || st.deliverOpens() != c.deliverOpens {
			t.Errorf("%q/%q: quiet %v deliverOpens %v", c.decider, c.voice, st.quiet(), st.deliverOpens())
		}
	}
}

// The conductor's file is built from the delivery's voice as it is
// submitted (TaskBranchTx): quiet for a quiet one, chatty for a conductor
// with no voice, none when Deliver decides.
func TestTheTaskBranchCarriesTheVoice(t *testing.T) {
	for _, c := range []struct{ state, want string }{
		{`{"decider":"conductor","voice":"quiet"}`, VoiceQuiet},
		{`{"decider":"conductor"}`, VoiceChatty},
		{`{"decider":"policy","voice":"quiet"}`, ""},
		{`{}`, ""},
	} {
		t.Run(c.state, func(t *testing.T) {
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			ctx := context.Background()
			for _, q := range []string{
				`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_v', $1, 'P', 'p', 'P')`,
				`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_v', $1, 'prj_v', 1, 'Greet', 'greet', 'running')`,
			} {
				if _, err := owner.Exec(ctx, q, org); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := owner.Exec(ctx, `INSERT INTO workflow_runs (id, organization_id, workflow_type, task_id, step, status, state, idempotency_key)
				VALUES ('wf_v', $1, $2, 'wi_v', 'implement', 'running', $3::jsonb, 'delivery:wi_v')`, org, WorkflowType, c.state); err != nil {
				t.Fatal(err)
			}
			var tb TaskBranch
			if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
				var err error
				tb, err = TaskBranchTx(ctx, tx, "wi_v", 1)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if tb.Voice != c.want {
				t.Errorf("voice %q, want %q", tb.Voice, c.want)
			}
		})
	}
}
