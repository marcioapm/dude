package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// The recovery worker (SettleRecoveries): carries each recover_run on,
// apart from the phase sweep, as SettlePublishes carries publishes. A
// requested recovery reads lux's checkpoints of the dead Run's lux Run and
// submits a short publisher Run cloned from them (submitted); once it runs,
// the commits it would push are read and scanned for the dead Run's
// secrets, and lux is asked to push them (asked); lux's git.push ends it.
// Every lux call is made with no transaction open, and each step is
// committed after its call returned: a worker that died replays the call
// (submit by its idempotency key, push by its request id, cancel at will).
// The publisher is cancelled once the recovery ends.

// recoverySlots is how many recoveries one pass carries on at once.
const recoverySlots = 3

// recoveryClaim is how long a claimed recovery is left to its worker.
const recoveryClaim = 2 * time.Minute

// recoveryStartGiveUp is how long a publisher Run may take to start.
const recoveryStartGiveUp = 10 * time.Minute

// recoveryEvery is how often a recovery waiting on lux is looked at.
const recoveryEvery = 2 * time.Second

// recoveryTimeout is the publisher Run's limit in lux: it only pushes.
const recoveryTimeout = "30m"

type recoveryRow struct {
	ID, Org, ProjectID, TaskID, RunID, Branch, Status string
	DeadLux, Publisher                                string
	Image                                             string
	BaseSHAs                                          map[string]string
	Checkpoints                                       json.RawMessage
	Dirty                                             []string
	EventsAfter                                       int64
	StepLong                                          bool
	Failures                                          int
	Claim                                             string
}

// recovered is one checkpoint chosen: its repository, head, and the commit
// the dead Run's checkout started from.
type recovered struct {
	Repo string `json:"repo"`
	Head string `json:"head"`
	Base string `json:"base"`
}

// SettleRecoveries is one pass of the recovery worker. Returns how many it
// claimed.
func (s *Syncer) SettleRecoveries(ctx context.Context) (int, error) {
	var todo []recoveryRow
	if err := s.DB.InSystem(ctx, "run-recoveries", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH due AS (
				SELECT id FROM run_recoveries
				WHERE ended_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at <= now())
				  AND (claimed_until IS NULL OR claimed_until <= now())
				ORDER BY next_attempt_at NULLS FIRST, created_at LIMIT $1 FOR UPDATE SKIP LOCKED),
			claimed AS (UPDATE run_recoveries c SET claim_token = gen_random_uuid()::text, claimed_until = now() + $2::interval
				FROM due WHERE c.id = due.id RETURNING c.*)
			SELECT c.id, c.organization_id, COALESCE(r.project_id, ''), c.task_id, c.run_id, c.branch, c.status,
				COALESCE(r.lux_run_id, ''), COALESCE(c.publisher_lux_run_id, ''), COALESCE(r.image->>'ref', ''), r.base_shas,
				c.checkpoints, COALESCE(c.dirty, '{}'), c.events_after, c.stepped_at < now() - $3::interval, c.failures, c.claim_token
			FROM claimed c JOIN runs r ON r.id = c.run_id ORDER BY c.created_at`,
			recoverySlots, recoveryClaim.String(), recoveryStartGiveUp.String())
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[recoveryRow])
		return err
	}); err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, c := range todo {
		wg.Go(func() { s.carryRecovery(ctx, c) })
	}
	wg.Wait()
	return len(todo), nil
}

// carryRecovery carries one claimed recovery a step on, renewing its claim
// meanwhile, and lets the claim go once done.
func (s *Syncer) carryRecovery(ctx context.Context, c recoveryRow) {
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		t := time.NewTicker(recoveryClaim / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE run_recoveries SET claimed_until = now() + $3::interval
						WHERE id = $1 AND claim_token = $2`, c.ID, c.Claim, recoveryClaim.String())
					return err
				})
			}
		}
	}()
	err := s.settleRecovery(ctx, c)
	close(done)
	<-renewed
	switch {
	case errors.Is(err, delivery.ErrClaimLost):
	case err != nil:
		s.Log.Warn("a recovery failed", "recovery", c.ID, "error", err)
		wait := min(publishBackoffBase<<min(c.Failures, 16), publishBackoffMax)
		s.recoveryAt(ctx, c, wait, true)
	}
}

