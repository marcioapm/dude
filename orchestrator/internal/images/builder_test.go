package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// fakePodman builds nothing: it records each Containerfile it is given,
// writes podman-like steps to the log, and pushes to a digest that is a
// hash of the tag. fail, when set, decides a build's or push's failure.
type fakePodman struct {
	mu     sync.Mutex
	builds []string // tag + "\n" + Containerfile
	free   []int64
	pruned int
	// Tags removed, in order; what Controllers answers (cpu, memory when nil).
	removed     []string
	controllers []string
	fail        func(op, tag, containerfile string) (string, error)
	// What the container check finds, per tag it was asked of; checked
	// lists those tags in order.
	found   Found
	checked []string
	block   chan struct{}
	// Closed, when set, once a build is blocked on block.
	blocked chan struct{}
	// The job's stage at each push, in order.
	pushStages []string
}

func (f *fakePodman) Build(ctx context.Context, dir, tag string, _ map[string]string, log io.Writer) error {
	raw, err := os.ReadFile(filepath.Join(dir, "Containerfile"))
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.builds = append(f.builds, tag+"\n"+string(raw))
	f.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i, l := range lines {
		fmt.Fprintf(log, "STEP %d/%d: %s\n", i+1, len(lines), l)
	}
	if f.block != nil {
		if f.blocked != nil {
			close(f.blocked)
			f.blocked = nil
		}
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.fail != nil {
		if out, err := f.fail("build", tag, string(raw)); err != nil {
			fmt.Fprint(log, out)
			return err
		}
	}
	return nil
}

func (f *fakePodman) Push(_ context.Context, tag string, log io.Writer) (string, error) {
	if p, ok := log.(*progress); ok {
		_, stage := p.full()
		f.mu.Lock()
		f.pushStages = append(f.pushStages, stage)
		f.mu.Unlock()
	}
	if f.fail != nil {
		if out, err := f.fail("push", tag, ""); err != nil {
			fmt.Fprint(log, out)
			return "", err
		}
	}
	sum := sha256.Sum256([]byte(tag))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (f *fakePodman) FreeBytes(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.free) == 0 {
		return 100 << 30, nil
	}
	n := f.free[0]
	if len(f.free) > 1 {
		f.free = f.free[1:]
	}
	return n, nil
}

func (f *fakePodman) Prune(context.Context, io.Writer) error {
	f.pruned++
	return nil
}

func (f *fakePodman) Remove(_ context.Context, tag string, _ io.Writer) error {
	f.mu.Lock()
	f.removed = append(f.removed, tag)
	f.mu.Unlock()
	return nil
}

func (f *fakePodman) CheckContainers(_ context.Context, tag string, _ io.Writer) (Found, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, tag)
	return f.found, nil
}

func (f *fakePodman) Controllers(context.Context) ([]string, error) {
	if f.controllers == nil {
		return []string{"cpuset", "cpu", "io", "memory", "pids"}, nil
	}
	return f.controllers, nil
}

func digestOf(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return "sha256:" + hex.EncodeToString(sum[:])
}

