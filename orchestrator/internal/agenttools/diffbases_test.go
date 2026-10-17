package agenttools_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFinalDiffBasesAreReadOnlyEvenAfterRunEnds(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_final_bases", "implementer", "running")
	sha := strings.Repeat("a", 40)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_diff_' || $1, $1, $2, 'target', 'https://github.com/acme/target.git', 'main')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, 'repo_diff_' || $1, 'write')`, f.org, f.item)
	mustExec(t, f.owner, `UPDATE runs SET status = 'completed', sync_history = jsonb_build_object('target', jsonb_build_object('main', jsonb_build_array($2::text)))
		WHERE id = $1`, "run_final_bases", sha)
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/git/diff-bases", token, 200},
		{"GET", "/git/diff-bases", "", 401},
		{"POST", "/git/diff-bases", token, 401},
		{"GET", "/git/coauthors", token, 401},
		{"POST", "/tools/record_event", token, 401},
	} {
		req, _ := http.NewRequest(tc.method, f.url+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, res.StatusCode, body)
		}
		if tc.status == 200 && string(body) != "target "+sha+"\n" {
			t.Fatalf("recorded bases %q", body)
		}
	}
}
