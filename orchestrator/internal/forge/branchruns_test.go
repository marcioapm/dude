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
	if len(asked) != 4 || asked[0] != "/repos/acme/api/actions/runs?branch=release%2F1+x&per_page=3" ||
		asked[1] != "/repos/acme/api/actions/runs/1/jobs?filter=latest&per_page=2" {
		t.Fatalf("asked %v", asked)
	}
}

// GitHub failing on one run's jobs keeps the runs already read, that one
// marked JobsUnread; its rate limit, or a refusal, still fails the read.
func TestBranchRunsKeepTheRunsWhenOneRunsJobsFail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		fails  bool
	}{
		{name: "a 502", status: 502, body: `{"message":"bad gateway"}`},
		{name: "a rate limit", status: 429, body: `{"message":"rate limit"}`, fails: true},
		{name: "a refusal", status: 403, body: `{"message":"Resource not accessible by integration"}`, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/acme/api/actions/runs":
					fmt.Fprint(w, `{"total_count":2,"workflow_runs":[
						{"id":2,"name":"CI","head_sha":"b","status":"completed","conclusion":"failure","created_at":"2026-11-11T10:00:00Z"},
						{"id":1,"name":"CI","head_sha":"a","status":"completed","conclusion":"success","created_at":"2026-11-11T09:00:00Z"}]}`)
				case "/repos/acme/api/actions/runs/1/jobs":
					fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":11,"name":"unit","status":"completed","conclusion":"success"}]}`)
				case "/repos/acme/api/actions/runs/2/jobs":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				default:
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			runs, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).BranchRuns(context.Background(), "acme/api", "main", 5, 5)
			if tc.fails {
				if err == nil {
					t.Fatalf("read %+v, want it refused", runs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 2 || runs[0].ID != 2 || !runs[0].JobsUnread || len(runs[0].Jobs) != 0 ||
				false || len(runs[1].Jobs) != 1 {
				t.Fatalf("runs %+v", runs)
			}
		})
	}
}

// No branch is refused before GitHub is asked: it would list every
// branch's runs.
func TestBranchRunsOfNoBranchAreRefusedUnasked(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked++ }))
	defer srv.Close()
	if _, err := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}).BranchRuns(context.Background(), "acme/api", "", 3, 2); err == nil || asked != 0 {
		t.Fatalf("err %v, %d requests", err, asked)
	}
}
