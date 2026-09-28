package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Who may wake a fixer: on a public repository a stranger's comment must
// not spend the organization's money, and on a private one a teammate's
// must not be ignored.
func TestOnlyThoseTheOrganizationTrustsWakeAFixer(t *testing.T) {
	for _, c := range []struct {
		who, permission string
		member, want    bool
	}{
		{WakeCollaborators, "write", false, true},
		{WakeCollaborators, "maintain", false, true},
		{WakeCollaborators, "admin", false, true},
		{WakeCollaborators, "triage", true, false},
		{WakeCollaborators, "read", true, false},
		{WakeCollaborators, "none", false, false},
		{WakeMembers, "none", true, true},
		{WakeMembers, "write", false, true},
		{WakeMembers, "read", false, false},
		{WakeAnyone, "none", false, true},
	} {
		if got := MayWake(c.who, c.permission, c.member); got != c.want {
			t.Errorf("MayWake(%s, %s, member=%v) = %v, want %v", c.who, c.permission, c.member, got, c.want)
		}
	}
}

func TestSettingsFallBackToDefaultsForWhatTheyDoNotKnow(t *testing.T) {
	s := ReadSettings([]byte(`{"whoCanWake":"everyone","mergeMethod":"rebase","fixRoundsPerPr":-3,"whenBehind":"tell"}`))
	if s.WhoCanWake != WakeCollaborators || s.MergeMethod != "rebase" || s.FixRoundsPerPR != 5 || s.WhenBehind != "tell" {
		t.Errorf("settings = %+v", s)
	}
	if d := ReadSettings(nil); d.MergeMethod != "squash" || d.CIStuck().Minutes() != 60 {
		t.Errorf("defaults = %+v", d)
	}
}

// GitHub computes mergeability lazily: null right after a push is not
// knowing, not clean.
func TestMergeableReadsGitHubsTwoFieldsAndTheDistance(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		ok     *bool
		state  string
		behind int
		want   string
	}{
		{nil, "unknown", 0, MergeUnknown},
		{&yes, "clean", 0, MergeClean},
		{&yes, "behind", 0, MergeBehind},
		{&yes, "clean", 3, MergeBehind},
		{&no, "dirty", 3, MergeConflicting},
		{nil, "dirty", 0, MergeConflicting},
		{&yes, "blocked", 0, MergeClean},
	} {
		if got := mergeable(c.ok, c.state, c.behind); got != c.want {
			t.Errorf("mergeable(%v, %s, %d) = %s, want %s", c.ok, c.state, c.behind, got, c.want)
		}
	}
}

// A question after an approval is still an approval; asked again, a
// reviewer is listed as requested.
func TestEachReviewersLatestVerdict(t *testing.T) {
	user := func(l string) *struct {
		Login string `json:"login"`
	} {
		return &struct {
			Login string `json:"login"`
		}{l}
	}
	at := "2026-09-28T10:00:00Z"
	got := latestReviews([]ghReview{
		{User: user("cy"), State: "CHANGES_REQUESTED", SubmittedAt: &at},
		{User: user("ana"), State: "APPROVED", SubmittedAt: &at},
		{User: user("ana"), State: "COMMENTED", SubmittedAt: &at},
		{User: user("cy"), State: "APPROVED", SubmittedAt: &at},
		{User: user("gus"), State: "COMMENTED", SubmittedAt: &at},
	}, []struct {
		Login string `json:"login"`
	}{{"bo"}})
	var words []string
	for _, r := range got {
		words = append(words, r.Login+":"+r.State)
	}
	if strings.Join(words, " ") != "cy:APPROVED ana:APPROVED gus:COMMENTED bo:REQUESTED" {
		t.Errorf("reviews = %v", words)
	}
}

func TestAConflictStopsForAPersonAndReadinessNeedsEverything(t *testing.T) {
	cur := open
	cur.Mergeable = MergeConflicting
	if s := Classify(prior, cur, nil, nil); s == nil || s.Kind != "conflict" {
		t.Errorf("conflict: %+v", s)
	}
	// With feedback, the fix and the conflict both.
	if s := Classify(prior, cur, []Feedback{comment("Please rename it")}, nil); s == nil || s.Kind != "actionable" || !s.Conflict {
		t.Errorf("conflict with feedback: %+v", s)
	}
	ready := Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksPassing, Review: ReviewApproved, Mergeable: MergeClean}
	if !Ready(ready) {
		t.Error("approved, green and clean is ready")
	}
	for name, change := range map[string]func(*Status){
		"a thread unresolved": func(s *Status) { s.UnresolvedThreads = 1 },
		"conflicting":         func(s *Status) { s.Mergeable = MergeConflicting },
		"checks running":      func(s *Status) { s.Checks = ChecksPending },
	} {
		s := ready
		change(&s)
		if Ready(s) {
			t.Errorf("ready while %s", name)
		}
	}
	behind := ready
	behind.Mergeable, behind.BehindBy = MergeBehind, 3
	if !Ready(behind) {
		t.Error("behind but clean is still mergeable")
	}
}

