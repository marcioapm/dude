package delivery

import (
	"encoding/json"
	"strings"
	"testing"
)

// A reviewer's real output: prose, a fence, several documents, and a
// multi-line description. Anything the parser cannot read is a finding lost,
// so the cases worth pinning are the ones a model actually produces.
const reviewOutput = "I reviewed the diff for correctness. Two problems:\n\n" +
	"```yaml\n" +
	"severity: blocking\n" +
	"category: correctness\n" +
	"file: src/api/routes/work.ts\n" +
	"line: 142\n" +
	"title: Attempt number races under concurrent creates\n" +
	"description: MAX(attempt)+1 is read outside the transaction, so two\n" +
	"  concurrent requests compute the same number.\n" +
	"suggested_fix: Derive it inside the insert.\n" +
	"---\n" +
	"severity: low\n" +
	"category: correctness\n" +
	"file: src/api/http.ts\n" +
	"title: Error message leaks the query\n" +
	"description: The 500 body includes the failing SQL.\n" +
	"```\n"

func TestParsesEveryDocument(t *testing.T) {
	findings := ParseFindings(reviewOutput)
	if len(findings) != 2 {
		t.Fatalf("len(findings) = %d, want 2", len(findings))
	}

	first := findings[0]
	if first.Severity != "blocking" {
		t.Errorf("severity = %q, want blocking", first.Severity)
	}
	if first.File != "src/api/routes/work.ts" {
		t.Errorf("file = %q", first.File)
	}
	if first.Line != 142 {
		t.Errorf("line = %d, want 142", first.Line)
	}
	if !strings.Contains(first.Description, "concurrent requests") {
		t.Errorf("description lost its continuation: %q", first.Description)
	}
	if first.SuggestedFix == "" {
		t.Error("suggested_fix was dropped")
	}
}

// A model that found nothing says so in prose. That must read as zero
// findings, not as one malformed one.
func TestCleanReviewReportsNothing(t *testing.T) {
	for _, output := range []string{
		"I reviewed the changes and found no problems.",
		"",
		"```yaml\n```",
	} {
		if findings := ParseFindings(output); len(findings) != 0 {
			t.Errorf("ParseFindings(%q) = %d findings, want 0", output, len(findings))
		}
	}
}

// A reviewer inventing a severity must not gain the power to block a work
// item by doing so.
func TestUnknownSeverityCannotBlock(t *testing.T) {
	findings := ParseFindings("severity: critical\ntitle: Invented a severity\n")
	if len(findings) != 1 {
		t.Fatalf("len = %d, want 1", len(findings))
	}
	if findings[0].Severity != "note" {
		t.Errorf("severity = %q, want note", findings[0].Severity)
	}
}

// A document with no title says nothing actionable; dropping it beats
// reporting a finding a person cannot act on.
func TestUntitledDocumentIsDropped(t *testing.T) {
	output := "severity: blocking\nfile: src/x.ts\n---\nseverity: high\ntitle: Real one\n"
	findings := ParseFindings(output)
	if len(findings) != 1 {
		t.Fatalf("len = %d, want 1", len(findings))
	}
	if findings[0].Title != "Real one" {
		t.Errorf("kept the wrong finding: %q", findings[0].Title)
	}
}

// One unreadable document must not cost the others.
func TestOneBadDocumentDoesNotLoseTheRest(t *testing.T) {
	output := "title: First\nseverity: high\n---\n{{ not yaml at all }}\n---\ntitle: Third\nseverity: low\n"
	if findings := ParseFindings(output); len(findings) != 2 {
		t.Fatalf("len = %d, want 2", len(findings))
	}
}

func TestQuotedValuesAreUnquoted(t *testing.T) {
	findings := ParseFindings(`title: "Quoted title"` + "\nseverity: 'high'\n")
	if len(findings) != 1 {
		t.Fatalf("len = %d, want 1", len(findings))
	}
	if findings[0].Title != "Quoted title" {
		t.Errorf("title = %q, want unquoted", findings[0].Title)
	}
	if findings[0].Severity != "high" {
		t.Errorf("severity = %q", findings[0].Severity)
	}
}

func TestBlockScalarDescription(t *testing.T) {
	// `description: |` puts the value on the following lines, which is how a
	// model writes anything longer than a sentence.
	output := "title: Has a block\nseverity: medium\ndescription: |\n  First line.\n  Second line.\n"
	findings := ParseFindings(output)
	if len(findings) != 1 {
		t.Fatalf("len = %d, want 1", len(findings))
	}
	if !strings.Contains(findings[0].Description, "Second line") {
		t.Errorf("description = %q, lost the block", findings[0].Description)
	}
}

func TestCategoryDefaultsRatherThanEmpty(t *testing.T) {
	// The control plane requires a category; an omitted one should not make
	// the whole report fail validation.
	findings := ParseFindings("title: No category\nseverity: low\n")
	if findings[0].Category == "" {
		t.Error("category was left empty")
	}
}

func TestNonNumericLineIsIgnored(t *testing.T) {
	findings := ParseFindings("title: Bad line\nseverity: low\nline: somewhere\n")
	if findings[0].Line != 0 {
		t.Errorf("line = %d, want 0", findings[0].Line)
	}
}

// A review that found nothing must encode as `[]`, not `null`: the control
// plane rejects a null list, and finding nothing is the common case.
func TestEmptyResultIsAnEmptySliceNotNil(t *testing.T) {
	findings := ParseFindings("I reviewed it and found nothing.")
	if findings == nil {
		t.Fatal("parseFindings returned nil; it must return an empty slice")
	}
	encoded, err := json.Marshal(map[string]any{"findings": findings})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "null") {
		t.Errorf("encoded as %s, which the control plane rejects", encoded)
	}
}
