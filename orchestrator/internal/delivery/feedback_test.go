package delivery_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// feedbackWorld is ABS-1 (epicWorld) with two people, Ana and Bo, and a
// delivery whose step and decider each test sets.
type feedbackWorld struct {
	*epicWorld
	task, ana, bo string
}

func newFeedbackWorld(t *testing.T) *feedbackWorld {
	t.Helper()
	w := &feedbackWorld{epicWorld: newEpicWorld(t)}
	w.task = w.tasks["ABS-1"]
	w.ana, w.bo = "per_ana"+w.org, "per_bo"+w.org
	w.exec(t, `INSERT INTO people (id, organization_id, name) VALUES ($1, $3, 'Ana'), ($2, $3, 'Bo')`, w.ana, w.bo, w.org)
	return w
}

// delivery makes the task's delivery, waiting at step, decided by decider.
func (w *feedbackWorld) delivery(t *testing.T, step, decider string, awaiting string) string {
	t.Helper()
	id := "wfr_" + w.org
	w.exec(t, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id, awaiting_signals)
		VALUES ($1, $2, 'task.delivery', 'delivery:'||$3, 'waiting', $4,
			jsonb_build_object('taskId', $3::text, 'projectId', $5::text, 'decider', $6::text, 'attempt', 1,
				'policy', jsonb_build_object('maxReviewIterations', 5, 'maxAttemptsPerFinding', 2, 'blockingSeverities', '["blocking","high"]'::jsonb)),
			$3, $7::jsonb)`, id, w.org, w.task, step, w.project, decider, awaiting)
	return id
}

// draft is a person's draft on the task's attempt 1.
func (w *feedbackWorld) draft(t *testing.T, id, person, body string) {
	t.Helper()
	w.exec(t, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor)
		VALUES ($1, $2, $3, 1, $4, $5, 'general', '{"kind":"general"}')`, id, w.org, w.task, person, body)
}

type noStarter struct{ started int }

// time0: events at now.
var time0 time.Time

func (n *noStarter) StartInTx(ctx context.Context, tx pgx.Tx, o workflow.StartOptions) (string, bool, error) {
	n.started++
	return "wfr_started", false, nil
}

func (w *feedbackWorld) submit(t *testing.T, person, key string, drafts ...string) (delivery.Submitted, error) {
	t.Helper()
	var out delivery.Submitted
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		out, err = delivery.SubmitFeedbackTx(context.Background(), tx, &noStarter{}, delivery.Submission{
			Org: w.org, TaskID: w.task, Person: person, Key: key, DraftIDs: drafts,
			Writer: delivery.Writer{ActorType: "person", ActorID: person, Person: person}})
		return err
	})
	return out, err
}

