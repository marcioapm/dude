//go:build !darwin && !windows

package main

import (
	"errors"
	"os/exec"
	"strings"
)

// The Secret Service (GNOME Keyring, KWallet, KeePassXC…) through
// secret-tool, from libsecret (apt install libsecret-tools; dnf install
// libsecret). Without it, or without a running keyring, the credentials
// file is used.
var osKeystore keystore = secretTool{}

type secretTool struct{}

func (secretTool) name() string { return "Secret Service keyring" }

func (secretTool) run(stdin string, args ...string) (string, error) {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return "", errNoKeystore
	}
	cmd := exec.Command("secret-tool", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		// No D-Bus session or no keyring service: no store here.
		if strings.Contains(msg, "DBus") || strings.Contains(msg, "D-Bus") || strings.Contains(msg, "dbus") ||
			strings.Contains(msg, "org.freedesktop.secrets") || strings.Contains(msg, "Cannot autolaunch") {
			return "", errNoKeystore
		}
		if msg == "" {
			return "", errNoKey // lookup and clear exit 1, silently, when nothing matches
		}
		return "", errors.New(msg)
	}
	return string(out), nil
}

func (s secretTool) get(host string) (string, error) {
	out, err := s.run("", "lookup", "service", host)
	if err != nil {
		return "", err
	}
	if k := strings.TrimSpace(out); k != "" {
		return k, nil
	}
	return "", errNoKey
}

// set gives secret-tool the key on stdin, never on its command line.
func (s secretTool) set(host, key string) error {
	_, err := s.run(key, "store", "--label", "dude "+host, "service", host)
	return err
}

func (s secretTool) del(host string) error {
	if _, err := s.get(host); err != nil {
		return err
	}
	_, err := s.run("", "clear", "service", host)
	return err
}
