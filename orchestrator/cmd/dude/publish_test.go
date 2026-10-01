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
	link := filepath.Join(t.TempDir(), "elsewhere.webm")
	if err := os.Link(video, link); err != nil {
		t.Fatal(err)
	}
	// As agents run it (by its own path, no --name), and by a hard link
	// under another name.
	for _, c := range []struct{ path, name string }{{video, ""}, {link, "walkthrough.webm"}} {
		name, n := publishes(t, c.path, c.name)
		if name != "walkthrough.webm" || n != int64(len(want)) {
			t.Fatalf("publishing %s: published %q, %d bytes; want walkthrough.webm, %d", c.path, name, n, len(want))
		}
		if b, _ := os.ReadFile(video); string(b) != want {
			t.Fatalf("after publishing %s, the file holds %q", c.path, b)
		}
	}
}

// Publishing under the name of a different file already published replaces
// it with the new bytes, and leaves the source alone.
func TestPublishingOverADifferentArtifactReplacesIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LUX_ARTIFACTS", dir)
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("an older, longer report"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(src, []byte("new report"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, n := publishes(t, src, "report.md")
	if name != "report.md" || n != 10 {
		t.Fatalf("published %q, %d bytes; want report.md, 10", name, n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "report.md")); string(b) != "new report" {
		t.Fatalf("the published file holds %q", b)
	}
	if b, _ := os.ReadFile(src); string(b) != "new report" {
		t.Fatalf("the source now holds %q", b)
	}
}