// recoveryAt sets when the recovery is next looked at, letting its claim
// go; failed counts a failure toward its back-off.
func (s *Syncer) recoveryAt(ctx context.Context, c recoveryRow, after time.Duration, failed bool) {
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE run_recoveries SET next_attempt_at = now() + $2::interval,
				failures = CASE WHEN $3 THEN failures + 1 ELSE 0 END, claim_token = NULL, claimed_until = NULL
			WHERE id = $1 AND ended_at IS NULL AND claim_token = $4`, c.ID, after.String(), failed, c.Claim)
		return err
	}); err != nil {
		s.Log.Warn("scheduling a recovery failed", "recovery", c.ID, "error", err)
	}
}

func (s *Syncer) settleRecovery(ctx context.Context, c recoveryRow) error {
	switch c.Status {
	case delivery.RecoveryRequested:
		return s.submitRecovery(ctx, c)
	case delivery.RecoverySubmitted:
		return s.scanAndPush(ctx, c)
	case delivery.RecoveryAsked:
		return s.awaitRecoveryPush(ctx, c)
	}
	return nil
}

// endRecovery cancels the publisher, if there is one, and records the end.
func (s *Syncer) endRecovery(ctx context.Context, c recoveryRow, status, why string, results []delivery.RecoveredRepo) error {
	if c.Publisher != "" {
		if err := s.Lux.Cancel(ctx, c.Publisher); err != nil && !lux.IsNotFound(err) {
			if le, ok := lux.AsError(err); !ok || le.Retryable() {
				return err
			}
		}
	}
	return s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		return delivery.RecoveryEndedTx(ctx, tx, delivery.RecoveryEnd{ID: c.ID, Org: c.Org, ProjectID: c.ProjectID, TaskID: c.TaskID,
			RunID: c.RunID, Branch: c.Branch, Claim: c.Claim, Status: status, Results: results, Dirty: c.Dirty, Error: why})
	})
}

// endRequested ends a recovery still requested, failed for why. A pass
// before this one that asked lux for its publisher (its checkpoints were
// recorded first) may have had lux take it and not recorded it: asked
// again under its idempotency key, lux answers that Run, which is recorded
// (its cost is read) and cancelled with the end. A recovery that never
// asked has no publisher to find, and none is asked for.
//
// It is asked for with the shell, not the spec submitted: that spec holds
// tokens minted for that submit, which no later one can send again.
func (s *Syncer) endRequested(ctx context.Context, c recoveryRow, why string, results []delivery.RecoveredRepo) error {
	if c.Checkpoints != nil {
		spec, err := s.recoveryShell(ctx, c)
		if err != nil {
			return err
		}
		run, err := s.Lux.Submit(ctx, spec, "recover:"+c.ID)
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			// Refused, not answered with a Run: lux holds none under the key.
			return s.endRecovery(ctx, c, delivery.RecoveryFailed, why, results)
		} else if err != nil {
			return err
		}
		if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE run_recoveries SET publisher_lux_run_id = $3, lux_cost_next_at = now()
				WHERE id = $1 AND claim_token = $2 AND status = 'requested'`, c.ID, c.Claim, run.ID)
			if err == nil && tag.RowsAffected() == 0 {
				return delivery.ErrClaimLost
			}
			return err
		}); err != nil {
			return err
		}
		c.Publisher = run.ID
	}
	return s.endRecovery(ctx, c, delivery.RecoveryFailed, why, results)
}