// A failing check names itself, and the fixer learns which.
func TestFailingChecksAreNamedForTheFixer(t *testing.T) {
	cur := open
	cur.Checks = ChecksFailing
	cur.CheckList = []Check{
		{Name: "unit", Status: "completed", Conclusion: "success"},
		{Name: "e2e (chrome)", Status: "completed", Conclusion: "failure", URL: "https://ci/1", RunID: 9},
	}
	s := Classify(prior, cur, nil, nil)
	if s == nil || len(s.Feedback) != 1 || len(s.Feedback[0].Checks) != 1 || s.Feedback[0].Checks[0].Name != "e2e (chrome)" ||
		s.Feedback[0].Checks[0].RunID != 9 {
		t.Fatalf("signal = %+v", s)
	}
}

// Checks by name from both ways CI reports, reviews and comments from every
// page, an approving review's words kept, and the threads GraphQL counts.
func TestAPullRequestIsReadAsAPersonSeesIt(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch r.URL.Path {
		case "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"},"base":{"ref":"main"},"mergeable":true,"mergeable_state":"behind"}`)
		case "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, `{"state":"success","total_count":1,"statuses":[{"context":"lint","state":"success","target_url":"https://ci/lint",
				"created_at":"2026-09-28T10:00:00Z","updated_at":"2026-09-28T10:00:21Z"}]}`)
		case "/repos/acme/api/commits/abc/check-runs":
			fmt.Fprint(w, `{"total_count":1,"check_runs":[{"id":5,"name":"e2e","status":"completed","conclusion":"failure",
				"html_url":"https://gh/runs/5","started_at":"2026-09-28T10:00:00Z","completed_at":"2026-09-28T10:04:12Z"}]}`)
		case "/repos/acme/api/pulls/1/reviews":
			// A full first page, then the rest: every page is read.
			if page == "1" {
				var items []string
				for i := range 100 {
					items = append(items, fmt.Sprintf(`{"id":%d,"state":"COMMENTED","body":"","user":{"login":"bot%d"},"submitted_at":"2026-09-28T09:00:00Z"}`, i, i))
				}
				fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
				return
			}
			fmt.Fprint(w, `[{"id":500,"state":"APPROVED","body":"Approved, but rename foo","user":{"login":"cy"},"submitted_at":"2026-09-28T11:00:00Z"}]`)
		case "/repos/acme/api/compare/main...abc":
			fmt.Fprint(w, `{"behind_by":3,"files":[]}`)
		case "/graphql":
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":false},{"isResolved":true},{"isResolved":false}],"pageInfo":{"hasNextPage":false}}}}}}`)
		case "/repos/acme/api/issues/1/comments", "/repos/acme/api/pulls/1/comments":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL})
	st, err := gh.PullRequest(context.Background(), "acme/api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Checks != ChecksFailing || len(st.CheckList) != 2 || st.CheckList[0].Name != "lint" || st.CheckList[0].DurationMs != 21000 ||
		st.CheckList[1].URL != "https://gh/runs/5" || st.CheckList[1].DurationMs != 252000 {
		t.Errorf("checks = %s %+v", st.Checks, st.CheckList)
	}
	if st.Review != ReviewApproved || len(st.Reviews) != 101 || st.Reviews[100].Login != "cy" {
		t.Errorf("review = %s, %d reviewers", st.Review, len(st.Reviews))
	}
	if st.Mergeable != MergeBehind || st.BehindBy != 3 || st.UnresolvedThreads != 2 {
		t.Errorf("mergeable = %s behind %d, threads %d", st.Mergeable, st.BehindBy, st.UnresolvedThreads)
	}
	fb, err := gh.Feedback(context.Background(), "acme/api", st, "")
	if err != nil || len(fb) != 1 || fb[0].Kind != KindReview || fb[0].Body != "Approved, but rename foo" {
		t.Errorf("feedback = %+v, %v", fb, err)
	}
	// An approving review's words are a request when they ask for one.
	if !IsActionableComment(fb[0], nil) {
		t.Error("an approval asking for a rename was not a request")
	}
}

func TestACheckLogKeepsItsEnd(t *testing.T) {
	long := strings.Repeat("noise\n", 1000) + "FAIL TestGreeting"
	if got := truncate(long, 100); !strings.HasSuffix(got, "FAIL TestGreeting") || len(got) > 110 || !strings.HasPrefix(got, "…") {
		t.Errorf("truncated = %q", got)
	}
}

