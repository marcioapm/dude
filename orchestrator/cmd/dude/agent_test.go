package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDudeAgentStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tools/start_agent" || r.Header.Get("X-Dude-Tool-Call-ID") == "" {
			t.Errorf("request %s without identity", r.URL.Path)
		}
		var args map[string]string
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		if args["profile"] != "investigator" || args["prompt"] != "Investigate" || args["note"] != "Details" || args["tier"] != "Thinker" {
			t.Errorf("arguments %v", args)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runId":"run_child"}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	if err := run([]string{"agent", "start", "investigator", "--prompt", "Investigate", "--note", "Details", "--tier", "Thinker"}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestDudeCohortAndInspection(t *testing.T) {
	for _, tc := range []struct {
		name, path, response string
		args                 []string
	}{
		{"cohort", "/tools/start_agents", `{"cohortId":"cohort_1"}`, []string{"agent", "cohort", "--runs-json", `[{"profile":"investigator","prompt":"Evidence"}]`, "--key", "batch"}},
		{"inspect", "/tools/read_run", `{"run":"run_child"}`, []string{"run", "inspect", "run_child", "--offset", "2", "--limit", "3"}},
		{"stop", "/tools/stop_run", `{"stopped":"run_0mvuxq61u6577fa9f4b1f37e3","status":"stopping"}`, []string{"stop", "run_0mvuxq61u6577fa9f4b1f37e3", "--reason", "Not needed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path %s", r.URL.Path)
				}
				var args map[string]any
				if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
					t.Fatal(err)
				}
				if tc.name == "cohort" {
					if r.Header.Get("X-Dude-Tool-Call-ID") == "" || args["key"] != "batch" || len(args["runs"].([]any)) != 1 {
						t.Errorf("cohort request %v", args)
					}
				} else if tc.name == "stop" {
					if args["run"] != "run_0mvuxq61u6577fa9f4b1f37e3" || args["reason"] != "Not needed" {
						t.Errorf("stop request %v", args)
					}
				} else if args["run"] != "run_child" || args["offset"] != float64(2) || args["limit"] != float64(3) {
					t.Errorf("inspection request %v", args)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			t.Setenv("LUX_SERVICE_DUDE", "")
			t.Setenv("DUDE_TOOLS_URL", srv.URL)
			var out bytes.Buffer
			if err := run(tc.args, &out); err != nil {
				t.Fatal(err)
			}
		})
	}
}
