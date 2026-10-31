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

func TestVerdictsOnEarlierFindingsAreReadAndNotTakenForFindings(t *testing.T) {
	reply := "Checked the fix.\n\n```yaml\nverdicts:\n  F1: fixed\n  F2: still\n  F3: \"FIXED\"\n```\n---\n" +
		"```yaml\nseverity: low\ncategory: correctness\ntitle: A new one\n```\n"
	v := ParseVerdicts(reply)
	if len(v) != 3 || !v[0] || v[1] || !v[2] {
		t.Errorf("verdicts = %v", v)
	}
	if f := ParseFindings(reply); len(f) != 1 || f[0].Title != "A new one" {
		t.Errorf("findings = %+v", f)
	}
}

func TestNoVerdictsIsNoJudgement(t *testing.T) {
	if v := ParseVerdicts("```yaml\nseverity: low\ntitle: x\n```"); len(v) != 0 {
		t.Errorf("verdicts = %v", v)
	}
}

// A re-review's whole reply: findings, verdicts on the earlier ones, and
// the summary the review ends with. The summary is no finding, even when an
// indented line of it reads like a field; a finding it was run into stays.
func TestAReviewSummaryIsNotAFinding(t *testing.T) {
	reply := "Re-reviewed after the fix.\n\n```yaml\n" +
		"severity: high\n" +
		"category: tests\n" +
		"file: src/export.test.ts\n" +
		"line: 12\n" +
		"title: The regression test passes without the fix\n" +
		"description: Traced: the fixture has one row, so the off-by-one never shows.\n" +
		"---\n" +
		"verdicts:\n  F1: fixed\n" +
		"---\n" +
		"review_summary:\n" +
		"  read: src/export.ts and its callers in src/routes/invoices.ts\n" +
		"  ran: bun test src/export.test.ts, 4 passed\n" +
		"  title: none of the above was skipped\n" +
		"  not_checked: the S3 upload; no credentials here\n" +
		"```\n"
	findings := ParseFindings(reply)
	if len(findings) != 1 || findings[0].Title != "The regression test passes without the fix" {
		t.Fatalf("findings = %+v, want only the one finding", findings)
	}
	if v := ParseVerdicts(reply); len(v) != 1 || !v[0] {
		t.Errorf("verdicts = %v", v)
	}

	joined := "```yaml\nseverity: high\ntitle: Kept\nreview_summary:\n  ran: go test ./..., ok\n```"
	if got := ParseFindings(joined); len(got) != 1 || got[0].Title != "Kept" || got[0].Description != "" {
		t.Errorf("a finding run into the summary: %+v", got)
	}
}

// The reviewer's contract puts the evidence word on the description's own
// line; a wrapped description continues on indented lines.
func TestAnEvidencePrefixedDescriptionParsesWhole(t *testing.T) {
	reply := "```yaml\n" +
		"severity: high\n" +
		"category: correctness\n" +
		"file: path/to/file.go\n" +
		"line: 12\n" +
		"title: The total is summed after rounding\n" +
		"description: Traced: path/to/file.go:12 rounds each amount before summing,\n" +
		"  total.go:40 then adds the rounded rows, so the total differs by a cent.\n" +
		"suggested_fix: Round once, after summing.\n" +
		"```\n"
	got := ParseFindings(reply)
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want one", got)
	}
	want := "Traced: path/to/file.go:12 rounds each amount before summing, total.go:40 then adds the rounded rows, so the total differs by a cent."
	if got[0].Description != want || got[0].SuggestedFix != "Round once, after summing." {
		t.Errorf("finding = %+v, want description %q", got[0], want)
	}
}

func TestAcceptanceIsRead(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"plain", "title: T\nseverity: high\nacceptance: An empty month exports a header row only.\n", "An empty month exports a header row only."},
		{"alias", "title: T\nseverity: high\nacceptance_criterion: Totals match to the cent.\n", "Totals match to the cent."},
		{"wrapped", "title: T\nseverity: high\nacceptance: Two concurrent creates\n  get distinct attempt numbers.\nsuggested_fix: x\n",
			"Two concurrent creates get distinct attempt numbers."},
		{"block", "title: T\nseverity: high\nacceptance: |\n  First part.\n  Second part.\n", "First part. Second part."},
		{"absent", "title: T\nseverity: high\n", ""},
	}
	for _, c := range cases {
		got := ParseFindings(c.doc)
		if len(got) != 1 {
			t.Fatalf("%s: findings = %+v, want one", c.name, got)
		}
		if got[0].Acceptance != c.want {
			t.Errorf("%s: acceptance = %q, want %q", c.name, got[0].Acceptance, c.want)
		}
	}
}

func TestCausedByFixIsYesOrTrueOnly(t *testing.T) {
	for value, want := range map[string]bool{"yes": true, "Yes": true, `"true"`: true, "no": false, "maybe": false, "": false} {
		doc := "title: T\nseverity: high\n"
		if value != "" {
			doc += "caused_by_fix: " + value + "\n"
		}
		got := ParseFindings(doc)
		if len(got) != 1 || got[0].CausedByFix != want {
			t.Errorf("caused_by_fix: %s → %+v, want %v", value, got, want)
		}
	}
}

// A model writing the prompt's "Done when:" label inside a document has a
// space in the key: the line continues the open field, it is no criterion.
func TestADoneWhenLineStaysInTheDescription(t *testing.T) {
	got := ParseFindings("title: T\nseverity: high\ndescription: Traced: it rounds per line.\nDone when: x\n")
	if len(got) != 1 || got[0].Description != "Traced: it rounds per line. Done when: x" || got[0].Acceptance != "" {
		t.Errorf("finding = %+v", got)
	}
}

