package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// publishes publishes path as name and returns what dude publish reports.
func publishes(t *testing.T, path, name string) (string, int64) {
	t.Helper()
	raw, err := publish(path, name)
	if err != nil {
		t.Fatalf("publish %s: %v", path, err)
	}
	var got struct {
		Published string `json:"published"`
		Bytes     int64  `json:"bytes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got.Published, got.Bytes
}

func TestPublishingAFileCopiesItIntoTheArtifacts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LUX_ARTIFACTS", dir)
	src := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(src, []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, n := publishes(t, src, "")
	if name != "notes.md" || n != 8 {
		t.Fatalf("published %q, %d bytes; want notes.md, 8", name, n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.md")); string(b) != "# notes\n" {
		t.Fatalf("the published file holds %q", b)
	}
}

// A browser's video is recorded straight into $LUX_ARTIFACTS, and an agent
// then publishes it from there: the file must keep its bytes, not be
// truncated by being copied onto itself.
func TestPublishingAFileAlreadyInTheArtifactsKeepsItsBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LUX_ARTIFACTS", dir)
	video := filepath.Join(dir, "walkthrough.webm")
	want := "webm bytes that must survive"
	if err := os.WriteFile(video, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	// By its own path, and by a hard link under another name.
	link := filepath.Join(t.TempDir(), "elsewhere.webm")
	if err := os.Link(video, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{video, link} {
		name, n := publishes(t, path, "walkthrough.webm")
		if name != "walkthrough.webm" || n != int64(len(want)) {
			t.Fatalf("publishing %s: published %q, %d bytes; want walkthrough.webm, %d", path, name, n, len(want))
		}
		if b, _ := os.ReadFile(video); string(b) != want {
			t.Fatalf("after publishing %s, the file holds %q", path, b)
		}
	}
}
