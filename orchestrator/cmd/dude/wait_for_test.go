package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// dude wait-for passes each --run and --pr (PR@HEAD pins a head) as one
// condition of wait_for, with its deadline, wake and note; without a
// deadline it is a usage error and calls nothing.
func TestDudeWaitForPassesItsConditions(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"waitId":"cwt_1","next":"end your turn"}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	head := "0123456789012345678901234567890123456789"
	if err := run([]string{"wait-for", "--run", "run_a", "--run", "run_b", "--pr", "api#12@" + head, "--pr", "7",
		"--deadline", "45m", "--any", "--note", "then review"}, &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"conditions": []any{
			map[string]any{"runsEnded": []any{"run_a", "run_b"}},
			map[string]any{"prChecksFinal": map[string]any{"pr": "api#12", "head": head}},
			map[string]any{"prChecksFinal": map[string]any{"pr": "7"}},
		},
		"deadline": "45m", "wake": "any", "note": "then review",
	}
	if len(paths) != 1 || paths[0] != "/tools/wait_for" || !reflect.DeepEqual(bodies[0], want) {
		t.Fatalf("called %v with %v", paths, bodies)
	}
	if err := run([]string{"wait-for", "--deadline", "2h"}, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bodies[1], map[string]any{"deadline": "2h"}) {
		t.Fatalf("a timer: %v", bodies[1])
	}
	err := run([]string{"wait-for", "--run", "run_a"}, &out)
	if err == nil || err.Error() != waitForUsage || !strings.HasPrefix(waitForUsage, "usage: dude wait-for [--run R]... [--pr P[@HEAD]]... ") || len(paths) != 2 {
		t.Fatalf("without a deadline: %v, calls %v", err, paths)
	}
}

// dude wait-for passes --workflow, --after, --sibling (TASK=B1,B2 names
// what it becomes), --pr-ended (PR=how) and --url ([!][STATUS,]URL, split
// on the first comma) as one condition each, after the runs and the pull
// requests; a malformed flag is a usage error and calls nothing.
func TestDudeWaitForPassesTheOtherConditions(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"waitId":"cwt_1","next":"end your turn"}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	if err := run([]string{"wait-for", "--run", "run_a", "--pr", "7",
		"--url", "https://example.com/up",
		"--sibling", "CP-62=done,failed", "--workflow", "wfd_1", "--after", "45m", "--sibling", "CP-63",
		"--pr-ended", "owner/repo#123=merged", "--pr-ended", "#9",
		"--url", "5xx,https://status.example.com/health",
		"--url", "!200,https://example.com/a,b?x=1,2&y=3",
		"--workflow", "wfd_2", "--deadline", "3h"}, &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"conditions": []any{
			map[string]any{"runsEnded": []any{"run_a"}},
			map[string]any{"prChecksFinal": map[string]any{"pr": "7"}},
			map[string]any{"workflowEnded": "wfd_1"},
			map[string]any{"workflowEnded": "wfd_2"},
			map[string]any{"after": "45m"},
			map[string]any{"siblingTask": map[string]any{"task": "CP-62", "becomes": []any{"done", "failed"}}},
			map[string]any{"siblingTask": map[string]any{"task": "CP-63"}},
			map[string]any{"prEnded": map[string]any{"pr": "owner/repo#123", "how": "merged"}},
			map[string]any{"prEnded": map[string]any{"pr": "#9"}},
			map[string]any{"url": map[string]any{"url": "https://example.com/up"}},
			map[string]any{"url": map[string]any{"url": "https://status.example.com/health", "status": "5xx"}},
			map[string]any{"url": map[string]any{"url": "https://example.com/a,b?x=1,2&y=3", "status": "200", "not": true}},
		},
		"deadline": "3h",
	}
	if len(paths) != 1 || paths[0] != "/tools/wait_for" || !reflect.DeepEqual(bodies[0], want) {
		t.Fatalf("called %v with %v", paths, bodies)
	}
	// A first field that is not a status is part of the URL.
	if err := run([]string{"wait-for", "--url", "!https://example.com/a,b", "--deadline", "1h"}, &out); err != nil {
		t.Fatal(err)
	}
	if want := []any{map[string]any{"url": map[string]any{"url": "https://example.com/a,b", "not": true}}}; !reflect.DeepEqual(bodies[1]["conditions"], want) {
		t.Fatalf("a URL with a comma: %v", bodies[1])
	}
	for _, bad := range [][]string{
		{"--sibling", "=done"}, {"--sibling", "CP-62="}, {"--pr-ended", "=merged"}, {"--url", "!"},
		{"--url", "5xx,"}, {"--after", "45m", "--after", "1h"},
	} {
		err := run(append(append([]string{"wait-for"}, bad...), "--deadline", "2h"), &out)
		if err == nil || err.Error() != waitForUsage || len(paths) != 2 {
			t.Fatalf("%v: %v, calls %v", bad, err, paths)
		}
	}
}

// The tool's refusal of a condition is dude wait-for's error, in its words:
// the CLI judges only the flags' syntax.
func TestDudeWaitForSurfacesTheToolsRefusal(t *testing.T) {
	// As the tools API answers a refusal: 422, {"error", "code"} (the
	// code "" for delivery's), here rWaitAfterTooShort's words.
	refusal := `after "5m" is under 30m: for a shorter time, just set the deadline`
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, args)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": refusal, "code": ""})
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	err := run([]string{"wait-for", "--after", "5m", "--url", "418,https://example.com", "--deadline", "2h"}, &out)
	if err == nil || err.Error() != refusal || out.Len() != 0 {
		t.Fatalf("the refusal: %v %q", err, out.String())
	}
	want := []any{map[string]any{"after": "5m"}, map[string]any{"url": map[string]any{"url": "https://example.com", "status": "418"}}}
	if len(bodies) != 1 || !reflect.DeepEqual(bodies[0]["conditions"], want) {
		t.Fatalf("sent %v", bodies)
	}
}