const (
	repo  = "registry.test/dude/custom"
	layer = "registry.test/dude/layer@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

type fixture struct {
	t      *testing.T
	owner  *pgx.Conn
	org    string
	podman *fakePodman
	b      *Builder
}

func setup(t *testing.T) *fixture {
	t.Helper()
	_, owner := dbtest.Open(t)
	f := &fixture{t: t, owner: owner, org: dbtest.Org(t, owner), podman: &fakePodman{}}
	f.b = &Builder{DB: dbtest.Builder(t, owner), Podman: f.podman, Repository: repo, Layer: layer,
		Limits:  Limits{Platform: "linux/arm64", CPUs: 1.5, Memory: "1536m", Timeout: time.Minute},
		MinFree: 8 << 30, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Flush: 10 * time.Millisecond}
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.owner.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fixture) image(id, name string) {
	f.exec(`INSERT INTO images (id, organization_id, name) VALUES ($1, $2, $3)`, id, f.org, name)
}

// queue adds version n of an image, queued to build with a job, FROM parents.
func (f *fixture) queue(image, version string, n int, containerfile string, parents ...string) string {
	f.exec(`INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state)
		VALUES ($1, $2, $3, $4, $5, 'queued')`, version, f.org, image, n, containerfile)
	for _, p := range parents {
		f.exec(`INSERT INTO image_version_parents (organization_id, version_id, parent_image_id) VALUES ($1, $2, $3)`, f.org, version, p)
	}
	build := "imb_" + version
	f.exec(`INSERT INTO image_builds (id, organization_id, image_version_id, kind) VALUES ($1, $2, $3, 'build')`, build, f.org, version)
	return build
}

func (f *fixture) once() {
	f.t.Helper()
	did, err := f.b.Once(context.Background())
	if err != nil || !did {
		f.t.Fatalf("Once = %v, %v", did, err)
	}
}

func (f *fixture) row(sql string, args ...any) []any {
	f.t.Helper()
	rows, err := f.owner.Query(context.Background(), sql, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		f.t.Fatalf("no row: %s", sql)
	}
	v, err := rows.Values()
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) str(sql string, args ...any) string {
	f.t.Helper()
	v := f.row(sql, args...)[0]
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprint(v)
}

// log is a job's kept log, its chunks in order.
func (f *fixture) log(build string) string {
	f.t.Helper()
	return f.str(`SELECT COALESCE(string_agg(chunk, '' ORDER BY start_offset), '') FROM image_build_log WHERE build_id = $1`, build)
}

func TestABuildPassingPublishesTheVersionWithItsUserImageAndItsFinal(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	build := f.queue("img_base", "imv_b1", 1, "FROM debian:bookworm-slim\nRUN apt-get install -y git\n")
	f.once()

	userRef := repo + "@" + digestOf(UserTag(repo, "imv_b1"))
	finalRef := repo + "@" + digestOf(FinalTag(repo, "imv_b1", layer))
	if got := f.row(`SELECT state, user_ref FROM image_versions WHERE id = 'imv_b1'`); got[0] != "published" || got[1] != userRef {
		t.Fatalf("version = %v", got)
	}
	if got := f.str(`SELECT published_version_id FROM images WHERE id = 'img_base'`); got != "imv_b1" {
		t.Errorf("published = %s", got)
	}
	if got := f.str(`SELECT final_ref FROM image_finals WHERE image_version_id = 'imv_b1' AND layer_ref = $1`, layer); got != finalRef {
		t.Errorf("final = %s, want %s", got, finalRef)
	}
	got := f.row(`SELECT state, layer_ref, build_seconds IS NOT NULL FROM image_builds WHERE id = $1`, build)
	if got[0] != "succeeded" || got[1] != layer || got[2] != true {
		t.Errorf("build = %v", got)
	}
	log := f.log(build)
	for _, want := range []string{"STEP 2/2: RUN apt-get install -y git", "pushed " + userRef, "— dude layer abcdef012345 —", "pushed " + finalRef} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// The finish is built FROM the user image just pushed, by digest.
	if !strings.Contains(f.podman.builds[1], "FROM "+userRef+"\nCOPY --from="+layer+" /rootfs/ /\n") {
		t.Errorf("finish built\n%s", f.podman.builds[1])
	}
	if got := f.str(`SELECT string_agg(event_type, ',' ORDER BY cursor) FROM events WHERE organization_id = $1`, f.org); got != "image.build_started,image.published" {
		t.Errorf("events = %s", got)
	}
	// Both pushed tags leave the builder's storage: the final once pushed,
	// the user image once the final was built from it.
	if want := []string{FinalTag(repo, "imv_b1", layer), UserTag(repo, "imv_b1")}; strings.Join(f.podman.removed, " ") != strings.Join(want, " ") {
		t.Errorf("removed %v, want %v", f.podman.removed, want)
	}
}

func TestAFailedBuildLeavesThePublishedVersionAndSaysWhy(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.once()
	f.podman.fail = func(op, _, cf string) (string, error) {
		if op == "build" && strings.Contains(cf, "RUN make") {
			return "Error: building at STEP \"RUN make\": exit status 137\n", errors.New("exit status 125")
		}
		return "", nil
	}
	build := f.queue("img_base", "imv_b2", 2, "FROM debian\nRUN make\n")
	f.once()
	if got := f.row(`SELECT state, error FROM image_versions WHERE id = 'imv_b2'`); got[0] != "failed" || got[1] != "ran out of memory (1.5 GB) at step 2" {
		t.Fatalf("version = %v", got)
	}
	if got := f.row(`SELECT state, error FROM image_builds WHERE id = $1`, build); got[0] != "failed" || got[1] != "ran out of memory (1.5 GB) at step 2" {
		t.Fatalf("build = %v", got)
	}
	if got := f.str(`SELECT published_version_id FROM images WHERE id = 'img_base'`); got != "imv_b1" {
		t.Errorf("published = %s", got)
	}
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`); got != "published" {
		t.Errorf("v1 = %s", got)
	}
}

func TestPublishingABaseRebuildsItsChildrenOnItsUserImage(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.image("img_node", "node-pnpm")
	f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.once()
	f.queue("img_node", "imv_n1", 1, "FROM image:acme-base\nRUN npm i -g pnpm\n", "img_base")
	f.once()
	baseV1 := repo + "@" + digestOf(UserTag(repo, "imv_b1"))
	if !strings.HasPrefix(f.podman.builds[2], UserTag(repo, "imv_n1")+"\nFROM "+baseV1+"\n") {
		t.Fatalf("child built\n%s", f.podman.builds[2])
	}
	if got := f.str(`SELECT parent_version_id FROM image_version_parents WHERE version_id = 'imv_n1'`); got != "imv_b1" {
		t.Errorf("built on %s", got)
	}

	f.queue("img_base", "imv_b2", 2, "FROM debian\nRUN true\n")
	f.once()
	got := f.row(`SELECT id, number, state, source, note, containerfile, created_by FROM image_versions
		WHERE image_id = 'img_node' AND state = 'queued'`)
	if got[1] != int32(2) || got[3] != "base_rebuild" || got[4] != "Rebuild on acme-base v2" ||
		got[5] != "FROM image:acme-base\nRUN npm i -g pnpm\n" || got[6] != nil {
		t.Fatalf("rebuild = %v", got)
	}
	f.once()
	baseV2 := repo + "@" + digestOf(UserTag(repo, "imv_b2"))
	if last := f.podman.builds[len(f.podman.builds)-2]; !strings.Contains(last, "\nFROM "+baseV2+"\n") {
		t.Fatalf("rebuild built\n%s", last)
	}
	if got := f.str(`SELECT pv.number FROM images i JOIN image_versions pv ON pv.id = i.published_version_id WHERE i.id = 'img_node'`); got != "2" {
		t.Errorf("node-pnpm published v%s", got)
	}
}

func TestABaseWithNothingPublishedFailsItsChild(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.image("img_node", "node-pnpm")
	f.queue("img_node", "imv_n1", 1, "FROM image:acme-base\n", "img_base")
	f.once()
	if got := f.str(`SELECT error FROM image_versions WHERE id = 'imv_n1'`); got != "base acme-base has no published version" {
		t.Errorf("error = %s", got)
	}
	if len(f.podman.builds) != 0 {
		t.Errorf("built %d", len(f.podman.builds))
	}
}

func TestAFinishGoesAheadOfBuildsAndAddsTheLayerToAPublishedVersion(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.once()
	f.image("img_other", "other")
	f.queue("img_other", "imv_o1", 1, "FROM debian\n")
	next := "registry.test/dude/layer@sha256:9999999999999999999999999999999999999999999999999999999999999999"
	// Asked for after the build, taken before it.
	f.exec(`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref, requested_at)
		VALUES ('imb_fin', $1, 'imv_b1', 'finish', $2, now() + interval '1 minute')`, f.org, next)
	f.once()
	if got := f.str(`SELECT state FROM image_builds WHERE id = 'imb_fin'`); got != "succeeded" {
		t.Fatalf("finish = %s", got)
	}
	if got := f.str(`SELECT state FROM image_builds WHERE id = 'imb_imv_o1'`); got != "queued" {
		t.Errorf("build = %s", got)
	}
	want := repo + "@" + digestOf(FinalTag(repo, "imv_b1", next))
	if got := f.str(`SELECT final_ref FROM image_finals WHERE image_version_id = 'imv_b1' AND layer_ref = $1`, next); got != want {
		t.Errorf("final = %s", got)
	}
	// The version stays published, and only a finish was built (no rebuild).
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`); got != "published" {
		t.Errorf("state = %s", got)
	}
	if last := f.podman.builds[len(f.podman.builds)-1]; !strings.HasPrefix(last, FinalTag(repo, "imv_b1", next)+"\nFROM ") {
		t.Errorf("built %s", last)
	}
}

