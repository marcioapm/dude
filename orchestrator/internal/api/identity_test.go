package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceIdentityUsesActiveMembershipAndCurrentRole(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	other := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_owner', $1, 'Owner'), ('per_other', $1, 'Other')`, org)
	exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_foreign', $1, 'Foreign')`, other)
	exec(`INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, person_id)
 VALUES ('key_one', $1, 'One', 'hash_one', 'dude_sk_', 'per_owner'), ('key_two', $1, 'Two', 'hash_two', 'dude_sk_', 'per_owner')`, org)
	s := &Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.auth(func(w http.ResponseWriter, r *http.Request, org string) error {
		p := principalOf(r)
		write(w, 200, map[string]any{"person": p.Person, "admin": p.Admin, "actorType": p.ActorType})
		return nil
	})
	call := func(kind, actor, person, role string, want int, admin bool) {
		t.Helper()
		r := httptest.NewRequest("POST", "/identity", nil)
		r.Header.Set("Authorization", "Bearer svc")
		r.Header.Set("X-Dude-Organization", org)
		r.Header.Set("X-Dude-Credential-Kind", kind)
		r.Header.Set("X-Dude-Actor", actor)
		r.Header.Set("X-Dude-Person", person)
		r.Header.Set("X-Dude-Role", role)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s/%s/%s: %d %s", kind, actor, person, w.Code, w.Body.String())
		}
		if want == 200 {
			var p struct {
				Person    string
				Admin     bool
				ActorType string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Person != "per_owner" || p.Admin != admin {
				t.Fatalf("resolved response: %+v %v", p, err)
			}
			wantType := "human"
			if kind == "person" {
				wantType = "person"
			}
			if p.ActorType != wantType {
				t.Fatalf("actor type: %s", p.ActorType)
			}
		}
	}
	call("person", "per_owner", "per_owner", "admin", 200, false)
	call("", "key_one", "", "admin", 200, false)
	call("api_key", "key_two", "per_owner", "member", 200, false)
	call("api_key", "key_one", "per_other", "admin", 403, false)
	call("person", "per_foreign", "per_foreign", "admin", 403, false)
	call("person", "key_one", "per_owner", "admin", 403, false)
	exec(`UPDATE people SET role = 'admin' WHERE id = 'per_owner'`)
	call("person", "per_owner", "per_owner", "member", 200, true)
	exec(`UPDATE api_keys SET revoked_at = now() WHERE id = 'key_one'`)
	call("", "key_one", "", "admin", 403, false)
	call("api_key", "key_two", "per_owner", "member", 200, true)
	call("person", "per_owner", "per_owner", "member", 200, true)
	exec(`UPDATE people SET removed_at = now() WHERE id = 'per_owner'`)
	call("person", "per_owner", "per_owner", "admin", 403, false)
	call("", "key_two", "", "admin", 403, false)
}

func TestOwnerOnlyUsesFirstActivePersonAndIgnoresLegacyKey(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_owner', $1, 'P', 'p', 'P')`, org)
	exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_first', $1, 'First'), ('per_second', $1, 'Second')`, org)
	exec(`INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, person_id) VALUES ('key_old', $1, 'Old', 'old', 'dude_sk_', 'per_second')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, owner_key_id) VALUES ('task_owner', $1, 'prj_owner', 1, 'Task', 'key_old')`, org)
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM task_people WHERE task_id = 'task_owner'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy key insertion assigned membership: %d %v", count, err)
	}
	exec(`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ('task_owner', 'per_first', $1, 3), ('task_owner', 'per_second', $1, 8)`, org)
	foreignOrg := dbtest.Org(t, owner)
	exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_foreign_owner', $1, 'Foreign')`, foreignOrg)
	if _, err := owner.Exec(ctx, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		VALUES ('task_owner', 'per_foreign_owner', $1, 0)`, org); err == nil {
		t.Fatal("cross-organization membership accepted")
	}
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_owner', $1, 'prj_owner', 'task_owner', 1, 'running', 'implement', 'implementer')`, org)
	s := &Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	check := func(person string, allowed bool) {
		t.Helper()
		exec(`DELETE FROM questions WHERE id = 'question_owner'`)
		exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt)
			VALUES ('question_owner', $1, 'task_owner', 'run_owner', 'Proceed?')`, org)
		r := httptest.NewRequest("POST", "/internal/questions/question_owner/answer", strings.NewReader(`{"text":"yes"}`))
		r.Header.Set("Authorization", "Bearer svc")
		r.Header.Set("X-Dude-Organization", org)
		r.Header.Set("X-Dude-Credential-Kind", "person")
		r.Header.Set("X-Dude-Actor", person)
		r.Header.Set("X-Dude-Person", person)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 403
		if allowed {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("%s: %d %s", person, w.Code, w.Body.String())
		}
		var status string
		if err := owner.QueryRow(ctx, `SELECT status::text FROM questions WHERE id = 'question_owner'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if (status == "answered") != allowed {
			t.Fatalf("%s question status: %s", person, status)
		}
		if allowed {
			var typ, id string
			if err := owner.QueryRow(ctx, `SELECT actor_type, actor_id FROM events WHERE event_type = 'question.answered' ORDER BY cursor DESC LIMIT 1`).Scan(&typ, &id); err != nil {
				t.Fatal(err)
			}
			if typ != "person" || id != person {
				t.Fatalf("actor: %s %s", typ, id)
			}
		}
	}
	check("per_first", true)
	check("per_second", false)
	exec(`UPDATE api_keys SET revoked_at = now() WHERE id = 'key_old'`)
	check("per_first", true)
	check("per_second", false)
	exec(`UPDATE tasks SET owner_key_id = NULL WHERE id = 'task_owner'`)
	check("per_second", false)
	exec(`UPDATE people SET removed_at = now() WHERE id = 'per_first'`)
	check("per_first", false)
	check("per_second", true)
	exec(`UPDATE people SET removed_at = now() WHERE id = 'per_second'`)
	check("per_first", false)
	exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_unowned', $1, 'Unowned')`, org)
	check("per_unowned", true)
}

