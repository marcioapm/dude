package images

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

func (b *Builder) ensureLayer(ctx context.Context) error {
	if b.LayerContext == "" || b.DudeBinary == "" || b.LayerRepository == "" {
		return fmt.Errorf("cannot build the dude layer: the release layer context, CLI or layer repository is not configured")
	}
	tag, err := LayerTag(b.LayerContext, b.DudeBinary, b.Limits.Platform)
	if err != nil {
		return fmt.Errorf("hashing the dude layer context: %w", err)
	}
	var existing string
	err = b.DB.Pool.QueryRow(ctx, `SELECT ref FROM image_layers WHERE tag = $1`, tag).Scan(&existing)
	if err == nil {
		b.Layer = existing
		_, err = b.DB.Pool.Exec(ctx, `UPDATE image_layers SET current_at = now() WHERE tag = $1`, tag)
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Podman needs a file named dude in the context; the release context is
	// immutable, so make a private directory rather than changing it.
	dir, err := os.MkdirTemp("", "dude-layer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := copyTree(b.LayerContext, dir); err != nil {
		return err
	}
	if err := copyFile(b.DudeBinary, filepath.Join(dir, "dude")); err != nil {
		return err
	}
	image := b.LayerRepository + ":" + tag
	if err := b.Podman.Build(ctx, dir, image, nil, os.Stdout); err != nil {
		return fmt.Errorf("building the dude layer: %w", err)
	}
	digest, err := b.Podman.Push(ctx, image, os.Stdout)
	if err != nil {
		return fmt.Errorf("pushing the dude layer: %w", err)
	}
	ref := b.LayerRepository + "@" + digest
	_, err = b.DB.Pool.Exec(ctx, `INSERT INTO image_layers (tag, ref, platform, version) VALUES ($1, $2, $3, $4)`, tag, ref, b.Limits.Platform, b.Version)
	if err != nil {
		return err
	}
	b.Layer = ref
	return nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(to, info.Mode().Perm())
		}
		return copyFile(path, to)
	})
}
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