// A question document is no finding, even one that also carries a title and
// a blocking severity: it goes to a person, never a fixer.
func TestAQuestionIsNotAFinding(t *testing.T) {
	reply := "```yaml\nseverity: high\ntitle: Kept\n```\n---\n" +
		"```yaml\nquestion: Is the export meant to include voided invoices?\ntitle: Voided invoices\nseverity: blocking\n```\n"
	if got := ParseFindings(reply); len(got) != 1 || got[0].Title != "Kept" {
		t.Errorf("findings = %+v, want only Kept", got)
	}
	if got := ParseQuestions(reply); len(got) != 1 || got[0].Question != "Is the export meant to include voided invoices?" {
		t.Errorf("questions = %+v", got)
	}
}

func TestQuestionFieldsAreRead(t *testing.T) {
	reply := "Two things I could not establish.\n\n```yaml\n" +
		"question: Which time zone do invoices close in?\n" +
		"why_it_matters: F2 is blocking only if months close in UTC.\n" +
		"evidence_checked: Read config/billing.ts and the\n" +
		"  migrations; neither names a zone.\n" +
		"needed_from: product\n" +
		"about: F2\n" +
		"---\n" +
		"question: |\n  Is a retry expected\n  after a timeout?\n" +
		"about: the rounding one\n" +
		"---\n" +
		"question: Are refunds in scope?\nabout: \"f3 (the refund path)\"\n" +
		"---\n" +
		"question: \"\"\ntitle: Dropped\n" +
		"---\n" +
		"verdicts:\n  F1: fixed\n" +
		"```\n"
	got := ParseQuestions(reply)
	want := []ReviewQuestion{
		{Question: "Which time zone do invoices close in?", Why: "F2 is blocking only if months close in UTC.",
			Evidence: "Read config/billing.ts and the migrations; neither names a zone.", NeededFrom: "product", About: 2},
		{Question: "Is a retry expected after a timeout?"},
		{Question: "Are refunds in scope?", About: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("questions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("question %d = %+v, want %+v", i+1, got[i], want[i])
		}
	}
	// The empty question's document is still a finding by its title, as
	// any titled document without a question is.
	if f := ParseFindings(reply); len(f) != 1 || f[0].Title != "Dropped" {
		t.Errorf("findings = %+v", f)
	}
	if v := ParseVerdicts(reply); len(v) != 1 || !v[0] {
		t.Errorf("verdicts = %v", v)
	}
}

func TestAnUnreadableAboutIsNone(t *testing.T) {
	for _, about := range []string{"", "about: F0\n", "about: F\n", "about: 2\n", "about: XF2\n"} {
		got := ParseQuestions("question: Q?\n" + about)
		if len(got) != 1 || got[0].About != 0 {
			t.Errorf("%q: questions = %+v, want one about none", about, got)
		}
	}
	if got := ParseQuestions("I have no questions."); len(got) != 0 {
		t.Errorf("questions = %+v, want none", got)
	}
}

// A finding's evidence is read field by field: trivial and introduced in
// three states, an improvised evidence kind as none, repair as the
// suggested fix when there is none.
func TestEvidenceFieldsAreRead(t *testing.T) {
	got := ParseFindings("severity: medium\ntitle: T\nfile: a.go\nline: 3\ntrivial: yes\nintroduced: no\n" +
		"scenario: POST /x with an empty body returns 200.\nevidence_kind: failing_test\nevidence: |\n  TestX fails:\n  want 400, got 200\nrepair: Check the body.\n" +
		"---\nseverity: low\ntitle: U\nevidence_kind: hunch\ntrivial: perhaps\n")
	if len(got) != 2 {
		t.Fatalf("%d findings", len(got))
	}
	f := got[0]
	if f.Trivial == nil || !*f.Trivial || f.Introduced == nil || *f.Introduced || f.EvidenceKind != "failing_test" ||
		f.Evidence != "TestX fails: want 400, got 200" || f.Scenario != "POST /x with an empty body returns 200." ||
		f.SuggestedFix != "Check the body." || f.Unverified {
		t.Errorf("%+v", f)
	}
	if u := got[1]; u.Trivial != nil || u.Introduced != nil || u.EvidenceKind != "" || u.Unverified {
		t.Errorf("%+v", u)
	}
}

// Presence only, and only at medium or above: a finding missing any of
// file, line, scenario, evidence_kind or evidence is unverified; its
// severity stands. A low or a note without them is not.
func TestUnverifiedIsAMediumOrAboveWithoutItsEvidence(t *testing.T) {
	full := map[string]string{"file": "file: a.go\n", "line": "line: 3\n", "scenario": "scenario: S.\n",
		"evidence_kind": "evidence_kind: trace\n", "evidence": "evidence: a.go:3 then b.go:9.\n"}
	doc := func(severity, without string) string {
		out := "severity: " + severity + "\ntitle: T\n"
		for _, k := range []string{"file", "line", "scenario", "evidence_kind", "evidence"} {
			if k != without {
				out += full[k]
			}
		}
		return out
	}
	for _, severity := range []string{"blocking", "high", "medium", "low", "note"} {
		if f := ParseFindings(doc(severity, ""))[0]; f.Unverified {
			t.Errorf("%s with all its evidence is unverified", severity)
		}
		for _, without := range []string{"file", "line", "scenario", "evidence_kind", "evidence"} {
			f := ParseFindings(doc(severity, without))[0]
			if want := severity != "low" && severity != "note"; f.Unverified != want || f.Severity != severity {
				t.Errorf("%s without %s: unverified %v at %s", severity, without, f.Unverified, f.Severity)
			}
		}
	}
	if f := ParseFindings(strings.Replace(doc("high", ""), "trace", "vibes", 1))[0]; !f.Unverified || f.EvidenceKind != "" {
		t.Errorf("an improvised evidence kind: %+v", f)
	}
}
