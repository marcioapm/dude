package servers

import (
	"testing"
	"time"
)

func TestPreviewReapDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		shared, branch, wantTask, wantBranch time.Duration
	}{
		{"defaults", 0, 0, 7 * 24 * time.Hour, 30 * 24 * time.Hour},
		{"shared override", time.Hour, 0, time.Hour, time.Hour},
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
