package delivery

import (
	"strings"
	"testing"
)

// The two reports production sent back as unparseable (JERV-1): each now
// names yaml.v3's reason, without its block-relative line number.
func TestAnUnparseableChecksReportNamesTheYAMLError(t *testing.T) {
	for name, tc := range map[string]struct{ reply, want string }{
		"text after a quoted value": {
			"Done.\n\n```yaml\nchecks:\n  - command: npm run build\n    result: pass\n" +
				"    evidence: \"✓ built in 2.30s\", exit 0 (vite build then tsc --noEmit)\n```\n",
			"did not find expected key",
		},
		"a colon in a plain value": {
			"checks:\n  - command: python -m pytest -q\n    result: not-run\n" +
				"    evidence: ModuleNotFoundError: No module named 'fastapi' (dependencies not installed)\n",
			"mapping values are not allowed in this context",
		},
		"a mapping where a list belongs": {
			"checks:\n  command: make\n  result: pass\n",
			"cannot unmarshal !!map into []delivery.Check",
		},
	} {
		if _, ok := ParseChecks(tc.reply); ok {
			t.Fatalf("%s: parsed", name)
		}
		got := ChecksYAMLError(tc.reply)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "line ") || strings.HasPrefix(got, "yaml:") {
			t.Errorf("%s: %q, want it to contain %q with no yaml: prefix or line number", name, got, tc.want)
		}
	}
}

// No block, a block that decodes, and one that decodes but fails a field
// check have no YAML error to name.
func TestAChecksReportThatIsYAMLHasNoYAMLError(t *testing.T) {
	for name, reply := range map[string]string{
		"none":      "Implemented it.",
		"valid":     "checks:\n  - command: 'go test ./...'\n    result: pass\n    evidence: 'ok  pkg 0.1s'\n",
		"bad field": "checks:\n  - command: a\n    result: maybe\n",
	} {
		if got := ChecksYAMLError(reply); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// The quoting the writers are told to use parses, including a doubled
// quote and the characters that broke the production reports.
func TestQuotedChecksValuesParse(t *testing.T) {
	reply := "checks:\n  - command: 'npm run build'\n    result: pass\n" +
		"    evidence: '\"✓ built in 2.30s\", exit 0: it''s done # really'\n"
	got, ok := ParseChecks(reply)
	if !ok || len(got) != 1 || got[0].Evidence != "\"✓ built in 2.30s\", exit 0: it's done # really" {
		t.Fatalf("ParseChecks = %#v, %v", got, ok)
	}
}
