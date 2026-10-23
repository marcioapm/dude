package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// The login keychain, a generic password whose service is the host.
var osKeystore keystore = macKeychain{}

type macKeychain struct{}

func (macKeychain) name() string { return "macOS keychain" }

func (macKeychain) get(host string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", host, "-w").Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 44: // errSecItemNotFound
		return "", errNoKey
	case errors.Is(err, exec.ErrNotFound):
		return "", errNoKeystore
	case err != nil:
		return "", err
	}
	if k := strings.TrimSpace(string(out)); k != "" {
		return k, nil
	}
	return "", errNoKey
}

// set gives security its command on stdin (security -i), so the key never
// appears in a process list.
func (macKeychain) set(host, key string) error {
	user := os.Getenv("USER")
	if user == "" {
		user = "dude"
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -a " + quote(user) + " -s " + quote(host) +
		" -l " + quote("dude "+host) + " -w " + quote(key) + "\n")
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrNotFound) {
		return errNoKeystore
	}
	// security -i exits 0 even when a command fails; it says so instead.
	if err != nil || strings.Contains(string(out), "rror") {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

func (macKeychain) del(host string) error {
	err := exec.Command("security", "delete-generic-password", "-s", host).Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 44:
		return errNoKey
	case errors.Is(err, exec.ErrNotFound):
		return errNoKeystore
	}
	return err
}

// quote is a word for security -i, which splits on spaces and honours
// double quotes with backslash escapes.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
