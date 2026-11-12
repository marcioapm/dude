package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// dude task update --question Q passes the answered question as
// update_task's questionId, with the criteria it writes down; without it
// no questionId is sent.
func TestDudeTaskUpdatePassesTheAnsweredQuestion(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"updated":true,"next":"ok"}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	if err := run([]string{"task", "update", "--criterion", "Greets in Portuguese", "--question", "q_1"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"task", "update", "--criterion", "Greets"}, &out); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"acceptanceCriteria": []any{"Greets in Portuguese"}, "questionId": "q_1"},
		{"acceptanceCriteria": []any{"Greets"}},
	}
	if !reflect.DeepEqual(paths, []string{"/tools/update_task", "/tools/update_task"}) || !reflect.DeepEqual(bodies, want) {
		t.Fatalf("called %v with %v", paths, bodies)
	}
}
