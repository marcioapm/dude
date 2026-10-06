package servers

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A project's preview secrets (project_secrets, migration 081) are lux
// secrets of every preview Run, as: env, so its servers and their setup
// scripts have them; no agent Run is given them (phases builds those
// specs and never reads the table). A preview spec's only env secrets are
// these: GIT_TOKEN and the registry login are declared without as.
//
// lux v0.1.11 requires a value at every resume for each secret the Run
// declared at submit (422 secrets_required otherwise) and ignores names
// it did not declare. So a Run records the names it was submitted with
// (runs.preview_secrets); a resume sends the current value of each, and
// nothing added since. A recorded name since removed from the project
// cannot be sent: the Run is replaced by a new one, which is not a failed
// start.

const previewSecretAs = "env"

// loadSecrets are the project's secrets, by name, as a preview's spec declares them.
func loadSecrets(ctx context.Context, tx pgx.Tx, projectID string) ([]lux.Secret, error) {
	rows, err := tx.Query(ctx, `SELECT name, value FROM project_secrets WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (lux.Secret, error) {
		s := lux.Secret{As: previewSecretAs}
		return s, row.Scan(&s.Name, &s.Value)
	})
}

// declaredSecrets are the names of a preview spec's project secrets.
func declaredSecrets(spec lux.Spec) []string {
	names := []string{}
	for _, s := range spec.Secrets {
		if s.As == previewSecretAs {
			names = append(names, s.Name)
		}
	}
	return names
}

// resumeSecrets are the current values of the secrets the preview's Run
// was submitted with, and the names among them the project no longer has.
func (p *Previews) resumeSecrets(ctx context.Context, r previewRun) (secrets []lux.Secret, gone []string, err error) {
	err = p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var recorded []string
		if err := tx.QueryRow(ctx, `SELECT preview_secrets FROM runs WHERE id = $1`, r.ID).Scan(&recorded); err != nil {
			return err
		}
		if len(recorded) == 0 {
			return nil
		}
		// Only the declared names' values: one added since is not sent.
		rows, err := tx.Query(ctx, `SELECT name, value FROM project_secrets WHERE project_id = $1 AND name = ANY($2)`,
			r.ProjectID, recorded)
		if err != nil {
			return err
		}
		current := map[string]string{}
		var name, value string
		if _, err := pgx.ForEachRow(rows, []any{&name, &value}, func() error {
			current[name] = value
			return nil
		}); err != nil {
			return err
		}
		for _, name := range recorded {
			value, ok := current[name]
			if !ok {
				gone = append(gone, name)
				continue
			}
			secrets = append(secrets, lux.Secret{Name: name, Value: value, As: previewSecretAs})
		}
		return nil
	})
	return secrets, gone, err
}

// submitKey is the idempotency key of a preview's lux Run: its id, and
// past the first lux Run, which one.
func submitKey(runID string, generation int) string {
	if generation == 0 {
		return runID
	}
	return fmt.Sprintf("%s/%d", runID, generation)
}

// cancelForReplacement cancels a lux Run the preview is about to replace,
// so lux drops the snapshot it kept to resume it. A refusal (already over,
// unknown) is as good.
func (p *Previews) cancelForReplacement(ctx context.Context, r previewRun) error {
	p.unfollow(r.ID)
	if err := p.Lux.Cancel(ctx, r.LuxRunID); err != nil {
		if le, ok := lux.AsError(err); !ok || le.Retryable() {
			return err
		}
	}
	return nil
}

// replaceParked puts a parked preview of the eager path back to pending
// with a new lux Run to come, its pending starts kept: its own lux Run
// declared a secret the project no longer has, so it cannot be resumed.
func (p *Previews) replaceParked(ctx context.Context, r previewRun, gone []string) error {
	p.Log.Info("a secret the preview's Run was submitted with was removed; submitting a new run", "run", r.ID, "secrets", gone)
	if err := p.cancelForReplacement(ctx, r); err != nil {
		return err
	}
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET status = 'pending', lux_run_id = NULL, lux_state = NULL, lux_after_event = 0,
			lux_start_event = 0, lux_ran_event = 0, lux_stop_reason = NULL, dude_pause = NULL, preview_secrets = '{}',
			lux_generation = lux_generation + 1, next_attempt_at = NULL
			WHERE id = $1 AND lux_run_id = $2 AND status = 'paused'`, r.ID, r.LuxRunID)
		return err
	})
}
