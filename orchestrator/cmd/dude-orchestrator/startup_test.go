package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunHelper is run's process for TestStartupLogsTheLoadersWarnings;
// alone it does nothing.
func TestRunHelper(t *testing.T) {
	if os.Getenv("DUDE_TEST_RUN_HELPER") != "1" {
		t.Skip("started by TestStartupLogsTheLoadersWarnings")
	}
	err := run(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err == nil {
		os.Exit(0)
	}
	os.Exit(1)
}

func TestStartupLogsTheLoadersWarnings(t *testing.T) {
	const sentinel = "S3NT1NEL-startup-lux-key-41ad"
	path := filepath.Join(t.TempDir(), "dude.toml")
	// Port 1 on loopback refuses at once, so startup fails right after logging.
	text := `[database]
url = "postgres://dude:x@127.0.0.1:1/dude?connect_timeout=2"
[orchestrator]
token = "file-token"
[lux]
url = "https://lux.file"
api_key = "` + sentinel + `"
`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunHelper$", "-test.count=1")
	// Only the file configures it: none of this machine's own settings.
	cmd.Env = []string{"DUDE_TEST_RUN_HELPER=1", "DUDE_CONFIG=" + path, "HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH")}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("startup did not exit: %s", stderr.String())
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want startup to fail on the database; stderr: %s", err, stderr.String())
	}
	out := stderr.String()
	if strings.Contains(out, sentinel) {
		t.Errorf("startup log carries the secret: %s", out)
	}
	var read, warned bool
	for _, line := range strings.Split(out, "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		msg, _ := rec["msg"].(string)
		switch {
		case msg == "configuration file read":
			read = rec["path"] == path
		case rec["level"] == "WARN" && strings.Contains(msg, "lux.api_key"):
			warned = true
		}
	}
	if !read || !warned {
		t.Errorf("file read logged with its path: %v, warning naming lux.api_key: %v; stderr: %s", read, warned, out)
	}
}
