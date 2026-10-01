package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Builder is dude-image-builder's loop: one job at a time, a Run's finish
// before any build, each kind oldest first. It connects as dude_builder,
// which sees every organization's image tables and nothing else
// (migration 068), and never runs inside the orchestrator or the backend.
type Builder struct {
	DB     *db.DB
	Podman Podman
	// Where versions are pushed: <registry>/<repo>, no tag.
	Repository string
	// The dude layer, a digest ref (DUDE_LAYER_IMAGE).
	Layer   string
	Limits  Limits
	MinFree int64
	Log     *slog.Logger
	// How often a running job's log and heartbeat are written; 2s when 0.
	Flush time.Duration
	// How long an idle builder waits before looking again; 3s when 0.
	Poll time.Duration
}

// ActorID is who the builder's events are by.
const ActorID = "dude-image-builder"

// job is a claimed image_builds row and the version it builds.
type job struct {
	ID, Org, VersionID, Kind, LayerRef string
	Restarts                           int
	ImageID, ImageName                 string
	Number                             int
	Containerfile                      string
	BuildArgs                          map[string]string
	UserRef                            string
}

// Run takes jobs until ctx ends.
func (b *Builder) Run(ctx context.Context) error {
	if err := b.Recover(ctx); err != nil {
		return err
	}
	poll := b.Poll
	if poll == 0 {
		poll = 3 * time.Second
	}
	for ctx.Err() == nil {
		did, err := b.Once(ctx)
		if err != nil {
			b.Log.Error("image builder", "error", err)
		}
		if !did || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(poll):
			}
		}
	}
	return nil
}

