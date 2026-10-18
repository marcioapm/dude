package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// release is an installed release's layer context and CLI, as dist.sh lays
// them out, with the given CLI bytes.
func release(t *testing.T, cli string) (dir, dude string) {
	t.Helper()
	prefix := t.TempDir()
	dir = filepath.Join(prefix, "share/dude/layer")
	for name, body := range map[string]string{"Dockerfile": "FROM scratch\n", "setup.sh": "#!/bin/sh\n", "git-hooks/dude-hook": "#!/bin/sh\n"} {
		write(t, filepath.Join(dir, name), body, 0o755)
	}
	dude = filepath.Join(prefix, "bin/dude")
	write(t, dude, cli, 0o755)
	return dir, dude
}

// The tag changes with any byte of the context, a file's name, the CLI or
// the platform, and with nothing else: the same inputs elsewhere tag the same.
func TestTheLayerTagIsItsContent(t *testing.T) {
	dir, dude := release(t, "cli v1")
	tag := func(dir, dude, platform string) string {
		t.Helper()
		got, err := LayerTag(dir, dude, platform)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	base := tag(dir, dude, "linux/arm64")
	if len(base) != 16 {
		t.Fatalf("tag %q", base)
	}
	other, otherDude := release(t, "cli v1")
	if got := tag(other, otherDude, "linux/arm64"); got != base {
		t.Errorf("the same inputs elsewhere: %s, want %s", got, base)
	}
	if got := tag(dir, dude, "linux/amd64"); got == base {
		t.Error("another platform has the same tag")
	}
	_, v2 := release(t, "cli v2")
	if got := tag(dir, v2, "linux/arm64"); got == base {
		t.Error("another CLI has the same tag")
	}
	write(t, filepath.Join(other, "git-hooks/dude-hook"), "#!/bin/sh\nexit 0\n", 0o755)
	if got := tag(other, otherDude, "linux/arm64"); got == base {
		t.Error("another hook has the same tag")
	}
	// A file moved, its bytes the same.
	moved, movedDude := release(t, "cli v1")
	if err := os.Rename(filepath.Join(moved, "setup.sh"), filepath.Join(moved, "setup2.sh")); err != nil {
		t.Fatal(err)
	}
	if got := tag(moved, movedDude, "linux/arm64"); got == base {
		t.Error("a renamed file has the same tag")
	}
}

func TestTheLayerRepositoryIsBesideTheLibrarys(t *testing.T) {
	for in, want := range map[string]string{
		"123456789012.dkr.ecr.eu-north-1.amazonaws.com/dude/custom": "123456789012.dkr.ecr.eu-north-1.amazonaws.com/dude/layer",
		"localhost:5000/custom": "localhost:5000/layer",
	} {
		if got := LayerRepository(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// layerBuilder is a builder whose release is release(cli), on this
// machine's platform, with no override.
func layerBuilder(t *testing.T, f *fixture, cli string) *Builder {
	t.Helper()
	b := *f.b
	b.Layer = ""
	b.Limits.Platform = "linux/" + runtime.GOARCH
	b.LayerContext, b.DudeBinary = release(t, cli)
	return &b
}

// The builder builds the release's layer into the repository beside the
// library's, tagged by content, from the context with the CLI added as
// dude, and records it current; another builder of the same release builds
// nothing and only makes it current again, over a newer one (a rollback).
// Runs resolve to whichever is current, and the operator's override wins.
func TestTheBuilderPublishesTheReleasesLayerOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := layerBuilder(t, f, "cli v1")
	if err := b.PublishLayer(ctx); err != nil {
		t.Fatal(err)
	}
	tag, _ := LayerTag(b.LayerContext, b.DudeBinary, b.Limits.Platform)
	image := "registry.test/dude/layer:" + tag
	want := "registry.test/dude/layer@" + digestOf(image)
	if b.Layer != want {
		t.Fatalf("layer = %s, want %s", b.Layer, want)
	}
	if len(f.podman.builds) != 1 || !strings.HasPrefix(f.podman.builds[0], image+"\nFROM scratch\n") {
		t.Fatalf("built %q", f.podman.builds)
	}
	if got := f.row(`SELECT ref, platform, count(*) OVER () FROM image_layers WHERE tag = $1`, tag); got[0] != want || got[1] != b.Limits.Platform || got[2] != int64(1) {
		t.Errorf("image_layers = %v", got)
	}
	if got := current(t, f, ""); got != want {
		t.Errorf("current = %s", got)
	}

	newer := layerBuilder(t, f, "cli v2")
	if err := newer.PublishLayer(ctx); err != nil {
		t.Fatal(err)
	}
	if current(t, f, "") != newer.Layer || newer.Layer == want {
		t.Fatalf("after v2: current %s, v2 %s", current(t, f, ""), newer.Layer)
	}
	again := layerBuilder(t, f, "cli v1")
	if err := again.PublishLayer(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.podman.builds) != 2 || again.Layer != want || current(t, f, "") != want {
		t.Errorf("v1 again: %d builds, layer %s, current %s", len(f.podman.builds), again.Layer, current(t, f, ""))
	}
	if got := current(t, f, layer); got != layer {
		t.Errorf("with an override: %s", got)
	}
}

// Before any layer, nothing is current; with only the override, it is.
func TestNoLayerIsCurrentBeforeTheBuilderPublishesOne(t *testing.T) {
	f := setup(t)
	if got := current(t, f, ""); got != "" {
		t.Errorf("current = %q", got)
	}
}

// A layer that fails to build or push is never recorded, and says which.
func TestALayerThatFailsToPushIsNotRecorded(t *testing.T) {
	f := setup(t)
	f.podman.fail = func(op, _, _ string) (string, error) {
		if op == "push" {
			return "", fmt.Errorf("unauthorized")
		}
		return "", nil
	}
	b := layerBuilder(t, f, "cli v1")
	err := b.PublishLayer(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "pushing the dude layer registry.test/dude/layer:") {
		t.Errorf("err = %v", err)
	}
	if got := f.str(`SELECT count(*) FROM image_layers`); got != "0" {
		t.Errorf("%s layers recorded", got)
	}
}

// A platform whose CLI the release does not carry is refused, not built
// with the wrong binary.
func TestALayerForAnotherPlatformIsRefused(t *testing.T) {
	f := setup(t)
	b := layerBuilder(t, f, "cli v1")
	b.Limits.Platform = "linux/riscv64"
	if err := b.PublishLayer(context.Background()); err == nil || !strings.Contains(err.Error(), "needs that platform's dude CLI") {
		t.Errorf("err = %v", err)
	}
	if len(f.podman.builds) != 0 {
		t.Errorf("built %v", f.podman.builds)
	}
}

func TestLayerPrivilegesAllowOnlyTheBuilderToPublish(t *testing.T) {
	f := setup(t)
	b := layerBuilder(t, f, "cli v1")
	ctx := context.Background()
	if err := b.PublishLayer(ctx); err != nil {
		t.Fatal(err)
	}
	if got := current(t, f, ""); got != b.Layer {
		t.Fatalf("app read = %q, want %q", got, b.Layer)
	}
	for _, statement := range []string{
		`INSERT INTO image_layers (tag, ref, platform, version) SELECT 'evil', ref, platform, version FROM image_layers`,
		`UPDATE image_layers SET current_at = now()`,
		`DELETE FROM image_layers`,
		`TRUNCATE image_layers`,
	} {
		t.Run(statement[:strings.IndexByte(statement, ' ')], func(t *testing.T) {
			tx, err := f.owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE dude_app`); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, statement)
			var denied *pgconn.PgError
			if !errors.As(err, &denied) || denied.Code != "42501" {
				t.Fatalf("app write = %v, want insufficient_privilege", err)
			}
		})
	}
	if err := b.PublishLayer(ctx); err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := b.DB.Pool.QueryRow(ctx, `SELECT ref FROM image_layers`).Scan(&ref); err != nil || ref != b.Layer {
		t.Fatalf("builder read = %q, %v", ref, err)
	}
}

func TestRunKeepsHeartbeatAndRecoversBeforeLayerPublication(t *testing.T) {
	f := setup(t)
	b := layerBuilder(t, f, "cli v1")
	b.Beat, b.Poll = 10*time.Millisecond, 10*time.Millisecond
	f.image("img_run", "run")
	job := f.queue("img_run", "imv_run", 1, "FROM debian\n")
	f.exec(`UPDATE image_builds SET state = 'running' WHERE id = $1`, job)
	f.podman.block, f.podman.blocked = make(chan struct{}), make(chan struct{})
	blocked := f.podman.blocked
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("layer build did not start")
	}
	waitLayer(t, f, `SELECT state = 'queued' AND restarts = 1 FROM image_builds WHERE id = 'imb_imv_run'`)
	f.exec(`UPDATE image_builder SET seen_at = now() - interval '1 hour'`)
	waitLayer(t, f, `SELECT seen_at > now() - interval '1 minute' FROM image_builder`)
	if got := f.str(`SELECT state FROM image_builds WHERE id = 'imb_imv_run'`); got != "queued" {
		t.Fatalf("claimed during layer build: %s", got)
	}
	f.podman.fail = func(op, tag, _ string) (string, error) {
		if op == "build" && strings.Contains(tag, "imv_run") {
			var count int
			if err := b.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM image_layers`).Scan(&count); err != nil || count != 1 {
				return "", fmt.Errorf("job started before current layer: count=%d, error=%v", count, err)
			}
		}
		return "", nil
	}
	close(f.podman.block)
	waitLayer(t, f, `SELECT state = 'succeeded' FROM image_builds WHERE id = 'imb_imv_run'`)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b.Layer == "" || current(t, f, "") != b.Layer {
		t.Fatal("Run did not publish its layer")
	}
}

func TestRunPublicationFailureReturnsCauseAndStopsHeartbeat(t *testing.T) {
	f := setup(t)
	b := layerBuilder(t, f, "cli v1")
	b.Beat = 10 * time.Millisecond
	f.image("img_run", "run")
	f.queue("img_run", "imv_run", 1, "FROM debian\n")
	cause := errors.New("registry unavailable")
	var logs bytes.Buffer
	b.Log = slog.New(slog.NewTextHandler(&logs, nil))
	f.podman.fail = func(op, _, _ string) (string, error) {
		if op == "push" {
			return "", cause
		}
		return "", nil
	}
	if err := b.Run(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("Run = %v", err)
	}
	if !strings.Contains(logs.String(), "registry unavailable") {
		t.Fatalf("startup failure not logged: %s", logs.String())
	}
	if got := f.str(`SELECT state FROM image_builds WHERE id = 'imb_imv_run'`); got != "queued" {
		t.Fatalf("job = %s", got)
	}
	if got := f.str(`SELECT count(*) FROM image_layers`); got != "0" {
		t.Fatalf("layers = %s", got)
	}
	time.Sleep(30 * time.Millisecond)
	before := f.str(`SELECT seen_at FROM image_builder`)
	time.Sleep(60 * time.Millisecond)
	if after := f.str(`SELECT seen_at FROM image_builder`); after != before {
		t.Fatalf("heartbeat continues after failure: %s -> %s", before, after)
	}
}

func waitLayer(t *testing.T, f *fixture, query string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.str(query) == "true" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", query)
}

// current is CurrentLayer in the orchestrator's role, in an organization's
// transaction, as Runs resolve it.
func current(t *testing.T, f *fixture, override string) string {
	t.Helper()
	tx, err := f.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE dude_app`); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentLayer(context.Background(), tx, override)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
