package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The workflow's store weighs a next round's predecessor against the time
// limit phase Runs are submitted with: agent.timeout, else its default. One
// that is not a Go duration leaves the check off, and says so at startup.
func TestTheDeliveryStoreTakesTheAgentsTimeLimit(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       time.Duration
		warns      bool
	}{
		{"set", required + "[agent]\ntimeout = \"48h\"\n", 48 * time.Hour, false},
		{"unset", required, 4 * time.Hour, false},
		{"not a Go duration", required + "[agent]\ntimeout = \"4 hours\"\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			s := deliveryStore(nil, mustSettings(t, loadConfig(t, tc.text, 0o600, nil)).Agent, slog.New(slog.NewTextHandler(&logged, nil)))
			if s.RunTimeLimit != tc.want {
				t.Errorf("RunTimeLimit = %v, want %v", s.RunTimeLimit, tc.want)
			}
			if warned := strings.Contains(logged.String(), "agent.timeout"); warned != tc.warns {
				t.Errorf("warned %v, want %v: %s", warned, tc.warns, logged.String())
			}
		})
	}
}