func TestAJobItsBuilderDiedUnderIsTriedOnceMore(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.exec(`UPDATE image_builds SET state = 'running', started_at = now() WHERE id = $1`, build)
	f.exec(`UPDATE image_versions SET state = 'building' WHERE id = 'imv_b1'`)
	if err := f.b.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.row(`SELECT state, restarts FROM image_builds WHERE id = $1`, build); got[0] != "queued" || got[1] != int32(1) {
		t.Fatalf("after one death: %v", got)
	}
	const restarted = "\n— builder restarted; trying again —\n"
	if got := f.log(build); got != restarted {
		t.Errorf("log = %q", got)
	}
	if got := f.row(`SELECT log_total, stage, heartbeat_at FROM image_builds WHERE id = $1`, build); got[0] != int64(len(restarted)) || got[1] != nil || got[2] != nil {
		t.Errorf("after recover: %v", got)
	}
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`); got != "queued" {
		t.Errorf("version = %s", got)
	}
	f.exec(`UPDATE image_builds SET state = 'running' WHERE id = $1`, build)
	if err := f.b.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.row(`SELECT state, error FROM image_builds WHERE id = $1`, build); got[0] != "failed" || got[1] != "the builder restarted while building it, twice" {
		t.Fatalf("after two: %v", got)
	}
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`); got != "failed" {
		t.Errorf("version = %s", got)
	}
}

