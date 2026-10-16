package phases

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A project's secrets reach the agent Runs of the phases each one names
// (project_secrets.recipients, migration 127) as lux env secrets, beside
// dude's own. The conductor and a session's brainstorm never get one: they
// read text anyone can write, such as pull request comments. A Run records
// the names it was given (runs.project_secret_names) and keeps a copy of
// the exact values (run_project_secrets, 128), which dude redacts its
// output with and scans its push for, until its output is collected.

// EvSecretsGiven names the project secrets a Run was given, never their
// values. Once per Run. Payload: {names}.
const EvSecretsGiven = "run.secrets.given"

const projectSecretAs = "env"

// secretRecipient is the recipient a phase's Runs are, by the settings role
// they run as; "" for a Run that is given no project secret.
var secretRecipient = map[string]string{
	"implementer": "implementer",
	"fixer":       "fixer",
	"simplifier":  "simplifier",
	"reviewer":    "reviewer",
	"qa_browser":  "tester",
}

// SecretRecipientFor is the recipient a Run of phase and role is; "" for
// the conductor, brainstorm and any phase no recipient names.
func SecretRecipientFor(phase, role string) string {
	if phase == "" {
		return ""
	}
	return secretRecipient[delivery.PromptRoleFor(phase, role)]
}

// loadProjectSecrets are the current values of the project's secrets given
// to recipient, by name; with names, only those (a resume's).
func loadProjectSecrets(ctx context.Context, tx pgx.Tx, projectID, recipient string, names []string) ([]lux.Secret, error) {
	if recipient == "" || projectID == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT name, value FROM project_secrets
		WHERE project_id = $1 AND $2 = ANY(recipients) AND ($3::text[] IS NULL OR name = ANY($3))
		ORDER BY name`, projectID, recipient, names)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (lux.Secret, error) {
		s := lux.Secret{As: projectSecretAs}
		return s, row.Scan(&s.Name, &s.Value)
	})
}

// withProjectSecrets adds the project's secrets to a spec's, leaving out a
// name the spec declares already: dude's own wins, and lux refuses a name
// declared twice. The API refuses such names at save; one saved before the
// refusal existed is not given.
func withProjectSecrets(spec *lux.Spec, project []lux.Secret) []lux.Secret {
	var given []lux.Secret
	for _, s := range project {
		if slices.ContainsFunc(spec.Secrets, func(d lux.Secret) bool { return d.Name == s.Name }) {
			continue
		}
		spec.Secrets = append(spec.Secrets, s)
		given = append(given, s)
	}
	return given
}

// declaredNames are the names among recorded the Run lux holds declares in
// its container: a retried submit is answered with whichever attempt's Run
// lux made, which may declare names this attempt did not send.
func declaredNames(lr lux.Run, recorded []string) []string {
	var out []string
	for _, n := range recorded {
		if slices.ContainsFunc(lr.Spec.Secrets, func(d lux.StoredSecret) bool { return d.Name == n && !d.RunnerOnly }) {
			out = append(out, n)
		}
	}
	return out
}

// errSecretsGone: names the Run was submitted with that the project no
// longer gives its phase (removed, or its recipients changed). lux refuses
// a resume missing a declared secret, so the Run is retired and a fresh
// one takes its slot (retireForSecrets), as a preview is replaced.
type errSecretsGone []string

func (e errSecretsGone) Error() string {
	return "the project no longer gives this phase " + strings.Join(e, ", ")
}

// missingNames are the recorded names have has no value for.
func missingNames(recorded []string, have []lux.Secret) []string {
	var gone []string
	for _, n := range recorded {
		if !slices.ContainsFunc(have, func(s lux.Secret) bool { return s.Name == n }) {
			gone = append(gone, n)
		}
	}
	return gone
}

// retireForSecrets ends a paused Run that cannot be resumed for gone and
// starts a fresh one of its phase in its slot, which is given the
// project's secrets now. Not a failure: neither Run fails, and the
// delivery waits on the fresh one. false: the delivery does not wait on
// this Run (the restart was refused), and the caller fails it instead.
func (s *Syncer) retireForSecrets(ctx context.Context, r phaseRun, gone errSecretsGone) (bool, error) {
	var refused bool
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := delivery.RestartRunTx(ctx, tx, r.Org, r.TaskID, delivery.Restart{RunID: r.ID, ByDude: true,
			Note:  "a secret it was given, " + strings.Join(gone, ", ") + ", is no longer given to its phase",
			Actor: delivery.Writer{ActorType: ledger.ActorSystem, ActorID: "dude"}})
		if delivery.IsRefusal(err) {
			refused = true
			return nil
		}
		return err
	})
	if err != nil || refused {
		return false, err
	}
	s.Log.Info("a secret the paused Run was given is no longer its phase's; started a fresh Run", "run", r.ID, "secrets", []string(gone))
	return true, nil
}

// copyBackstop is how long after its Run ended a copy is deleted whatever
// its collection did (a collection given up on, an orchestrator stopped
// between the two).
const copyBackstop = time.Hour

// ForgetSecretCopies deletes, in each organization in turn, the copies of
// Runs that ended over copyBackstop ago. Per organization because the
// sweeper role has no grant on run_project_secrets. Returns how many
// copies it deleted.
func (s *Syncer) ForgetSecretCopies(ctx context.Context) (int, error) {
	var orgs []string
	if err := s.DB.InSystem(ctx, "secret-copies", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM organizations ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, org := range orgs {
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM run_project_secrets s USING runs r
				WHERE r.id = s.run_id AND r.status IN ('completed', 'failed', 'aborted')
				  AND r.ended_at < now() - make_interval(secs => $1)`, copyBackstop.Seconds())
			n += int(tag.RowsAffected())
			return err
		}); err != nil {
			return n, err
		}
	}
	return n, nil
}

