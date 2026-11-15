package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// 267 applies over main's schema with reviewer questions of both
// addressees, keeps them, and takes a question to the author with its
// GitHub comment and login; any other addressee is still refused.
func TestMigration267AuthorQuestionsUpFromMain(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "267")
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_267', 'O', 'o267')`)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_267', 'org_267', 'P', 'p267', 'P')`)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_267', 'org_267', 'prj_267', 1, 'T')`)
	mustExec(t, owner, `INSERT INTO review_questions (id, organization_id, task_id, n, category, question, addressee)
		VALUES ('rvq_a', 'org_267', 'wi_267', 1, 'correctness', 'Q?', 'person'), ('rvq_b', 'org_267', 'wi_267', 2, 'correctness', 'Q?', 'conductor')`)
	if _, err := owner.Exec(ctx, `INSERT INTO review_questions (id, organization_id, task_id, n, category, question, addressee)
		VALUES ('rvq_c', 'org_267', 'wi_267', 3, 'conductor', 'Q?', 'author')`); err == nil {
		t.Fatal("an author question before 267")
	}
	apply()
	if n := scalar[int](t, owner, `SELECT count(*) FROM review_questions WHERE task_id = 'wi_267'`); n != 2 {
		t.Fatalf("%d questions kept", n)
	}
	mustExec(t, owner, `INSERT INTO review_questions (id, organization_id, task_id, n, category, question, addressee, asked_of, github_comment_id, github_url)
		VALUES ('rvq_c', 'org_267', 'wi_267', 3, 'conductor', 'Q?', 'author', 'alice', 'issue-comment-1', 'https://x')`)
	if _, err := owner.Exec(ctx, `INSERT INTO review_questions (id, organization_id, task_id, n, category, question, addressee)
		VALUES ('rvq_d', 'org_267', 'wi_267', 4, 'conductor', 'Q?', 'someone')`); err == nil {
		t.Fatal("an unknown addressee")
	}
	// Added NOT VALID: no validation scan under ACCESS EXCLUSIVE.
	if v := scalar[bool](t, owner, `SELECT convalidated FROM pg_constraint WHERE conname = 'review_questions_addressee_check'`); v {
		t.Error("the widened CHECK was validated by a full scan")
	}
	// The held-question withdrawal (248) leaves the author's question open.
	mustExec(t, owner, `SELECT withdraw_held_review_questions('wi_267')`)
	if s := scalar[string](t, owner, `SELECT status FROM review_questions WHERE id = 'rvq_c'`); s != "open" {
		t.Errorf("the author's question is %s after the held withdrawal", s)
	}
}
