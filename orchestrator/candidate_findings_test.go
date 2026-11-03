package orchestrator_test

import ()

// reviewFinding is a blocking correctness finding on FACTORY.md, in the
// scripted reviewer's format, titled title.
func reviewFinding(title string) string {
	return "```yaml\n---\nseverity: blocking\ncategory: correctness\nfile: FACTORY.md\nline: 1\n" +
		"title: " + title + "\ndescription: \"Traced: FACTORY.md:1.\"\nsuggested_fix: Fix it.\n```\n"
}
