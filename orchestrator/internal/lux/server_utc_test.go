package lux_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A server's times leave dude in UTC; the rest of what lux sent, a null
// and a field dude does not know included, leaves as lux sent it.
func TestAServersTimesArePassedOnInUTC(t *testing.T) {
	var s lux.Server
	if err := json.Unmarshal([]byte(`{"name":"web","port":3000,"state":"stopped","readySince":null,
		"since":"2026-10-09T17:30:00+05:30","lastRequestAt":"2026-10-09T17:29:59.25+05:30","somethingNew":{"a":[1,"x"]}}`), &s); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal([]lux.Server{s})
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
		t.Fatalf("passed on %s (%v)", out, err)
	}
	for key, want := range map[string]string{
		"since":         `"2026-10-09T12:00:00Z"`,
		"lastRequestAt": `"2026-10-09T11:59:59.25Z"`,
		"readySince":    `null`,
		"somethingNew":  `{"a":[1,"x"]}`,
		"name":          `"web"`,
		"port":          `3000`,
		"state":         `"stopped"`,
	} {
		if v, ok := got[0][key]; !ok || string(v) != want {
			t.Errorf("%s = %s, want %s (all: %s)", key, v, want, out)
		}
	}
	if len(got[0]) != 7 {
		t.Errorf("passed on %d keys: %s", len(got[0]), out)
	}
	// The same instants lux meant.
	var back lux.Server
	if err := json.Unmarshal(out[1:len(out)-1], &back); err != nil {
		t.Fatal(err)
	}
	if !back.Since.Equal(*s.Since) || !back.LastRequestAt.Equal(*s.LastRequestAt) || back.ReadySince != nil {
		t.Errorf("since %v / %v, lastRequestAt %v / %v, readySince %v", back.Since, s.Since, back.LastRequestAt, s.LastRequestAt, back.ReadySince)
	}
	if !back.Since.Equal(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v", back.Since)
	}

	var ready lux.Server
	if err := json.Unmarshal([]byte(`{"state":"ready","readySince":"2026-10-09T17:30:00+05:30"}`), &ready); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(ready); string(out) != `{"readySince":"2026-10-09T12:00:00Z","state":"ready"}` {
		t.Errorf("a ready server passed on %s", out)
	}
}