func TestGraphQLIsBesideTheRESTRoot(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.github.com":      "https://api.github.com/graphql",
		"https://ghe.acme.dev/api/v3": "https://ghe.acme.dev/api/graphql",
	} {
		if got := NewGitHub(Credential{APIBaseURL: base}).graphqlURL(); got != want {
			t.Errorf("%s: %s, want %s", base, got, want)
		}
	}
}

// GraphQL answers most failures with 200 and "errors": a rate limit is a
// sync to try again; anything else is threads unknown — never zero, which
// would let a thread nobody read count as resolved.
func TestGraphQLErrorsAreNotZeroThreads(t *testing.T) {
	for name, c := range map[string]struct {
		body              string
		unknown, retrying bool
	}{
		"rate limited":  {`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, false, true},
		"no repository": {`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve"}]}`, true, false},
		"no data":       {`{"errors":[{"message":"Something went wrong"}]}`, true, false},
		"refused":       {`403`, true, false},
		"two open":      {`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":false},{"isResolved":false}],"pageInfo":{"hasNextPage":false}}}}}}`, false, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.body == "403" {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"message":"Resource not accessible by personal access token"}`)
				return
			}
			fmt.Fprint(w, c.body)
		}))
		n, unknown, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).unresolvedThreads(context.Background(), "acme/api", 1)
		srv.Close()
		if c.retrying != (err != nil && Transient(err)) || unknown != c.unknown || !c.retrying && err != nil {
			t.Errorf("%s: n=%d unknown=%v err=%v", name, n, unknown, err)
		}
		if name == "two open" && n != 2 {
			t.Errorf("%s: %d threads", name, n)
		}
	}
}

// What keeps a pull request from merging, as a person is told it.
func TestBlockersNameWhatStopsAMerge(t *testing.T) {
	ready := Status{PullRequestRef: PullRequestRef{State: StateOpen}, Checks: ChecksUnknown, Review: ReviewApproved, Mergeable: MergeBehind}
	if b := Blockers(ready); len(b) != 0 {
		t.Errorf("approved, no CI, behind but clean: blocked by %v", b)
	}
	s := Status{Checks: ChecksFailing, Review: ReviewChangesRequested, Mergeable: MergeConflicting, UnresolvedThreads: 2}
	if b := Blockers(s); len(b) != 4 || b[0] != "checks are failing" || b[3] != "2 review thread(s) unresolved" {
		t.Errorf("blockers = %v", b)
	}
	if b := Blockers(Status{Checks: ChecksPending}); len(b) != 2 || b[1] != "nobody has approved it" {
		t.Errorf("blockers = %v", b)
	}
	draft := ready
	draft.State = StateDraft
	if b := Blockers(draft); len(b) != 1 || b[0] != "it is a draft" {
		t.Errorf("a draft: %v", b)
	}
}

// A submitted review, or a line comment filed under one, is a new round;
// a conversation comment or CI is not.
func TestOnlyAReviewStartsANewRound(t *testing.T) {
	for kind, want := range map[string]bool{KindReview: true, KindChangesRequested: true, KindLineComment: true, KindComment: false, "": false} {
		if got := (ActionableFeedback{Kind: kind}).IsReview(); got != want {
			t.Errorf("%q: %v", kind, got)
		}
	}
	s := Classify(prior, open, []Feedback{{ID: "r", Author: "cy", Body: "Please rename it", Kind: KindChangesRequested}}, nil)
	if s == nil || !s.Feedback[0].IsReview() {
		t.Errorf("a review's feedback lost its kind: %+v", s)
	}
}

// Saving settings is strict where reading them is forgiving: a person
// told "saved" must have saved what they chose.
func TestSavingSettingsRefusesWhatDudeDoesNotKnow(t *testing.T) {
	if s, err := ParseSettings([]byte(`{"mergeMethod":"rebase","fixRoundsPerPr":0,"whoCanWake":"members"}`)); err != nil ||
		s.MergeMethod != "rebase" || s.FixRoundsPerPR != 0 || s.WhoCanWake != WakeMembers || s.OpenAs != "ready" {
		t.Errorf("settings = %+v, %v", s, err)
	}
	for _, bad := range []string{`{"mergeMethod":"octopus"}`, `{"whoCanWake":"everyone"}`, `{"fixRoundsPerPr":51}`,
		`{"fixRoundsPerPr":"five"}`, `{"ciStuckMinutes":-1}`, `{"ciStuckMinutes":0}`} {
		if _, err := ParseSettings([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if keys := SettingKeys(); len(keys) != 8 || keys[0] != "whoCanWake" {
		t.Errorf("keys = %v", keys)
	}
}
