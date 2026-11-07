package phases

import (
	"context"
	"strings"
	"testing"
)

// A report production sent back unparseable (JERV-1): the verdict the
// writer gets names yaml.v3's reason and how to quote, not only that the
// block is unparseable.
func TestAnUnparseableChecksReportIsSentBackWithTheYAMLError(t *testing.T) {
	w := newHarnessWorld(t)
	checksReply(w, "Built.\n\n```yaml\nchecks:\n  - command: npm run build\n    result: pass\n"+
		"    evidence: \"✓ built in 2.30s\", exit 0 (vite build then tsc --noEmit)\n```\n")
	d, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	if d.Verified || len(d.Verdicts) != 1 || d.Verdicts[0].Command != "checks:" {
		t.Fatalf("decision %+v", d)
	}
	reason := d.Verdicts[0].Reason
	for _, want := range []string{"missing or unparseable checks YAML block", "did not find expected key",
		"Wrap every command and evidence value in single quotes"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q lacks %q", reason, want)
		}
	}
}

// A reply with no checks block keeps the bare reason: there is no YAML
// to name.
func TestAMissingChecksReportIsSentBackWithoutAYAMLError(t *testing.T) {
	w := newHarnessWorld(t)
	checksReply(w, "Built it.")
	d, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Verdicts) != 1 || d.Verdicts[0].Reason != "missing or unparseable checks YAML block" {
		t.Fatalf("decision %+v", d)
	}
}

// YAML of the wrong shape is told what shape a report has, not to quote.
func TestAChecksReportOfTheWrongShapeIsSentBackWithTheShape(t *testing.T) {
	w := newHarnessWorld(t)
	checksReply(w, "checks:\n  command: make\n  result: pass\n")
	d, err := w.s.judgeChecks(context.Background(), w.run)
	if err != nil {
		t.Fatal(err)
	}
	reason := d.Verdicts[0].Reason
	if !strings.Contains(reason, "must be a list of entries") || strings.Contains(reason, "single quotes") ||
		strings.Contains(reason, "delivery.") {
		t.Errorf("reason %q", reason)
	}
}
