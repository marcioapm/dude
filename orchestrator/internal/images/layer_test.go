package images

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
