package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// timeLimitServer answers a pull request whose head has one Actions check
// run of the given conclusion, whose annotations are annotations (a JSON
// list) or answer status when it is set; it records every path asked.
func timeLimitServer(t *testing.T, conclusion, annotations string, status int) (*GitHub, func() []string) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"}}`)
		case "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, `{"state":"pending","total_count":0}`)
		case "/repos/acme/api/commits/abc/check-runs":
			fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"id":41,"name":"unit","status":"completed","conclusion":%q,"app":{"slug":"github-actions"}}]}`, conclusion)
		case "/repos/acme/api/check-runs/41/annotations":
			if status != 0 {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"no"}`)
				return
			}
			fmt.Fprint(w, annotations)
		case "/repos/acme/api/pulls/1/reviews":
			fmt.Fprint(w, `[]`)
		case "/graphql":
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`)
		case "/repos/acme/api/compare/...abc":
			fmt.Fprint(w, `{"behind_by":0}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

const timeLimitAnnotations = `[{"path":".github","start_line":1,"annotation_level":"failure","message":"The job has exceeded the maximum execution time of 6m0s"}]`

// A job GitHub cancelled at its timeout-minutes reports only "cancelled";
// its one annotation says it ran out of time. That is a failure, which
// wakes the conductor like any other, said to have timed out. A job a
// person cancelled, or a newer run superseded, stays not failing; a
// timed_out conclusion stays failing; and only a cancelled run costs the
// annotations read.
func TestAJobCancelledAtItsTimeLimitIsFailing(t *testing.T) {
	for _, tc := range []struct {
		name, conclusion, annotations string
		status                        int
		checks                        string
		timedOut, readsAnnotations    bool
	}{
		{name: "cancelled at its time limit", conclusion: "cancelled", annotations: timeLimitAnnotations,
			checks: ChecksFailing, timedOut: true, readsAnnotations: true},
		{name: "cancelled by a person", conclusion: "cancelled", annotations: `[]`,
			checks: ChecksPending, readsAnnotations: true},
		{name: "superseded by concurrency", conclusion: "cancelled",
			annotations: `[{"path":".github","start_line":1,"annotation_level":"failure","message":"Canceling since a higher priority waiting request for 'ci-refs/heads/x' exists"}]`,
			checks:      ChecksPending, readsAnnotations: true},
		{name: "annotations refused", conclusion: "cancelled", status: 403, checks: ChecksPending, readsAnnotations: true},
		{name: "timed_out", conclusion: "timed_out", checks: ChecksFailing, timedOut: true},
		{name: "failure", conclusion: "failure", checks: ChecksFailing},
		{name: "success", conclusion: "success", checks: ChecksPassing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh, asked := timeLimitServer(t, tc.conclusion, tc.annotations, tc.status)
			st, err := gh.PullRequest(context.Background(), "acme/api", 1)
			if err != nil {
				t.Fatal(err)
			}
			if st.Checks != tc.checks {
				t.Errorf("checks = %s, want %s", st.Checks, tc.checks)
			}
			if len(st.CheckList) != 1 || st.CheckList[0].TimedOut() != tc.timedOut ||
				st.CheckList[0].Failed() != (tc.checks == ChecksFailing) {
				t.Errorf("check list %+v: timed out %v, failed %v", st.CheckList, tc.timedOut, tc.checks == ChecksFailing)
			}
			if got := slices.Contains(asked(), "/repos/acme/api/check-runs/41/annotations"); got != tc.readsAnnotations {
				t.Errorf("annotations read %v, want %v", got, tc.readsAnnotations)
			}
			was := Status{PullRequestRef: PullRequestRef{State: StateOpen, HeadSHA: st.HeadSHA}, Checks: ChecksPending}
			s := Classify(was, st, nil, nil)
			if tc.checks != ChecksFailing {
				if s != nil && s.Kind == "actionable" {
					t.Errorf("not failing, yet signalled %+v", s)
				}
				return
			}
			if s == nil || s.Kind != "actionable" || len(s.Feedback) != 1 || len(s.Feedback[0].Checks) != 1 ||
				s.Feedback[0].Checks[0].Name != "unit" || s.Feedback[0].Checks[0].RunID != 41 ||
				s.Feedback[0].Checks[0].TimedOut != tc.timedOut {
				t.Errorf("signal %+v", s)
			}
		})
	}
}

// A failure that may pass on the annotations read fails the sync, to be
// tried again, rather than reading the job as cancelled by a person.
func TestATransientAnnotationsReadFailsTheSync(t *testing.T) {
	gh, _ := timeLimitServer(t, "cancelled", "", 502)
	if _, err := gh.PullRequest(context.Background(), "acme/api", 1); err == nil || !Transient(err) {
		t.Fatalf("err %v, want a transient failure", err)
	}
}
