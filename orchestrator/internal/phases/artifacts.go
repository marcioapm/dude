package phases

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Artifacts records what agents published, once lux has it.
//
// An agent publishes a file with `dude publish` (lux-shim publish). lux
// reports each one on the Run's stream as artifact.published once it can
// be downloaded, and the translator records it then, while the Run goes
// on. This sweep is the backstop: for an older lux, which collects
// $LUX_ARTIFACTS only when the container exits, and for anything the
// stream's follower missed. dude only records them: the bytes stay in lux
// and are streamed from there when someone opens one. Both record through
// recordArtifact, idempotent on lux's artifact id.
//
// A Run is due for collection when it stops (a trigger sets
// artifacts_due_at on every stopping status). lux reports an exit's
// artifacts a moment after the container has gone, once the host has sent
// what it kept, and uploads them after the snapshot's volumes, which can
// take a long while on a busy host. So a due Run is asked again — every few
// seconds at first, then less often — until every exit is reported and
// every artifact uploaded. Only an exit lux will never report (a host lost
// with its Run) is given up on, and only after a day.
type Artifacts struct {
	DB  *db.DB
	Lux lux.Client
	// nil logs to slog.Default.
	Log *slog.Logger
}

// RecordMemoryLimit adds to a Run's runs.machine the memory limit lux
// reports for its latest placement, once: so a finished Run still says what
// its container got. Nothing when lux reports none, the Run recorded no
// machine, or the limit is already there. A failed write is logged, not
// returned: the limit only informs the run chip, and the next read of the
// lux Run writes it again.
func RecordMemoryLimit(ctx context.Context, d *db.DB, log *slog.Logger, org, runID string, lr lux.Run) {
	limit := lr.MemoryLimit()
	if limit == nil {
		return
	}
	err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET machine = jsonb_set(machine, '{memoryLimit}', to_jsonb($2::bigint))
			WHERE id = $1 AND machine IS NOT NULL AND machine->>'memoryLimit' IS NULL`, runID, *limit)
		return err
	})
	if err != nil && ctx.Err() == nil {
		if log == nil {
			log = slog.Default()
		}
		log.Warn("recording a Run's memory limit failed", "run", runID, "error", err)
	}
}

// How long to keep asking lux about an exit it has not reported, or about
// artifacts it has not uploaded. Long, because giving up loses files lux
// may still deliver; the asking backs off meanwhile.
const artifactsPatience = 24 * time.Hour

// ArtifactEventType is the ledger event for a newly recorded artifact.
const ArtifactEventType = "artifact.created"

type dueRun struct {
	ID, Org, ProjectID, TaskID, LuxRunID string
	// dude's status for it: stopped, or running again after a resume.
	Status string
	DueAt  time.Time
	// Asked about for longer than artifactsPatience, by the database's clock.
	Overdue bool
}

// Sweep records the artifacts of every Run due, a few at a time.
func (a *Artifacts) Sweep(ctx context.Context) (int, error) {
	var due []dueRun
	if err := a.DB.InSystem(ctx, "artifacts", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id, COALESCE(project_id, ''), COALESCE(task_id, ''), lux_run_id, status::text, artifacts_due_at,
				artifacts_due_at < now() - make_interval(secs => $1)
			FROM runs WHERE artifacts_due_at IS NOT NULL AND artifacts_next_at <= now()
			ORDER BY artifacts_next_at LIMIT 50`, artifactsPatience.Seconds())
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (dueRun, error) {
			var r dueRun
			return r, row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.TaskID, &r.LuxRunID, &r.Status, &r.DueAt, &r.Overdue)
		})
		return err
	}); err != nil {
		return 0, err
	}
	// A few at a time: one slow answer from lux must not hold up the rest.
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, r := range due {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := a.collect(ctx, r); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(due), errors.Join(errs...)
}