func (w *feedbackWorld) status(t *testing.T, item string) string {
	t.Helper()
	var s string
	if err := w.owner.QueryRow(context.Background(), `SELECT status FROM feedback_items WHERE id = $1`, item).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A submit moves only the person's own drafts on the current attempt, all
// or none, and the same key again is the same batch, routed once.
func TestASubmitIsAtomicPrivateAndIdempotent(t *testing.T) {
	w := newFeedbackWorld(t)
	w.run(t, "run_impl", "ABS-1", "implement", "implementer", "running")
	w.draft(t, "fbi_a1", w.ana, "Rename the flag")
	w.draft(t, "fbi_a2", w.ana, "And document it")
	w.draft(t, "fbi_b1", w.bo, "Bo's private thought")

	var fe delivery.FeedbackError
	if _, err := w.submit(t, w.ana, "k1", "fbi_a1", "fbi_b1"); !errors.As(err, &fe) || fe.Kind != "bad_request" ||
		strings.Contains(fe.Msg, "fbi_b1") {
		t.Fatalf("a submit naming another's draft: %v", err)
	}
	if w.status(t, "fbi_a1") != "draft" || w.status(t, "fbi_b1") != "draft" {
		t.Fatal("a refused submit moved a draft")
	}
	first, err := w.submit(t, w.ana, "k1", "fbi_a1", "fbi_a2")
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.submit(t, w.ana, "k1", "fbi_a2", "fbi_a1")
	if err != nil || again.BatchID != first.BatchID || !again.Repeated {
		t.Fatalf("the same key again: %+v %v (first %+v)", again, err, first)
	}
	if n := w.count(t, `SELECT count(*) FROM feedback_batches`); n != 1 {
		t.Errorf("%d batches", n)
	}
	if n := w.count(t, `SELECT count(*) FROM directives WHERE run_id = 'run_impl'`); n != 1 {
		t.Errorf("%d directives for one batch submitted twice", n)
	}
	if _, err := w.submit(t, w.ana, "k1", "fbi_a1"); !errors.As(err, &fe) || fe.Kind != "conflict" {
		t.Errorf("a key reused for other drafts: %v", err)
	}
	// Bo's draft never reached anyone: not the directive, not the ledger.
	if n := w.count(t, `SELECT count(*) FROM directives WHERE text LIKE '%Bo''s private%' OR text LIKE '%fbi_b1%'`); n != 0 {
		t.Error("a draft reached a directive")
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE payload::text LIKE '%fbi_b1%' OR payload::text LIKE '%Rename the flag%'`); n != 0 {
		t.Error("the ledger carries a draft id or an item's words")
	}
}

// Who hears of a batch first is who drives: the conductor deciding, else a
// working implementer or fixer, else the delivery's own steps. A
// conductor that is live while the policy decides is not told; a reviewer
// is never steered; a publishing Run never.
func TestFeedbackGoesToWhoDrives(t *testing.T) {
	cases := []struct {
		name, decider, step string
		runs                []struct{ id, phase, role, status, extra string }
		want, wantRun       string
	}{
		{name: "conductor deciding", decider: "conductor", step: "awaitFix",
			runs: []struct{ id, phase, role, status, extra string }{{"run_fix", "fix", "implementer", "running", ""}, {"run_c", "", "conductor", "running", ""}},
			want: delivery.RoutedConductor},
		{name: "policy with a live conductor and a fixer", decider: "policy", step: "awaitFix",
			runs: []struct{ id, phase, role, status, extra string }{{"run_fix", "fix", "implementer", "running", ""}, {"run_c", "", "conductor", "running", ""}},
			want: delivery.RoutedSteer, wantRun: "run_fix"},
		{name: "a reviewer working", decider: "policy", step: "awaitReview",
			runs: []struct{ id, phase, role, status, extra string }{{"run_rev", "review", "reviewer", "running", ""}, {"run_c", "", "conductor", "running", ""}},
			want: delivery.RoutedWorkflow},
		{name: "a fixer publishing", decider: "policy", step: "awaitFix",
			runs: []struct{ id, phase, role, status, extra string }{{"run_fix", "fix", "implementer", "running", "turn_done_at = now()"}},
			want: delivery.RoutedWorkflow},
		{name: "a fixer aborting", decider: "policy", step: "awaitFix",
			runs: []struct{ id, phase, role, status, extra string }{{"run_fix", "fix", "implementer", "running", "control = 'abort'"}},
			want: delivery.RoutedWorkflow},
		{name: "an implementer waiting on a person's answer", decider: "policy", step: "awaitImplement",
			runs: []struct{ id, phase, role, status, extra string }{{"run_impl", "implement", "implementer", "paused", "dude_pause = 'person', waiting_since = now()"}},
			want: delivery.RoutedSteer, wantRun: "run_impl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newFeedbackWorld(t)
			wf := w.delivery(t, c.step, c.decider, `["phase.finished"]`)
			for _, r := range c.runs {
				w.run(t, r.id, "ABS-1", r.phase, r.role, r.status)
				if r.extra != "" {
					w.exec(t, `UPDATE runs SET `+r.extra+` WHERE id = $1`, r.id)
				}
			}
			w.draft(t, "fbi_1", w.ana, "The VAT rate for PT is 23%, not 20%.")
			out, err := w.submit(t, w.ana, "k", "fbi_1")
			if err != nil {
				t.Fatal(err)
			}
			if out.RoutedTo != c.want || out.RunID != c.wantRun {
				t.Fatalf("routed %+v, want %s %s", out, c.want, c.wantRun)
			}
			directives := w.count(t, `SELECT count(*) FROM directives WHERE text LIKE '%The VAT rate for PT is 23%'`)
			wakes := w.count(t, `SELECT count(*) FROM conductor_wakes WHERE kind = 'feedback'`)
			switch c.want {
			case delivery.RoutedSteer:
				var text string
				var interrupt bool
				var runID string
				if err := w.owner.QueryRow(context.Background(), `SELECT run_id, text, interrupt FROM directives`).Scan(&runID, &text, &interrupt); err != nil {
					t.Fatal(err)
				}
				if runID != c.wantRun || interrupt || !strings.Contains(text, "### fbi_1 — Ana") ||
					!strings.Contains(text, "> The VAT rate for PT is 23%, not 20%.") {
					t.Errorf("the steer to %s: interrupt %v\n%s", runID, interrupt, text)
				}
				if wakes != 0 {
					t.Error("a policy delivery woke the conductor")
				}
				// Steered, not yet read: open, and no other Run was steered.
				if s := w.status(t, "fbi_1"); s != "open" {
					t.Errorf("before the read: %s", s)
				}
			case delivery.RoutedConductor:
				var line string
				if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE kind = 'feedback'`).Scan(&line); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(line, "fbi_1") || strings.Contains(line, "23%") {
					t.Errorf("the conductor's reason: %q", line)
				}
				if directives != 0 {
					t.Error("under the conductor a worker was steered directly")
				}
				var decider string
				_ = w.owner.QueryRow(context.Background(), `SELECT state->>'decider' FROM workflow_runs WHERE id = $1`, wf).Scan(&decider)
				if decider != "conductor" {
					t.Errorf("the decider changed to %s", decider)
				}
			case delivery.RoutedWorkflow:
				if directives != 0 || wakes != 0 {
					t.Errorf("the workflow's: %d directives, %d wakes", directives, wakes)
				}
			}
		})
	}
}

