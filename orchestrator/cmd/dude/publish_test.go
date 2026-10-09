package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// publishes publishes path as name, with description, through an older
// lux's $LUX_ARTIFACTS and returns what dude publish reports.
func publishes(t *testing.T, path, name, description string) (string, int64) {
	t.Helper()
	raw, err := publish(path, name, description)
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

// fakeShim stands in for /.lux/bin/lux-shim: it writes its arguments, one
// per line, to args, prints answer and exits with code.
func fakeShim(t *testing.T, answer string, code int) (args string) {
	t.Helper()
	args = filepath.Join(t.TempDir(), "args")
	useShim(t, "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> "+args+"; done\n"+
		"printf '%s\\n' '"+answer+"'\nexit "+strconv.Itoa(code)+"\n")
	return args
}

// useShim makes script the lux-shim dude publish runs, for this test.
func useShim(t *testing.T, script string) {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "lux-shim")
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := luxShim
	luxShim = shim
	t.Cleanup(func() { luxShim = old })
}

// On a lux with artifact-publish ($LUX_ARTIFACTS unset), dude publish is
// lux-shim publish with the name and description, and its answer is
// lux-shim's.
func TestPublishingRunsLuxShimPublish(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	answer := `{"id":"art_aaaaaaaaaaaaaaaa","name":"design/notes.md","size":8,"sha256":"ab"}`
	args := fakeShim(t, answer, 0)
	raw, err := publish("/workspace/notes.md", "design/notes.md", "Why the export streams")
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	_ = json.Unmarshal([]byte(answer), &want)
	if err := json.Unmarshal(raw, &got); err != nil || got["id"] != want["id"] || got["name"] != want["name"] {
		t.Fatalf("dude publish answered %s; want lux-shim's %s", raw, answer)
	}
	b, _ := os.ReadFile(args)
	if got := strings.Split(strings.TrimSpace(string(b)), "\n"); strings.Join(got, "|") !=
		"publish|--name|design/notes.md|--description|Why the export streams|--|/workspace/notes.md" {
		t.Fatalf("lux-shim was run with %q", got)
	}
}

// Without --name or --description, neither flag is passed: lux-shim names
// the file by its base name.
func TestPublishingWithoutANamePassesNoFlags(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	args := fakeShim(t, `{"id":"art_aaaaaaaaaaaaaaaa","name":"notes.md","size":1,"sha256":"ab"}`, 0)
	if _, err := publish("notes.md", "", ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(args); string(b) != "publish\n--\nnotes.md\n" {
		t.Fatalf("lux-shim was run with %q", b)
	}
}

// The CLI's flags reach lux-shim, the file last after `--`; a description
// with no FILE is a mistake, not a conductor's publish.
func TestDudePublishPassesItsFlagsToLuxShim(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	args := fakeShim(t, `{"id":"art_aaaaaaaaaaaaaaaa","name":"n.md","size":1,"sha256":"ab"}`, 0)
	var out strings.Builder
	if err := run([]string{"publish", "notes.md", "--name", "n.md", "--description", "Why"}, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(args); string(b) != "publish\n--name\nn.md\n--description\nWhy\n--\nnotes.md\n" {
		t.Fatalf("lux-shim was run with %q", b)
	}
	if err := run([]string{"publish", "--description", "Why"}, &out); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no FILE: %v", err)
	}
}

// A file whose name starts with - is published, not read as a flag.
func TestAFileNamedLikeAFlagIsPublished(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	args := fakeShim(t, `{"id":"art_aaaaaaaaaaaaaaaa","name":"-notes.md","size":1,"sha256":"ab"}`, 0)
	var out strings.Builder
	if err := run([]string{"publish", "--description", "Why", "--", "-notes.md"}, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(args); string(b) != "publish\n--description\nWhy\n--\n-notes.md\n" {
		t.Fatalf("lux-shim was run with %q", b)
	}
}

// lux-shim killed by a signal has no status to pass on: dude's own error,
// which main prints and exits 1 with.
func TestALuxShimKilledByASignalIsDudesError(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	useShim(t, "#!/bin/sh\nkill -KILL $$\n")
	err := run([]string{"publish", "notes.md"}, &strings.Builder{})
	var passed shimFailed
	if err == nil || errors.As(err, &passed) || !strings.Contains(err.Error(), "lux-shim publish: signal: killed") {
		t.Fatalf("publish answered %v; want dude's own error naming the signal", err)
	}
}

// lux-shim's refusal is dude publish's: the CLI built and run, its stderr
// and exit status are lux-shim's own.
func TestALuxShimRefusalIsPassedThrough(t *testing.T) {
	useShim(t, "#!/bin/sh\necho 'lux-shim publish: description has a control character' >&2\nexit 3\n")
	bin := filepath.Join(t.TempDir(), "dude")
	build := exec.Command("go", "build", "-ldflags", "-X main.luxShim="+luxShim, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "publish", "notes.md", "--description", "x")
	cmd.Env = append(os.Environ(), "LUX_ARTIFACTS=")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("dude publish exited %v; want lux-shim's 3", err)
	}
	if stderr.String() != "lux-shim publish: description has a control character\n" || stdout.String() != "" {
		t.Fatalf("stdout %q, stderr %q; want lux-shim's words alone on stderr", stdout.String(), stderr.String())
	}
}

func TestPublishingAFileCopiesItIntoAnOlderLuxsArtifacts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LUX_ARTIFACTS", dir)
	// Never run on an older lux: one that is would fail the test.
	fakeShim(t, "", 9)
	src := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(src, []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, n := publishes(t, src, "", "dropped on an older lux")
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
		name, n := publishes(t, c.path, c.name, "")
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
	name, n := publishes(t, src, "report.md", "")
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
