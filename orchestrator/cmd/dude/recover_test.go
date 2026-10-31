package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// dude recover RUN passes the Run, and its --branch and --note when given,
// to recover_run; without a Run it is a usage error and calls nothing.
func TestDudeRecoverPassesItsRunBranchAndNote(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"recoveryId":"rcv_1","status":"requested"}`))
	}))
	defer srv.Close()
	t.Setenv("LUX_SERVICE_DUDE", "")
	t.Setenv("DUDE_TOOLS_URL", srv.URL)
	var out bytes.Buffer
	run1 := "run_0mvuxq61u6577fa9f4b1f37e3"
	if err := run([]string{"recover", run1, "--branch", "dude/T/recovered-mine", "--note", "keep it"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"recover", run1}, &out); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"run": run1, "branch": "dude/T/recovered-mine", "note": "keep it"}, {"run": run1}}
	if !reflect.DeepEqual(paths, []string{"/tools/recover_run", "/tools/recover_run"}) || !reflect.DeepEqual(bodies, want) {
		t.Fatalf("called %v with %v", paths, bodies)
	}
	err := run([]string{"recover"}, &out)
	if err == nil || err.Error() != "usage: dude recover RUN [--branch B] [--note N]" || len(paths) != 2 {
		t.Fatalf("without a Run: %v, calls %v", err, paths)
	}
}
