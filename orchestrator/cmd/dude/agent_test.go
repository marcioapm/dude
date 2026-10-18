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