// submitRecovery reads lux's checkpoints of the dead Run and submits the
// publisher Run cloned from those with commits beyond the Run's start, in
// the repositories the task may change.
func (s *Syncer) submitRecovery(ctx context.Context, c recoveryRow) error {
	if c.DeadLux == "" {
		return s.endRecovery(ctx, c, delivery.RecoveryFailed, "it never ran on lux: nothing was committed", nil)
	}
	// A Run stopped or restarted a moment ago is aborted in dude before the
	// syncer has had lux end it: its checkpoints are those of its last stop,
	// not of its end. Read them once lux has ended it, or once the recovery
	// has waited past its give-up: a control nothing clears, or a Run lux
	// never ends, is not waited on for ever.
	if !c.StepLong {
		var control string
		if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT control::text FROM runs WHERE id = $1`, c.RunID).Scan(&control)
		}); err != nil {
			return err
		}
		if control != "none" {
			s.recoveryAt(ctx, c, recoveryEvery, false)
			return nil
		}
		run, err := s.Lux.Get(ctx, c.DeadLux)
		if le, ok := lux.AsError(err); err != nil && !lux.IsNotFound(err) && (!ok || le.Retryable()) {
			return err
		} else if err == nil && !lux.Terminal(run.State) {
			s.recoveryAt(ctx, c, recoveryEvery, false)
			return nil
		}
	}
	cps, err := s.Lux.Checkpoints(ctx, c.DeadLux)
	if err != nil {
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return s.endRequested(ctx, c, "lux has no checkpoint of it: "+le.Message, nil)
		}
		return err
	}
	var repos []delivery.Repository
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
		repos, err = delivery.TaskRepositories(ctx, tx, c.TaskID)
		return err
	}); err != nil {
		return err
	}
	byName := repositoriesBySpecName(repos)
	var chosen []recovered
	var dirty, notUploaded []string
	for _, cp := range cps {
		repo, ok := byName[cp.Repo]
		if !ok || repo.Access == "read" {
			continue
		}
		if cp.Dirty {
			dirty = append(dirty, cp.Repo)
		}
		base := c.BaseSHAs[cp.Repo]
		if cp.Head == "" || cp.Head == base {
			continue
		}
		if !cp.Uploaded {
			notUploaded = append(notUploaded, cp.Repo)
			continue
		}
		chosen = append(chosen, recovered{Repo: cp.Repo, Head: cp.Head, Base: base})
	}
	c.Dirty = dirty
	if len(chosen) == 0 {
		why := "nothing committed beyond its start"
		if len(cps) == 0 {
			why = "lux kept no checkpoint of its checkouts"
		} else if len(notUploaded) > 0 {
			why = "lux's checkpoint of " + strings.Join(notUploaded, ", ") + " was never uploaded"
		}
		return s.endRequested(ctx, c, why, nil)
	}
	spec, err := s.recoverySpec(ctx, c, chosen, byName)
	if err != nil {
		if forge.Refused(err) {
			return s.endRequested(ctx, c, "GitHub would not give dude a token to push: "+err.Error(), nil)
		}
		return err
	}
	// The checkpoints chosen are recorded before lux is asked: a pass that
	// ends this recovery still requested looks for a publisher only then.
	raw, _ := json.Marshal(chosen)
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE run_recoveries SET checkpoints = $3::jsonb
			WHERE id = $1 AND claim_token = $2 AND status = 'requested'`, c.ID, c.Claim, raw)
		if err == nil && tag.RowsAffected() == 0 {
			return delivery.ErrClaimLost
		}
		return err
	}); err != nil {
		return err
	}
	run, err := s.Lux.Submit(ctx, spec, "recover:"+c.ID)
	if err != nil {
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return s.endRecovery(ctx, c, delivery.RecoveryFailed, "lux would not start the Run that pushes it: "+le.Message, nil)
		}
		return err
	}
	return s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		// Its publisher's cost is read from now on (Costs), as a Run's is.
		tag, err := tx.Exec(ctx, `UPDATE run_recoveries SET status = 'submitted', publisher_lux_run_id = $3, checkpoints = $4::jsonb,
				dirty = $5, stepped_at = now(), lux_cost_next_at = now(), next_attempt_at = now() + $6::interval, failures = 0, claim_token = NULL, claimed_until = NULL
			WHERE id = $1 AND claim_token = $2 AND status = 'requested'`, c.ID, c.Claim, run.ID, raw, dirty, recoveryEvery.String())
		if err == nil && tag.RowsAffected() == 0 {
			return delivery.ErrClaimLost
		}
		return err
	})
}

