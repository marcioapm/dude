package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	// How often the builder says it is alive (image_builder), idle or not;
	// BeatEvery when 0.
	Beat time.Duration
	// Written with each heartbeat: the release the builder runs.
	Version string
}

// BeatEvery is how often a running builder writes its heartbeat. One not
// heard from for Offline is offline: the Images page and waiting Runs say
// so, and a Run that has waited GiveUp of that time fails. Offline and
// GiveUp are @dude/domain's BUILDER_OFFLINE_SECONDS and
// BUILDER_GIVE_UP_MINUTES too (tests/fixtures/images/builder.json).
const (
	BeatEvery = 30 * time.Second
	Offline   = 2 * time.Minute
	GiveUp    = 30 * time.Minute
)

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

// Run takes jobs until ctx ends, writing its heartbeat throughout.
func (b *Builder) Run(ctx context.Context) error {
	if err := b.Recover(ctx); err != nil {
		return err
	}
	if err := b.Heartbeat(ctx); err != nil {
		return err
	}
	beat := b.Beat
	if beat == 0 {
		beat = BeatEvery
	}
	go func() {
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := b.Heartbeat(ctx); err != nil && ctx.Err() == nil {
					b.Log.Warn("writing the builder's heartbeat", "error", err)
				}
			}
		}
	}()
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
				if _, err := tx.Exec(ctx, appendLog, j.ID, "\n— builder restarted; trying again —\n", nil, LogMax); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE image_builds SET state = 'queued', restarts = restarts + 1, started_at = NULL,
					heartbeat_at = NULL, stage = NULL WHERE id = $1`, j.ID); err != nil {
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

// Heartbeat records that the builder is alive now.
func (b *Builder) Heartbeat(ctx context.Context) error {
	_, err := b.DB.Pool.Exec(ctx, `INSERT INTO image_builder (id, seen_at, version) VALUES (true, now(), $1)
		ON CONFLICT (id) DO UPDATE SET seen_at = now(), version = EXCLUDED.version`, b.Version)
	return err
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

// progress is a running job's log and stage. Every Flush the output
// written since the last one is appended to the job's log (a chunk in
// image_build_log), with the stage and the heartbeat on its row, so the
// build page follows it.
type progress struct {
	mu    sync.Mutex
	tail  Tail
	stage string
	// Output not yet in the log, and whether stage changed since.
	pending    []byte
	stageDirty bool
}

func (p *progress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = append(p.pending, b...)
	if over := len(p.pending) - LogMax; over > 0 {
		p.pending = p.pending[over:]
	}
	return p.tail.Write(b)
}

func (p *progress) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p, format, args...)
}

func (p *progress) setStage(s string) {
	p.mu.Lock()
	p.stage, p.stageDirty = s, true
	p.mu.Unlock()
}

// delta takes the output not yet written, up to its last whole rune, as
// text Postgres takes (valid UTF-8, no NUL); dirty when there is any, or
// the stage changed.
func (p *progress) delta() (text, stage string, dirty bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cut := len(p.pending)
	for i := 1; i <= 3 && i <= len(p.pending); i++ {
		c := p.pending[len(p.pending)-i]
		if c&0xC0 == 0xC0 {
			// A rune's first byte: kept back if its rune is incomplete.
			if !utf8.FullRune(p.pending[len(p.pending)-i:]) {
				cut = len(p.pending) - i
			}
			break
		}
		if c < 0x80 {
			break
		}
	}
	text = strings.ReplaceAll(strings.ToValidUTF8(string(p.pending[:cut]), "\uFFFD"), "\x00", "")
	p.pending = append(p.pending[:0], p.pending[cut:]...)
	dirty, p.stageDirty = text != "" || p.stageDirty, false
	return text, p.stage, dirty
}

// unwritten puts back a delta a flush could not write.
func (p *progress) unwritten(text string) {
	p.mu.Lock()
	p.pending = append([]byte(text), p.pending...)
	p.stageDirty = true
	p.mu.Unlock()
}

// full is the kept log and the stage the job reached, for its failure's
// sentence.
func (p *progress) full() (log, stage string) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
// A job the builder is stopped under (SIGTERM on a deploy) is queued again
// as it was, never failed: the next builder does it.
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
				b.flush(context.WithoutCancel(ctx), j, p, true)
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
	record := context.WithoutCancel(ctx)
	if err != nil && ctx.Err() != nil {
		b.Log.Info("image job stopped with the builder; queued again", "build", j.ID)
		p.printf("— the builder stopped; queued again —\n")
		b.flush(record, j, p, true)
		if rerr := b.requeue(record, j); rerr != nil {
			// Left running: Recover re-queues it when the builder starts.
			b.Log.Error("re-queueing a stopped image job", "build", j.ID, "error", rerr)
		}
		return
	}
	// The job may have ended already (published): appendLog writes output
	// whatever the job's state, so neither a live flush that ran after the
	// publish nor this last one drops any.
	b.flush(record, j, p, false)
	log, stage := p.full()
	if _, werr := b.DB.Pool.Exec(record, `UPDATE image_builds SET build_seconds = $2, push_seconds = $3 WHERE id = $1`,
		j.ID, nullable(timings["build"]), nullable(timings["push"])); werr != nil {
		b.Log.Error("recording an image job's timings", "build", j.ID, "error", werr)
	}
	if err == nil {
		b.Log.Info("image job succeeded", "build", j.ID)
		return
	}
	timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTimeout)
	var sentence string
	if r, ok := err.(reason); ok {
		sentence = string(r)
	} else {
		sentence = Failure(stage, log, err, b.Limits.Memory, timedOut)
	}
	b.Log.Warn("image job failed", "build", j.ID, "error", sentence, "cause", err)
	if ferr := pgx.BeginFunc(record, b.DB.Pool, func(tx pgx.Tx) error {
		return b.failIn(record, tx, j, sentence)
	}); ferr != nil {
		b.Log.Error("recording a failed image job", "build", j.ID, "error", ferr)
	}
}

// requeue puts a job the builder stopped under back in the queue, as it
// was when claimed; its version waits again.
func (b *Builder) requeue(ctx context.Context, j job) error {
	return pgx.BeginFunc(ctx, b.DB.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE image_builds SET state = 'queued', started_at = NULL, heartbeat_at = NULL, stage = NULL
			WHERE id = $1 AND state = 'running'`, j.ID); err != nil {
			return err
		}
		if j.Kind != "build" {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE image_versions SET state = 'queued', updated_at = now()
			WHERE id = $1 AND state IN ('building', 'pushing')`, j.VersionID)
		return err
	})
}

func nullable(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

// appendLog adds $2 to job $1's log as one image_build_log chunk at
// log_total, and deletes the chunks that end before the last $4 bytes
// (start_offset < bound keeps that delete on the primary key). While the
// job runs it also sets its heartbeat and, unless $3 is NULL, its stage; a
// job that has ended (published while the output was in flight) still gets
// the output, never a stage again.
const appendLog = `
	WITH b AS (
		UPDATE image_builds SET log_total = log_total + octet_length($2),
			stage = CASE WHEN state = 'running' AND $3::text IS NOT NULL THEN $3 ELSE stage END,
			heartbeat_at = CASE WHEN state = 'running' THEN now() ELSE heartbeat_at END
		WHERE id = $1 RETURNING organization_id, log_total),
	chunk AS (
		INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk)
		SELECT organization_id, $1, log_total - octet_length($2), $2 FROM b WHERE $2 <> '')
	DELETE FROM image_build_log l USING b
	WHERE l.build_id = $1 AND l.start_offset < b.log_total - $4 AND l.start_offset + octet_length(l.chunk) <= b.log_total - $4`

// flush appends the output written since the last flush to the job's log,
// with the stage and the heartbeat; with nothing new, only the heartbeat.
// live: a flush while the job runs; the last one (false) writes only when
// there is output or a stage left.
func (b *Builder) flush(ctx context.Context, j job, p *progress, live bool) {
	text, stage, dirty := p.delta()
	var err error
	switch {
	case dirty:
		_, err = b.DB.Pool.Exec(ctx, appendLog, j.ID, text, stage, LogMax)
	case live:
		_, err = b.DB.Pool.Exec(ctx, `UPDATE image_builds SET heartbeat_at = now() WHERE id = $1 AND state = 'running'`, j.ID)
	}
	if err != nil {
		p.unwritten(text)
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
	// Nothing local needs it once its finish is built: children and
	// finishes name the pushed digest.
	defer b.remove(ctx, tag, p)
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
	err = b.Podman.Build(ctx, dir, tag, nil, p)
	// Runs pull it from the registry; the builder never uses it again.
	defer b.remove(ctx, tag, p)
	if err != nil {
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

// remove untags an image the job built, whether or not it was pushed, so
// the builder's storage holds only base images and the dude layer between
// jobs. A failure costs disk, not the job: the next prune takes it.
func (b *Builder) remove(ctx context.Context, tag string, p *progress) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if err := b.Podman.Remove(rctx, tag, p); err != nil {
		b.Log.Warn("removing a built image", "tag", tag, "error", err)
	}
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
	p.printf("%s free, below %s: pruning every image no container uses\n", gib(free), gib(b.MinFree))
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
