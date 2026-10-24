package automations

import (
	"encoding/json"
	"testing"
	"time"
)

func TestVariablesAreSubstituted(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"},
		"sender": map[string]any{"login": "rui"},
		"pull_request": map[string]any{"number": 42, "title": "Fix login", "html_url": "https://github.com/acme/web/pull/42",
			"head": map[string]any{"ref": "fix/login"}}})
	vars := Variables([]Payload{{DeliveryID: "d-9", Event: "pull_request", Action: "opened", Body: body}}, nil, "UTC")
	got := Substitute("{{ event.name }}/{{event.action}} {{event.delivery_id}}: #{{pr.number}} {{pr.title}} ({{pr.url}}) on {{branch}} in {{repository}} by {{sender}}; {{unknown}}", vars)
	want := "pull_request/opened d-9: #42 Fix login (https://github.com/acme/web/pull/42) on fix/login in acme/web by rui; {{unknown}}"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	at := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	if got := Substitute("at {{schedule.time}}", Variables(nil, &at, "Europe/Lisbon")); got != "at 2026-10-23 03:00 WEST" {
		t.Errorf("schedule.time: %q", got)
	}
}
