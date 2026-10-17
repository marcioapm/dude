package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LayerTag is content_tag from the former layer builder: paths and bytes in
// the release context, then the release CLI bytes and platform, all framed so
// concatenated inputs cannot collide.
func LayerTag(dir, dude string, platform string) (string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	field := func(b []byte) { _, _ = fmt.Fprintf(h, "%d:", len(b)); _, _ = h.Write(b) }
	for _, path := range files {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return "", err
		}
		field([]byte(filepath.ToSlash(rel)))
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		field(b)
	}
	b, err := os.ReadFile(dude)
	if err != nil {
		return "", err
	}
	field(append([]byte("dude_bin="), b...))
	field([]byte("platform=" + platform))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// CurrentLayer returns the latest layer selected by the builder. An explicit
// operator override is handled by callers before this database lookup.
func CurrentLayer(ctx context.Context, tx pgx.Tx) (string, error) {
	var ref string
	err := tx.QueryRow(ctx, `SELECT ref FROM image_layers ORDER BY current_at DESC, tag DESC LIMIT 1`).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ref, err
}

func LayerRepository(repository string) string { return layerRepository(repository) }

func layerRepository(repository string) string {
	if i := strings.LastIndexByte(repository, '/'); i >= 0 {
		return repository[:i] + "/layer"
	}
	return repository + "/layer"
}