func TestAFullDiskIsPrunedOnceThenRefused(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.podman.free = []int64{1 << 30, 20 << 30}
	f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.once()
	if f.podman.pruned != 1 || f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`) != "published" {
		t.Fatalf("pruned %d, then %s", f.podman.pruned, f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`))
	}
	f.podman.free = []int64{1 << 30}
	f.queue("img_base", "imv_b2", 2, "FROM debian\nRUN true\n")
	f.once()
	if got := f.str(`SELECT error FROM image_versions WHERE id = 'imv_b2'`); got != "the builder's disk is full: 1.0 GiB free after pruning, and a build needs 8.0 GiB" {
		t.Errorf("error = %s", got)
	}
}

func TestABuildPastItsTimeLimitFailsSayingSo(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.b.Limits.Timeout = 50 * time.Millisecond
	f.podman.block = make(chan struct{})
	f.queue("img_base", "imv_b1", 1, "FROM debian\nRUN sleep 1000\n")
	f.once()
	if got := f.str(`SELECT error FROM image_versions WHERE id = 'imv_b1'`); got != "took longer than the build's time limit at step 2" {
		t.Errorf("error = %s", got)
	}
}

func TestTheLogIsWrittenWhileItBuilds(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.podman.block = make(chan struct{})
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\nRUN make\n")
	done := make(chan struct{})
	go func() { f.once(); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stage, log := f.str(`SELECT stage FROM image_builds WHERE id = $1`, build), f.log(build)
		if stage == "building" && strings.Contains(log, "STEP 2/2: RUN make") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw the running log: %s %q", stage, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(f.podman.block)
	<-done
}

func TestAFailedBuildsImageIsRemovedToo(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.podman.fail = func(op, _, _ string) (string, error) {
		if op == "push" {
			return "Error: writing blob: unauthorized\n", errors.New("exit status 125")
		}
		return "", nil
	}
	f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.once()
	if got := strings.Join(f.podman.removed, " "); got != UserTag(repo, "imv_b1") {
		t.Errorf("removed %q", got)
	}
}

func TestABuilderStoppedMidJobQueuesItAgainUnfailed(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	f.podman.block = make(chan struct{})
	f.podman.blocked = make(chan struct{})
	blocked := f.podman.blocked
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\nRUN make\n")
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := f.b.Once(ctx); err != nil {
			t.Errorf("Once: %v", err)
		}
	}()
	<-blocked
	// What SIGTERM does to the builder's context.
	stop()
	<-done
	got := f.row(`SELECT state, error, restarts, started_at FROM image_builds WHERE id = $1`, build)
	if got[0] != "queued" || got[1] != nil || got[2] != int32(0) || got[3] != nil {
		t.Fatalf("job = %v", got)
	}
	if log := f.log(build); !strings.Contains(log, "— the builder stopped; queued again —") {
		t.Errorf("log = %s", log)
	}
	if got := f.row(`SELECT state, error FROM image_versions WHERE id = 'imv_b1'`); got[0] != "queued" || got[1] != nil {
		t.Errorf("version = %v", got)
	}
	if got := f.str(`SELECT count(*) FROM events WHERE event_type = 'image.build_failed'`); got != "0" {
		t.Errorf("%s failure events", got)
	}
	// The next builder takes it and publishes it.
	f.podman.block = nil
	f.once()
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_b1'`); got != "published" {
		t.Errorf("then %s", got)
	}
}

