package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// The dude layer is built by the builder itself from the installed
// release: share/dude/layer/ (scripts/layer-context.sh) with the release's
// bin/dude added as `dude`. It is tagged by content (LayerTag), pushed to
// LayerRepository, and recorded in image_layers (migration 143), whose most
// recently selected row is the current layer for every process.

// LayerTag is the layer's tag: the first 16 hex of a sha256 over every
// file under dir (sorted relative paths and contents), the bytes of the
// CLI at dude, and the platform. Every field is length-prefixed, so no two
// inputs share an encoding. A release whose inputs are byte-identical gets
// the same tag, and so no new layer.
func LayerTag(dir, dude, platform string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	field := func(b []byte) {
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	for _, p := range files {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		field([]byte(filepath.ToSlash(rel)))
		field(b)
	}
	cli, err := os.ReadFile(dude)
	if err != nil {
		return "", err
	}
	field(append([]byte("dude_bin="), cli...))
	field([]byte("platform=" + platform))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// LayerRepository is where the builder pushes the layer: beside the
// library's repository, as layer (…/dude/custom → …/dude/layer).
func LayerRepository(repository string) string {
	return path.Join(path.Dir(repository), "layer")
}

// NoLayer is why the library cannot run before the builder has published
// a layer and no override names one.
const NoLayer = "dude-image-builder has not published a dude layer yet"

// CurrentLayer is the layer library images are finished with now: the
// operator's override (DUDE_LAYER_IMAGE) when set, else the one the
// builder selected last; "" for none yet.
func CurrentLayer(ctx context.Context, tx pgx.Tx, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	var ref string
	err := tx.QueryRow(ctx, `SELECT ref FROM image_layers ORDER BY current_at DESC, tag DESC LIMIT 1`).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ref, err
}

// PublishLayer makes the release's layer current: built and pushed as
// LayerRepository:<LayerTag>, unless a layer of that tag is recorded
// already (the same inputs, or a rollback to them), which only becomes
// current again: registry tags are immutable. Sets b.Layer to its ref.
func (b *Builder) PublishLayer(ctx context.Context) error {
	platform := "linux/" + runtime.GOARCH
	if b.Limits.Platform != platform {
		return fmt.Errorf("the dude layer for %s needs that platform's dude CLI; this release's bin/dude is %s", b.Limits.Platform, platform)
	}
	tag, err := LayerTag(b.LayerContext, b.DudeBinary, b.Limits.Platform)
	if err != nil {
		return fmt.Errorf("reading the dude layer's build context: %w", err)
	}
	var ref string
	err = b.DB.Pool.QueryRow(ctx, `UPDATE image_layers SET current_at = now() WHERE tag = $1 RETURNING ref`, tag).Scan(&ref)
	if err == nil {
		b.Log.Info("dude layer current", "tag", tag, "ref", ref)
		b.Layer = ref
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	ref, err = b.buildLayer(ctx, tag)
	if err != nil {
		return err
	}
	if _, err := b.DB.Pool.Exec(ctx, `INSERT INTO image_layers (tag, ref, platform, version) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tag) DO UPDATE SET current_at = now()`, tag, ref, b.Limits.Platform, b.Version); err != nil {
		return err
	}
	b.Log.Info("dude layer published", "tag", tag, "ref", ref)
	b.Layer = ref
	return nil
}

// buildLayer builds and pushes the layer, and returns its ref by digest.
func (b *Builder) buildLayer(ctx context.Context, tag string) (string, error) {
	dir, err := b.layerDir()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	timeout := b.Limits.Timeout
	if timeout == 0 {
		timeout = time.Hour
	}
	limit, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	image := LayerRepository(b.Repository) + ":" + tag
	out := os.Stderr
	b.Log.Info("building the dude layer", "image", image)
	if err := b.Podman.Build(limit, dir, image, nil, out); err != nil {
		return "", fmt.Errorf("building the dude layer %s: %w", image, err)
	}
	digest, err := b.Podman.Push(limit, image, out)
	if err != nil {
		return "", fmt.Errorf("pushing the dude layer %s: %w", image, err)
	}
	return LayerRepository(b.Repository) + "@" + digest, nil
}

// layerDir is a copy of the release's layer context with the CLI added as
// dude, the caller's to remove: the installed release is not written to.
func (b *Builder) layerDir() (string, error) {
	dir, err := os.MkdirTemp("", "dude-layer-")
	if err != nil {
		return "", err
	}
	err = os.CopyFS(dir, os.DirFS(b.LayerContext))
	if err == nil {
		// CLI.Build reads <dir>/Containerfile.
		err = os.Rename(filepath.Join(dir, "Dockerfile"), filepath.Join(dir, "Containerfile"))
	}
	var cli []byte
	if err == nil {
		cli, err = os.ReadFile(b.DudeBinary)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "dude"), cli, 0o755)
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("preparing the dude layer's build context: %w", err)
	}
	return dir, nil
}
