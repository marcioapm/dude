// Parsing a reviewer's findings out of its output.
//
// A reviewer is asked for YAML documents separated by `---`, which is a
// format a model produces reliably and a machine can read without a model in
// the loop. The alternative — a second model call to extract structure from
// the first one's prose — costs tokens to recover information the first call
// already had.
//
// Deliberately forgiving about what surrounds the documents. Models wrap
// output in fences, prefix it with a sentence, and occasionally emit a
// document with a field missing. A finding that cannot be read is dropped
// with a warning rather than failing the review: losing one finding is
// better than losing all of them.

package delivery

import (
	"regexp"
	"strconv"
	"strings"
)

// Severities the control plane accepts. Anything else is a reviewer
// improvising, and is recorded at the lowest severity rather than rejected.
var validSeverities = map[string]bool{
	"blocking": true, "high": true, "medium": true, "low": true, "note": true,
}

// Fenced blocks, so ```yaml … ``` around a document does not become part of
// a field's value.
var fenceLine = regexp.MustCompile("(?m)^\\s*```[a-zA-Z]*\\s*$")

/*
ParseFindings reads every YAML-ish document in a reviewer's reply.

Not a YAML library: the format is a flat map of scalars with one multi-line
case (`description`), the input is a model's output rather than a config
file, and a parser that accepts exactly what we asked for is easier to reason
about than one that accepts all of YAML and then has to reject most of it.
*/
func ParseFindings(output string) []Finding {
	cleaned := fenceLine.ReplaceAllString(output, "")

	// Non-nil, so a review that found nothing encodes as `[]` rather than
	// `null`. A nil slice would be rejected by the control plane, which is
	// the common case — most reviews find nothing.
	findings := []Finding{}
	for _, doc := range strings.Split(cleaned, "\n---") {
		if isVerdicts(doc) {
			continue
		}
		if f, ok := parseFinding(doc); ok {
			findings = append(findings, f)
		}
	}
	return findings
}

func parseFinding(doc string) (Finding, bool) {
	fields := map[string]string{}
	// The field a continuation line belongs to, and what has accumulated
	// under it. Any field can continue: a model wrapping a long description
	// indents the rest of it, and a `|` block scalar does the same.
	var currentKey string
	var continued []string

	flush := func() {
		if currentKey != "" && len(continued) > 0 {
			joined := strings.TrimSpace(strings.Join(continued, " "))
			if existing := fields[currentKey]; existing != "" {
				fields[currentKey] = existing + " " + joined
			} else {
				fields[currentKey] = joined
			}
		}
		currentKey, continued = "", nil
	}

	for _, raw := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, value, found := strings.Cut(trimmed, ":")
		key = strings.TrimSpace(strings.ToLower(key))

		/*
		 * A continuation rather than a new field when the line is indented,
		 * or has no colon, or has something before the colon that cannot be
		 * a key. The indent test matters most: a wrapped description whose
		 * second line happens to contain a colon would otherwise be read as
		 * a new field and silently truncate the description.
		 */
		indented := raw != trimmed
		if indented || !found || strings.ContainsAny(key, " \t") {
			if currentKey != "" {
				continued = append(continued, trimmed)
			}
			continue
		}

		flush()
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)

		if value == "" || value == "|" || value == ">" {
			// A block scalar: the value is on the following lines.
			currentKey = key
			continue
		}
		fields[key] = value
		// Stays open, so an unindented wrap of this field still lands here.
		currentKey = key
	}
	flush()

	// A finding with no title says nothing actionable, whatever else it has.
	title := fields["title"]
	if title == "" {
		return Finding{}, false
	}

	severity := strings.ToLower(fields["severity"])
	if !validSeverities[severity] {
		// An improvised severity is recorded rather than dropped, at the
		// level that cannot block: a reviewer inventing "critical" should
		// not silently gain the power to stop a work item.
		severity = "note"
	}

	line := 0
	if n, err := strconv.Atoi(fields["line"]); err == nil && n > 0 {
		line = n
	}

	return Finding{
		Severity:     severity,
		Category:     valueOr(fields["category"], "correctness"),
		Title:        title,
		Description:  fields["description"],
		SuggestedFix: valueOr(fields["suggested_fix"], fields["suggestedfix"]),
		Repo:         fields["repo"],
		File:         fields["file"],
		Line:         line,
	}, true
}

func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// Finding is one problem a reviewer reported.
// verdictLine is one answer about an earlier finding: `  F2: fixed`.
var verdictLine = regexp.MustCompile(`(?mi)^\s*F(\d+)\s*:\s*"?(fixed|still)"?\s*$`)

func isVerdicts(doc string) bool {
	return regexp.MustCompile(`(?m)^\s*verdicts\s*:\s*$`).MatchString(doc)
}

/*
ParseVerdicts reads a re-review's judgement of the earlier findings it was
shown, numbered from 1 in the order it was shown them:

	verdicts:
	  F1: fixed
	  F2: still

Returns index → fixed. A finding the reviewer said nothing about is absent,
and stays as it was: silence is not a verdict.
*/
func ParseVerdicts(output string) map[int]bool {
	verdicts := map[int]bool{}
	for _, doc := range strings.Split(fenceLine.ReplaceAllString(output, ""), "\n---") {
		if !isVerdicts(doc) {
			continue
		}
		for _, m := range verdictLine.FindAllStringSubmatch(doc, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				verdicts[n-1] = strings.EqualFold(m[2], "fixed")
			}
		}
	}
	return verdicts
}

type Finding struct {
	Severity     string `json:"severity"`
	Category     string `json:"category"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	SuggestedFix string `json:"suggestedFix"`
	Repo         string `json:"repo"`
	File         string `json:"file"`
	Line         int    `json:"line"`
}
