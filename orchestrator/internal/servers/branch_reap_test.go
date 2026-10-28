package servers

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

func TestPreviewReapDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		shared, branch, wantTask, wantBranch time.Duration
	}{
		{"defaults", 0, 0, 7 * 24 * time.Hour, 30 * 24 * time.Hour},
		{"task override", time.Hour, 0, time.Hour, 30 * 24 * time.Hour},
		{"branch override", time.Hour, 2 * time.Hour, time.Hour, 2 * time.Hour},
		{"branch only", 0, 2 * time.Hour, 7 * 24 * time.Hour, 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Previews{ReapAfter: tc.shared, BranchReapAfter: tc.branch}
			if task, branch := p.reapAfter(false), p.reapAfter(true); task != tc.wantTask || branch != tc.wantBranch {
				t.Fatalf("task=%s branch=%s; want %s %s", task, branch, tc.wantTask, tc.wantBranch)
			}
		})
	}
}

func TestPreviewReapWithConfigDefaults(t *testing.T) {
	cfg, err := config.Load(config.Orchestrator, config.Options{
		Getenv: func(string) string { return "" }, DefaultPath: filepath.Join(t.TempDir(), "absent.toml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Previews{ReapAfter: cfg.Duration("DUDE_PREVIEW_REAP_AFTER")}
	if task, branch := p.reapAfter(false), p.reapAfter(true); task != 7*24*time.Hour || branch != 30*24*time.Hour {
		t.Fatalf("real config defaults: task=%s branch=%s; want 7d and 30d", task, branch)
	}
}
