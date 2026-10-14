// Package mainfixture runs tests under dbtest.Main for dbtest's own test
// (TestMainGivesEachOpenAFreshDatabaseAndLeavesNoneBehind), which runs it
// with `go test` and checks the server once it exits. Each Open's database
// name is appended to the file DBTEST_FIXTURE_OUT names.
package mainfixture

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func record(t *testing.T, kind, name string) {
	t.Helper()
	out := os.Getenv("DBTEST_FIXTURE_OUT")
	if out == "" {
		return
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s\n", kind, name); err != nil {
		t.Fatal(err)
	}
}

// open opens a database, records it, and requires it to be fresh from the
// template: no fixture_marks table, which every test here creates.
func open(t *testing.T) (*pgx.Conn, string) {
	t.Helper()
	_, owner := dbtest.Open(t)
	name := owner.Config().Database
	record(t, "open", name)
	var marks *string
	if err := owner.QueryRow(context.Background(), `SELECT to_regclass('public.fixture_marks')::text`).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if marks != nil {
		t.Fatalf("%s already has fixture_marks: not a fresh clone", name)
	}
	return owner, name
}

func mark(t *testing.T, owner *pgx.Conn, label string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `CREATE TABLE fixture_marks (label text);
		INSERT INTO fixture_marks VALUES ('`+label+`')`); err != nil {
		t.Fatal(err)
	}
}

func marks(t *testing.T, owner *pgx.Conn) string {
	t.Helper()
	var s string
	if err := owner.QueryRow(context.Background(), `SELECT string_agg(label, ',' ORDER BY label) FROM fixture_marks`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTwoOpensInOneTest(t *testing.T) {
	one, oneName := open(t)
	mark(t, one, "one")
	two, twoName := open(t)
	if oneName == twoName {
		t.Fatalf("both Opens got %s", oneName)
	}
	mark(t, two, "two")
	if got := marks(t, one); got != "one" {
		t.Errorf("the first database's rows: %q, want one", got)
	}
	if got := marks(t, two); got != "two" {
		t.Errorf("the second database's rows: %q, want two", got)
	}
}

func TestASubtestsOpenIsItsOwn(t *testing.T) {
	parent, parentName := open(t)
	mark(t, parent, "parent")
	t.Run("child", func(t *testing.T) {
		child, childName := open(t)
		if childName == parentName {
			t.Fatalf("the subtest got its parent's %s", parentName)
		}
		mark(t, child, "child")
	})
	if got := marks(t, parent); got != "parent" {
		t.Errorf("the parent's rows after its subtest: %q, want parent", got)
	}
}

// The clone made ahead for an Open no test makes: recorded so dbtest's
// test can check Main drops it.
func TestTheNextCloneIsMadeAhead(t *testing.T) {
	owner, name := open(t)
	cut := strings.LastIndex(name, "_")
	prefix := name[:cut+1]
	own, err := strconv.Atoi(name[cut+1:])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var spare string
		err := owner.QueryRow(context.Background(), `SELECT datname FROM pg_database
			WHERE left(datname, length($1)) = $1 AND substr(datname, length($1) + 1) ~ '^[0-9]+$'
			AND substr(datname, length($1) + 1)::int > $2 LIMIT 1`, prefix, own).Scan(&spare)
		if err == nil {
			record(t, "spare", spare)
			return
		}
		if err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no clone made ahead of the next Open")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
