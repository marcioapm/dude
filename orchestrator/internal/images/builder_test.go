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
	block       chan struct{}
	// Closed, when set, once a build is blocked on block.
	blocked chan struct{}
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
	got := f.row(`SELECT state, layer_ref, log, build_seconds IS NOT NULL FROM image_builds WHERE id = $1`, build)
	if got[0] != "succeeded" || got[1] != layer || got[3] != true {
		t.Errorf("build = %v", got[:2])
	}
	for _, want := range []string{"STEP 2/2: RUN apt-get install -y git", "pushed " + userRef, "— dude layer abcdef012345 —", "pushed " + finalRef} {
		if !strings.Contains(got[2].(string), want) {
			t.Errorf("log lacks %q:\n%s", want, got[2])
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
		got := f.row(`SELECT stage, log FROM image_builds WHERE id = $1`, build)
		if got[0] == "building" && strings.Contains(fmt.Sprint(got[1]), "STEP 2/2: RUN make") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw the running log: %v", got)
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
	got := f.row(`SELECT state, error, restarts, started_at, log FROM image_builds WHERE id = $1`, build)
	if got[0] != "queued" || got[1] != nil || got[2] != int32(0) || got[3] != nil {
		t.Fatalf("job = %v", got[:4])
	}
	if !strings.Contains(fmt.Sprint(got[4]), "— the builder stopped; queued again —") {
		t.Errorf("log = %s", got[4])
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
	f.exec(`UPDATE image_builds SET state = 'running', log = E'kept\n' WHERE id = $1`, build)
	j := job{ID: build}
	p := &progress{tail: Tail{Max: LogMax}, stage: "building"}
	p.setStage("building")
	p.printf("STEP 1/2: FROM debian\n")
	f.b.flush(context.Background(), j, p, true)
	p.printf("STEP 2/2: RUN é")
	// Half a rune is held back until the rest of it comes.
	_, _ = p.Write([]byte{0xC3})
	f.b.flush(context.Background(), j, p, true)
	if got := f.str(`SELECT log FROM image_builds WHERE id = $1`, build); got != "kept\nSTEP 1/2: FROM debian\nSTEP 2/2: RUN é" {
		t.Fatalf("log = %q", got)
	}
	_, _ = p.Write([]byte{0xA9, '\n'})
	f.b.flush(context.Background(), j, p, true)
	f.exec(`UPDATE image_builds SET log = 'replaced', heartbeat_at = now() - interval '1 hour' WHERE id = $1`, build)
	// Nothing written since: the log is left alone, the heartbeat moves.
	f.b.flush(context.Background(), j, p, true)
	got := f.row(`SELECT log, heartbeat_at > now() - interval '1 minute' FROM image_builds WHERE id = $1`, build)
	if got[0] != "replaced" || got[1] != true {
		t.Errorf("after an empty flush: %v", got)
	}
}

func TestTheRowKeepsTheLastLogMaxOfALongLog(t *testing.T) {
	f := setup(t)
	f.image("img_base", "acme-base")
	build := f.queue("img_base", "imv_b1", 1, "FROM debian\n")
	f.exec(`UPDATE image_builds SET state = 'running' WHERE id = $1`, build)
	j := job{ID: build}
	p := &progress{tail: Tail{Max: LogMax}, stage: "building"}
	line := strings.Repeat("x", 1023) + "\n"
	for i := range 1100 {
		p.printf("%s", line)
		if i%100 == 0 {
			f.b.flush(context.Background(), j, p, true)
		}
	}
	p.printf("the end\n")
	f.b.flush(context.Background(), j, p, true)
	got := f.row(`SELECT log, log_total FROM image_builds WHERE id = $1`, build)
	log := got[0].(string)
	if len(log) != LogMax || !strings.HasSuffix(log, "the end\n") {
		t.Errorf("log is %d bytes, ending %q", len(log), log[len(log)-10:])
	}
	if got[1] != int64(1100*1024+len("the end\n")) {
		t.Errorf("log_total = %v", got[1])
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