func TestAFlushWithNothingNewWritesOnlyTheHeartbeat(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.exec(`UPDATE image_builds SET state = 'running', log_total = 5 WHERE id = $1`, build)
	f.exec(`INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk) VALUES ($1, $2, 0, E'kept\n')`, f.org, build)
	j := job{ID: build}
	p := &progress{tail: Tail{Max: LogMax}, stage: "building"}
	p.setStage("building")
	p.printf("STEP 1/2: FROM debian\n")
	f.b.flush(context.Background(), j, p, true)
	p.printf("STEP 2/2: RUN é")
	// Half a rune is held back until the rest of it comes.
	_, _ = p.Write([]byte{0xC3})
	f.b.flush(context.Background(), j, p, true)
	if got := f.log(build); got != "kept\nSTEP 1/2: FROM debian\nSTEP 2/2: RUN é" {
		t.Fatalf("log = %q", got)
	}
	_, _ = p.Write([]byte{0xA9, '\n'})
	f.b.flush(context.Background(), j, p, true)
	want := "kept\nSTEP 1/2: FROM debian\nSTEP 2/2: RUN éé\n"
	got := f.row(`SELECT log_total, (SELECT string_agg(start_offset::text, ',' ORDER BY start_offset) FROM image_build_log WHERE build_id = $1)
		FROM image_builds WHERE id = $1`, build)
	if got[0] != int64(len(want)) || got[1] != "0,5,27,43" || f.log(build) != want {
		t.Fatalf("log_total, chunk offsets = %v, log = %q", got, f.log(build))
	}
	f.exec(`UPDATE image_builds SET heartbeat_at = now() - interval '1 hour' WHERE id = $1`, build)
	// Nothing written since: no chunk, the heartbeat moves.
	f.b.flush(context.Background(), j, p, true)
	got = f.row(`SELECT (SELECT count(*) FROM image_build_log WHERE build_id = $1), log_total, heartbeat_at > now() - interval '1 minute'
		FROM image_builds WHERE id = $1`, build)
	if got[0] != int64(4) || got[1] != int64(len(want)) || got[2] != true {
		t.Errorf("after an empty flush: %v", got)
	}
}

