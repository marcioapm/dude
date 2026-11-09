package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// ForgetSecretCopies runs in every organization on every tick over every
// copy retained there (up to 30 days of them), and artifacts are never
// deleted. Seeded as a long-lived organization (20,000 Runs, 1,000 of them
// with a copy, 120,000 artifacts) beside nine small ones, its delete, on a
// custom plan as well as a generic one (pgx plans each statement custom
// for its first five executions on a connection), finds the
// organization's copies on run_project_secrets_org_idx (257), never by
// scanning every organization's; reads artifacts only on
// artifacts_run_idx, never through a hashed SubPlan over every artifact
// of the organization; and probes them at most once per Run with a copy
// (EXPLAIN ANALYZE, rolled back).
func TestForgetSecretCopiesProbesEachRunOnce(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	const withCopy = 1000
	var org string
	seed := func(o string, runs, artifacts int) {
		exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, o)
		exec(`INSERT INTO sessions (id, organization_id, titled_by) SELECT $1 || '_s' || n, $1, NULL FROM generate_series(1, $2) n`, o, runs)
		// The Runs with a copy (1..withCopy) ended over the last 20 days;
		// the others before, as an organization's history.
		exec(`INSERT INTO runs (id, organization_id, session_id, attempt, phase, status, ended_at, lux_run_id)
			SELECT $1 || '_r' || n, $1, $1 || '_s' || n, 1, (ARRAY['implement','review','fix','test','simplify'])[n % 5 + 1]::run_phase,
				(ARRAY['completed','failed','aborted','completed'])[n % 4 + 1]::run_status,
				now() - make_interval(secs => n * 1700), 'lux_' || $1 || n FROM generate_series(1, $2) n`, o, runs)
		exec(`INSERT INTO run_project_secrets (run_id, organization_id, name, value)
			SELECT $1 || '_r' || n, $1, 'S' || k, 'value-' || n || '-' || k FROM generate_series(1, $2) n, generate_series(1, 2) k`, o, withCopy)
		exec(`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, storage_key, sha256)
			SELECT $1 || '_a' || n, $1, $1 || '_r' || (n % $2 + 1), 'published', 'f' || n,
				(ARRAY['text/markdown; charset=utf-8','image/png','application/json','video/webm'])[n % 4 + 1], 'k' || $1 || n, 'x'
			FROM generate_series(1, $3) n`, o, runs, artifacts)
	}
	base := dbtest.Org(t, owner)
	for i := range 9 {
		seed(fmt.Sprintf("%s_%d", base, i), 3000, 4000)
	}
	org = base + "_big"
	seed(org, 1000, 120000)
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
		if dir := os.Getenv("DUDE_PLAN_OUT"); dir != "" {
			// The plan as text, for a reader, in a savepoint rolled back
			// so that the plan checked below deletes the same copies.
			var text []string
			_, _ = tx.Exec(ctx, `SAVEPOINT text`)
			rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) EXECUTE forget(3600, '`+org+`')`)
			if err == nil {
				for rows.Next() {
					var l string
					_ = rows.Scan(&l)
					text = append(text, l)
				}
				rows.Close()
			}
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT text`)
			_ = os.WriteFile(dir+"/"+mode+".txt", []byte(strings.Join(text, "\n")+"\n"), 0o644)
		}
		var plan []byte
		if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE forget(3600, '`+org+`')`).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		_, _ = tx.Exec(ctx, `DEALLOCATE forget`)
		_ = tx.Rollback(ctx)
		scans := map[string][]string{}
		loops := 0.0
		var walk func(node map[string]any)
		walk = func(node map[string]any) {
			if rel, ok := node["Relation Name"].(string); ok {
				how := fmt.Sprint(node["Node Type"])
				if name, ok := node["Index Name"].(string); ok {
					how += " " + name
				}
				scans[rel] = append(scans[rel], how)
				if rel == "artifacts" {
					l, _ := node["Actual Loops"].(float64)
					loops += l
				}
			}
			if name, ok := node["Index Name"].(string); ok && node["Relation Name"] == nil {
				scans["bitmap"] = append(scans["bitmap"], name)
			}
			if p, ok := node["Subplan Name"].(string); ok && strings.HasPrefix(p, "hashed") {
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
			if how != "Index Scan artifacts_run_idx" && how != "Index Only Scan artifacts_run_idx" {
				t.Errorf("%s: artifacts read by %s, want artifacts_run_idx; plan %s", mode, how, plan)
			}
		}
		if len(scans["artifacts"]) == 0 || len(scans["hashed"]) != 0 {
			t.Errorf("%s: artifacts read %v, hashed subplans %v; plan %s", mode, scans["artifacts"], scans["hashed"], plan)
		}
		if loops < 1 || loops > withCopy {
			t.Errorf("%s: artifacts probed %v times, want 1 to %d (once at most per Run with a copy); plan %s", mode, loops, withCopy, plan)
		}
		for _, how := range scans["run_project_secrets"] {
			if how == "Seq Scan" {
				t.Errorf("%s: every organization's copies scanned; plan %s", mode, plan)
			}
		}
	}
}
