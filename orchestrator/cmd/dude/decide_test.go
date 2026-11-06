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

// dude decide binds each --action to the --choice just before it, in the
// order the flags are given: a choice with no --action carries none, an
// --action before any --choice or a second one for the same choice is a
// usage error, and calls nothing.
func TestDudeDecideBindsEachActionToTheChoiceBeforeIt(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, args)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decided":"ask_person"}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	for _, c := range []struct {
		name string
		args []string
		want []any
	}{
		{"the action after the second choice", []string{"--choice", "Run Docker locally", "--choice", "Open the PR", "--action", "open"},
			[]any{map[string]any{"label": "Run Docker locally"}, map[string]any{"label": "Open the PR", "action": "open"}}},
		{"the action after the first choice", []string{"--choice", "Open the PR", "--action", "draft", "--choice", "Run Docker locally"},
			[]any{map[string]any{"label": "Open the PR", "action": "draft"}, map[string]any{"label": "Run Docker locally"}}},
		{"none says no action", []string{"--choice", "Wait", "--action", "none", "--choice", "Open", "--action", "open"},
			[]any{map[string]any{"label": "Wait"}, map[string]any{"label": "Open", "action": "open"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(bodies)
			if err := run(append([]string{"decide", "ask_person", "--note", "How?"}, c.args...), &out); err != nil {
				t.Fatal(err)
			}
			if len(bodies) != before+1 || !reflect.DeepEqual(bodies[before]["choices"], c.want) {
				t.Fatalf("choices %v, want %v", bodies[len(bodies)-1]["choices"], c.want)
			}
		})
	}
	for _, args := range [][]string{
		{"--action", "open", "--choice", "Open the PR"},
		{"--choice", "Open the PR", "--action", "open", "--action", "draft"},
	} {
		before := len(bodies)
		err := run(append([]string{"decide", "ask_person", "--note", "How?"}, args...), &out)
		if err == nil || !strings.Contains(err.Error(), "--action") || len(bodies) != before {
			t.Errorf("%v: %v, %d calls", args, err, len(bodies)-before)
		}
	}
}