func TestOutputFlushedAfterTheJobEndedIsStillWritten(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.exec(`UPDATE image_builds SET state = 'succeeded', stage = NULL, heartbeat_at = NULL WHERE id = $1`, build)
	p := &progress{tail: Tail{Max: LogMax}}
	p.setStage("publishing")
	p.printf("pushed it\n")
	// A live flush racing the publish, then the last one: both write output.
	f.b.flush(context.Background(), job{ID: build}, p, true)
	p.printf("done\n")
	f.b.flush(context.Background(), job{ID: build}, p, false)
	if got := f.log(build); got != "pushed it\ndone\n" {
		t.Errorf("log = %q", got)
	}
	if got := f.row(`SELECT stage, heartbeat_at FROM image_builds WHERE id = $1`, build); got[0] != nil || got[1] != nil {
		t.Errorf("an ended job got stage, heartbeat %v", got)
	}
}

func TestTheLogKeepsTheLastLogMaxOfALongLog(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.exec(`UPDATE image_builds SET state = 'running' WHERE id = $1`, build)
	j := job{ID: build}
	p := &progress{tail: Tail{Max: LogMax}, stage: "building"}
	line := strings.Repeat("x", 1023) + "\n"
	for i := range 1200 {
		p.printf("%s", line)
		if i%100 == 99 {
			f.b.flush(context.Background(), j, p, true)
		}
	}
	p.printf("the end\n")
	f.b.flush(context.Background(), j, p, true)
	total := int64(1200*1024 + len("the end\n"))
	if got := f.str(`SELECT log_total FROM image_builds WHERE id = $1`, build); got != fmt.Sprint(total) {
		t.Errorf("log_total = %s", got)
	}
	// Flushes of 100 KiB: what is kept starts at the chunk holding byte
	// total - LogMax, the 2nd (offset 102400), and runs to the end.
	got := f.row(`SELECT min(start_offset), count(*) FROM image_build_log WHERE build_id = $1`, build)
	if got[0] != int64(100*1024) || got[1] != int64(12) {
		t.Errorf("first kept offset, chunks = %v", got)
	}
	if log := f.log(build); int64(len(log)) != total-100*1024 || !strings.HasSuffix(log, "x\nthe end\n") {
		t.Errorf("log is %d bytes", len(log))
	}
}

func TestAnIdleBuilderKeepsWritingItsHeartbeat(t *testing.T) {
	f := setup(t)
	f.b.Beat, f.b.Poll = 10*time.Millisecond, 10*time.Millisecond
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.b.Run(ctx) }()
	wait := func(what, sql string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); f.str(sql) != "true"; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				stop()
				t.Fatalf("never saw %s", what)
			}
		}
	}
	wait("the first heartbeat", `SELECT count(*) = 1 FROM image_builder`)
	// No job: only the ticker writes it now.
	f.exec(`UPDATE image_builder SET seen_at = now() - interval '1 hour'`)
	wait("a heartbeat from the ticker", `SELECT seen_at > now() - interval '1 minute' FROM image_builder`)
	stop()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
}

