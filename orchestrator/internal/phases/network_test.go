package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// dns feeds the translator one of lux's dns lifecycle events, in a
// transaction of its own as a batch would be.
func (w *receiptWorld) dns(name string, allowed bool) {
	w.t.Helper()
	data, _ := json.Marshal(map[string]any{"name": name, "allowed": allowed, "answers": []string{"140.82.112.3"}})
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.tr.luxEvent(context.Background(), tx, w.s, lux.Frame{Kind: "lux", EventType: "dns", EventData: data})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// A name lux refused is said once on the Run, with the agent's role, and
// kept once for the project's Network page; one it allowed is noise, and
// nothing of what it answered is kept.
func TestARefusedLookupIsSaidOnceAndKeptForTheProject(t *testing.T) {
	w := newReceiptWorld(t)
	w.tr.run.Phase, w.tr.run.Role = "fix", "implementer"
	w.dns("files.pythonhosted.org", false)
	w.dns("files.pythonhosted.org.", false)
	w.dns("FILES.pythonhosted.org", false)
	w.dns("api.github.com", true)

	var events []map[string]any
	rows, _ := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE event_type = $1 AND run_id = $2`,
		evNetworkRefused, w.tr.run.ID)
	for rows.Next() {
		var p map[string]any
		_ = rows.Scan(&p)
		events = append(events, p)
	}
	rows.Close()
	if len(events) != 1 || events[0]["name"] != "files.pythonhosted.org" || events[0]["role"] != "fixer" || len(events[0]) != 2 {
		t.Errorf("refused events = %v, want one for files.pythonhosted.org by the fixer", events)
	}
	var project, role string
	if err := w.owner.QueryRow(context.Background(), `SELECT project_id, role FROM agent_egress_refusals
		WHERE run_id = $1 AND name = 'files.pythonhosted.org'`, w.tr.run.ID).Scan(&project, &role); err != nil {
		t.Fatal(err)
	}
	if project != w.tr.run.ProjectID || role != "fixer" {
		t.Errorf("refusal = %s %s, want the project's, the fixer's", project, role)
	}
	if n := w.events(evNetworkRefused); n != 1 {
		t.Errorf("%d refused events in all, want 1", n)
	}
	var allowed int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM agent_egress_refusals WHERE name = 'api.github.com'`).Scan(&allowed)
	if allowed != 0 {
		t.Error("an allowed lookup was counted as refused")
	}
}

// A session's Run is told what lux refused it, as a project's is; with no
// project page to list them, nothing is kept.
func TestASessionsRefusedLookupIsSaidAndNotKept(t *testing.T) {
	w := newReceiptWorld(t)
	w.tr.run.ProjectID, w.tr.run.Role = "", "brainstorm"
	w.dns("pypi.org", false)
	w.dns("pypi.org", false)
	var rows int
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM agent_egress_refusals`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if n := w.events(evNetworkRefused); rows != 0 || n != 1 {
		t.Errorf("%d rows, %d events; want none kept and one said", rows, n)
	}
}

// A Run's refused names stop being kept and said at the cap: lux bounds
// none, and the table, the ledger and the Run's transcript would grow with
// every random name an agent resolved. The cap is each Run's: another Run
// of the organisation at its own cap takes none of this one's. A session's
// Run, which keeps none, stops being told at the same cap.
func TestARunsRefusedNamesStopAtTheCap(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "session"}[session], func(t *testing.T) {
			w := newReceiptWorld(t)
			if _, err := w.owner.Exec(context.Background(), `INSERT INTO agent_egress_refusals (run_id, name, organization_id, project_id, role)
				SELECT 'other_'||$1, 'o' || i || '.example.com', $1, 'prj_'||$1, 'fixer' FROM generate_series(1, $2::int) i`,
				w.org, maxRefusedNames); err != nil {
				t.Fatal(err)
			}
			want := maxRefusedNames
			if session {
				w.tr.run.ProjectID, want = "", 0
			}
			for i := range maxRefusedNames + 1 {
				w.dns(fmt.Sprintf("h%d.example.com", i), false)
			}
			var rows int
			if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM agent_egress_refusals WHERE run_id = $1`,
				w.tr.run.ID).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if n := w.events(evNetworkRefused); rows != want || n != maxRefusedNames {
				t.Errorf("%d rows, %d events; want %d rows, %d events", rows, n, want, maxRefusedNames)
			}
		})
	}
}