// recoverySpec is the publisher Run: a generic container that only waits,
// each chosen repository cloned from the dead Run's checkpoint, pushed to
// the recovery's branch with the forge's tokens, which the container never
// sees; the network floor; never resumed by lux.
func (s *Syncer) recoverySpec(ctx context.Context, c recoveryRow, chosen []recovered, byName map[string]delivery.Repository) (lux.Spec, error) {
	gh, err := s.Forges.For(ctx, c.Org)
	if err != nil {
		return lux.Spec{}, err
	}
	var creds []CredentialRepo
	for _, cp := range chosen {
		creds = append(creds, CredentialRepo{Name: cp.Repo, URL: byName[cp.Repo].URL})
	}
	names, tokens, err := RepoCredentials(ctx, gh, creds)
	if err != nil {
		return lux.Spec{}, err
	}
	spec, err := s.recoveryShell(ctx, c)
	if err != nil {
		return lux.Spec{}, err
	}
	spec.Workload.Workdir = RepoPath(chosen[0].Repo)
	spec.Git = &lux.Git{Push: &lux.Push{Branch: c.Branch}}
	for _, cp := range chosen {
		spec.Git.Repositories = append(spec.Git.Repositories, lux.Repository{Name: cp.Repo, Path: RepoPath(cp.Repo),
			From: &lux.RepositoryFrom{Run: c.DeadLux, Repo: cp.Repo}, Credential: names[cp.Repo]})
	}
	for _, t := range tokens {
		t.As = "none"
		spec.Secrets = append(spec.Secrets, t)
	}
	return spec, nil
}

// recoveryShell is the publisher Run without its repositories: what
// endRequested asks lux for again, by its idempotency key, to learn the
// publisher lux took.
func (s *Syncer) recoveryShell(ctx context.Context, c recoveryRow) (lux.Spec, error) {
	image := c.Image
	if image == "" {
		image = s.Agent.DefaultImage
	}
	spec := lux.Spec{
		Name:         "recover " + c.RunID,
		Labels:       map[string]string{lux.AppLabel: lux.App, "dude.org": c.Org, "dude.task": c.TaskID, "dude.kind": "recovery", "dude.recovery": c.ID, "dude.run": c.RunID},
		Image:        lux.Image{Ref: image},
		Workload:     lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}},
		Volumes:      []lux.Volume{{Name: "workspace", Path: workspaceDir, Kind: "state"}},
		Timeout:      recoveryTimeout,
		Network:      egress(s.Agent, nil, nil),
		ResumePolicy: "never",
	}
	login, err := LoginFor(ctx, s.Registry, image, nil)
	if err != nil {
		return lux.Spec{}, err
	}
	login.Apply(&spec)
	return spec, nil
}

