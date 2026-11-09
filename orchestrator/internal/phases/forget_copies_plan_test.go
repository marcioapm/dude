package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// ForgetSecretCopies runs in every organization on every tick over every
// copy retained there (up to 30 days of them), and artifacts are never
// deleted. Seeded as an organization of 2,000 Runs, 1,000 with a copy,
// and 120,000 artifacts (enough that, without copyNeeded's fences, a
// custom plan pulls its artifacts probe up into a hashed SubPlan over every
// artifact of the organization) beside nine small ones, its delete, on a
// custom plan as well as a generic one (pgx plans each statement custom
// for its first five executions on a connection), finds the
// organization's copies on run_project_secrets_org_idx (257), never by
// scanning every organization's; reads artifacts only on
// artifacts_run_idx, never through a hashed SubPlan; and probes them at
// most once per Run with a copy (not per value, nor per Run of the
// organization). A collection's delete of one Run's copy
// (collectCopy) reads them the same way, at most once per value of that
// Run; and so does copyNeeded over every Run of the organization, whatever
// statement embeds it. EXPLAIN ANALYZE, rolled back.
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
	seed(org, 2000, 120000)
	exec(`ANALYZE`)
	for _, st := range []struct {
		name, sql, args string
		maxProbes       float64
	}{
		{"forgetCopies", forgetCopies, "(3600, '" + org + "')", withCopy},
		// One Run, two values: probed at most once per value, joined.
		{"collectCopy", collectCopy, "('" + org + "_r1')", 2},
		// copyNeeded itself, over every ended Run of the organization, as
		// any statement may embed it: at most one probe per Run.
		{"copyNeeded", `SELECT count(*) FROM runs r WHERE r.organization_id = $1
			AND r.status IN ('completed', 'failed', 'aborted') AND NOT ` + copyNeeded, "('" + org + "')", 2000},
	} {
		// enable_indexscan off: the bitmap plan the planner may pick on its
		// own (it did at 20,000 Runs and 1.12 million artifacts), a Bitmap
		// Heap Scan driven by artifacts_run_idx, still once per Run.
		for _, mode := range []struct {
			name, cache string
			bitmap      bool
		}{
			{"force_custom_plan", "force_custom_plan", false},
			{"force_generic_plan", "force_generic_plan", false},
			{"bitmap", "force_custom_plan", true},
		} {
			plan := explainCopyDelete(t, owner, org, mode.cache, mode.bitmap, st.name, st.sql, st.args)
			scans, probes := map[string][]string{}, 0.0
			var walk func(node map[string]any)
			walk = func(node map[string]any) {
				if rel, ok := node["Relation Name"].(string); ok {
					how := fmt.Sprint(node["Node Type"])
					name, ok := node["Index Name"].(string)
					if children, _ := node["Plans"].([]any); how == "Bitmap Heap Scan" && len(children) == 1 {
						// Read through its one Bitmap Index Scan's index.
						name, ok = children[0].(map[string]any)["Index Name"].(string)
					}
					if ok {
						how += " " + name
					}
					scans[rel] = append(scans[rel], how)
					if rel == "artifacts" {
						l, _ := node["Actual Loops"].(float64)
						probes += l
					}
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
			bitmaps := 0
			for _, how := range scans["artifacts"] {
				switch how {
				case "Bitmap Heap Scan artifacts_run_idx":
					bitmaps++
				case "Index Scan artifacts_run_idx", "Index Only Scan artifacts_run_idx":
				default:
					t.Errorf("%s, %s: artifacts read by %s, want artifacts_run_idx; plan %s", st.name, mode.name, how, plan)
				}
			}
			if mode.bitmap && bitmaps == 0 {
				t.Errorf("%s, %s: artifacts read %v, want a Bitmap Heap Scan; plan %s", st.name, mode.name, scans["artifacts"], plan)
			}
			if len(scans["artifacts"]) == 0 || len(scans["hashed"]) != 0 {
				t.Errorf("%s, %s: artifacts read %v, hashed subplans %v; plan %s", st.name, mode.name, scans["artifacts"], scans["hashed"], plan)
			}
			if probes < 1 || probes > st.maxProbes {
				t.Errorf("%s, %s: artifacts probed %v times, want 1 to %v; plan %s", st.name, mode.name, probes, st.maxProbes, plan)
			}
			for _, how := range scans["run_project_secrets"] {
				if how == "Seq Scan" {
					t.Errorf("%s, %s: every organization's copies scanned; plan %s", st.name, mode.name, plan)
				}
			}
		}
	}
}

// explainCopyDelete is the plan, as JSON, of the delete sql run once with
// args as dude_app in org under plan_cache_mode mode, with index scans off
// if bitmap, rolled back. With DUDE_PLAN_OUT set, the plan as text is
// written there too, for a reader.
func explainCopyDelete(t *testing.T, owner *pgx.Conn, org, mode string, bitmap bool, name, sql, args string) []byte {
	t.Helper()
	ctx := context.Background()
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
		_, _ = owner.Exec(ctx, `DEALLOCATE copy_delete`) // a session's, not the transaction's
	}()
	if _, err := tx.Exec(ctx, `SELECT set_config('role', 'dude_app', true), set_config('app.organization_id', $1, true),
		set_config('jit', 'off', true), set_config('plan_cache_mode', $2, true),
		set_config('enable_indexscan', CASE WHEN $3 THEN 'off' ELSE 'on' END, true)`, org, mode, bitmap); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `PREPARE copy_delete AS `+sql); err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("DUDE_PLAN_OUT"); dir != "" {
		// In a savepoint rolled back, so the plan checked deletes the same copies.
		var text []string
		_, _ = tx.Exec(ctx, `SAVEPOINT text`)
		if rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) EXECUTE copy_delete`+args); err == nil {
			for rows.Next() {
				var l string
				_ = rows.Scan(&l)
				text = append(text, l)
			}
			rows.Close()
		}
		_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT text`)
		if bitmap {
			mode += "-bitmap"
		}
		_ = os.WriteFile(dir+"/"+name+"-"+mode+".txt", []byte(strings.Join(text, "\n")+"\n"), 0o644)
	}
	var plan []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) EXECUTE copy_delete`+args).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}
