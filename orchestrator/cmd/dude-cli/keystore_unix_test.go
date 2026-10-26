//go:build !darwin && !windows

package main

import (
	"errors"
	"strings"
	"testing"
)

func TestSecretToolAdapter(t *testing.T) {
	argv, stdin := fakeKeystoreCommand(t, "secret-tool")
	store := secretTool{}
	key := `secret"with\escapes`
	if err := store.set("host/acme", key); err != nil {
		t.Fatal(err)
	}
	if got := readRecorded(t, argv); got != "store\n--label\ndude host/acme\nservice\nhost/acme\n" || strings.Contains(got, key) {
		t.Fatalf("argv: %q", got)
	}
	if got := readRecorded(t, stdin); got != key {
		t.Fatalf("stdin: %q", got)
	}
	t.Setenv("STORE_OUT", "k3y\n")
	if got, err := store.get("host/acme"); err != nil || got != "k3y" {
		t.Fatalf("get: %q %v", got, err)
	}
	if got := readRecorded(t, argv); got != "lookup\nservice\nhost/acme\n" {
		t.Fatalf("get argv: %q", got)
	}
	t.Setenv("STORE_OUT", "")
	t.Setenv("STORE_EXIT", "44")
	if _, err := store.get("host/acme"); !errors.Is(err, errNoKey) {
		t.Fatalf("missing key: %v", err)
	}
	t.Setenv("STORE_ERROR", "Cannot autolaunch D-Bus without X11")
	if _, err := store.get("host/acme"); !errors.Is(err, errNoKeystore) {
		t.Fatalf("D-Bus: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := store.get("host/acme"); !errors.Is(err, errNoKeystore) {
		t.Fatalf("missing binary: %v", err)
	}
}