// The steered Run reading the directive delivers the items; failing to,
// or ending without settling them, leaves them open for the next fixer.
func TestASteerReadDeliversAndAnUnreadEndReopens(t *testing.T) {
	w := newFeedbackWorld(t)
	w.delivery(t, "awaitFix", "policy", `["phase.finished"]`)
	ref := w.run(t, "run_fix", "ABS-1", "fix", "implementer", "running")
	w.draft(t, "fbi_1", w.ana, "one")
	if _, err := w.submit(t, w.ana, "k", "fbi_1"); err != nil {
		t.Fatal(err)
	}
	var dir string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM directives`).Scan(&dir)
	w.in(t, func(tx pgx.Tx) error { return delivery.FeedbackHeardTx(context.Background(), tx, w.org, dir) })
	if s := w.status(t, "fbi_1"); s != "delivered" {
		t.Fatalf("after the read: %s", s)
	}
	var ids []string
	_ = w.owner.QueryRow(context.Background(), `SELECT feedback_item_ids FROM runs WHERE id = 'run_fix'`).Scan(&ids)
	if len(ids) != 1 || ids[0] != "fbi_1" {
		t.Errorf("the Run's items: %v", ids)
	}
	w.exec(t, `UPDATE runs SET status = 'failed', ended_at = now() WHERE id = 'run_fix'`)
	w.in(t, func(tx pgx.Tx) error {
		return delivery.FailUnreadTx(context.Background(), tx, ref, time0, "the run failed before the agent read it")
	})
	if s := w.status(t, "fbi_1"); s != "open" {
		t.Errorf("after its Run failed: %s", s)
	}
}

// Replies, resolutions and reopenings: by any member, never on a draft,
// never on an earlier attempt; a reply blocks nothing, a reopen routes.
func TestPeopleReplyResolveAndReopen(t *testing.T) {
	w := newFeedbackWorld(t)
	w.delivery(t, "awaitReview", "policy", `["phase.finished"]`)
	w.draft(t, "fbi_1", w.ana, "one")
	w.draft(t, "fbi_draft", w.ana, "still private")
	if _, err := w.submit(t, w.ana, "k", "fbi_1"); err != nil {
		t.Fatal(err)
	}
	act := func(item, person, body string) delivery.PersonAction {
		return delivery.PersonAction{Org: w.org, ItemID: item, Person: person, Body: body,
			Writer: delivery.Writer{ActorType: "person", ActorID: person, Person: person}}
	}
	var fe delivery.FeedbackError
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.PersonReplyTx(context.Background(), tx, act("fbi_draft", w.bo, "peek"))
	})
	if !errors.As(err, &fe) || fe.Kind != "not_found" {
		t.Errorf("Bo replying to Ana's draft: %v", err)
	}
	w.in(t, func(tx pgx.Tx) error {
		return delivery.PersonReplyTx(context.Background(), tx, act("fbi_1", w.bo, "Agreed, low priority"))
	})
	if s := w.status(t, "fbi_1"); s != "replied" {
		t.Errorf("after Bo's reply: %s", s)
	}
	var blocking int
	w.in(t, func(tx pgx.Tx) (err error) {
		blocking, err = delivery.BlockingFeedbackTx(context.Background(), tx, w.task, 1)
		return err
	})
	if blocking != 0 {
		t.Errorf("a replied item blocks: %d", blocking)
	}
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.PersonReopenTx(context.Background(), tx, &noStarter{}, act("fbi_1", w.ana, "No, it matters"))
		return err
	})
	if s := w.status(t, "fbi_1"); s != "open" {
		t.Errorf("after the reopen: %s", s)
	}
	w.in(t, func(tx pgx.Tx) error {
		return delivery.PersonResolveTx(context.Background(), tx, act("fbi_1", w.bo, ""))
	})
	if s := w.status(t, "fbi_1"); s != "resolved" {
		t.Errorf("after Bo's resolve: %s", s)
	}
	if n := w.count(t, `SELECT count(*) FROM feedback_replies WHERE item_id = 'fbi_1'`); n != 3 {
		t.Errorf("history has %d entries", n)
	}
	// A start over: the earlier attempt's items are read-only.
	w.exec(t, `UPDATE workflow_runs SET state = jsonb_set(state, '{attempt}', '2')`)
	err = w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.PersonReopenTx(context.Background(), tx, &noStarter{}, act("fbi_1", w.ana, "again"))
		return err
	})
	if !errors.As(err, &fe) || fe.Kind != "conflict" {
		t.Errorf("reopening an earlier attempt's: %v", err)
	}
	w.in(t, func(tx pgx.Tx) (err error) {
		blocking, err = delivery.BlockingFeedbackTx(context.Background(), tx, w.task, 0)
		return err
	})
	if blocking != 0 {
		t.Errorf("the new attempt starts with %d blocking", blocking)
	}
}

// A person replying to an item an agent resolved with a commit not yet
// published is refused: the item keeps holding the gate.
func TestAReplyOnAPendingResolutionIsRefused(t *testing.T) {
	w := newFeedbackWorld(t)
	w.delivery(t, "awaitFix", "policy", `["phase.finished"]`)
	ref := w.run(t, "run_fix", "ABS-1", "fix", "implementer", "running")
	w.exec(t, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_'||$1, $1, $2, 'api', 'https://github.com/acme/api')`, w.org, w.project)
	w.exec(t, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ($1, 'repo_'||$2, $2, 'write')`, w.task, w.org)
	w.draft(t, "fbi_1", w.ana, "one")
	if _, err := w.submit(t, w.ana, "k", "fbi_1"); err != nil {
		t.Fatal(err)
	}
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.AgentResolveTx(context.Background(), tx, ref, delivery.Resolution{ItemID: "fbi_1", Commit: "abcdef1234"})
		return err
	})
	var fe delivery.FeedbackError
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.PersonReplyTx(context.Background(), tx, delivery.PersonAction{Org: w.org, ItemID: "fbi_1", Person: w.bo,
			Body: "is this really fixed?", Writer: delivery.Writer{ActorType: "person", ActorID: w.bo, Person: w.bo}})
	})
	if !errors.As(err, &fe) || fe.Kind != "conflict" || !strings.Contains(fe.Msg, "reopen") {
		t.Errorf("a reply on a pending resolution: %v", err)
	}
	var blocking int
	w.in(t, func(tx pgx.Tx) (err error) {
		blocking, err = delivery.BlockingFeedbackTx(context.Background(), tx, w.task, 0)
		return err
	})
	if blocking != 1 || w.status(t, "fbi_1") != "resolved" {
		t.Errorf("after the refused reply: %d blocking, %s", blocking, w.status(t, "fbi_1"))
	}
}

// A submit on a blocked task never delivered keeps the batch open and
// says why nobody works on it; nothing starts.
func TestASubmitOnABlockedTaskNeverDeliveredWaits(t *testing.T) {
	w := newFeedbackWorld(t)
	w.exec(t, `UPDATE tasks SET status = 'queued' WHERE id = $1`, w.task)
	w.exec(t, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $2, $3, 'blocks', $4)`, w.org, w.tasks["ABS-2"], w.task, w.ana)
	w.draft(t, "fbi_1", w.ana, "one")
	starter := &noStarter{}
	var out delivery.Submitted
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		out, err = delivery.SubmitFeedbackTx(context.Background(), tx, starter, delivery.Submission{
			Org: w.org, TaskID: w.task, Person: w.ana, Key: "k", DraftIDs: []string{"fbi_1"},
			Writer: delivery.Writer{ActorType: "person", ActorID: w.ana, Person: w.ana}})
		return err
	})
	if err != nil {
		t.Fatalf("submit on a blocked task: %v", err)
	}
	if !strings.Contains(out.Waiting, "blocked") || out.RoutedTo != delivery.RoutedWorkflow {
		t.Errorf("submitted %+v", out)
	}
	if s := w.status(t, "fbi_1"); s != "open" {
		t.Errorf("the item is %s", s)
	}
	if n := w.count(t, `SELECT count(*) FROM workflow_runs WHERE task_id = $1`, w.task); starter.started != 0 || n != 0 {
		t.Errorf("a blocked task started: %d starts, %d workflow runs", starter.started, n)
	}
}

