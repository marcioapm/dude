package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	if err == nil || err.Error() != "usage: dude wait-for [--run R]... [--pr P[@HEAD]]... --deadline D [--any] [--note N]" || len(paths) != 2 {
		t.Fatalf("without a deadline: %v, calls %v", err, paths)
	}
}
