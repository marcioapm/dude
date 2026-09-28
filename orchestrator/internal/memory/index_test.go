package memory_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The index is kept by triggers (migration 054): these pin what they do, as
// the owner sees the table, and that the app sees only its organization's.

func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

type doc struct {
	title, body string
	embedded    bool
	project     *string
}

func document(t *testing.T, c *pgx.Conn, typ, id string) (doc, bool) {
	t.Helper()
	var d doc
	err := c.QueryRow(context.Background(), `SELECT title, body, embedding IS NOT NULL, project_id
		FROM search_documents WHERE source_type = $1 AND source_id = $2`, typ, id).Scan(&d.title, &d.body, &d.embedded, &d.project)
	if err == pgx.ErrNoRows {
		return d, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return d, true
}

// seed makes a project TEXT with an epic and task TEXT-1 in a new organization.
func seed(t *testing.T, owner *pgx.Conn) (org, project, epic, task string) {
	t.Helper()
	org = dbtest.Org(t, owner)
	project, epic, task = "prj_"+org, "epc_"+org, "wi_"+org
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, description)
		VALUES ($1, $2, 'control-plane', $1, 'TEXT', 'The API and webhooks')`, project, org)
	exec(t, owner, `INSERT INTO epics (id, organization_id, project_id, title, description)
		VALUES ($1, $2, $3, 'Webhook reliability', 'Verified, deduplicated, retried')`, epic, org, project)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
		VALUES ($1, $2, $3, 1, 'Dedupe deliveries', 'A redelivery is processed once', '["same id twice is one row", "survives a restart"]', 'running')`,
		task, org, project)
	return org, project, epic, task
}

func TestTheWorkIsIndexedAsItIsWritten(t *testing.T) {
	_, owner := dbtest.Open(t)
	_, project, epic, task := seed(t, owner)

	d, ok := document(t, owner, "task", task)
	if !ok {
		t.Fatal("a new task has no document")
	}
	if d.title != "TEXT-1 Dedupe deliveries" {
		t.Errorf("a task's title is its key and title, got %q", d.title)
	}
	if d.body != "A redelivery is processed once\nsame id twice is one row\nsurvives a restart" {
		t.Errorf("a task's body is its goal and each criterion, got %q", d.body)
	}
	if d, ok := document(t, owner, "epic", epic); !ok || d.title != "Webhook reliability" || d.body != "Verified, deduplicated, retried" {
		t.Errorf("epic document = %+v, %v", d, ok)
	}
	if d, ok := document(t, owner, "project", project); !ok || d.title != "control-plane" || d.project == nil || *d.project != project {
		t.Errorf("a project's document is its name, in itself: %+v, %v", d, ok)
	}
}

func TestOnlyNewWordsCostAnEmbedding(t *testing.T) {
	_, owner := dbtest.Open(t)
	_, _, _, task := seed(t, owner)
	embed := func() {
		exec(t, owner, `UPDATE search_documents SET embedding = array_fill(0.1, ARRAY[768])::halfvec, embedding_model = 'm', embedded_at = now()
			WHERE source_id = $1`, task)
	}

	embed()
	exec(t, owner, `UPDATE tasks SET status = 'review' WHERE id = $1`, task)
	if d, _ := document(t, owner, "task", task); !d.embedded {
		t.Error("a status change dropped the embedding")
	}
	exec(t, owner, `UPDATE tasks SET title = title WHERE id = $1`, task)
	if d, _ := document(t, owner, "task", task); !d.embedded {
		t.Error("saving the same title dropped the embedding")
	}
	exec(t, owner, `UPDATE tasks SET goal = 'A redelivery is processed exactly once' WHERE id = $1`, task)
	if d, _ := document(t, owner, "task", task); d.embedded {
		t.Error("a new goal kept the old words' embedding")
	}
}

func TestARenamedPrefixRenamesTheKeys(t *testing.T) {
	_, owner := dbtest.Open(t)
	_, project, _, task := seed(t, owner)
	exec(t, owner, `UPDATE projects SET key_prefix = 'CP' WHERE id = $1`, project)
	if d, _ := document(t, owner, "task", task); d.title != "CP-1 Dedupe deliveries" {
		t.Errorf("title after the prefix changed = %q", d.title)
	}
}

func TestAnArchivedMemoryIsOutOfTheIndexUntilRestored(t *testing.T) {
	_, owner := dbtest.Open(t)
	org, _, _, _ := seed(t, owner)
	exec(t, owner, `INSERT INTO memories (id, organization_id, title, content, author_kind, system_reason)
		VALUES ('mem_1', $1, 'Commit messages are prose', 'No feat: prefixes.', 'system', 'test')`, org)

	d, ok := document(t, owner, "memory", "mem_1")
	if !ok || d.project != nil {
		t.Fatalf("an organization's memory is indexed with no project: %+v, %v", d, ok)
	}
	exec(t, owner, `UPDATE memories SET archived_at = now() WHERE id = 'mem_1'`)
	if _, ok := document(t, owner, "memory", "mem_1"); ok {
		t.Error("an archived memory is still searchable")
	}
	exec(t, owner, `UPDATE memories SET archived_at = NULL WHERE id = 'mem_1'`)
	if _, ok := document(t, owner, "memory", "mem_1"); !ok {
		t.Error("a restored memory is not searchable")
	}
	exec(t, owner, `DELETE FROM memories WHERE id = 'mem_1'`)
	if _, ok := document(t, owner, "memory", "mem_1"); ok {
		t.Error("a deleted memory left its document")
	}
}

func TestAnOrganizationSeesOnlyItsOwnIndex(t *testing.T) {
	app, owner := dbtest.Open(t)
	a, _, _, _ := seed(t, owner)
	b, _, _, _ := seed(t, owner)
	exec(t, owner, `INSERT INTO memories (id, organization_id, title, content, author_kind, system_reason)
		VALUES ('mem_b', $1, 'B only', 'secret', 'system', 'test')`, b)

	var docs, mems int
	err := app.InOrg(context.Background(), a, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM search_documents WHERE organization_id <> $1`, a).Scan(&docs); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM memories`).Scan(&mems)
	})
	if err != nil {
		t.Fatal(err)
	}
	if docs != 0 || mems != 0 {
		t.Errorf("organization A sees %d of B's documents and %d of its memories", docs, mems)
	}
}

// The app writes tasks and memories as itself; the triggers must work for it,
// not only for the owner.
func TestTheAppsWritesKeepTheIndex(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, task := seed(t, owner)
	err := app.InOrg(context.Background(), org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `UPDATE tasks SET title = 'Dedupe webhook deliveries' WHERE id = $1`, task); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
			VALUES ('mem_app', $1, $2, 'GitHub retries for 3 days', 'Dedupe on X-GitHub-Delivery.', 'system', 'test')`, org, project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := document(t, owner, "task", task); d.title != "TEXT-1 Dedupe webhook deliveries" {
		t.Errorf("task document after the app's edit = %q", d.title)
	}
	if _, ok := document(t, owner, "memory", "mem_app"); !ok {
		t.Error("the app's memory is not indexed")
	}
}
