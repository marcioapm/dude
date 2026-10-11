package lux_test

import (
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

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
	var ready lux.Server
	if err := json.Unmarshal([]byte(`{"state":"ready","readySince":"2026-10-09T17:30:00+05:30"}`), &ready); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(ready); string(out) != `{"readySince":"2026-10-09T12:00:00Z","state":"ready"}` {
		t.Errorf("a ready server passed on %s", out)
	}

	// Zero-offset spellings and whitespace stay byte-for-byte unchanged.
	asSent := `{ "state":"ready", "since":"2026-10-09T12:00:00.2500Z", "readySince":"2026-10-09T12:00:00+00:00" }`
	var utc lux.Server
	if err := json.Unmarshal([]byte(asSent), &utc); err != nil {
		t.Fatal(err)
	}
	if out, _ := utc.MarshalJSON(); string(out) != asSent {
		t.Errorf("a server already in UTC passed on %s", out)
	}
}