func TestPersonOwnershipUpgradePreservesAssignmentsAndBackfillsOnlyAbsent(t *testing.T) {
	_, bootstrap := dbtest.Open(t)
	ctx := context.Background()
	host := os.Getenv("DUDE_TEST_PG")
	if host == "" {
		host = "localhost:5433"
	}
	name := fmt.Sprintf("dude_ownership_upgrade_%d", time.Now().UnixNano())
	if _, err := bootstrap.Exec(ctx, "CREATE DATABASE "+name+" OWNER dude"); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, "postgres://dude:dude@"+host+"/"+name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx); _, _ = bootstrap.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)") })
	if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(path string) {
		t.Helper()
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, path := range files {
		if filepath.Base(path)[:3] <= "055" {
			apply(path)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	org := dbtest.Org(t, conn)
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_upgrade', $1, 'P', 'p', 'P')`, org)
	exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_legacy', $1, 'Legacy'), ('per_assigned', $1, 'Assigned')`, org)
	exec(`INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, person_id)
		VALUES ('key_upgrade', $1, 'Key', 'upgrade', 'dude_sk_', 'per_legacy')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, owner_key_id)
		VALUES ('task_absent', $1, 'prj_upgrade', 1, 'Absent', 'key_upgrade'),
		('task_assigned', $1, 'prj_upgrade', 2, 'Assigned', 'key_upgrade')`, org)
	exec(`DELETE FROM task_people WHERE task_id IN ('task_absent', 'task_assigned')`)
	exec(`INSERT INTO task_people (task_id, person_id, organization_id, position)
		VALUES ('task_assigned', 'per_assigned', $1, 4)`, org)
	apply("../../../migrations/056_person_ownership.sql")
	assertOwner := func(task, person string, position int) {
		t.Helper()
		var got string
		var pos int
		if err := conn.QueryRow(ctx, `SELECT person_id, position FROM task_people WHERE task_id = $1`, task).Scan(&got, &pos); err != nil {
			t.Fatal(err)
		}
		if got != person || pos != position {
			t.Fatalf("%s: %s/%d", task, got, pos)
		}
	}
	assertOwner("task_absent", "per_legacy", 0)
	assertOwner("task_assigned", "per_assigned", 4)
	exec(`UPDATE tasks SET owner_key_id = NULL WHERE id = 'task_assigned'`)
	exec(`UPDATE tasks SET owner_key_id = 'key_upgrade' WHERE id = 'task_assigned'`)
	exec(`UPDATE api_keys SET revoked_at = now() WHERE id = 'key_upgrade'`)
	assertOwner("task_absent", "per_legacy", 0)
	assertOwner("task_assigned", "per_assigned", 4)
	app, err := db.Open(ctx, "postgres://dude_app:dude_app@"+host+"/"+name)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		return ownerOnly(ctx, tx, "task_assigned", "per_assigned", "answer")
	}); err != nil {
		t.Fatal(err)
	}
}
