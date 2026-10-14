package dbtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func admin(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	c, err := adminConn(ctx)
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	t.Cleanup(func() { c.Close(ctx) })
	return c
}

func create(t *testing.T, admin *pgx.Conn, name string) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+name+" OWNER dude"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { drop(name) })
}

func prefixed(prefix string) func(string) bool {
	return func(name string) bool { return strings.HasPrefix(name, prefix) }
}

// What crashed runs left, past the age, with no session and no live binary,
// is dropped: a template, its clone, an Upgrade database. Kept: a template
// and clone whose binary still holds its lock, a clone a session is
// connected to, and a template younger than the age.
func TestOnlyWhatNoLiveRunUsesIsDroppedAsStale(t *testing.T) {
	ctx := context.Background()
	a := admin(t)
	// Unique names two hours old, so nothing else on the server matches.
	old := time.Now().Add(-2 * time.Hour).UnixNano()
	crashed, held, open := fmt.Sprintf("dude_gotest_%d", old), fmt.Sprintf("dude_gotest_%d", old+1), fmt.Sprintf("dude_gotest_%d", old+2)
	young := fmt.Sprintf("dude_gotest_%d", time.Now().UnixNano())
	upgrade := fmt.Sprintf("dude_gotest_upgrade_%d_1", old)
	for _, name := range []string{crashed, crashed + "_1", upgrade, held, held + "_1", open + "_1", young} {
		create(t, a, name)
	}
	holder, err := adminConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { holder.Close(ctx) })
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1, hashtext($2))", liveLock, held); err != nil {
		t.Fatal(err)
	}
	user, err := pgx.Connect(ctx, fmt.Sprintf("postgres://dude:dude@%s/%s", host(), open+"_1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { user.Close(ctx) })

	mine := func(name string) bool {
		return slices.ContainsFunc([]string{crashed, held, open, young, upgrade}, func(p string) bool { return strings.HasPrefix(name, p) })
	}
	dropped := dropStale(ctx, a, time.Hour, mine)
	slices.Sort(dropped)
	want := []string{crashed, crashed + "_1", upgrade}
	slices.Sort(want)
	if !slices.Equal(dropped, want) {
		t.Errorf("dropped %v, want %v", dropped, want)
	}
	for _, name := range want {
		if n := databases(t, a, name); n != 0 {
			t.Errorf("%s is still on the server", name)
		}
	}
	for _, name := range []string{held, held + "_1", open + "_1", young} {
		if n := databases(t, a, name); n != 1 {
			t.Errorf("%s was dropped", name)
		}
	}
}

// A binary running under Main (testdata/mainfixture, held mid-test) while
// another cleans up with no age limit at all: nothing of the running one
// is dropped, not its template, nor the clone its test holds open, nor
// the clone made ahead that no one has connected to. It then finishes
// with its rows intact, and leaves nothing behind.
func TestACleanupNeverTouchesALiveBinarysDatabases(t *testing.T) {
	ctx := context.Background()
	a := admin(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out, release := filepath.Join(dir, "opened"), filepath.Join(dir, "release")
	cmd := exec.Command(goBin, "test", "-count=1", "-v", "-run", "^TestHoldWhileAnotherBinaryCleansUp$", "./testdata/mainfixture")
	cmd.Env = append(os.Environ(), "DBTEST_FIXTURE_OUT="+out, "DBTEST_FIXTURE_HOLD="+release)
	var log strings.Builder
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		<-exited
	})
	var held string
	for deadline := time.Now().Add(90 * time.Second); held == ""; time.Sleep(20 * time.Millisecond) {
		raw, _ := os.ReadFile(out)
		for _, line := range strings.Split(string(raw), "\n") {
			if name, ok := strings.CutPrefix(line, "held "); ok {
				held = name
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fixture never held its database:\n%s", log.String())
		}
	}
	template := held[:strings.LastIndex(held, "_")]
	t.Cleanup(func() {
		for _, name := range clonesOf(t, a, template) {
			drop(name)
		}
		drop(template)
	})
	// The clone made ahead for the next Open: no session on it.
	var spare string
	for deadline := time.Now().Add(10 * time.Second); spare == ""; time.Sleep(20 * time.Millisecond) {
		for _, name := range clonesOf(t, a, template) {
			if name != held {
				spare = name
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the fixture made no clone ahead")
		}
	}

	if dropped := dropStale(ctx, a, 0, prefixed(template)); len(dropped) != 0 {
		t.Errorf("a cleanup dropped a live binary's %v", dropped)
	}
	for _, name := range []string{template, held, spare} {
		if n := databases(t, a, name); n != 1 {
			t.Errorf("%s is gone while its binary runs", name)
		}
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-exited; err != nil {
		t.Fatalf("the held fixture failed: %v\n%s", err, log.String())
	}
	exited <- nil
	if n := databases(t, a, template); n != 0 {
		t.Errorf("the fixture's template %s is left after it exited", template)
	}
	if left := clonesOf(t, a, template); len(left) != 0 {
		t.Errorf("clones of %s left after the fixture exited: %v", template, left)
	}
}

func clonesOf(t *testing.T, admin *pgx.Conn, template string) []string {
	t.Helper()
	rows, err := admin.Query(context.Background(), `SELECT datname FROM pg_database WHERE left(datname, length($1) + 1) = $1 || '_'`, template)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}
