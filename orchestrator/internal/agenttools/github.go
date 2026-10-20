package agenttools

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Read-only GitHub tools (capabilities.GitHubRead), the same for a
// session's agent and a task's conductor: dude makes the reads with the organisation's GitHub
// credential, which the agent never holds. Each is checked in the call's
// transaction (the repository is one the caller may read, the Run's hourly
// reads), made with no transaction open, and recorded as a github.read
// event holding what was asked and how many bytes came back, never the
// content. Everything people wrote on GitHub reaches the agent wrapped as
// external text (delivery.ExternalText), within the byte caps below.

// EvGitHubRead is one read an agent's tool made on GitHub.
const EvGitHubRead = "github.read"

// Bounds of one read. A body, comment or review's text and a file's diff
// are cut to their cap, and the whole result to resultCap; each cut says
// "…cut".
const (
	githubReadsPerHour = 60
	textCap            = 8 << 10
	diffCap            = 16 << 10
	resultCap          = 64 << 10
	searchMax          = 30
	commentsMax        = 50
	reviewsMax         = 50
	filesMax           = 300
	diffPathsMax       = 20
)

var (
	cutMark          = githubText("cut", nil)
	whoseTask        = githubText("whose-task", nil)
	whoseSession     = githubText("whose-session", nil)
	issuePullRequest = githubText("issue-pull-request", nil)
	noDiff           = githubText("no-diff", nil)
	budgetCut        = githubText("budget-cut", cutMark)
)

// githubText renders delivery/agents/results/github.md's text name.
func githubText(name string, data any) string { return githubTexts[name].Render(data) }

// githubTexts are github.md's texts, resolved at init.
var githubTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"refused", "whose-task", "whose-session", "cut", "search-more", "issue-pull-request",
		"comments-cut", "comments-newest-cut", "no-diff", "checks-cut", "reviews-cut", "threads-unknown", "files-cut", "budget-cut", "text-cut"} {
		out[name] = delivery.MustText("results/github/" + name)
	}
	return out
}()

type githubSearchIn struct {
	Repo  string `json:"repo"`
	Query string `json:"query,omitempty"`
	Kind  string `json:"kind"`
	State string `json:"state,omitempty"`
}

type githubIssueIn struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

type githubPullRequestIn struct {
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Files  []string `json:"files,omitempty"`
}

type githubCheckLogIn struct {
	Repo    string `json:"repo"`
	CheckID int64  `json:"checkId"`
}

// githubRead is a read checked in the call's transaction: what to ask
// GitHub, and how its ledger event names it.
type githubRead struct {
	Tool, Slug string
	Number     int
	CheckID    int64
	Query      string
	Kind       string
	State      string
	Files      []string
}

// readableRepos are the GitHub repositories (owner/name) the caller may
// read: a session's linked projects', or a conductor's task's project's.
func readableRepos(ctx context.Context, tx pgx.Tx, c Caller) ([]string, error) {
	var projects []string
	if c.Role == delivery.RoleBrainstorm {
		linked, err := delivery.SessionProjects(ctx, tx, c.SessionID)
		if err != nil {
			return nil, err
		}
		for _, p := range linked {
			projects = append(projects, p.ID)
		}
	} else if c.ProjectID != "" {
		projects = []string{c.ProjectID}
	}
	rows, err := tx.Query(ctx, `SELECT url FROM repositories WHERE project_id = ANY($1) ORDER BY name`, projects)
	if err != nil {
		return nil, err
	}
	urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var slugs []string
	for _, u := range urls {
		if s := forge.SlugFromURL(u); s != "" && !slices.ContainsFunc(slugs, func(o string) bool { return strings.EqualFold(o, s) }) {
			slugs = append(slugs, s)
		}
	}
	return slugs, nil
}

// A session's and a task's GitHub reads in the last hour. Both share one
// predicate, the WHERE of their partial indexes (migrations 147-150),
// written with literals, as a parameter would not prove the index's WHERE.
const (
	githubReadsInHour = ` AND event_type = 'agent.tool.attempted' AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log')
		AND occurred_at > now() - interval '1 hour'`
	githubReadsBySession = `SELECT count(*) FROM events WHERE session_id = $1` + githubReadsInHour
	githubReadsByTask    = `SELECT count(*) FROM events WHERE task_id = $1` + githubReadsInHour
)

