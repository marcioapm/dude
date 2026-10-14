package dbtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Main, run over a package (testdata/mainfixture) three times: each Open,
// two in one test or one in a subtest, gets a fresh database of its own
// (the fixture checks rows), and once the binary exits no database made
// for it is left: neither its template nor any clone, the one made ahead
// for an Open that never came included.
func TestMainGivesEachOpenAFreshDatabaseAndLeavesNoneBehind(t *testing.T) {
	ctx := context.Background()
	admin, err := adminConn(ctx)
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	t.Cleanup(func() { admin.Close(ctx) })
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "opened")
	cmd := exec.Command(goBin, "test", "-count=3", "-v", "-skip", "TestHoldWhileAnotherBinaryCleansUp", "./testdata/mainfixture")
	cmd.Env = append(os.Environ(), "DBTEST_FIXTURE_OUT="+out)
	log, runErr := cmd.CombinedOutput()
	raw, _ := os.ReadFile(out)
	var opened, spares []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		kind, name, _ := strings.Cut(line, " ")
		switch kind {
		case "spare":
			spares = append(spares, name)
		case "open":
			opened = append(opened, name)
		}
	}
	if len(opened) == 0 {
		t.Fatalf("the fixture opened no database: %v\n%s", runErr, log)
	}
	// A failing fixture can leave its template and clones: this test drops
	// them all.
	template := opened[0][:strings.LastIndex(opened[0], "_")]
	clones := func() []string {
		rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE left(datname, length($1) + 1) = $1 || '_'`, template)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	t.Cleanup(func() {
		for _, name := range clones() {
			drop(name)
		}
		drop(template)
	})
	if runErr != nil {
		t.Fatalf("the fixture failed: %v\n%s", runErr, log)
	}
	if strings.Contains(string(log), "--- SKIP") {
		t.Fatalf("the fixture skipped:\n%s", log)
	}
	// Per run: two in one test, a parent and its subtest, one more.
	if len(opened) != 3*5 || len(spares) != 3 {
		t.Fatalf("%d opens and %d spares recorded, want 15 and 3:\n%s", len(opened), len(spares), raw)
	}
	seen := map[string]bool{}
	for _, name := range opened {
		if seen[name] {
			t.Errorf("%s was opened twice", name)
		}
		seen[name] = true
	}
	if !seen[spares[0]] || !seen[spares[1]] {
		t.Errorf("the first runs' spares %v were not the next runs' databases", spares[:2])
	}
	if seen[spares[2]] {
		t.Errorf("the last run's spare %s was opened, but no test came after it", spares[2])
	}
	for _, name := range append(opened, spares[2]) {
		if strings.LastIndex(name, "_") != len(template) || !strings.HasPrefix(name, template) {
			t.Fatalf("%s is not a clone of %s", name, template)
		}
	}
	if left := clones(); len(left) != 0 {
		t.Errorf("clones of %s left after the fixture exited: %v", template, left)
	}
	if n := databases(t, admin, template); n != 0 {
		t.Errorf("the fixture's template %s is left after it exited", template)
	}
}
