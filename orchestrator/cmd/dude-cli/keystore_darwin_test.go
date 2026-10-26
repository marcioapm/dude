package main

import (
	"errors"
	"strings"
	"testing"
)

func TestMacKeychainAdapter(t *testing.T) {
	argv, stdin := fakeKeystoreCommand(t, "security")
	t.Setenv("USER", "ana")
	store := macKeychain{}
	key := `secret"with\escapes`
	if err := store.set("host/acme", key); err != nil {
		t.Fatal(err)
	}
	if got := readRecorded(t, argv); got != "-i\n" || strings.Contains(got, key) {
		t.Fatalf("argv: %q", got)
	}
	if got, want := readRecorded(t, stdin), `add-generic-password -U -a "ana" -s "host/acme" -l "dude host/acme" -w "secret\"with\\escapes"`; got != want {
		t.Fatalf("stdin: %q, want %q", got, want)
	}
	t.Setenv("STORE_OUT", "k3y\n")
	if got, err := store.get("host/acme"); err != nil || got != "k3y" {
		t.Fatalf("get: %q %v", got, err)
	}
	if got := readRecorded(t, argv); got != "find-generic-password\n-s\nhost/acme\n-w\n" {
		t.Fatalf("get argv: %q", got)
	}
	t.Setenv("STORE_OUT", "")
	t.Setenv("STORE_EXIT", "44")
	if _, err := store.get("host/acme"); !errors.Is(err, errNoKey) {
		t.Fatalf("missing key: %v", err)
	}
	if err := store.del("host/acme"); !errors.Is(err, errNoKey) {
		t.Fatalf("delete missing key: %v", err)
	}
	t.Setenv("STORE_EXIT", "0")
	t.Setenv("STORE_ERROR", "security: Error writing item")
	if err := store.set("host/acme", key); err == nil {
		t.Fatal("security -i error output was accepted")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := store.get("host/acme"); !errors.Is(err, errNoKeystore) {
		t.Fatalf("missing binary: %v", err)
	}
	if err := store.set("host/acme", key); !errors.Is(err, errNoKeystore) {
		t.Fatalf("missing binary on set: %v", err)
	}
}