// Recover deals with jobs a builder left running when it died: one is put
// back in the queue the first time, failed the second. There is one
// builder, so at its start every running job is a dead one's.
func (b *Builder) Recover(ctx context.Context) error {
	return pgx.BeginFunc(ctx, b.DB.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id, b.organization_id, b.image_version_id, b.kind, b.restarts, v.image_id, i.name, COALESCE(v.number, 0)
			FROM image_builds b JOIN image_versions v ON v.id = b.image_version_id JOIN images i ON i.id = v.image_id
			WHERE b.state = 'running' FOR UPDATE OF b`)
		if err != nil {
			return err
		}
		var dead []job
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.ID, &j.Org, &j.VersionID, &j.Kind, &j.Restarts, &j.ImageID, &j.ImageName, &j.Number); err != nil {
				return err
			}
			dead = append(dead, j)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, j := range dead {
			if j.Restarts == 0 {
				b.Log.Warn("re-queueing a job its builder died under", "build", j.ID)
				if _, err := tx.Exec(ctx, `UPDATE image_builds SET state = 'queued', restarts = restarts + 1, started_at = NULL,
					heartbeat_at = NULL, stage = NULL, log = log || E'\n— builder restarted; trying again —\n' WHERE id = $1`, j.ID); err != nil {
					return err
				}
				if j.Kind == "build" {
					if _, err := tx.Exec(ctx, `UPDATE image_versions SET state = 'queued', updated_at = now()
						WHERE id = $1 AND state IN ('building', 'pushing')`, j.VersionID); err != nil {
						return err
					}
				}
				continue
			}
			if err := b.failIn(ctx, tx, j, "the builder restarted while building it, twice"); err != nil {
				return err
			}
		}
		return nil
	})
}

// Once claims and does one job; false when there was none.
func (b *Builder) Once(ctx context.Context) (bool, error) {
	j, ok, err := b.claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	b.Log.Info("image job started", "build", j.ID, "kind", j.Kind, "image", j.ImageName, "version", j.Number)
	b.do(ctx, j)
	return true, nil
}

func (b *Builder) claim(ctx context.Context) (job, bool, error) {
	var j job
	var args []byte
	err := pgx.BeginFunc(ctx, b.DB.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE image_builds SET state = 'running', started_at = now(), heartbeat_at = now(), stage = 'resolving'
			WHERE id = (SELECT id FROM image_builds WHERE state = 'queued'
			            ORDER BY (kind = 'build'), requested_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
			RETURNING id, organization_id, image_version_id, kind, COALESCE(layer_ref, ''), restarts`).
			Scan(&j.ID, &j.Org, &j.VersionID, &j.Kind, &j.LayerRef, &j.Restarts)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT v.image_id, i.name, COALESCE(v.number, 0), v.containerfile, v.build_args, COALESCE(v.user_ref, '')
			FROM image_versions v JOIN images i ON i.id = v.image_id WHERE v.id = $1`, j.VersionID).
			Scan(&j.ImageID, &j.ImageName, &j.Number, &j.Containerfile, &args, &j.UserRef); err != nil {
			return err
		}
		if j.Kind == "build" {
			if _, err := tx.Exec(ctx, `UPDATE image_versions SET state = 'building', updated_at = now() WHERE id = $1`, j.VersionID); err != nil {
				return err
			}
		}
		return b.event(ctx, tx, j, "image.build_started", map[string]any{"imageId": j.ImageID, "buildId": j.ID, "kind": j.Kind})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return job{}, false, nil
	}
	if err != nil {
		return job{}, false, err
	}
	_ = json.Unmarshal(args, &j.BuildArgs)
	return j, true, nil
}

// progress is a running job's log and stage, written to its row every
// Flush with the heartbeat, so the build page follows it.
type progress struct {
	mu    sync.Mutex
	tail  Tail
	stage string
	dirty bool
}

func (p *progress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirty = true
	return p.tail.Write(b)
}

func (p *progress) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p, format, args...)
}

func (p *progress) setStage(s string) {
	p.mu.Lock()
	p.stage, p.dirty = s, true
	p.mu.Unlock()
}

func (p *progress) snapshot() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirty = false
	return p.tail.String(), p.stage
}

func (b *Builder) flushEvery() time.Duration {
	if b.Flush == 0 {
		return 2 * time.Second
	}
	return b.Flush
}

// do runs a claimed job to its end, recording how it ended. Its own
// failures are the job's; a database that cannot record them is logged.
func (b *Builder) do(ctx context.Context, j job) {
	p := &progress{tail: Tail{Max: LogMax}, stage: "resolving"}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(b.flushEvery())
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				b.flush(context.WithoutCancel(ctx), j, p)
			}
		}
	}()
	var err error
	timings := map[string]float64{}
	if j.Kind == "finish" {
		err = b.finishJob(ctx, j, p, timings)
	} else {
		err = b.buildJob(ctx, j, p, timings)
	}
	close(stop)
	<-done
	log, stage := p.snapshot()
	record := context.WithoutCancel(ctx)
	if err != nil {
		timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTimeout)
		var sentence string
		if r, ok := err.(reason); ok {
			sentence = string(r)
		} else {
			sentence = Failure(stage, log, err, b.Limits.Memory, timedOut)
		}
		b.Log.Warn("image job failed", "build", j.ID, "error", sentence, "cause", err)
		if ferr := pgx.BeginFunc(record, b.DB.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(record, `UPDATE image_builds SET log = $2, build_seconds = $3, push_seconds = $4 WHERE id = $1`,
				j.ID, log, nullable(timings["build"]), nullable(timings["push"])); err != nil {
				return err
			}
			return b.failIn(record, tx, j, sentence)
		}); ferr != nil {
			b.Log.Error("recording a failed image job", "build", j.ID, "error", ferr)
		}
		return
	}
	b.Log.Info("image job succeeded", "build", j.ID)
	if _, err := b.DB.Pool.Exec(record, `UPDATE image_builds SET log = $2, build_seconds = $3, push_seconds = $4 WHERE id = $1`,
		j.ID, log, nullable(timings["build"]), nullable(timings["push"])); err != nil {
		b.Log.Error("recording an image job's log", "build", j.ID, "error", err)
	}
}

func nullable(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func (b *Builder) flush(ctx context.Context, j job, p *progress) {
	log, stage := p.snapshot()
	if _, err := b.DB.Pool.Exec(ctx, `UPDATE image_builds SET log = $2, stage = $3, heartbeat_at = now() WHERE id = $1 AND state = 'running'`,
		j.ID, log, stage); err != nil {
		b.Log.Warn("writing an image job's log", "build", j.ID, "error", err)
	}
}

// reason is a failure already in words: the job's error as it stands.
type reason string

func (r reason) Error() string { return string(r) }

// failIn ends a job as failed with a sentence; a build's version fails
// with it, and the image's published version is left as it was.
func (b *Builder) failIn(ctx context.Context, tx pgx.Tx, j job, sentence string) error {
	if _, err := tx.Exec(ctx, `UPDATE image_builds SET state = 'failed', error = $2, finished_at = now(), stage = NULL
		WHERE id = $1`, j.ID, sentence); err != nil {
		return err
	}
	if j.Kind == "build" {
		if _, err := tx.Exec(ctx, `UPDATE image_versions SET state = 'failed', error = $2, updated_at = now()
			WHERE id = $1 AND state IN ('queued', 'building', 'pushing')`, j.VersionID, sentence); err != nil {
			return err
		}
	}
	return b.event(ctx, tx, j, "image.build_failed", map[string]any{"imageId": j.ImageID, "name": j.ImageName,
		"versionId": j.VersionID, "version": j.Number, "buildId": j.ID, "kind": j.Kind, "error": sentence, "layer": j.LayerRef})
}

func (b *Builder) event(ctx context.Context, tx pgx.Tx, j job, typ string, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: j.Org, ActorType: ledger.ActorSystem,
		ActorID: ActorID, Source: ledger.SourceOrchestrator, Payload: payload})
	return err
}

// buildJob builds a version from its Containerfile, pushes its user image,
// finishes it with the current dude layer, and publishes it.
func (b *Builder) buildJob(ctx context.Context, j job, p *progress, timings map[string]float64) error {
	p.printf("build of %s v%d · podman, rootless · %g CPUs · %s memory · %s\n", j.ImageName, j.Number,
		b.Limits.CPUs, HumanMemory(b.Limits.Memory), b.Limits.Platform)
	refs, err := b.resolveParents(ctx, j, p)
	if err != nil {
		return err
	}
	if err := b.space(ctx, p); err != nil {
		return err
	}
	dir, err := contextDir(Substitute(j.Containerfile, refs))
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	limit, cancel := context.WithTimeout(ctx, b.Limits.Timeout)
	defer cancel()
	p.setStage("building")
	tag := UserTag(b.Repository, j.VersionID)
	start := time.Now()
	err = b.Podman.Build(limit, dir, tag, j.BuildArgs, p)
	timings["build"] = time.Since(start).Seconds()
	if err != nil {
		if limit.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("%w: %w", errTimeout, err)
		}
		return err
	}
	if err := b.setVersion(ctx, j, "pushing"); err != nil {
		return err
	}
	p.setStage("pushing")
	start = time.Now()
	digest, err := b.Podman.Push(limit, tag, p)
	timings["push"] = time.Since(start).Seconds()
	if err != nil {
		return err
	}
	j.UserRef = b.Repository + "@" + digest
	p.printf("pushed %s\n", j.UserRef)
	if _, err := b.DB.Pool.Exec(ctx, `UPDATE image_versions SET user_ref = $2, built_at = now(), updated_at = now() WHERE id = $1`,
		j.VersionID, j.UserRef); err != nil {
		return err
	}
	p.printf("— dude layer %s —\n", LayerShort(b.Layer))
	p.setStage("finishing")
	final, err := b.finish(limit, j, b.Layer, p, timings)
	if err != nil {
		return err
	}
	p.setStage("publishing")
	return pgx.BeginFunc(ctx, b.DB.Pool, func(tx pgx.Tx) error {
		if err := insertFinal(ctx, tx, j, b.Layer, final); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT image_id, image_name, version_id, version, build_id FROM image_publish($1)`, j.VersionID)
		if err != nil {
			return err
		}
		type rebuild struct {
			ImageID, Name, VersionID string
			Version                  int
			BuildID                  string
		}
		rebuilds, err := pgx.CollectRows(rows, pgx.RowToStructByPos[rebuild])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE image_builds SET state = 'succeeded', finished_at = now(), stage = NULL, layer_ref = $2
			WHERE id = $1`, j.ID, b.Layer); err != nil {
			return err
		}
		if err := b.event(ctx, tx, j, "image.published", map[string]any{"imageId": j.ImageID, "name": j.ImageName,
			"versionId": j.VersionID, "version": j.Number, "republished": false, "ref": final}); err != nil {
			return err
		}
		for _, r := range rebuilds {
			if err := b.event(ctx, tx, job{Org: j.Org}, "image.build_queued", map[string]any{"imageId": r.ImageID, "name": r.Name,
				"versionId": r.VersionID, "version": r.Version, "buildId": r.BuildID, "source": "base_rebuild"}); err != nil {
				return err
			}
		}
		return nil
	})
}

// finishJob adds a dude layer to a version already built, for a Run
// waiting on it.
func (b *Builder) finishJob(ctx context.Context, j job, p *progress, timings map[string]float64) error {
	if j.UserRef == "" {
		return reason(fmt.Sprintf("%s v%d was never built", j.ImageName, j.Number))
	}
	p.printf("finishing %s v%d with the dude layer %s\n", j.ImageName, j.Number, LayerShort(j.LayerRef))
	if err := b.space(ctx, p); err != nil {
		return err
	}
	limit, cancel := context.WithTimeout(ctx, b.Limits.Timeout)
	defer cancel()
	p.setStage("finishing")
	final, err := b.finish(limit, j, j.LayerRef, p, timings)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, b.DB.Pool, func(tx pgx.Tx) error {
		if err := insertFinal(ctx, tx, j, j.LayerRef, final); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE image_builds SET state = 'succeeded', finished_at = now(), stage = NULL WHERE id = $1`, j.ID); err != nil {
			return err
		}
		return b.event(ctx, tx, j, "image.finished", map[string]any{"imageId": j.ImageID, "versionId": j.VersionID,
			"buildId": j.ID, "layer": j.LayerRef, "ref": final})
	})
}