// scanAndPush waits for the publisher to run, then reads every commit the
// push would send and scans it for the dead Run's secrets; only then is
// lux asked to push. A secret found refuses the recovery for good.
func (s *Syncer) scanAndPush(ctx context.Context, c recoveryRow) error {
	run, err := s.Lux.Get(ctx, c.Publisher)
	if err != nil {
		if lux.IsNotFound(err) {
			return s.endRecovery(ctx, c, delivery.RecoveryFailed, "lux lost the Run that pushes it", nil)
		}
		return err
	}
	switch {
	case lux.Terminal(run.State):
		return s.endRecovery(ctx, c, delivery.RecoveryFailed, "the Run that pushes it ended before it pushed: "+run.State, nil)
	case run.State != "running" && c.StepLong:
		return s.endRecovery(ctx, c, delivery.RecoveryFailed, "the Run that pushes it never started", nil)
	case run.State != "running":
		s.recoveryAt(ctx, c, recoveryEvery, false)
		return nil
	}
	var chosen []recovered
	if err := json.Unmarshal(c.Checkpoints, &chosen); err != nil {
		return s.endRecovery(ctx, c, delivery.RecoveryFailed, "its checkpoints are unreadable", nil)
	}
	found, err := s.scanRecovery(ctx, c, chosen)
	var unchecked errUnchecked
	switch {
	case errors.As(err, &unchecked):
		return s.endRecovery(ctx, c, delivery.RecoveryFailed, err.Error(), nil)
	case err != nil:
		return err
	case len(found) > 0:
		return s.refuseRecovery(ctx, c, found)
	}
	creds, err := s.recoveryCredentials(ctx, c, chosen)
	if err != nil {
		return err
	}
	if err := Push(ctx, s.Lux, s.Log, c.Publisher, c.ID, creds); err != nil {
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return s.endRecovery(ctx, c, delivery.RecoveryFailed, "lux would not push: "+le.Message, nil)
		}
		return err
	}
	return s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE run_recoveries SET status = 'asked', stepped_at = now(), failures = 0,
				next_attempt_at = now() + $3::interval, claim_token = NULL, claimed_until = NULL
			WHERE id = $1 AND claim_token = $2 AND status = 'submitted'`, c.ID, c.Claim, recoveryEvery.String())
		if err == nil && tag.RowsAffected() == 0 {
			return delivery.ErrClaimLost
		}
		return err
	})
}

// scanRecovery finds the dead Run's secrets in what the push would send:
// every commit from where the dead Run's checkout started to the
// checkpoint's head, and the branch's name. Nothing for a Run given none.
func (s *Syncer) scanRecovery(ctx context.Context, c recoveryRow, chosen []recovered) ([]SecretInWork, error) {
	var red *redact.Redactor
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
		red, err = ledger.Redactor(ctx, tx, c.RunID)
		return err
	}); err != nil || red.Empty() {
		return nil, err
	}
	bases := map[string]string{}
	for _, cp := range chosen {
		if cp.Base == "" {
			return nil, errUnchecked{"lux never reported where " + cp.Repo + "'s checkout started"}
		}
		bases[cp.Repo] = cp.Base
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	history, err := s.execChecked(ctx, phaseRun{ID: c.RunID, LuxRunID: c.Publisher}, historyCommand(bases, nil), "the history")
	exit := execExit{}
	switch {
	case errors.As(err, &exit) && exit.code == 4:
		return nil, errUnchecked{exit.Error()}
	case errors.As(err, &exit) && exit.code == 5, err == nil && len(history) > historyLimit:
		return nil, errUnchecked{fmt.Sprintf("the commits to push are over %d MiB, more than dude reads", historyLimit>>20)}
	case err != nil:
		return nil, err
	}
	if missing := missingHistory(history, bases); len(missing) > 0 {
		return nil, errUnchecked{"no history was read for " + strings.Join(missing, ", ")}
	}
	var found hits
	found.scanHistory(red, history)
	if name, ok := red.Contains(c.Branch); ok {
		found.add(name, "the branch name")
	}
	return found, nil
}

// refuseRecovery ends a recovery whose work holds a secret: nothing is
// pushed, and the dead Run's ledger says which secret and where, never
// the value.
func (s *Syncer) refuseRecovery(ctx context.Context, c recoveryRow, found []SecretInWork) error {
	var secrets, files, said []string
	for _, f := range found {
		if !slices.Contains(secrets, f.Secret) {
			secrets = append(secrets, f.Secret)
		}
		if !slices.Contains(files, f.File) {
			files = append(files, f.File)
		}
		said = append(said, f.Secret+" in "+f.File)
	}
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		var said bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE run_id = $1 AND event_type = $2
			AND payload->>'recoveryId' = $3)`, c.RunID, EvPushRefused, c.ID).Scan(&said); err != nil || said {
			return err
		}
		return s.event(ctx, tx, phaseRun{ID: c.RunID, Org: c.Org, ProjectID: c.ProjectID, TaskID: c.TaskID}, EvPushRefused,
			ledger.ActorSystem, map[string]any{"secrets": secrets, "files": files, "recoveryId": c.ID})
	}); err != nil {
		return err
	}
	s.Log.Warn("refused to push a recovery holding a project secret", "recovery", c.ID, "run", c.RunID, "secrets", secrets)
	return s.endRecovery(ctx, c, delivery.RecoveryRefused,
		"its work contains the project secret "+strings.Join(said, ", ")+"; a person must remove it", nil)
}