// checkGitHubRead holds a read to the repositories the caller may read and
// to the session's hourly reads, before anything reaches GitHub.
func checkGitHubRead(ctx context.Context, tx pgx.Tx, c Caller, read githubRead) (githubRead, error) {
	gh, err := githubOf(c)
	if err != nil {
		return read, err
	}
	if gh == nil {
		return read, refuseText(rThisOrganisationHasNoGithubConnection)
	}
	want := strings.Trim(strings.TrimSpace(read.Slug), "/")
	slugs, err := readableRepos(ctx, tx, c)
	if err != nil {
		return read, err
	}
	i := slices.IndexFunc(slugs, func(s string) bool { return strings.EqualFold(s, want) })
	if i < 0 {
		whose := whoseTask
		if c.Role == delivery.RoleBrainstorm {
			whose = whoseSession
		}
		if len(slugs) == 0 {
			return read, refuse(rNotRepositoryYouMayReadHave, want, whose)
		}
		return read, refuse(rNotRepositoryYouMayReadOnly, want, whose, strings.Join(slugs, ", "))
	}
	read.Slug = slugs[i]
	// The session's (or, for a conductor with none, the task's) Runs share
	// one budget, counted one caller at a time.
	key, count, scope := "task:"+c.TaskID, githubReadsByTask, c.TaskID
	if c.SessionID != "" {
		key, count, scope = "session:"+c.SessionID, githubReadsBySession, c.SessionID
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('github_reads:' || $1))`, key); err != nil {
		return read, err
	}
	var reads int
	if err := tx.QueryRow(ctx, count, scope).Scan(&reads); err != nil {
		return read, err
	}
	if reads >= githubReadsPerHour {
		return read, refuse(rAtMostGithubReadsHourThis, githubReadsPerHour)
	}
	return read, nil
}

func searchGitHub(ctx context.Context, tx pgx.Tx, c Caller, in githubSearchIn) (githubRead, error) {
	kind, state := strings.TrimSpace(in.Kind), strings.TrimSpace(in.State)
	switch {
	case kind != "issue" && kind != "pr":
		return githubRead{}, refuseText(rKindIssueOrPr)
	case state != "" && state != "open" && state != "closed" && state != "all":
		return githubRead{}, refuseText(rStateOpenClosedOrAll)
	case len(in.Query) > 256:
		return githubRead{}, refuseText(rQueryAtMost256Characters)
	}
	for _, term := range strings.Fields(strings.ToLower(in.Query)) {
		for _, q := range []string{"repo:", "org:", "user:", "owner:"} {
			if strings.HasPrefix(strings.TrimPrefix(term, "-"), q) {
				return githubRead{}, refuse(rNameRepositoryInRepoNotWith, q)
			}
		}
	}
	return checkGitHubRead(ctx, tx, c, githubRead{Tool: "github_search", Slug: in.Repo, Query: strings.TrimSpace(in.Query),
		Kind: kind, State: state})
}

func readGitHubIssue(ctx context.Context, tx pgx.Tx, c Caller, in githubIssueIn) (githubRead, error) {
	if in.Number <= 0 {
		return githubRead{}, refuseText(rNumberIssueFrom1)
	}
	return checkGitHubRead(ctx, tx, c, githubRead{Tool: "github_issue", Slug: in.Repo, Number: in.Number})
}

func readGitHubPullRequest(ctx context.Context, tx pgx.Tx, c Caller, in githubPullRequestIn) (githubRead, error) {
	if in.Number <= 0 {
		return githubRead{}, refuseText(rNumberPullRequestFrom1)
	}
	files := uniquePaths(in.Files)
	for i, f := range files {
		files[i] = strings.TrimSpace(delivery.Unquoted(f))
	}
	if len(files) > diffPathsMax {
		return githubRead{}, refuse(rFilesAtMostPathsAtOnce, diffPathsMax)
	}
	return checkGitHubRead(ctx, tx, c, githubRead{Tool: "github_pull_request", Slug: in.Repo, Number: in.Number, Files: files})
}

func readGitHubCheckLog(ctx context.Context, tx pgx.Tx, c Caller, in githubCheckLogIn) (githubRead, error) {
	if in.CheckID <= 0 {
		return githubRead{}, refuse(rCheckIdCheckRunFrom1)
	}
	return checkGitHubRead(ctx, tx, c, githubRead{Tool: "github_check_log", Slug: in.Repo, CheckID: in.CheckID})
}

// readOnGitHub makes the read. GitHub refusing it, or its rate limit, is
// said to the agent as a refusal.
func readOnGitHub(ctx context.Context, c Caller, read githubRead) (json.RawMessage, error) {
	gh, err := githubOf(c)
	if err != nil {
		return nil, err
	}
	w := newWrapper()
	var out map[string]any
	switch read.Tool {
	case "github_search":
		state := read.State
		if state == "all" {
			state = ""
		}
		lines, total, err := gh.SearchIssues(ctx, read.Slug, read.Query, read.Kind, state, searchMax)
		if err != nil {
			return nil, githubRefusal(read, err)
		}
		out = w.search(lines, total)
	case "github_issue":
		issue, err := gh.Issue(ctx, read.Slug, read.Number, commentsMax)
		if err != nil {
			return nil, githubRefusal(read, err)
		}
		out = w.issue(issue)
	case "github_pull_request":
		pr, err := gh.PullRequestRead(ctx, read.Slug, read.Number, filesMax, commentsMax, read.Files...)
		if err != nil {
			return nil, githubRefusal(read, err)
		}
		out = w.pullRequest(pr, read.Files)
	case "github_check_log":
		log, err := gh.CheckLog(ctx, read.Slug, read.CheckID, (16<<10)-len("…\n"))
		if err != nil {
			return nil, githubRefusal(read, err)
		}
		out = w.checkLog(log)
	default:
		return nil, fmt.Errorf("no GitHub read %s", read.Tool)
	}
	return w.fit(out)
}

// githubRefusal says why GitHub did not answer a read, for the agent, and
// records it as a github.read event naming the refusal's kind and HTTP
// status. Both are in dude's words only: forge.Error.Message may hold
// GitHub's, and reaches neither the agent nor the ledger.
func githubRefusal(read githubRead, err error) error {
	what := read.Slug
	if read.Number > 0 {
		what = fmt.Sprintf("%s#%d", read.Slug, read.Number)
	}
	e := forgeError(err)
	var u *forge.Unreachable
	if e == nil && !errors.As(err, &u) {
		return err
	}
	status := 0
	if e != nil {
		status = e.Status
	}
	wait := ""
	if d := forge.RetryAfter(err); d > 0 {
		// Never "in 0s": a wait under a minute reads as one.
		wait = max(d, time.Minute).Round(time.Minute).String()
	}
	kind := refusalKind(err)
	var lacks [][]string
	if kind == "forbidden" {
		lacks = permissionSets(e.AcceptedPermissions)
	}
	msg := githubText("refused", struct {
		Kind, Slug, What, Wait string
		Status                 int
		Lacks                  [][]string
		Issue                  bool
	}{kind, read.Slug, what, wait, status, lacks, read.Tool == "github_issue"})
	payload := readPayload(read)
	payload["refused"] = kind
	if e != nil {
		payload["status"] = e.Status
	}
	if len(lacks) > 0 {
		payload["permission"] = e.AcceptedPermissions
	}
	return refuseRecorded(EvGitHubRead, payload, msg)
}

// permissionSets is forge.Error.AcceptedPermissions as GitHub's settings
// name each permission ("pull_requests=read": "Pull requests: read"), one
// list per alternative set.
func permissionSets(accepted string) [][]string {
	var sets [][]string
	for _, set := range strings.Split(accepted, "; ") {
		var names []string
		for _, p := range strings.Split(set, ",") {
			name, access, ok := strings.Cut(p, "=")
			if !ok || name == "" {
				return nil
			}
			name = strings.ReplaceAll(name, "_", " ")
			names = append(names, strings.ToUpper(name[:1])+name[1:]+": "+access)
		}
		sets = append(sets, names)
	}
	return sets
}

// forgeError is GitHub's refusal err wraps, or nil for none (no answer).
func forgeError(err error) *forge.Error {
	var e *forge.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// refusalKind is why a read was refused, in dude's words.
func refusalKind(err error) string {
	e := forgeError(err)
	switch {
	case e == nil:
		return "unavailable"
	case e.Refusal != "":
		return e.Refusal
	case e.NotFound():
		return "not_found"
	case forge.RateLimited(err):
		return "rate_limited"
	case forge.Transient(err):
		return "unavailable"
	case e.Status == http.StatusForbidden:
		return "forbidden"
	}
	return "refused"
}

// recordGitHubRead records the read: what was asked and the result's size.
func recordGitHubRead(ctx context.Context, tx pgx.Tx, c Caller, read githubRead, result json.RawMessage) (json.RawMessage, error) {
	payload := readPayload(read)
	payload["bytes"] = len(result)
	_, err := ledger.Append(ctx, tx, c.event(EvGitHubRead, payload))
	return result, err
}

// readPayload is what a github.read event says was asked.
func readPayload(read githubRead) map[string]any {
	payload := map[string]any{"tool": read.Tool, "repo": read.Slug}
	if read.Number > 0 {
		payload["number"] = read.Number
	}
	if read.CheckID > 0 {
		payload["checkId"] = read.CheckID
	}
	if read.Tool == "github_search" {
		payload["query"], payload["kind"] = read.Query, read.Kind
		if read.State != "" {
			payload["state"] = read.State
		}
	}
	if len(read.Files) > 0 {
		payload["files"] = read.Files
	}
	return payload
}

// wrapper writes what a read returns: GitHub's text wrapped as external,
// under one id unguessable before the call, so text cannot end its own wrap.
type wrapper struct {
	id string
}

func newWrapper() wrapper {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return wrapper{id: hex.EncodeToString(b[:])}
}

// text is s wrapped as external text, cut to limit bytes first. Invalid
// UTF-8 is replaced (U+FFFD) before the cut, so the cut and its count are
// of what the agent is shown.
func (w wrapper) text(s string, limit int) string {
	if s == "" {
		return ""
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	cut := ""
	if len(s) > limit {
		cut = githubText("text-cut", struct {
			Cut         string
			Kept, Total int
		}{cutMark, len(cutBytes(s, limit)), len(s)})
		s = cutBytes(s, limit)
	}
	return delivery.ExternalText(w.id, s) + cut
}

func (w wrapper) labels(ls []string) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, w.text(l, 100))
	}
	return out
}

// cutBytes is s's first limit bytes, ending on a whole character. s is
// valid UTF-8 (text made it so), so at most UTFMax-1 bytes are given back.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for i := 0; i < utf8.UTFMax && limit > 0 && !utf8.RuneStart(s[limit]); i++ {
		limit--
	}
	return s[:limit]
}

type ghLineOut struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	State     string   `json:"state"`
	Labels    []string `json:"labels"`
	UpdatedAt string   `json:"updatedAt"`
	URL       string   `json:"url"`
}

func (w wrapper) line(l forge.IssueLine) ghLineOut {
	return ghLineOut{Number: l.Number, Title: w.text(l.Title, textCap), Author: w.text(l.Author, 100), State: l.State,
		Labels: w.labels(l.Labels), UpdatedAt: l.UpdatedAt, URL: l.URL}
}

type ghCommentOut struct {
	Author    string `json:"author"`
	CreatedAt string `json:"createdAt,omitempty"`
	Body      string `json:"body"`
}

func (w wrapper) comments(cs []forge.IssueComment) []ghCommentOut {
	out := make([]ghCommentOut, 0, len(cs))
	for _, c := range cs {
		out = append(out, ghCommentOut{Author: w.text(c.Author, 100), CreatedAt: c.CreatedAt, Body: w.text(c.Body, textCap)})
	}
	return out
}

func (w wrapper) search(lines []forge.IssueLine, total int) map[string]any {
	items := make([]ghLineOut, 0, len(lines))
	for _, l := range lines {
		items = append(items, w.line(l))
	}
	out := map[string]any{"items": items, "total": total, "external": w.note()}
	if total > len(items) {
		out["more"] = githubText("search-more", total-len(items))
	}
	return out
}

// note is what each result says of its wrap, beside the text.
func (w wrapper) note() string {
	return delivery.GitHubReadNote(w.id)
}

func (w wrapper) issue(i forge.IssueRead) map[string]any {
	out := map[string]any{"issue": w.line(i.IssueLine), "body": w.text(i.Body, textCap), "comments": w.comments(i.Comments),
		"external": w.note()}
	if i.PullRequest {
		out["pullRequest"] = issuePullRequest
	}
	if more := commentsCut(len(i.Comments), i.CommentCount); more != "" {
		out["moreComments"] = more
	}
	return out
}

// commentsCut says only shown of total comments were kept; "" for all.
func commentsCut(shown, total int) string {
	if total <= shown {
		return ""
	}
	return githubText("comments-cut", struct {
		Cut          string
		Shown, Total int
	}{cutMark, shown, total})
}

type ghReviewOut struct {
	Author      string `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submittedAt"`
	Body        string `json:"body,omitempty"`
}

