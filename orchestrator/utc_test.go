package orchestrator_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// On a host and a database outside UTC, every time the API sends is UTC: a
// time Go encodes ends in Z, and one Postgres renders in JSON is at
// +00:00. The browser merges the session's proposal cards with its UTC
// event times; a card stamped +01:00 once sorted an hour late, after
// everything said since. Both zones have a fixed offset and no daylight
// saving (the host at +01:00, the database in Asia/Kolkata at +05:30), so
// the check holds on any date.
func TestTheAPISendsUTCOnAHostOutsideIt(t *testing.T) {
	// Set before the world starts its goroutines, which read time.Local.
	local := time.Local
	time.Local = time.FixedZone("UTC+1", 3600)
	t.Cleanup(func() { time.Local = local })
	s := newSessionWorld(t)
	mustExec(t, s.owner, fmt.Sprintf(`ALTER DATABASE %q SET timezone = 'Asia/Kolkata'`, s.owner.Config().Database))

	// A pool opened after both, as the orchestrator opens its own.
	app, err := db.Open(t0(), s.app.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	srv := httptest.NewServer((&api.Server{DB: app, Lux: s.syncer.Lux, Token: "svc", Log: quiet, Kick: func() {}}).Handler())
	t.Cleanup(srv.Close)
	get := func(method, path string, body string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		for k, v := range map[string]string{"Authorization": "Bearer svc", "X-Dude-Organization": s.org, "Content-Type": "application/json",
			"X-Dude-Credential-Kind": "person", "X-Dude-Person": s.admin, "X-Dude-Actor": s.admin} {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %v", method, path, res.StatusCode, out)
		}
		return out
	}

	var zone string
	if err := app.Pool.QueryRow(t0(), `SHOW timezone`).Scan(&zone); err != nil || zone != "UTC" {
		t.Errorf("the pool's session zone is %q (%v), want UTC", zone, err)
	}

	session := s.ok(s.admin, "POST", "/internal/sessions", map[string]any{"title": "Zones"})["id"].(string)
	s.proposal(session, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"}})
	detail := get("GET", "/internal/sessions/"+session, "")
	proposals, _ := detail["proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("proposals: %v", detail["proposals"])
	}
	if at, _ := proposals[0].(map[string]any)["createdAt"].(string); !strings.HasSuffix(at, "Z") {
		t.Errorf("proposals[0].createdAt = %q, want UTC (…Z)", at)
	}
	if at, _ := detail["session"].(map[string]any)["createdAt"].(string); !strings.HasSuffix(at, "+00:00") {
		t.Errorf("session.createdAt = %q, want Postgres's UTC (+00:00)", at)
	}

	memory := get("POST", "/internal/memory/memories", `{"title": "Meter windows", "content": "24h"}`)
	for _, field := range []string{"createdAt", "updatedAt"} {
		if at, _ := memory[field].(string); !strings.HasSuffix(at, "Z") {
			t.Errorf("memory %s = %q, want UTC (…Z)", field, at)
		}
	}
}
