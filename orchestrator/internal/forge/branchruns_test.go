package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A branch's runs are read with one request for the list and one per run
// for its latest attempt's jobs, newest first whatever order GitHub
// answered in, at most the runs and jobs asked for.
func TestBranchRunsAreNewestFirstWithTheirJobs(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		switch r.URL.Path {
		case "/repos/acme/api/actions/runs":
			fmt.Fprint(w, `{"total_count":3,"workflow_runs":[
				{"id":1,"name":"CI","head_sha":"a","event":"push","status":"completed","conclusion":"success","created_at":"2026-11-11T09:00:00Z"},
				{"id":3,"name":"CI","head_sha":"c","event":"push","status":"completed","conclusion":"failure","created_at":"2026-11-11T11:00:00Z"},
				{"id":2,"name":"CI","head_sha":"b","event":"push","status":"in_progress","conclusion":null,"created_at":"2026-11-11T10:00:00Z"}]}`)
		case "/repos/acme/api/actions/runs/1/jobs", "/repos/acme/api/actions/runs/2/jobs", "/repos/acme/api/actions/runs/3/jobs":
			fmt.Fprint(w, `{"total_count":3,"jobs":[{"id":11,"name":"unit","status":"completed","conclusion":"failure"},
				{"id":12,"name":"lint","status":"completed","conclusion":"success"},{"id":13,"name":"e2e","status":"completed","conclusion":"success"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	runs, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).BranchRuns(context.Background(), "acme/api", "release/1 x", 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].ID != 3 || runs[1].ID != 2 || runs[2].ID != 1 || runs[0].HeadSHA != "c" || runs[0].Conclusion != "failure" {
		t.Fatalf("runs %+v", runs)
	}
	if len(runs[0].Jobs) != 2 || runs[0].TotalJobs != 3 || runs[0].Jobs[0].Name != "unit" || runs[0].Jobs[0].Conclusion != "failure" || runs[0].Jobs[0].ID != 11 {
		t.Fatalf("jobs %+v", runs[0])
	}
	if len(asked) != 4 || asked[0] != "/repos/acme/api/actions/runs?branch=release%2F1+x&per_page=3" {
		t.Fatalf("asked %v", asked)
	}
}
