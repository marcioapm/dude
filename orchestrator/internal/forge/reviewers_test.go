package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// graphQLServer answers /graphql with `answer` for the query and variables
// it was sent, and records what REST was sent.
func graphQLServer(t *testing.T, answer func(query string, vars map[string]any) string) (*GitHub, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		if r.URL.Path == "/graphql" {
			vars, _ := in["variables"].(map[string]any)
			q, _ := in["query"].(string)
			_, _ = io.WriteString(w, answer(q, vars))
			return
		}
		mu.Lock()
		posted = append(posted, in)
		mu.Unlock()
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}), &posted
}

const noTeams = `{"data":{"organization":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to an Organization"}]}`

// Before any words: GitHub's suggestions, why each, without the author; no
// teams are asked for.
func TestReviewerSuggestionsForAPullRequest(t *testing.T) {
	gh, _ := graphQLServer(t, func(q string, v map[string]any) string {
		if strings.Contains(q, "teams") {
			t.Error("asked for teams without words")
		}
		if v["pr"] != true || v["suggest"] != true || v["search"] != false || v["number"] != float64(7) {
			t.Errorf("variables = %v", v)
		}
		return `{"data":{"repository":{"pullRequest":{"author":{"login":"dude-bot"},
			"suggestedReviewers":[
				{"isCommenter":false,"reviewer":{"login":"ana","name":"Ana Ribeiro","avatarUrl":"https://a/ana"}},
				{"isCommenter":true,"reviewer":{"login":"tom","name":"Tom Okafor","avatarUrl":"https://a/tom"}},
				{"isCommenter":false,"reviewer":{"login":"dude-bot","name":"","avatarUrl":""}}]}}}}`
	})
	got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 7, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != "ana" || got[0].Reason != "changed" || got[0].Name != "Ana Ribeiro" ||
		got[1].Login != "tom" || got[1].Reason != "commented" {
		t.Errorf("candidates = %+v", got)
	}
}

// With words: the repository's assignable people, then the owner's teams
// as "org/slug", the author left out; suggestions are not asked for.
func TestReviewerSearchFindsPeopleAndTeams(t *testing.T) {
	gh, _ := graphQLServer(t, func(q string, v map[string]any) string {
		if v["q"] != "an" {
			t.Errorf("variables = %v", v)
		}
		if strings.Contains(q, "teams") {
			return `{"data":{"organization":{"teams":{"nodes":[{"slug":"platform","name":"Platform","members":{"totalCount":7}}]}}}}`
		}
		if v["suggest"] != false || v["search"] != true {
			t.Errorf("variables = %v", v)
		}
		return `{"data":{"repository":{"pullRequest":{"author":{"login":"ana"}},
			"assignableUsers":{"nodes":[{"login":"ana","name":"Ana Ribeiro"},{"login":"hanna","name":"Hanna Lindqvist","avatarUrl":"https://a/h"}]}}}}`
	})
	got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 7, "an")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != "hanna" || got[0].Kind != "user" ||
		got[1].Login != "acme/platform" || got[1].Kind != "team" || got[1].Members != 7 {
		t.Errorf("candidates = %+v", got)
	}
}

// Teams GitHub will not give — a user owns the repository, the token has
// no read:org, a team hidden by SSO — are no teams; the people still come
// back. A rate limit is GitHub's to say again; another error about the
// people is a refusal.
func TestReviewerSearchErrors(t *testing.T) {
	people := `{"data":{"repository":{"pullRequest":null,"assignableUsers":{"nodes":[{"login":"cy"}]}}}}`
	for name, c := range map[string]struct {
		people, teams string
		n             int
		transient     bool
		refused       bool
	}{
		"user-owned": {people, noTeams, 1, false, false},
		"no read:org": {people, `{"errors":[{"type":"INSUFFICIENT_SCOPES","message":"requires one of the following scopes: ['read:org']"}]}`,
			1, false, false},
		"a team hidden by SSO": {people, `{"data":{"organization":{"teams":{"nodes":[null]}}},
			"errors":[{"type":"FORBIDDEN","path":["organization","teams","nodes",0,"members"],"message":"SSO"}]}`, 1, false, false},
		"rate limited":  {`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, noTeams, 0, true, false},
		"no repository": {`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve"}]}`, noTeams, 0, false, true},
	} {
		gh, _ := graphQLServer(t, func(q string, _ map[string]any) string {
			if strings.Contains(q, "teams") {
				return c.teams
			}
			return c.people
		})
		got, err := gh.ReviewerCandidates(context.Background(), "cy/dots", 0, "c")
		if len(got) != c.n || Transient(err) != c.transient || Refused(err) != c.refused {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// No pull request and no words: nothing to suggest, and GitHub is not asked.
func TestNoSuggestionsWithoutAPullRequest(t *testing.T) {
	gh, _ := graphQLServer(t, func(string, map[string]any) string {
		t.Error("asked GitHub")
		return "{}"
	})
	if got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 0, ""); err != nil || len(got) != 0 {
		t.Errorf("%+v, %v", got, err)
	}
}

// A team is asked as a team: "org/slug" goes to team_reviewers, and only
// the repository owner's — another owner's team cannot review here.
func TestRequestReviewersSplitsTeams(t *testing.T) {
	gh, posted := graphQLServer(t, func(string, map[string]any) string { return "{}" })
	if err := gh.RequestReviewers(context.Background(), "acme/api", 7, []string{"ana", "acme/platform", "other/platform"}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal((*posted)[0])
	if !strings.Contains(string(b), `"reviewers":["ana"]`) || !strings.Contains(string(b), `"team_reviewers":["platform"]`) {
		t.Errorf("posted %s", b)
	}
}
