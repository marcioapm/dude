package fakelux

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Publishing, as lux#77 does it: a workload runs `lux-shim publish FILE
// --name NAME [--description TEXT]`, the file becomes an artifact of the
// Run at once (listed, not yet downloadable), and a moment later — its
// upload done — it is available and artifact.published is on the Run's
// stream. A name published again is its next version.
//
// The scripted agent publishes in-process (publish). A command the fake
// runs in a Run's checkout — exec, the beforeStop hook — finds a stand-in
// at /.lux/bin/lux-shim (shimScript), which stages each file in a
// directory the fake reads once the command is over.

// publish makes content an artifact of the Run under name, as lux's shim
// does. Callers hold s.mu.
func (s *Server) publish(run *Run, name, content, description string) {
	path := lux.PublishedPrefix + name
	version := 1
	for _, a := range run.artifacts {
		if a.Path == path {
			version = max(version, a.Version+1)
		}
	}
	sum := sha256.Sum256([]byte(content))
	s.nextArt++
	art := &artifact{ID: fmt.Sprintf("art_%d", s.nextArt), Path: path, Version: version, Description: description,
		ContentType: mimeFor(name), SHA256: hex.EncodeToString(sum[:]), Epoch: run.Epoch, Size: int64(len(content)), Content: content}
	run.artifacts = append(run.artifacts, art)
	// Uploaded a moment later, as a runner's upload trails the report.
	go func() {
		time.Sleep(5 * time.Millisecond)
		s.mu.Lock()
		defer s.mu.Unlock()
		art.Available = true
		s.luxEvent(run, "artifact.published", map[string]any{"artifactId": art.ID, "path": art.Path, "name": name,
			"version": art.Version, "description": art.Description, "size": art.Size, "sha256": art.SHA256,
			"contentType": art.ContentType})
	}()
}

// save is the agent publishing files, name → content, in name order, each
// with its description; with LegacyArtifacts, writing them into
// $LUX_ARTIFACTS instead, without one. Callers hold s.mu.
func (s *Server) save(run *Run, files, descriptions map[string]string) {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if s.LegacyArtifacts {
			if run.published == nil {
				run.published = map[string]string{}
			}
			run.published[name] = files[name]
			continue
		}
		s.publish(run, name, files[name], descriptions[name])
	}
}

// Publish is a running Run's agent publishing a file now, as `lux-shim
// publish FILE --name name --description description` does.
func (s *Server) Publish(id, name, content, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil && run.State == "running" {
		s.save(run, map[string]string{name: content}, map[string]string{name: description})
	}
}

// shimScript stands in for lux-shim: `publish FILE [--name N]
// [--description D]` copies FILE into $FAKELUX_PUBLISH/<order>/ with its
// name and description beside it, and answers as lux-shim does.
const shimScript = `#!/bin/sh
[ "$1" = publish ] || { echo "lux-shim: only publish, in the fake" >&2; exit 2; }
shift
file= name= description=
while [ $# -gt 0 ]; do
  case $1 in
    --name) name=$2; shift 2 ;;
    --description) description=$2; shift 2 ;;
    --content-type) shift 2 ;;
    *) file=$1; shift ;;
  esac
done
[ -f "$file" ] || { echo "lux-shim publish: open $file: no such file or directory" >&2; exit 1; }
[ -n "$name" ] || name=$(basename "$file")
d="$FAKELUX_PUBLISH/$(date +%s%N)-$$"
mkdir -p "$d" && cp "$file" "$d/content" && printf %s "$name" > "$d/name" && printf %s "$description" > "$d/description" || exit 1
printf '{"id":"art_staged","name":"%s","size":%s,"sha256":"%s"}\n' "$name" "$(wc -c < "$file" | tr -d ' ')" "$(sha256sum "$file" | cut -c1-64)"
`

// shim is the stand-in's path, written once.
func (s *Server) shim() string {
	s.shimOnce.Do(func() {
		dir, err := os.MkdirTemp(s.Workspaces, "fakelux-shim-")
		if err == nil {
			s.shimPath = filepath.Join(dir, "lux-shim")
			err = os.WriteFile(s.shimPath, []byte(shimScript), 0o755)
		}
		if err != nil {
			s.shimPath = "/nonexistent/lux-shim"
		}
	})
	return s.shimPath
}

// publishing gives a command in the Run's checkout the stand-in shim: its
// arguments with lux.ShimBinary read as it, the environment to run with,
// and collect, which publishes what it staged once the command is done
// (called with s.mu held).
func (s *Server) publishing(args []string) ([]string, []string, func(run *Run)) {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, lux.ShimBinary, s.shim())
	}
	staged, err := os.MkdirTemp(s.Workspaces, "fakelux-publish-")
	if err != nil {
		staged = filepath.Join(os.TempDir(), "fakelux-publish-unavailable")
	}
	// lux#77 has no $LUX_ARTIFACTS: a command must publish.
	env := slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "LUX_ARTIFACTS=") })
	env = append(env, "FAKELUX_PUBLISH="+staged)
	return out, env, func(run *Run) {
		defer os.RemoveAll(staged)
		entries, _ := os.ReadDir(staged)
		for _, e := range entries {
			read := func(f string) string { b, _ := os.ReadFile(filepath.Join(staged, e.Name(), f)); return string(b) }
			s.publish(run, read("name"), read("content"), read("description"))
		}
	}
}

// listArtifacts is GET /v1/runs/{id}/artifacts: each path's latest
// version, or with ?versions=all every one.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	all := r.URL.Query().Get("versions") == "all"
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := map[string]int{}
	for _, a := range run.artifacts {
		latest[a.Path] = max(latest[a.Path], a.Version)
	}
	out := []any{}
	for _, a := range run.artifacts {
		if !all && a.Version != latest[a.Path] {
			continue
		}
		out = append(out, map[string]any{"id": a.ID, "epoch": a.Epoch, "path": a.Path, "version": a.Version,
			"description": a.Description, "contentType": a.ContentType, "size": a.Size, "sha256": a.SHA256,
			"available": a.Available})
	}
	writeJSON(w, 200, map[string]any{"artifacts": out})
}