type ghThreadOut struct {
	Path     string         `json:"path"`
	Line     int            `json:"line,omitempty"`
	Outdated bool           `json:"outdated,omitempty"`
	Comments []ghCommentOut `json:"comments"`
	More     string         `json:"more,omitempty"`
}

type ghCheckOut struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	ID         int64  `json:"id,omitempty"`
	Log        bool   `json:"log,omitempty"`
}

type ghFileOut struct {
	Path      string `json:"path"`
	Status    string `json:"status,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Diff      string `json:"diff,omitempty"`
}

func (w wrapper) pullRequest(p forge.PullRequestRead, paths []string) map[string]any {
	reviews := []ghReviewOut{}
	for _, r := range p.Reviews[:min(len(p.Reviews), reviewsMax)] {
		reviews = append(reviews, ghReviewOut{Author: w.text(r.Author, 100), State: r.State, SubmittedAt: r.SubmittedAt, Body: w.text(r.Body, textCap)})
	}
	threads := []ghThreadOut{}
	for _, t := range p.Threads {
		threads = append(threads, ghThreadOut{Path: w.text(t.Path, textCap), Line: t.Line, Outdated: t.Outdated,
			Comments: w.comments(t.Comments), More: commentsCut(len(t.Comments), t.CommentCount)})
	}
	checks := []ghCheckOut{}
	for _, c := range p.Checks {
		checks = append(checks, ghCheckOut{Name: w.text(c.Name, 200), Status: c.Status, Conclusion: c.Conclusion, ID: c.RunID, Log: c.App == forge.ActionsApp})
	}
	files := []ghFileOut{}
	var missing []string
	for _, path := range paths {
		if !slices.ContainsFunc(p.Files, func(f forge.FileRead) bool { return f.Path == path }) {
			missing = append(missing, w.text(path, textCap))
		}
	}
	for _, f := range p.Files {
		out := ghFileOut{Path: w.text(f.Path, textCap), Status: f.Status, Additions: f.Additions, Deletions: f.Deletions}
		if slices.Contains(paths, f.Path) {
			out.Diff = w.text(f.Patch, diffCap)
			if f.Patch == "" {
				out.Diff = noDiff
			}
		}
		files = append(files, out)
	}
	pr := map[string]any{"pullRequest": w.line(p.IssueLine), "body": w.text(p.Body, textCap), "draft": p.Draft,
		"head": w.text(p.Head, 300), "base": w.text(p.Base, 300), "reviews": reviews, "unresolvedThreads": threads,
		"checks": checks, "files": files, "comments": w.comments(p.Comments), "external": w.note()}
	if p.CommentCount > len(p.Comments) {
		pr["moreComments"] = githubText("comments-newest-cut", struct {
			Cut          string
			Shown, Total int
		}{cutMark, len(p.Comments), p.CommentCount})
	}
	if p.ChecksCut {
		pr["moreChecks"] = githubText("checks-cut", cutMark)
	}
	if p.ReviewsCut || len(p.Reviews) > reviewsMax {
		pr["moreReviews"] = githubText("reviews-cut", struct {
			Cut string
			Max int
		}{cutMark, reviewsMax})
	}
	if p.ThreadsUnknown {
		pr["threadsUnknown"] = githubText("threads-unknown", cutMark)
	}
	if len(p.Files) >= filesMax {
		pr["moreFiles"] = githubText("files-cut", struct {
			Cut string
			Max int
		}{cutMark, filesMax})
	}
	if len(missing) > 0 {
		if len(p.Files) >= filesMax {
			pr["notListed"] = missing
		} else {
			pr["notChanged"] = missing
		}
	}
	return pr
}

func (w wrapper) checkLog(log forge.CheckLogRead) map[string]any {
	return map[string]any{"source": log.Source, "actions": log.Actions, "log": w.text(log.Text, 16<<10), "external": w.note()}
}

// fit encodes a result within resultCap, its note first. Past it, one
// pass in fitRank's order gives each text what the budget has left and
// ends lists once it runs out, then the tree is encoded once more, and
// "cut" says so.
func (w wrapper) fit(v map[string]any) (json.RawMessage, error) {
	out, err := marshalResult(v)
	if err != nil || len(out) <= resultCap {
		return out, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	// Room for the cut note, separators and numbers the pass undercounts.
	remaining := resultCap - 4096
	var budget func(any) any
	budget = func(v any) any {
		switch x := v.(type) {
		case string:
			if n := jsonLen(x); n > remaining-256 {
				x = w.recut(x, max(0, remaining-512))
				if n = jsonLen(x); n > remaining-256 {
					x = w.recut(x, max(0, (remaining-512)*(remaining-512)/n))
				}
			}
			remaining -= jsonLen(x)
			return x
		case []any:
			out := make([]any, 0, len(x))
			for _, item := range x {
				if remaining < 2048 {
					break
				}
				out = append(out, budget(item))
			}
			return out
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.SortFunc(keys, func(a, b string) int {
				return cmp.Or(cmp.Compare(fitRank(a), fitRank(b)), strings.Compare(a, b))
			})
			for _, k := range keys {
				remaining -= jsonLen(k) + 2
				x[k] = budget(x[k])
			}
			return x
		default:
			remaining -= 24
			return v
		}
	}
	budget(m)
	m["cut"] = budgetCut
	return marshalResult(m)
}

var marshalResult = delivery.GitHubReadJSON

// fitRank is the order fit spends the budget in: the note saying the
// quotes are data, then small structural metadata (the item, its state,
// draft, head and base), then the flags saying what is missing, and the
// bodies and long lists last. Ties go by name.
func fitRank(key string) int {
	switch key {
	case "external":
		return 0
	case "issue", "pullRequest", "number", "title", "url", "state", "draft", "head", "base":
		return 1
	case "cut", "more", "moreComments", "moreReviews", "moreChecks", "moreFiles", "threadsUnknown", "notListed", "notChanged":
		return 2
	}
	return 3
}

// jsonLen is s's length as a JSON string, quotes included, as encoding/json
// writes it (HTML characters and U+2028/U+2029 escaped).
func jsonLen(s string) int {
	n := 2
	for _, r := range s {
		switch {
		case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
			n += 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029' || r == utf8.RuneError:
			n += 6
		default:
			n += utf8.RuneLen(r)
		}
	}
	return n
}

// recut shortens a text written by text (or a plain one) to about n bytes,
// wrapped again so it still ends its wrap.
func (w wrapper) recut(s string, n int) string {
	open, end := delivery.ExternalTextOpen(w.id), delivery.ExternalTextClose(w.id)
	inner, wrapped := strings.CutPrefix(s, open)
	if !wrapped {
		return cutBytes(s, n) + " " + cutMark
	}
	inner, _, _ = strings.Cut(inner, end)
	return w.text(inner, max(0, n-len(open)-len(end)-64))
}

var githubReadTools = []tool{
	around(define("github_search", searchGitHub).before(forgeFor).countingAttempts().withholdingResult(),
		readOnGitHub, recordGitHubRead),
	around(define("github_issue", readGitHubIssue).before(forgeFor).countingAttempts().withholdingResult(),
		readOnGitHub, recordGitHubRead),
	around(define("github_pull_request", readGitHubPullRequest).before(forgeFor).countingAttempts().withholdingResult(),
		readOnGitHub, recordGitHubRead),
	around(define("github_check_log", readGitHubCheckLog).before(forgeFor).countingAttempts().withholdingResult(),
		readOnGitHub, recordGitHubRead),
}
