package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GitHub's compare answer, abridged from its REST reference: the merge base
// and the base are commit objects.
const githubCompare = `{"status": "diverged", "ahead_by": 2, "behind_by": 1, "total_commits": 2,
  "base_commit": {"sha": "1111111111111111111111111111111111111111", "parents": []},
  "merge_base_commit": {"sha": "2222222222222222222222222222222222222222", "parents": [{"sha": "3333333333333333333333333333333333333333"}]},
  "commits": [{"sha": "4444444444444444444444444444444444444444", "parents": [{"sha": "2222222222222222222222222222222222222222"}]}],
  "files": [{"filename": "mine.go", "status": "added", "additions": 3, "deletions": 0, "changes": 3,
    "sha": "5555555555555555555555555555555555555555", "patch": "@@ -0,0 +1,3 @@"}]}`

func TestACompareReadsGitHubsAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(githubCompare))
	}))
	t.Cleanup(srv.Close)
	gh := NewGitHub(Credential{Auth: "pat", Secret: "ghp_x", APIBaseURL: srv.URL})
	for name, read := range map[string]func() (Comparison, error){
		"Compare": func() (Comparison, error) { return gh.Compare(context.Background(), "acme/target", "main", "head") },
		"CompareCommits": func() (Comparison, error) {
			c, err := gh.CompareCommits(context.Background(), "acme/target", "main", "head")
			return c.Comparison, err
		},
	} {
		t.Run(name, func(t *testing.T) {
			cmp, err := read()
			if err != nil {
				t.Fatal(err)
			}
			if cmp.MergeBase.SHA != "2222222222222222222222222222222222222222" || cmp.AheadBy != 2 || cmp.BehindBy != 1 ||
				len(cmp.Files) != 1 || cmp.Files[0].Additions != 3 {
				t.Fatalf("%+v", cmp)
			}
		})
	}
}
