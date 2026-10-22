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
	} {
		if _, ok := ParseChecks(tc.reply); ok {
			t.Fatalf("%s: parsed", name)
		}
		got := ChecksYAMLErrorOf(tc.reply)
		if got.Shape || !strings.Contains(got.Syntax, tc.want) || strings.Contains(got.Syntax, "line ") ||
			strings.HasPrefix(got.Syntax, "yaml:") {
			t.Errorf("%s: %+v, want Syntax containing %q with no yaml: prefix or line number", name, got, tc.want)
		}
	}
}

// YAML of the wrong shape is not a quoting fault: it is told apart, and no
// Go type name reaches the writer.
func TestAChecksBlockOfTheWrongShapeIsNotASyntaxError(t *testing.T) {
	for name, reply := range map[string]string{
		"a mapping where a list belongs":      "checks:\n  command: make\n  result: pass\n",
		"a list value where a string belongs": "checks:\n  - command: [a]\n    result: pass\n",
	} {
		if _, ok := ParseChecks(reply); ok {
			t.Fatalf("%s: parsed", name)
		}
		if got := ChecksYAMLErrorOf(reply); !got.Shape || got.Syntax != "" {
			t.Errorf("%s: %+v, want Shape only", name, got)
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
		if got := ChecksYAMLErrorOf(reply); got != (ChecksYAMLError{}) {
			t.Errorf("%s: %+v", name, got)
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