// finish builds and pushes the user image with layer added; returns the
// final image's ref by digest.
func (b *Builder) finish(ctx context.Context, j job, layer string, p *progress, timings map[string]float64) (string, error) {
	dir, err := contextDir(FinishContainerfile(j.UserRef, layer))
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	tag := FinalTag(b.Repository, j.VersionID, layer)
	start := time.Now()
	if err := b.Podman.Build(ctx, dir, tag, nil, p); err != nil {
		return "", wrapTimeout(ctx, err)
	}
	timings["build"] += time.Since(start).Seconds()
	start = time.Now()
	digest, err := b.Podman.Push(ctx, tag, p)
	timings["push"] += time.Since(start).Seconds()
	if err != nil {
		return "", wrapTimeout(ctx, err)
	}
	final := b.Repository + "@" + digest
	p.printf("pushed %s\n", final)
	return final, nil
}

func wrapTimeout(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errTimeout, err)
	}
	return err
}

func insertFinal(ctx context.Context, tx pgx.Tx, j job, layer, final string) error {
	_, err := tx.Exec(ctx, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
		VALUES ($1, $2, $3, $4) ON CONFLICT (image_version_id, layer_ref) DO NOTHING`, j.Org, j.VersionID, layer, final)
	return err
}

func (b *Builder) setVersion(ctx context.Context, j job, state string) error {
	_, err := b.DB.Pool.Exec(ctx, `UPDATE image_versions SET state = $2, updated_at = now() WHERE id = $1`, j.VersionID, state)
	return err
}

// resolveParents maps each `image:<name>` the version names to its
// parent's published user image, and records which version that was.
// Never the parent's final: the dude layer must not stack.
func (b *Builder) resolveParents(ctx context.Context, j job, p *progress) (map[string]string, error) {
	refs := map[string]string{}
	err := pgx.BeginFunc(ctx, b.DB.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT pi.id, pi.name, pv.id, pv.number, pv.user_ref
			FROM image_version_parents vp JOIN images pi ON pi.id = vp.parent_image_id
			LEFT JOIN image_versions pv ON pv.id = pi.published_version_id
			WHERE vp.version_id = $1 ORDER BY pi.name`, j.VersionID)
		if err != nil {
			return err
		}
		type parent struct {
			ImageID, Name string
			VersionID     *string
			Number        *int
			UserRef       *string
		}
		parents, err := pgx.CollectRows(rows, pgx.RowToStructByPos[parent])
		if err != nil {
			return err
		}
		for _, pa := range parents {
			if pa.VersionID == nil || pa.UserRef == nil {
				return reason(fmt.Sprintf("base %s has no published version", pa.Name))
			}
			refs[pa.Name] = *pa.UserRef
			p.printf("resolve image:%s → v%d = %s\n", pa.Name, *pa.Number, *pa.UserRef)
			if _, err := tx.Exec(ctx, `UPDATE image_version_parents SET parent_version_id = $3
				WHERE version_id = $1 AND parent_image_id = $2`, j.VersionID, pa.ImageID, *pa.VersionID); err != nil {
				return err
			}
		}
		return nil
	})
	return refs, err
}

// space makes sure podman's storage has MinFree free, pruning once.
func (b *Builder) space(ctx context.Context, p *progress) error {
	if b.MinFree <= 0 {
		return nil
	}
	free, err := b.Podman.FreeBytes(ctx)
	if err != nil {
		return err
	}
	if free >= b.MinFree {
		return nil
	}
	p.printf("%s free, below %s: pruning images unused for a day\n", gib(free), gib(b.MinFree))
	if err := b.Podman.Prune(ctx, p); err != nil {
		return err
	}
	if free, err = b.Podman.FreeBytes(ctx); err != nil {
		return err
	}
	if free < b.MinFree {
		return reason(fmt.Sprintf("the builder's disk is full: %s free after pruning, and a build needs %s", gib(free), gib(b.MinFree)))
	}
	return nil
}

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }

// contextDir is a build context holding only the Containerfile: an image
// has no build files.
func contextDir(containerfile string) (string, error) {
	dir, err := os.MkdirTemp("", "dude-image-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(containerfile), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
