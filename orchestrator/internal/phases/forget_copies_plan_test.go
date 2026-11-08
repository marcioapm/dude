package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// ForgetSecretCopies runs in every organization on every tick over every
// copy retained there (up to 30 days of them). With ten organizations of
// a thousand Runs with copies and four thousand artifacts each, its
// delete, on a generic plan as well as a custom one, finds the
// organization's copies on run_project_secrets_org_idx (257), never by
// scanning every organization's, and probes artifacts on
// artifacts_run_idx, never by scanning (or hashing) every artifact.
func TestForgetSecretCopiesProbesEachRunOnce(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var org string
	for i := range 10 {
		o := fmt.Sprintf("%s_%d", dbtest.Org(t, owner), i)
		exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, o)
		org = o
		exec(`INSERT INTO sessions (id, organization_id, titled_by) SELECT $1 || '_s' || n, $1, NULL FROM generate_series(1, 3000) n`, o)
		exec(`INSERT INTO runs (id, organization_id, session_id, attempt, phase, status, ended_at, lux_run_id)
			SELECT $1 || '_r' || n, $1, $1 || '_s' || n, 1, (ARRAY['implement','review','fix','test','simplify'])[n % 5 + 1]::run_phase,
				(ARRAY['completed','failed','aborted','completed'])[n % 4 + 1]::run_status,
				now() - make_interval(secs => n * 1700), 'lux_' || $1 || n FROM generate_series(1, 3000) n`, o)
		exec(`INSERT INTO run_project_secrets (run_id, organization_id, name, value)
			SELECT $1 || '_r' || n, $1, 'S' || k, 'value-' || n || '-' || k FROM generate_series(1, 1000) n, generate_series(1, 2) k`, o)
		exec(`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, storage_key, sha256)
			SELECT $1 || '_a' || n, $1, $1 || '_r' || (n % 3000 + 1), 'published', 'f' || n,
				(ARRAY['text/markdown; charset=utf-8','image/png','application/json','video/webm'])[n % 4 + 1], 'k' || $1 || n, 'x'
			FROM generate_series(1, 4000) n`, o)
	}
	exec(`ANALYZE`)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('role', 'dude_app', true), set_config('app.organization_id', $1, true),
			set_config('jit', 'off', true), set_config('plan_cache_mode', $2, true)`, org, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `PREPARE forget AS `+forgetCopies); err != nil {
			t.Fatal(err)
		}
		var plan []byte
		if err := tx.QueryRow(ctx, `EXPLAIN (FORMAT JSON) EXECUTE forget(3600, '`+org+`')`).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		_, _ = tx.Exec(ctx, `DEALLOCATE forget`)
		_ = tx.Rollback(ctx)
		scans := map[string][]string{}
		var walk func(node map[string]any)
		walk = func(node map[string]any) {
			if rel, ok := node["Relation Name"].(string); ok {
				how := fmt.Sprint(node["Node Type"])
				if name, ok := node["Index Name"].(string); ok {
					how += " " + name
				}
				scans[rel] = append(scans[rel], how)
			}
			if name, ok := node["Index Name"].(string); ok && node["Relation Name"] == nil {
				scans["bitmap"] = append(scans["bitmap"], name)
			}
			if p, ok := node["Subplan Name"].(string); ok && len(p) > 6 && p[:6] == "hashed" {
				scans["hashed"] = append(scans["hashed"], p)
			}
			children, _ := node["Plans"].([]any)
			for _, c := range children {
				walk(c.(map[string]any))
			}
		}
		var nodes []struct{ Plan map[string]any }
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			walk(n.Plan)
		}
		for _, how := range scans["artifacts"] {
			if how != "Index Scan artifacts_run_idx" {
				t.Errorf("%s: artifacts read by %s, want artifacts_run_idx; plan %s", mode, how, plan)
			}
		}
		if len(scans["artifacts"]) == 0 || len(scans["hashed"]) != 0 {
			t.Errorf("%s: artifacts read %v, hashed subplans %v; plan %s", mode, scans["artifacts"], scans["hashed"], plan)
		}
		for _, how := range scans["run_project_secrets"] {
			if how == "Seq Scan" {
				t.Errorf("%s: every organization's copies scanned; plan %s", mode, plan)
			}
		}
	}
}
