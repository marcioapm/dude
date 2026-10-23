//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeKeystoreCommand(t *testing.T, binary string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argv, stdin := filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGV\"\nwhile IFS= read -r line || [ -n \"$line\" ]; do printf '%s' \"$line\"; done > \"$INPUT\"\nprintf '%s' \"$STORE_ERROR\" >&2\nexit \"${STORE_EXIT:-0}\"\n"
	if err := os.WriteFile(filepath.Join(dir, binary), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ARGV", argv)
	t.Setenv("INPUT", stdin)
	t.Setenv("STORE_ERROR", "")
	t.Setenv("STORE_EXIT", "0")
	return argv, stdin
}

func readRecorded(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