// collect records a Run's published artifacts, and either marks it done or
// asks again shortly.
func (a *Artifacts) collect(ctx context.Context, r dueRun) error {
	settled, err := a.settled(ctx, r)
	if err != nil {
		return a.later(ctx, r, err)
	}
	if !settled && !r.Overdue {
		return a.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error { return a.askAgainLater(ctx, tx, r) })
	}
	found, err := a.Lux.Artifacts(ctx, r.LuxRunID)
	if lux.IsNotFound(err) {
		found, err = nil, nil // lux no longer has it: nothing more will come
	}
	if err != nil {
		return a.later(ctx, r, err)
	}
	// Not yet uploaded from the host: recorded once it can be downloaded,
	// so a listed artifact always opens.
	waiting := false
	var ready []lux.Artifact
	for _, art := range found {
		if !strings.HasPrefix(art.Path, lux.PublishedPrefix) {
			continue // only what the agent published is for people
		}
		if !art.Available {
			waiting = true
			continue
		}
		ready = append(ready, art)
	}
	// The final diff the beforeStop hook left is dude's own: recorded as
	// the Run's diff, never listed as a file for people. The latest exit's,
	// and of each repository's patch its latest version (every version is
	// listed).
	var final []lux.Artifact
	files := ready[:0]
	for _, art := range ready {
		if !strings.HasPrefix(art.Path, FinalDiffPrefix) {
			files = append(files, art)
			continue
		}
		if _, ok := finalDiffRepo(art.Path); !ok {
			continue // not one of its patches: a file it had not finished
		}
		switch i := slices.IndexFunc(final, func(f lux.Artifact) bool { return f.Path == art.Path }); {
		case len(final) == 0 || art.Epoch > final[0].Epoch:
			final = []lux.Artifact{art}
		case art.Epoch < final[0].Epoch:
		case i < 0:
			final = append(final, art)
		case art.Version > final[i].Version:
			final[i] = art
		}
	}
	// Never at the expense of the files: a diff lux refuses for good (gone,
	// expired) is left out, and one it could not serve just now is asked
	// for again while the files are recorded.
	var finalErr error
	if len(final) > 0 {
		finalErr = a.recordFinalDiff(ctx, r, final)
		if e, ok := lux.AsError(finalErr); ok && e.Status >= 400 && e.Status < 500 {
			finalErr = nil
		}
	}
	err = a.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		for _, art := range files {
			if err := recordArtifact(ctx, tx, artifactRun{r.Org, r.ProjectID, r.TaskID, r.ID}, art); err != nil {
				return err
			}
		}
		// Only if nothing marked it due again meanwhile — a resume and a
		// second exit — so that exit is collected too.
		if !waiting && finalErr == nil || r.Overdue {
			_, err := tx.Exec(ctx, `UPDATE runs SET artifacts_due_at = NULL, artifacts_next_at = NULL
				WHERE id = $1 AND artifacts_due_at = $2`, r.ID, r.DueAt)
			return err
		}
		return a.askAgainLater(ctx, tx, r)
	})
	return errors.Join(err, finalErr)
}

// settled: lux has reported every exit so far, so the list of what the Run
// published is complete.
//
// dude marks a Run stopped before lux has stopped it — a stop or cancel is
// asked for, then carried out — so a lux Run still going is not settled yet.
// Unless dude has resumed it since: then its live placement is a new start,
// whose exit will be collected when it comes.
func (a *Artifacts) settled(ctx context.Context, r dueRun) (bool, error) {
	run, err := a.Lux.Get(ctx, r.LuxRunID)
	if lux.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	RecordMemoryLimit(ctx, a.DB, a.Log, r.Org, r.ID, run)
	resumed := r.Status == statusRunning || r.Status == statusScheduled
	if !lux.Terminal(run.State) && !resumed {
		return false, nil
	}
	for _, p := range run.Placements {
		switch {
		case p.State == "lost", p.WorkloadStartedAt == nil && p.State == "exited":
			// A lost host never reports; a placement whose agent never
			// started published nothing and sends no snapshot either.
		case p.State == "exited":
			if p.SnapshotDoneAt == nil {
				return false, nil
			}
		case !resumed:
			return false, nil // still stopping
		}
	}
	return true, nil
}

// artifactRun is the dude Run an artifact is recorded on.
type artifactRun struct{ Org, ProjectID, TaskID, ID string }

// recordArtifact records one artifact lux can serve, with artifact.created,
// once per lux artifact id however often it is seen (the stream's
// artifact.published, a replay of it, the stop-time sweep).
//
// created_at is clock_timestamp(), not the transaction's now(): versions of
// one name recorded in one transaction (lux lists them in order) keep that
// order in the listing, which numbers them by created_at.
func recordArtifact(ctx context.Context, tx pgx.Tx, r artifactRun, art lux.Artifact) error {
	id := ids.New(ids.Artifact)
	name := strings.TrimPrefix(art.Path, lux.PublishedPrefix)
	ctype := art.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	tag, err := tx.Exec(ctx, `INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type,
			size_bytes, storage_key, sha256, epoch, description, created_at)
		VALUES ($1, $2, $3, 'published', $4, $5, $6, $7, $8, $9, $10, clock_timestamp())
		ON CONFLICT (organization_id, storage_key) DO NOTHING`,
		id, r.Org, r.ID, name, ctype, art.Size, art.ID, art.SHA256, art.Epoch, art.Description)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{
		Type: ArtifactEventType, OrganizationID: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID,
		ActorType: ledger.ActorAgent, ActorID: r.ID, Source: ledger.SourceRunner, CorrelationID: r.TaskID,
		Payload: map[string]any{"artifactId": id, "name": name, "contentType": ctype,
			"sizeBytes": art.Size, "sha256": art.SHA256, "description": art.Description},
	})
	return err
}

// askAgainLater waits a little longer each time, by how long the Run has
// been due: a tenth of that, between a quarter of a second and five minutes. A
// report that trails by a second is picked up at once; an upload stuck for
// an hour is not asked about every two seconds.
func (a *Artifacts) askAgainLater(ctx context.Context, tx pgx.Tx, r dueRun) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET artifacts_next_at = now()
			+ LEAST(GREATEST((now() - artifacts_due_at) / 10, interval '250 milliseconds'), interval '5 minutes')
		WHERE id = $1 AND artifacts_due_at = $2`, r.ID, r.DueAt)
	return err
}

// later backs a Run off after lux did not answer.
func (a *Artifacts) later(ctx context.Context, r dueRun, cause error) error {
	if err := a.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error { return a.askAgainLater(ctx, tx, r) }); err != nil {
		return err
	}
	return cause
}
