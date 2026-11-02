package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

// convert-deliveries takes no arguments: extra ones are refused with its
// usage, exit 2, before any configuration or connection.
func TestConvertDeliveriesRefusesArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := convertCommand(context.Background(), []string{"now"}, config.Options{}, &stdout, &stderr); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: dude-orchestrator convert-deliveries") || stdout.Len() != 0 {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}
