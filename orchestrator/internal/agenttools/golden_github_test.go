package agenttools

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// What a refused GitHub read and a GitHub read's notes say to the agent,
// beside what it read.
func TestGoldenGitHubAndMachineWords(t *testing.T) {
	read := githubRead{Tool: "github_issue", Slug: "acme/api", Number: 7}
	slug := githubRead{Tool: "github_search", Slug: "acme/api"}
	refused := func(r githubRead, err error) string { return githubRefusal(r, err).Error() }
	var lines [][2]string
	for _, c := range []struct {
		name string
		read githubRead
		err  error
	}{
		{"moved", slug, &forge.Error{Status: 301, Refusal: forge.RefusalMoved}},
		{"moved-item", read, &forge.Error{Status: 301, Refusal: forge.RefusalMovedItem}},
		{"redirect", read, &forge.Error{Status: 302, Refusal: forge.RefusalRedirect}},
		{"too-large", read, &forge.Error{Status: 413, Refusal: forge.RefusalTooLarge}},
		{"rate-reserve", read, &forge.Error{Status: 429, Refusal: forge.RefusalRateReserve}},
		{"rate-reserve-wait", read, &forge.Error{Status: 429, Refusal: forge.RefusalRateReserve, RetryAfter: 125 * time.Second}},
		{"rate-limited", read, &forge.Error{Status: 429}},
		{"not-found", read, &forge.Error{Status: 404}},
		{"unavailable", read, &forge.Error{Status: 502}},
		{"unreachable", slug, &forge.Unreachable{}},
		{"forbidden", read, &forge.Error{Status: 403}},
		{"forbidden-pull-request", githubRead{Tool: "github_pull_request", Slug: "acme/api", Number: 7}, &forge.Error{Status: 403}},
		{"forbidden-lacks", read, &forge.Error{Status: 403, AcceptedPermissions: "issues=read"}},
		{"forbidden-lacks-either", githubRead{Tool: "github_pull_request", Slug: "acme/api", Number: 7},
			&forge.Error{Status: 403, AcceptedPermissions: "pull_requests=read,contents=read; issues=read"}},
		{"refused", read, &forge.Error{Status: 422}},
	} {
		lines = append(lines, [2]string{"github-" + c.name, refused(c.read, c.err)})
	}
	w := wrapper{id: "abc"}
	search := w.search([]forge.IssueLine{{Number: 1}}, 3)
	issue := w.issue(forge.IssueRead{IssueLine: forge.IssueLine{Number: 2, PullRequest: true}, CommentCount: 60,
		Comments: make([]forge.IssueComment, 50)})
	pr := w.pullRequest(forge.PullRequestRead{ChecksCut: true, ReviewsCut: true, ThreadsUnknown: true,
		Files: append(make([]forge.FileRead, filesMax-1), forge.FileRead{Path: "bin"}), CommentCount: 73,
		Comments: make([]forge.IssueComment, commentsMax)}, []string{"bin"})
	lines = append(lines,
		[2]string{"search-more", fmt.Sprint(search["more"])},
		[2]string{"whose-task", whoseTask},
		[2]string{"whose-session", whoseSession},
		[2]string{"budget-cut", budgetCut},
		[2]string{"issue-pull-request", fmt.Sprint(issue["pullRequest"])},
		[2]string{"issue-more-comments", fmt.Sprint(issue["moreComments"])},
		[2]string{"pr-more-checks", fmt.Sprint(pr["moreChecks"])},
		[2]string{"pr-more-reviews", fmt.Sprint(pr["moreReviews"])},
		[2]string{"pr-threads-unknown", fmt.Sprint(pr["threadsUnknown"])},
		[2]string{"pr-more-files", fmt.Sprint(pr["moreFiles"])},
		[2]string{"pr-more-comments", fmt.Sprint(pr["moreComments"])},
		[2]string{"pr-no-diff", pr["files"].([]ghFileOut)[filesMax-1].Diff},
		[2]string{"cut", cutMark},
		[2]string{"text-cut", strings.TrimPrefix(w.text("abcdefghij", 4), w.text("abcd", 4))},
	)
	githubGoldenLines(t, "results-github", lines)
}

// githubGoldenLines renders named texts as one golden, each under its name.
func githubGoldenLines(t *testing.T, name string, lines [][2]string) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("## " + l[0] + "\n\n" + l[1] + "\n\n")
	}
	golden(t, name, b.String())
}