// Done refuses; failed and aborted say Recover; the drafts stay.
func TestEndedTasksRefuseAndKeepTheDrafts(t *testing.T) {
	for status, want := range map[string]string{"done": "follow-up", "failed": "Recover", "aborted": "Recover"} {
		t.Run(status, func(t *testing.T) {
			w := newFeedbackWorld(t)
			w.draft(t, "fbi_1", w.ana, "late")
			w.exec(t, `UPDATE tasks SET status = $2::task_status WHERE id = $1`, w.task, status)
			_, err := w.submit(t, w.ana, "k", "fbi_1")
			var fe delivery.FeedbackError
			if !errors.As(err, &fe) || !strings.Contains(fe.Msg, want) || !strings.Contains(fe.Msg, "drafts are kept") {
				t.Fatalf("submit on a %s task: %v", status, err)
			}
			if s := w.status(t, "fbi_1"); s != "draft" {
				t.Errorf("the draft is %s", s)
			}
		})
	}
}

// An agent replies or resolves only its own task's current attempt's
// items; a resolution holds the gate until its commit is published, and a
// Run that ends without publishing reopens it.
func TestAnAgentsResolutionHoldsUntilPublished(t *testing.T) {
	w := newFeedbackWorld(t)
	w.delivery(t, "awaitFix", "policy", `["phase.finished"]`)
	ref := w.run(t, "run_fix", "ABS-1", "fix", "implementer", "running")
	other := w.run(t, "run_other", "ABS-2", "fix", "implementer", "running")
	w.exec(t, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_'||$1, $1, $2, 'api', 'https://github.com/acme/api')`, w.org, w.project)
	w.exec(t, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ($1, 'repo_'||$2, $2, 'write')`, w.task, w.org)
	for _, id := range []string{"fbi_1", "fbi_2"} {
		w.draft(t, id, w.ana, "fix "+id)
	}
	if _, err := w.submit(t, w.ana, "k", "fbi_1", "fbi_2"); err != nil {
		t.Fatal(err)
	}
	var fe delivery.FeedbackError
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.AgentResolveTx(context.Background(), tx, other, delivery.Resolution{ItemID: "fbi_1", Commit: "abcdef1"})
		return err
	})
	if !errors.As(err, &fe) || fe.Kind != "not_found" {
		t.Errorf("another task's Run resolving: %v", err)
	}
	err = w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.AgentResolveTx(context.Background(), tx, ref, delivery.Resolution{ItemID: "fbi_1", Commit: "not a sha"})
		return err
	})
	if !errors.As(err, &fe) || fe.Kind != "bad_request" {
		t.Errorf("a non-SHA commit: %v", err)
	}
	for _, id := range []string{"fbi_1", "fbi_2"} {
		w.in(t, func(tx pgx.Tx) error {
			_, err := delivery.AgentResolveTx(context.Background(), tx, ref, delivery.Resolution{ItemID: id, Commit: "abcdef1234"})
			return err
		})
	}
	blocking := func() int {
		var n int
		w.in(t, func(tx pgx.Tx) (err error) {
			n, err = delivery.BlockingFeedbackTx(context.Background(), tx, w.task, 0)
			return err
		})
		return n
	}
	if n := blocking(); n != 2 {
		t.Fatalf("resolved, unpublished: %d blocking, want 2", n)
	}
	// Published: fbi_1's commit held, fbi_2's did not.
	w.in(t, func(tx pgx.Tx) error {
		return delivery.SettleResolutionsTx(context.Background(), tx, w.org, "run_fix", map[string]bool{"fbi_1": true})
	})
	if n := blocking(); n != 1 || w.status(t, "fbi_2") != "open" {
		t.Errorf("after publishing: %d blocking, fbi_2 %s", n, w.status(t, "fbi_2"))
	}
	// A second resolution, then the Run fails: reopened.
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.AgentResolveTx(context.Background(), tx, ref, delivery.Resolution{ItemID: "fbi_2", Commit: "abcdef1234"})
		return err
	})
	w.exec(t, `UPDATE runs SET status = 'failed', ended_at = now() WHERE id = 'run_fix'`)
	w.in(t, func(tx pgx.Tx) error { return delivery.FailUnreadTx(context.Background(), tx, ref, time0, "failed") })
	if s := w.status(t, "fbi_2"); s != "open" {
		t.Errorf("a failed Run's resolution: %s", s)
	}
	if s := w.status(t, "fbi_1"); s != "resolved" {
		t.Errorf("a published resolution after the Run failed: %s", s)
	}
}

// The compare direction: a commit holds when it is in the published head
// (compare commit...head: behind_by 0) and new since the Run's base
// (compare base...commit: ahead_by > 0), as GitHub answers both.
func TestAResolutionsCommitMustBeInTheHeadAndNewSinceTheBase(t *testing.T) {
	// GitHub's compare of base...head, recorded on a repository where
	// base=b0, the Run committed c1 then c2 (head), and o9 is on another branch.
	history := map[string][2]int{ // base...head → ahead_by, behind_by
		"c1...c2": {1, 0}, "c2...c2": {0, 0}, "b0...c1": {1, 0}, "b0...c2": {2, 0},
		"o9...c2": {2, 1}, "b0...o9": {1, 0}, "b0...b0": {0, 0}, "pre...c2": {3, 0}, "b0...pre": {0, 1},
	}
	cmp := func(_ context.Context, _ string, base, head string) (int, int, error) {
		got, ok := history[base+"..."+head]
		if !ok {
			return 0, 0, errors.New("no comparison " + base + "..." + head)
		}
		return got[0], got[1], nil
	}
	for _, c := range []struct {
		commit string
		want   bool
	}{{"c1", true}, {"c2", true}, {"o9", false}, {"pre", false}, {"b0", false}} {
		got, err := delivery.ResolutionHolds(context.Background(), cmp, "api", "b0", "c2", c.commit)
		if err != nil || got != c.want {
			t.Errorf("%s holds = %v (%v), want %v", c.commit, got, err, c.want)
		}
	}
	if got, _ := delivery.ResolutionHolds(context.Background(), cmp, "api", "b0", "", "c1"); got {
		t.Error("nothing published, yet it holds")
	}
}

// What a fix Run is given: the items' ids, authors, anchors and words, and
// how to settle each; a restart of the Run reads the same.
func TestTheFixPromptCarriesTheOriginalWords(t *testing.T) {
	w := newFeedbackWorld(t)
	w.delivery(t, "awaitReview", "policy", `["phase.finished"]`)
	w.exec(t, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, run_id, anchor)
		VALUES ('fbi_code', $1, $2, 1, $3, 'Use the rate table, not a literal.', 'code', NULL,
		  '{"kind":"code","runId":"run_x","repo":"api","path":"src/vat.ts","side":"new","startLine":11,"endLine":12,
		    "baseSha":"b0","headSha":null,"excerpt":[{"kind":"+","old":null,"new":11,"text":"  const rate = 0.23;"}]}')`,
		w.org, w.task, w.ana)
	if _, err := w.submit(t, w.ana, "k", "fbi_code"); err != nil {
		t.Fatal(err)
	}
	w.run(t, "run_fix", "ABS-1", "fix", "implementer", "pending")
	w.exec(t, `UPDATE runs SET feedback_item_ids = '{fbi_code}' WHERE id = 'run_fix'`)
	var items []delivery.FeedbackItem
	w.in(t, func(tx pgx.Tx) (err error) {
		items, err = delivery.RunFeedbackTx(context.Background(), tx, "run_fix")
		return err
	})
	got := delivery.Prompt(delivery.PhaseFix, delivery.PromptInput{Title: "T", Feedback: items, CLI: true})
	for _, want := range []string{"## Feedback from people", "### fbi_code — Ana", "On `api/src/vat.ts:11-12 (new side)`, uncommitted work of Run run_x",
		"+  const rate = 0.23;", "> Use the rate table, not a literal.", "dude feedback resolve ID --commit HEAD", "dude feedback reply ID"} {
		if !strings.Contains(got, want) {
			t.Errorf("the fix prompt lacks %q", want)
		}
	}
}
