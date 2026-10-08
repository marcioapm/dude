package phases

import (
	"context"
	"encoding/json"
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
// counted for the project's Network page; one it allowed is noise, and
// nothing of what it answered is kept.
func TestARefusedLookupIsSaidOnceAndCountedForTheProject(t *testing.T) {
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
	var count int
	if err := w.owner.QueryRow(context.Background(), `SELECT project_id, role, count FROM agent_egress_refusals
		WHERE run_id = $1 AND name = 'files.pythonhosted.org'`, w.tr.run.ID).Scan(&project, &role, &count); err != nil {
		t.Fatal(err)
	}
	if project != w.tr.run.ProjectID || role != "fixer" || count != 3 {
		t.Errorf("refusal = %s %s %d, want the project's, the fixer's, three times", project, role, count)
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