// secretNames are the names of list, in its order.
func secretNames(list []lux.Secret) []string {
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	return names
}

// recordSending records, committed before lux sees them, the names and
// exact values a submit or a resume sends: lux may start the agent, and its
// output reach dude, before it answers, and that output is redacted with
// these. Both only grow. An earlier submit whose answer was lost may have
// made the Run lux holds, with a name since unticked or a value since
// rotated. A resume keeps the checkout, its commits and the conversation,
// which may hold any value sent before; and a retried resume may be
// answered with an earlier attempt's, which sent another value. Only
// forgetSubmitting (a submit refused for good), recordGiven's trim to the
// names lux accepted, the copy's collection and ForgetSecretCopies remove
// any. A crash before Submit leaves them on a pending Run, which the next
// attempt adds to.
func (s *Syncer) recordSending(ctx context.Context, r phaseRun, sent []lux.Secret) error {
	if len(sent) == 0 {
		return nil
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET project_secret_names =
			ARRAY(SELECT DISTINCT n FROM unnest(project_secret_names || $2::text[]) n ORDER BY n) WHERE id = $1`,
			r.ID, secretNames(sent)); err != nil {
			return err
		}
		for _, sec := range sent {
			if _, err := tx.Exec(ctx, `INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, r.ID, r.Org, sec.Name, sec.Value); err != nil {
				return err
			}
		}
		return nil
	})
}

// forgetSubmitting undoes every attempt's recordSending for a submit
// that failed for good: the Run was given nothing.
func (s *Syncer) forgetSubmitting(ctx context.Context, r phaseRun) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET project_secret_names = '{}' WHERE id = $1`, r.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM run_project_secrets WHERE run_id = $1`, r.ID)
		return err
	})
}

// recordGiven records what the Run lux accepted holds: the names every
// attempt recorded are trimmed to those lux's Run declares, with their
// copies, and the one event names them. lux's answer names its secrets but
// carries no value, so every value any attempt sent for a kept name stays:
// which one the Run holds cannot be told, and redacting an unused one is
// harmless.
func (s *Syncer) recordGiven(ctx context.Context, tx pgx.Tx, r phaseRun, lr lux.Run) error {
	var recorded []string
	if err := tx.QueryRow(ctx, `SELECT project_secret_names FROM runs WHERE id = $1`, r.ID).Scan(&recorded); err != nil {
		return err
	}
	if len(recorded) == 0 {
		return nil
	}
	names := declaredNames(lr, recorded)
	if len(names) < len(recorded) {
		if _, err := tx.Exec(ctx, `UPDATE runs SET project_secret_names = $2 WHERE id = $1`, r.ID, db.NonNil(names)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_project_secrets WHERE run_id = $1 AND name <> ALL($2)`, r.ID, db.NonNil(names)); err != nil {
			return err
		}
	}
	if len(names) == 0 {
		return nil
	}
	return s.event(ctx, tx, r, EvSecretsGiven, ledger.ActorSystem, map[string]any{"names": names})
}
