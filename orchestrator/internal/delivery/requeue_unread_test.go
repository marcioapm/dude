package delivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A Run stopped before its agent read what it was given, and taken back
// up: the copy RequeueUnreadTx queues carries the original's feedback
// items and peer messages, so the resumed agent reading it marks them
// heard and read. A turn's nudge and a conductor's steer are not queued
// again.
func TestARequeuedDirectiveCarriesItsFeedbackAndPeerMessages(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_q', $1, 'P', 'p', 'Q')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_q', $1, 'prj_q', 1, 'Fix', 'fix', 'running'), ('wi_peer', $1, 'prj_q', 2, 'Peer', 'peer', 'running')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase)
			VALUES ('run_fix', $1, 'prj_q', 'wi_q', 1, 'running', 'fix'),
			       ('run_peer', $1, 'prj_q', 'wi_peer', 1, 'running', 'implement'),
			       ('run_cond', $1, 'prj_q', 'wi_q', 1, 'running', NULL)`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_q_'||$1, $1, 'Ana')`,
		`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
			VALUES ('fbt_q', $1, 'wi_q', 1, 'per_q_'||$1, 'k')`,
		`INSERT INTO directives (id, organization_id, task_id, run_id, text, scope, created_at) VALUES
			('dir_brief', $1, 'wi_q', 'run_fix', '## Review findings', 'run', now() - interval '4 seconds'),
			('dir_peer', $1, 'wi_q', 'run_fix', 'A message from QA-2', 'run', now() - interval '3 seconds'),
			('dir_retry', $1, 'wi_q', 'run_fix', 'carry on after the temporary error', 'turn', now() - interval '2 seconds')`,
		`INSERT INTO directives (id, organization_id, task_id, run_id, text, scope, conductor_run_id, created_at) VALUES
			('dir_steer', $1, 'wi_q', 'run_fix', 'Look at the tests too', 'run', 'run_cond', now() - interval '1 second')`,
		`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor,
				status, submitted_at, directive_id, delivered_run_id)
			VALUES ('fbi_q', $1, 'wi_q', 1, 'fbt_q', 'per_q_'||$1, 'Rename it', 'general', '{"kind":"general"}', 'open', now(),
				'dir_brief', 'run_fix')`,
		`UPDATE runs SET feedback_item_ids = '{fbi_q}' WHERE id = 'run_fix'`,
		`INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text, directive_id, delivered_run_id)
			VALUES ('pm_q', $1, 'wi_peer', 'run_peer', 'wi_q', 'Which endpoint?', 'dir_peer', 'run_fix')`,
	} {
		args := []any{org}
		if !strings.Contains(sql, "$1") {
			args = nil
		}
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	ref := RunRef{Org: org, ProjectID: "prj_q", TaskID: "wi_q", RunID: "run_fix"}
	var copies map[string]string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'aborted', ended_at = now() WHERE id = 'run_fix'`); err != nil {
			return err
		}
		if err := FailUnreadTx(ctx, tx, ref, time.Time{}, UnreadRunAborted); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'paused' WHERE id = 'run_fix'`); err != nil {
			return err
		}
		if err := RequeueUnreadTx(ctx, tx, []string{"run_fix"}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT resends, id FROM directives WHERE run_id = 'run_fix' AND resends IS NOT NULL`)
		if err != nil {
			return err
		}
		copies = map[string]string{}
		for rows.Next() {
			var from, to string
			if err := rows.Scan(&from, &to); err != nil {
				return err
			}
			copies[from] = to
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 || copies["dir_brief"] == "" || copies["dir_peer"] == "" {
		t.Fatalf("requeued %v, want the brief and the peer message only (not the turn retry nor the conductor's steer)", copies)
	}
	// The resumed agent reads the copies.
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		for _, id := range copies {
			if err := FeedbackHeardTx(ctx, tx, org, id); err != nil {
				return err
			}
			if err := CoordinationReadTx(ctx, tx, org, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var heardBy *string
	if err := owner.QueryRow(ctx, `SELECT status::text, delivered_run_id FROM feedback_items WHERE id = 'fbi_q'`).Scan(&status, &heardBy); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || heardBy == nil || *heardBy != "run_fix" {
		t.Errorf("the brief's feedback item is %s (delivered to %v) after the copy was read, want delivered to run_fix", status, heardBy)
	}
	var read bool
	if err := owner.QueryRow(ctx, `SELECT read_at IS NOT NULL FROM peer_messages WHERE id = 'pm_q'`).Scan(&read); err != nil {
		t.Fatal(err)
	}
	if !read {
		t.Errorf("the peer message is unread after its copy was read")
	}
}

// A turn's nudge a stop failed unread (a turn retry, a move's or idle
// carry-on) was about the turn the stop ended: taking the Run back up
// does not give it again.
func TestATurnsNudgeIsNotRequeued(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_n', $1, 'P', 'p', 'N')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_n', $1, 'prj_n', 1, 'Fix', 'fix', 'running')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase) VALUES ('run_n', $1, 'prj_n', 'wi_n', 1, 'paused', 'fix')`,
		`INSERT INTO directives (id, organization_id, task_id, run_id, text, scope, failed_at, error) VALUES
			('dir_retry_n', $1, 'wi_n', 'run_n', 'carry on after the temporary error', 'turn', now(), '` + UnreadRunAborted + `'),
			('dir_brief_n', $1, 'wi_n', 'run_n', '## Review findings', 'run', now(), '` + UnreadRunAborted + `')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	var resent []string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := RequeueUnreadTx(ctx, tx, []string{"run_n"}); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(array_agg(resends ORDER BY resends), '{}') FROM directives
			WHERE run_id = 'run_n' AND resends IS NOT NULL`).Scan(&resent)
	}); err != nil {
		t.Fatal(err)
	}
	if len(resent) != 1 || resent[0] != "dir_brief_n" {
		t.Fatalf("requeued copies of %v, want only the brief", resent)
	}
}