func TestTheHeartbeatSaysWhenTheBuilderWasLastSeen(t *testing.T) {
	f := setup(t)
	f.b.Version = "1.2.3"
	if err := f.b.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE image_builder SET seen_at = now() - interval '1 hour'`)
	if err := f.b.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.row(`SELECT count(*), bool_and(seen_at > now() - interval '1 minute'), max(version) FROM image_builder`); got[0] != int64(1) || got[1] != true || got[2] != "1.2.3" {
		t.Errorf("image_builder = %v", got)
	}
}

func TestCheckLimitsRefusesAPodmanWithoutCPUAndMemoryControllers(t *testing.T) {
	for _, c := range []struct {
		have []string
		want string
	}{
		{[]string{"cpu", "memory", "pids"}, ""},
		{[]string{"pids"}, "podman cannot limit builds: the cpu and memory cgroup controllers are not delegated to this user (podman has [pids]); delegate cpu and memory to its user@.service"},
		{[]string{"cpu", "pids"}, "podman cannot limit builds: the memory cgroup controller is not delegated to this user (podman has [cpu pids]); delegate cpu and memory to its user@.service"},
	} {
		err := CheckLimits(context.Background(), &fakePodman{controllers: c.have})
		if got := fmt.Sprint(err); (c.want == "" && err != nil) || (c.want != "" && got != c.want) {
			t.Errorf("%v: %v", c.have, err)
		}
	}
}

// able is what the check finds in an image that can run containers.
var able = Found{Podman: "/usr/bin/podman", PodmanVersion: "podman version 5.4.2", FuseOverlayfs: "/usr/bin/fuse-overlayfs",
	Newuidmap: Mapper{Path: "/usr/bin/newuidmap", FileCap: true}, Newgidmap: Mapper{Path: "/usr/bin/newgidmap", FileCap: true},
	Subuid: []string{"agent:1:999", "agent:1001:64535"}, Subgid: []string{"agent:1:999", "agent:1001:64535"}}

func TestAVersionThatCanRunContainersIsCheckedOnItsFinalBeforeItIsPushed(t *testing.T) {
	f := setup(t)
	f.image("img_p", "agents-podman")
	build := f.queue("img_p", "imv_p1", 1, "FROM debian\nRUN apt-get install -y podman\n")
	f.exec(`UPDATE image_versions SET can_run_containers = true WHERE id = 'imv_p1'`)
	f.podman.found = able
	f.once()
	// The final, with the dude layer's agent on it, is what is checked.
	if got := strings.Join(f.podman.checked, " "); got != FinalTag(repo, "imv_p1", layer) {
		t.Errorf("checked %q", got)
	}
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_p1'`); got != "published" {
		t.Fatalf("state = %s", got)
	}
	got := f.row(`SELECT containers_check->>'passed', containers_check->>'detail', check_seconds IS NOT NULL FROM image_builds WHERE id = $1`, build)
	if got[0] != "true" || got[1] != "podman 5.4, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent" || got[2] != true {
		t.Errorf("check = %v", got)
	}
	log := f.log(build)
	for _, want := range []string{
		"Check containers: v1 is marked Can run containers, so dude checks it.\n",
		"check  engine  podman: /usr/bin/podman (5.4) · docker: not found\n",
		"check  subuid  agent:1:999, agent:1001:64535\n",
		"Check passed: it can run containers.\npushed " + repo + "@",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestAVersionThatCannotRunContainersFailsItsBuildAndThePreviousStaysPublished(t *testing.T) {
	f := setup(t)
	f.image("img_n", "node-22")
	f.queue("img_n", "imv_n4", 4, "FROM node:22\n")
	f.once()
	pushes := 0
	f.podman.fail = func(op, _, _ string) (string, error) {
		if op == "push" {
			pushes++
		}
		return "", nil
	}
	build := f.queue("img_n", "imv_n5", 5, "FROM node:22\nRUN true\n")
	f.exec(`UPDATE image_versions SET can_run_containers = true WHERE id = 'imv_n5'`)
	f.podman.found = Found{Subuid: able.Subuid, Subgid: able.Subgid}
	f.once()
	const sentence = "Can't run containers: the image has no podman or rootless Docker, no fuse-overlayfs, and no newuidmap or newgidmap."
	if got := f.row(`SELECT state, error FROM image_versions WHERE id = 'imv_n5'`); got[0] != "failed" || got[1] != sentence {
		t.Fatalf("v5 = %v", got)
	}
	if got := f.str(`SELECT published_version_id FROM images WHERE id = 'img_n'`); got != "imv_n4" {
		t.Errorf("published = %s", got)
	}
	// The user image was pushed for children; the final, which failed its
	// check, was not.
	if pushes != 1 {
		t.Errorf("%d pushes, want the user image's alone", pushes)
	}
	if got := f.str(`SELECT count(*) FROM image_finals WHERE image_version_id = 'imv_n5'`); got != "0" {
		t.Errorf("%s finals", got)
	}
	if got := f.row(`SELECT containers_check->>'passed', containers_check->>'detail' FROM image_builds WHERE id = $1`, build); got[0] != "false" ||
		got[1] != "Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap" {
		t.Errorf("check = %v", got)
	}
	log := f.log(build)
	for _, want := range []string{"check  fuse-overlayfs  not found\n", "check  newuidmap  missing\n", sentence + "\nNot pushed. v4 stays published.\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestAVersionNotMarkedIsNotChecked(t *testing.T) {
	f := setup(t)
	f.image("img_b", "acme-base")
	f.queue("img_b", "imv_b1", 1, "FROM debian\n")
	f.once()
	if len(f.podman.checked) != 0 {
		t.Errorf("checked %v", f.podman.checked)
	}
}

// A published version that can run containers, finished again for a new
// dude layer, is checked again: the new layer must keep it able.
func TestAFinishOfAVersionThatCanRunContainersIsCheckedToo(t *testing.T) {
	f := setup(t)
	f.image("img_p", "agents-podman")
	f.queue("img_p", "imv_p1", 1, "FROM debian\n")
	f.exec(`UPDATE image_versions SET can_run_containers = true WHERE id = 'imv_p1'`)
	f.podman.found = able
	f.once()
	next := "registry.test/dude/layer@sha256:9999999999999999999999999999999999999999999999999999999999999999"
	f.exec(`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref) VALUES ('imb_fin', $1, 'imv_p1', 'finish', $2)`, f.org, next)
	f.podman.found = Found{}
	f.once()
	if got := f.row(`SELECT state, error FROM image_builds WHERE id = 'imb_fin'`); got[0] != "failed" || !strings.HasPrefix(fmt.Sprint(got[1]), "Can't run containers: ") {
		t.Errorf("finish = %v", got)
	}
	if got := f.str(`SELECT state FROM image_versions WHERE id = 'imv_p1'`); got != "published" {
		t.Errorf("version = %s", got)
	}
}

// The final's push, once its check passed, is the pushing stage; when it
// fails the passed check stays saved beside the push's failure.
func TestAPassedCheckIsDoneWhileTheFinalIsPushedAndAfterItsPushFails(t *testing.T) {
	f := setup(t)
	f.image("img_p", "agents-podman")
	build := f.queue("img_p", "imv_p1", 1, "FROM debian\n")
	f.exec(`UPDATE image_versions SET can_run_containers = true WHERE id = 'imv_p1'`)
	f.podman.found = able
	final := FinalTag(repo, "imv_p1", layer)
	f.podman.fail = func(op, tag, _ string) (string, error) {
		if op == "push" && tag == final {
			return "Error: pushing " + tag + ": 502 Bad Gateway\n", errors.New("exit status 125")
		}
		return "", nil
	}
	f.once()
	if got := strings.Join(f.podman.pushStages, " "); got != "pushing pushing" {
		t.Errorf("stages at each push = %q, want the user image's and the final's both pushing", got)
	}
	got := f.row(`SELECT state, error, containers_check->>'passed' FROM image_builds WHERE id = $1`, build)
	if got[0] != "failed" || !strings.HasPrefix(fmt.Sprint(got[1]), "pushing to the registry failed: ") || got[2] != "true" {
		t.Errorf("build = %v", got)
	}
}
