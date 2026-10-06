package servers

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
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

// acceptedSecrets are the names of the project secrets of the Run lux
// accepted, from its stored spec: on a retried submit lux answers with the
// first submit's Run, whatever the retry's spec declared.
func acceptedSecrets(lr lux.Run) []string {
	names := []string{}
	for _, s := range lr.Spec.Secrets {
		if s.As == previewSecretAs && !s.RunnerOnly {
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

// replaceReserved replaces the preview's lux Run under the reservation op:
// its servers read (to carry), the Run cancelled when cancel, each lux call
// within the reservation's time; then record, with the carried servers, in
// one transaction that holds only while op is still the row's. false: the
// replacement was not recorded (record's condition, or the reservation,
// no longer held).
func (p *Previews) replaceReserved(ctx context.Context, op *operation, r previewRun, cancel bool, record func(pgx.Tx) (bool, error)) (bool, error) {
	octx, stop := op.context(ctx)
	defer stop()
	var list []lux.Server
	err := op.call(octx, func(c context.Context) (err error) {
		list, err = p.Lux.Servers(c, r.LuxRunID)
		if lux.IsNotFound(err) {
			// A Run lux no longer has carries nothing.
			list, err = nil, nil
		}
		return err
	})
	if err == nil && cancel {
		err = op.call(octx, func(c context.Context) error { return p.cancelForReplacement(c, r) })
	} else if err == nil {
		p.unfollow(r.ID)
	}
	if err != nil {
		p.release(ctx, op)
		return false, err
	}
	return p.finish(ctx, op, func(tx pgx.Tx) (bool, error) {
		won, err := record(tx)
		if err != nil || !won {
			return false, err
		}
		return true, recordCarried(ctx, tx, r, list)
	})
}

// recordCarried records, as a preview's lux Run is replaced, the servers a
// person added to it (list, as lux had them): neither its spec's (the new
// spec has the project's autostart servers) nor a wakeable preview's own
// (attached to every Run). restoreServers adds them to the new Run.
func recordCarried(ctx context.Context, tx pgx.Tx, r previewRun, list []lux.Server) error {
	rows, err := tx.Query(ctx, `SELECT name FROM preview_servers WHERE run_id = $1`, r.ID)
	if err != nil {
		return err
	}
	own, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var carried []lux.ServerInput
	if err := tx.QueryRow(ctx, `SELECT carried_servers FROM runs WHERE id = $1`, r.ID).Scan(&carried); err != nil {
		return err
	}
	no := false
	for _, sv := range list {
		if sv.FromSpec || slices.Contains(own, sv.Name) ||
			slices.ContainsFunc(carried, func(c lux.ServerInput) bool { return c.Name == sv.Name }) {
			continue
		}
		// Not started on the new Run by itself, as a resumed Run does not
		// start an added server: a pending start (startPending), or a
		// person, starts it.
		carried = append(carried, lux.ServerInput{Name: sv.Name, Port: sv.Port, Command: sv.Command, Workdir: sv.Workdir,
			Env: sv.Env, Start: &no})
	}
	_, err = tx.Exec(ctx, `UPDATE runs SET carried_servers = $2 WHERE id = $1`, r.ID, db.NonNil(carried))
	return err
}

// restoreServers adds the servers carryServers recorded to the preview's
// new lux Run, before the submit is recorded: a retry of the same submit
// (same key, same Run) adds them again, one there already being as good.
// One lux refuses for good (the Run ended meanwhile, a server it will not
// take) is left out; the recording clears them.
func (p *Previews) restoreServers(ctx context.Context, r previewRun, luxRunID string) error {
	var carried []lux.ServerInput
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT carried_servers FROM runs WHERE id = $1`, r.ID).Scan(&carried)
	}); err != nil {
		return err
	}
	for _, in := range carried {
		_, err := p.Lux.AddServer(ctx, luxRunID, in)
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			if le.Code != "name_taken" {
				p.Log.Warn("a server added to the preview's replaced Run was not added to the new one", "run", r.ID,
					"server", in.Name, "error", le.Message)
			}
			err = nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// replaceParked puts a parked preview of the eager path back to pending
// with a new lux Run to come, its pending starts kept: its own lux Run
// declared a secret the project no longer has, so it cannot be resumed.
// The row is reserved from the check through the cancel and the update,
// as a resume reserves it (parkedOnSQL): a sweep that read the preview
// before another moved it on (resumed, replaced) cancels nothing.
func (p *Previews) replaceParked(ctx context.Context, r previewRun, gone []string) error {
	op, _, err := p.reserve(ctx, r.Org, r.ID, opReplace, parkedOnSQL, r.LuxRunID, r.Generation)
	if err != nil || op == nil {
		return err
	}
	p.Log.Info("a secret the preview's Run was submitted with was removed; submitting a new run", "run", r.ID, "secrets", gone)
	_, err = p.replaceReserved(ctx, op, r, true, func(tx pgx.Tx) (bool, error) {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'pending', lux_run_id = NULL, lux_state = NULL, lux_after_event = 0,
			lux_start_event = 0, lux_ran_event = 0, lux_stop_reason = NULL, dude_pause = NULL, preview_secrets = '{}',
			lux_generation = lux_generation + 1, next_attempt_at = NULL
			WHERE id = $1 AND `+parkedOnSQL, r.ID, r.LuxRunID, r.Generation)
		return err == nil && tag.RowsAffected() == 1, err
	})
	return err
}
