package phases

import (
	"context"
	"errors"
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
// An agent publishes a file by writing it into $LUX_ARTIFACTS. lux collects
// that directory whenever the container exits — finished, paused, aborted,
// failed — uploads the files, and lists them on the Run. dude only records
// them: the bytes stay in lux and are streamed from there when someone
// opens one.
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
}

// How long to keep asking lux about an exit it has not reported, or about
// artifacts it has not uploaded. Long, because giving up loses files lux
// may still deliver; the asking backs off meanwhile.
const artifactsPatience = 24 * time.Hour

// ArtifactEventType is the ledger event for a newly recorded artifact.
const ArtifactEventType = "artifact.created"

type dueRun struct {
	ID, Org, ProjectID, WorkItemID, LuxRunID string
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
		rows, err := tx.Query(ctx, `SELECT id, organization_id, project_id, work_item_id, lux_run_id, status::text, artifacts_due_at,
				artifacts_due_at < now() - make_interval(secs => $1)
			FROM runs WHERE artifacts_due_at IS NOT NULL AND artifacts_next_at <= now()
			ORDER BY artifacts_next_at LIMIT 50`, artifactsPatience.Seconds())
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (dueRun, error) {
			var r dueRun
			return r, row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.WorkItemID, &r.LuxRunID, &r.Status, &r.DueAt, &r.Overdue)
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
	return a.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		for _, art := range ready {
			if err := a.record(ctx, tx, r, art); err != nil {
				return err
			}
		}
		// Only if nothing marked it due again meanwhile — a resume and a
		// second exit — so that exit is collected too.
		if !waiting || r.Overdue {
			_, err := tx.Exec(ctx, `UPDATE runs SET artifacts_due_at = NULL, artifacts_next_at = NULL
				WHERE id = $1 AND artifacts_due_at = $2`, r.ID, r.DueAt)
			return err
		}
		return a.askAgainLater(ctx, tx, r)
	})
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

func (a *Artifacts) record(ctx context.Context, tx pgx.Tx, r dueRun, art lux.Artifact) error {
	id := ids.New(ids.Artifact)
	name := strings.TrimPrefix(art.Path, lux.PublishedPrefix)
	ctype := art.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	tag, err := tx.Exec(ctx, `INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type,
			size_bytes, storage_key, sha256, epoch)
		VALUES ($1, $2, $3, 'published', $4, $5, $6, $7, $8, $9)
		ON CONFLICT (organization_id, storage_key) DO NOTHING`,
		id, r.Org, r.ID, name, ctype, art.Size, art.ID, art.SHA256, art.Epoch)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{
		Type: ArtifactEventType, OrganizationID: r.Org, ProjectID: r.ProjectID, WorkItemID: r.WorkItemID, RunID: r.ID,
		ActorType: ledger.ActorAgent, ActorID: r.ID, Source: ledger.SourceRunner, CorrelationID: r.WorkItemID,
		Payload: map[string]any{"artifactId": id, "name": name, "contentType": ctype,
			"sizeBytes": art.Size, "sha256": art.SHA256},
	})
	return err
}

// askAgainLater waits a little longer each time, by how long the Run has
// been due: a tenth of that, between two seconds and five minutes. A
// report that trails by a second is picked up at once; an upload stuck for
// an hour is not asked about every two seconds.
func (a *Artifacts) askAgainLater(ctx context.Context, tx pgx.Tx, r dueRun) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET artifacts_next_at = now()
			+ LEAST(GREATEST((now() - artifacts_due_at) / 10, interval '2 seconds'), interval '5 minutes')
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
