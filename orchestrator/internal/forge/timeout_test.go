package forge

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// timeLimitServer answers a pull request whose head has one check run of
// the given conclusion, reported by app (Actions when ""), whose
// annotations are annotations (a JSON list) or answer status when it is
// set; it records every path asked.
func timeLimitServer(t *testing.T, app, conclusion, annotations string, status int) (*GitHub, func() []string) {
	if app == "" {
		app = ActionsApp
	}
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
			fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"id":41,"name":"unit","status":"completed","conclusion":%q,"app":{"slug":%q}}]}`, conclusion, app)
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
		name, app, conclusion, annotations string
		status                             int
		checks                             string
		timedOut, readsAnnotations         bool
	}{
		{name: "cancelled at its time limit", conclusion: "cancelled", annotations: timeLimitAnnotations,
			checks: ChecksFailing, timedOut: true, readsAnnotations: true},
		// Every annotation is looked at, not only the first.
		{name: "cancelled at its time limit, said second", conclusion: "cancelled",
			annotations: `[{"path":".github","start_line":1,"annotation_level":"failure","message":"The operation was canceled."},` + timeLimitAnnotations[1:],
			checks:      ChecksFailing, timedOut: true, readsAnnotations: true},
		// Only Actions annotates its time limit: another app's cancelled
		// run costs no read and stays not failing.
		{name: "cancelled, another app's", app: "circleci", conclusion: "cancelled", annotations: timeLimitAnnotations,
			checks: ChecksPending},
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
			gh, asked := timeLimitServer(t, tc.app, tc.conclusion, tc.annotations, tc.status)
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
	gh, _ := timeLimitServer(t, "", "cancelled", "", 502)
	if _, err := gh.PullRequest(context.Background(), "acme/api", 1); err == nil || !Transient(err) {
		t.Fatalf("err %v, want a transient failure", err)
	}
}

// annotationsServer answers a pull request whose head's check runs are
// runs() (a JSON list) at the time of asking; every cancelled run's
// annotations are timeLimitAnnotations for an id in timedOut, else a
// concurrency cancellation. It counts the annotations reads by run id.
func annotationsServer(t *testing.T, runs func() string, timedOut ...int64) (*GitHub, func() map[string]int) {
	var mu sync.Mutex
	reads := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/api/pulls/1":
			fmt.Fprint(w, `{"number":1,"state":"open","head":{"sha":"abc"}}`)
		case r.URL.Path == "/repos/acme/api/commits/abc/status":
			fmt.Fprint(w, `{"state":"pending","total_count":0}`)
		case r.URL.Path == "/repos/acme/api/commits/abc/check-runs":
			list := runs()
			fmt.Fprintf(w, `{"total_count":%d,"check_runs":%s}`, strings.Count(list, `"id"`), list)
		case strings.HasSuffix(r.URL.Path, "/annotations"):
			mu.Lock()
			reads[r.URL.Path]++
			mu.Unlock()
			for _, id := range timedOut {
				if r.URL.Path == fmt.Sprintf("/repos/acme/api/check-runs/%d/annotations", id) {
					fmt.Fprint(w, timeLimitAnnotations)
					return
				}
			}
			fmt.Fprint(w, `[{"message":"Canceling since a higher priority waiting request for 'ci' exists"}]`)
		case r.URL.Path == "/repos/acme/api/pulls/1/reviews":
			fmt.Fprint(w, `[]`)
		case r.URL.Path == "/graphql":
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`)
		case r.URL.Path == "/repos/acme/api/compare/...abc":
			fmt.Fprint(w, `{"behind_by":0}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}), func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(reads)
	}
}

func cancelledRuns(ids ...int64) string {
	var runs []string
	for _, id := range ids {
		runs = append(runs, fmt.Sprintf(`{"id":%d,"name":"job %d","status":"completed","conclusion":"cancelled","app":{"slug":"github-actions"}}`, id, id))
	}
	return "[" + strings.Join(runs, ",") + "]"
}

// A completed check run never changes, so its annotations are read once:
// a sync given the checks it stored last (WithKnownChecks) reuses each
// cancelled run's verdict, timed out or not, and reads only a run it has
// not classified yet.
func TestACancelledRunsAnnotationsAreReadOnce(t *testing.T) {
	ids := []int64{41, 42}
	gh, reads := annotationsServer(t, func() string { return cancelledRuns(ids...) }, 41, 43)
	first, err := gh.PullRequest(context.Background(), "acme/api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Checks != ChecksFailing || !first.CheckList[0].TimedOut() || first.CheckList[1].Conclusion != "cancelled" {
		t.Fatalf("first sync %s %+v", first.Checks, first.CheckList)
	}
	if got := reads(); len(got) != 2 {
		t.Fatalf("first sync read %v, want both runs' annotations", got)
	}
	ids = []int64{41, 42, 43}
	second, err := gh.PullRequest(WithKnownChecks(context.Background(), first.CheckList), "acme/api", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"/repos/acme/api/check-runs/41/annotations": 1, "/repos/acme/api/check-runs/42/annotations": 1,
		"/repos/acme/api/check-runs/43/annotations": 1}
	if got := reads(); !maps.Equal(got, want) {
		t.Errorf("after two syncs read %v, want %v", got, want)
	}
	if second.Checks != ChecksFailing || len(second.CheckList) != 3 || !second.CheckList[0].TimedOut() ||
		second.CheckList[1].Conclusion != "cancelled" || !second.CheckList[2].TimedOut() {
		t.Errorf("second sync %s %+v", second.Checks, second.CheckList)
	}
}

// An agent's read of checks (github_pull_request) makes at most
// agentAnnotationReads annotations reads; a cancelled run past them is
// left cancelled (pending, never passing), and one it is given a verdict
// for costs nothing.
func TestAnAgentsChecksReadBoundsItsAnnotationsReads(t *testing.T) {
	var ids []int64
	for id := int64(1); id <= agentAnnotationReads+5; id++ {
		ids = append(ids, id)
	}
	gh, reads := annotationsServer(t, func() string { return cancelledRuns(ids...) }, ids[len(ids)-1])
	known := []Check{{Name: "job 1", Status: "completed", Conclusion: "timed_out", RunID: 1, App: ActionsApp}}
	rollup, list, _, err := gh.checks(agentReadContext(WithKnownChecks(context.Background(), known)), "acme/api", "abc")
	if err != nil {
		t.Fatal(err)
	}
	got := reads()
	if len(got) != agentAnnotationReads || got["/repos/acme/api/check-runs/1/annotations"] != 0 {
		t.Errorf("read %d runs' annotations (%v), want %d, none of run 1", len(got), got, agentAnnotationReads)
	}
	if rollup != ChecksFailing || !list[0].TimedOut() || list[len(list)-1].Conclusion != "cancelled" {
		t.Errorf("rollup %s, list %+v", rollup, list)
	}
}
