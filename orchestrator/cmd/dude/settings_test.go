package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// dude settings passes each flag given as the tool's argument and nothing
// for a flag left out; an argument is a usage error and calls nothing.
func TestDudeSettingsPassesItsFlags(t *testing.T) {
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tools/settings" {
			t.Errorf("request %s", r.URL.Path)
		}
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		asked = append(asked, args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	if err := run([]string{"settings", "--project", "API", "--task", "API-3", "--section", "models", "--limit", "50", "--cursor", "opaque"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"settings"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0]["project"] != "API" || asked[0]["task"] != "API-3" || asked[0]["section"] != "models" || asked[0]["limit"] != 50.0 || asked[0]["cursor"] != "opaque" ||
		len(asked[0]) != 5 || len(asked[1]) != 0 {
		t.Fatalf("calls: %v", asked)
	}
	if err := run([]string{"settings", "delivery"}, &out); err == nil ||
		err.Error() != "usage: dude settings [--project P] [--task T] [--section S] [--limit N] [--cursor C]" {
		t.Errorf("an argument: %v", err)
	}
	if len(asked) != 2 {
		t.Errorf("a usage error called %v", asked[2:])
	}
}