// recoveryCredentials are fresh tokens for the publisher's push, by its
// repositories' names.
func (s *Syncer) recoveryCredentials(ctx context.Context, c recoveryRow, chosen []recovered) (map[string]string, error) {
	gh, err := s.Forges.For(ctx, c.Org)
	if err != nil || gh == nil {
		return nil, err
	}
	var repos []delivery.Repository
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
		repos, err = delivery.TaskRepositories(ctx, tx, c.TaskID)
		return err
	}); err != nil {
		return nil, err
	}
	byName := repositoriesBySpecName(repos)
	have := make([]string, 0, len(chosen))
	creds := make([]CredentialRepo, 0, len(chosen))
	for _, cp := range chosen {
		have = append(have, cp.Repo)
		creds = append(creds, CredentialRepo{Name: cp.Repo, URL: byName[cp.Repo].URL})
	}
	return OperationCredentials(ctx, gh, have, creds)
}

// awaitRecoveryPush reads the publisher's lux events for its git.push, by
// the recovery's id, and ends the recovery with it; given up on once the
// push has gone unreported for askedGiveUp.
func (s *Syncer) awaitRecoveryPush(ctx context.Context, c recoveryRow) error {
	after := c.EventsAfter
	for range askedPages {
		frames, err := s.Lux.Events(ctx, c.Publisher, after)
		if err != nil {
			if le, ok := lux.AsError(err); ok && !le.Retryable() {
				return s.endRecovery(ctx, c, delivery.RecoveryFailed, "lux will not list the push: "+le.Message, nil)
			}
			return err
		}
		for _, f := range frames {
			after = max(after, f.EventID)
			if f.EventType != "git.push" {
				continue
			}
			var push lux.PushResult
			if json.Unmarshal(f.EventData, &push) != nil || push.RequestID != c.ID {
				continue
			}
			var results []delivery.RecoveredRepo
			status := delivery.RecoveryFailed
			for _, r := range push.Results {
				results = append(results, delivery.RecoveredRepo{Repo: r.Repo, Status: r.Status, Commit: r.Commit, Error: r.Error})
				if r.Status == "pushed" || r.Status == "up-to-date" {
					status = delivery.RecoveryDone
				}
			}
			why := ""
			if status == delivery.RecoveryFailed {
				why = "lux pushed none of it"
			}
			return s.endRecovery(ctx, c, status, why, results)
		}
		if len(frames) == 0 {
			break
		}
	}
	if c.StepLong {
		return s.endRecovery(ctx, c, delivery.RecoveryFailed, "lux never reported the push", nil)
	}
	return s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE run_recoveries SET events_after = GREATEST(events_after, $3),
				next_attempt_at = now() + $4::interval, failures = 0, claim_token = NULL, claimed_until = NULL
			WHERE id = $1 AND claim_token = $2 AND status = 'asked'`, c.ID, c.Claim, after, recoveryEvery.String())
		return err
	})
}
