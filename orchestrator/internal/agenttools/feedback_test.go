package agenttools_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// feedbackFixture is the fixture task with Ana's submitted item and her
// private draft, and a repository the task changes.
func feedbackFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_ana_'||$1, $1, 'Ana')`, f.org)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_'||$1, $1, $2, 'api', 'https://github.com/acme/api')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ($1, 'repo_'||$2, $2, 'write')`, f.item, f.org)
	mustExec(t, f.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		VALUES ('fbt_1', $1, $2, 1, 'per_ana_'||$1, 'k')`, f.org, f.item)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor, status, submitted_at) VALUES
		('fbi_open', $1, $2, 1, 'fbt_1', 'per_ana_'||$1, 'Name the flag --dry-run, as git does.', 'general', '{"kind":"general"}', 'open', now()),
		('fbi_two', $1, $2, 1, 'fbt_1', 'per_ana_'||$1, 'And show it in --help.', 'general', '{"kind":"general"}', 'open', now())`, f.org, f.item)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor)
		VALUES ('fbi_draft', $1, $2, 1, 'per_ana_'||$1, 'secret draft words', 'general', '{"kind":"general"}')`, f.org, f.item)
	return f
}

// An implementer lists the open items in their words, never a draft,
// replies to one and resolves the other with HEAD from its checkout; a
// reviewer has none of it, and the conductor reads and replies but does
// not resolve.
func TestFeedbackFromTheShellByRole(t *testing.T) {
	f := feedbackFixture(t)
	token := f.run(t, "run_fix", "implementer", "running")
	bin := cli(t)
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "fix"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env, cmd.Dir = env, repo
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	out, err := dude("feedback")
	if err != nil || !strings.Contains(out, "Name the flag --dry-run, as git does.") || !strings.Contains(out, `"fbi_two"`) ||
		strings.Contains(out, "fbi_draft") || strings.Contains(out, "secret draft") {
		t.Fatalf("dude feedback: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "fbi_draft"); err != nil || !strings.Contains(out, `"unknown"`) || strings.Contains(out, "secret draft") {
		t.Errorf("a draft by id: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "reply", "fbi_two", "The help already lists every flag"); err != nil || !strings.Contains(out, `"replied"`) {
		t.Errorf("reply: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "resolve", "fbi_open", "--commit", "HEAD"); err != nil || !strings.Contains(out, strings.TrimSpace(string(head))) {
		t.Errorf("resolve HEAD: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "resolve", "fbi_draft", "--commit", "HEAD"); err == nil {
		t.Errorf("resolving a draft: %s", out)
	}
	if out, err := dude("feedback", "resolve", "fbi_two", "--commit", "nosuchref"); err == nil || !strings.Contains(out, "git does not know") {
		t.Errorf("an unknown commit: %v %s", err, out)
	}
	var status, commit string
	_ = f.owner.QueryRow(context.Background(), `SELECT status, resolved_commit FROM feedback_items WHERE id = 'fbi_open'`).Scan(&status, &commit)
	if status != "resolved" || commit != strings.TrimSpace(string(head)) {
		t.Errorf("fbi_open: %s %s", status, commit)
	}
	// A refused call never records the draft's words.
	var leaked int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE payload::text LIKE '%secret draft%'`).Scan(&leaked)
	if leaked != 0 {
		t.Error("the ledger has a draft's words")
	}

	reviewer := f.run(t, "run_rev", "reviewer", "running")
	var res map[string]any
	for _, tool := range []string{"feedback", "feedback_reply", "feedback_resolve"} {
		if status := f.postAs(t, reviewer, tool, `{}`, &res); status != 404 {
			t.Errorf("a reviewer's %s: %d", tool, status)
		}
	}
	conductor := f.conductor(t)
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if status := f.postAs(t, conductor, "feedback", `{"all":true}`, &listed); status != 200 {
		t.Errorf("the conductor's feedback: %d %v", status, listed)
	}
	var got []string
	for _, it := range listed.Items {
		got = append(got, it.ID)
	}
	if !slices.Contains(got, "fbi_open") || slices.Contains(got, "fbi_draft") {
		t.Errorf("the conductor's feedback lists %v", got)
	}
	if status := f.postAs(t, conductor, "feedback_resolve", `{"id":"fbi_two","commit":"abcdef1"}`, &res); status != 404 {
		t.Errorf("the conductor's resolve: %d", status)
	}
}

// The conductor at the decision before the pull request, with a person's
// item open: open_pull_request is refused naming the feedback, and a fix
// for it is taken.
func TestTheConductorOpensNoPullRequestPastOpenFeedback(t *testing.T) {
	f := feedbackFixture(t)
	conductor := f.conductor(t)
	mustExec(t, f.owner, `UPDATE feedback_items SET status = 'replied' WHERE id = 'fbi_two'`)
	mustExec(t, f.owner, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id,
			awaiting_signals)
		VALUES ('wfr_g', $1, 'task.delivery', 'delivery:g', 'waiting', 'conductorDecision',
			jsonb_build_object('taskId', $2::text, 'decider', 'conductor', 'attempt', 1, 'policy', jsonb_build_object('maxReviewIterations', 5,
				'maxAttemptsPerFinding', 2), 'decision', jsonb_build_object('point', 'before_pull_request', 'policy', 'openPullRequest')),
			$2, '["conductor.decision"]')`, f.org, f.item)
	var refused map[string]any
	if status := f.postAs(t, conductor, "decide", `{"action":"open_pull_request"}`, &refused); status != 422 ||
		!strings.Contains(fmt.Sprint(refused["error"]), "feedback items from people are not handled") {
		t.Fatalf("open_pull_request with an item open: %d %v", status, refused)
	}
	var taken map[string]any
	if status := f.postAs(t, conductor, "start_phase", `{"phase":"fix"}`, &taken); status != 200 {
		t.Fatalf("start_phase fix: %d %v", status, taken)
	}
	var action, phase string
	_ = f.owner.QueryRow(context.Background(), `SELECT state->'decision'->'taken'->>'action', state->'decision'->'taken'->>'phase'
		FROM workflow_runs WHERE id = 'wfr_g'`).Scan(&action, &phase)
	if action != "start_phase" || phase != "fix" {
		t.Errorf("the decision taken: %s %s", action, phase)
	}
}

// The conductor passes items to a fixer by id: dude writes their words
// into the steer, never a draft, never to a reviewer.
func TestTheConductorPassesFeedbackByIDInThePersonsWords(t *testing.T) {
	f := feedbackFixture(t)
	conductor := f.conductor(t)
	f.run(t, "run_fix", "implementer", "running")
	f.run(t, "run_rev", "reviewer", "running")
	var res map[string]any
	if status := f.postAs(t, conductor, "steer", `{"run":"run_fix","text":"Start with these.","feedbackIds":["fbi_open"]}`, &res); status != 200 {
		t.Fatalf("steer with feedback: %d %v", status, res)
	}
	var text string
	var ids []string
	_ = f.owner.QueryRow(context.Background(), `SELECT d.text, r.feedback_item_ids FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE d.run_id = 'run_fix'`).Scan(&text, &ids)
	if !strings.HasPrefix(text, "Start with these.") || !strings.Contains(text, "### fbi_open — Ana") ||
		strings.Count(text, "> Name the flag --dry-run, as git does.") != 1 || strings.Contains(text, "fbi_two") {
		t.Errorf("the steer:\n%s", text)
	}
	if len(ids) != 1 || ids[0] != "fbi_open" {
		t.Errorf("the fixer's items: %v", ids)
	}
	// The ledger's run.steered has the conductor's words and the item's id,
	// never the person's words.
	var said string
	var steeredIDs []string
	_ = f.owner.QueryRow(context.Background(), `SELECT payload->>'text', ARRAY(SELECT jsonb_array_elements_text(payload->'feedbackItemIds'))
		FROM events WHERE run_id = 'run_fix' AND event_type = 'run.steered'`).Scan(&said, &steeredIDs)
	if said != "Start with these." || len(steeredIDs) != 1 || steeredIDs[0] != "fbi_open" {
		t.Errorf("run.steered: text %q, items %v", said, steeredIDs)
	}
	var leaked int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE payload::text LIKE '%Name the flag%'`).Scan(&leaked)
	if leaked != 0 {
		t.Error("the ledger carries the person's words")
	}
	for body, why := range map[string]string{
		`{"run":"run_rev","feedbackIds":["fbi_open"]}`:  "a reviewer",
		`{"run":"run_fix","feedbackIds":["fbi_draft"]}`: "a draft",
		`{"run":"run_fix","feedbackIds":["find_x"]}`:    "a finding's id",
	} {
		if status := f.postAs(t, conductor, "steer", body, &res); status == 200 {
			t.Errorf("steering %s's feedback was accepted", why)
		}
	}
	var n int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM directives WHERE text LIKE '%secret draft%' OR run_id = 'run_rev'`).Scan(&n)
	if n != 0 {
		t.Error("a refused steer was queued")
	}
}

// A person's Interrupt now on the conductor's feedback steer sends the
// run.steered text back: the agent gets the person's words again, once, as
// the same instruction, settled to the conductor.
func TestInterruptNowOnAConductorsFeedbackSteerResendsThePersonsWords(t *testing.T) {
	f := feedbackFixture(t)
	conductor := f.conductor(t)
	f.run(t, "run_fix", "implementer", "running")
	var res map[string]any
	if status := f.postAs(t, conductor, "steer", `{"run":"run_fix","text":"Start with these.","feedbackIds":["fbi_open"]}`, &res); status != 200 {
		t.Fatalf("steer with feedback: %d %v", status, res)
	}
	ctx := context.Background()
	var first, said string
	_ = f.owner.QueryRow(ctx, `SELECT payload->>'directiveId', payload->>'text' FROM events
		WHERE run_id = 'run_fix' AND event_type = 'run.steered'`).Scan(&first, &said)
	var again delivery.Steered
	if err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		var err error
		again, err = delivery.Steer(ctx, tx, f.org, delivery.SteerInput{RunID: "run_fix", Text: said, Supersedes: first,
			Interrupt: true, Actor: delivery.Writer{ActorType: ledger.ActorHuman, ActorID: "per_ana_" + f.org}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var text string
	var resends, conductorRun *string
	var interruptOnly *bool
	_ = f.owner.QueryRow(ctx, `SELECT text, resends, interrupt_only, conductor_run_id FROM directives WHERE id = $1`, again.ID).
		Scan(&text, &resends, &interruptOnly, &conductorRun)
	if !strings.HasPrefix(text, "Start with these.") || strings.Count(text, "> Name the flag --dry-run, as git does.") != 1 {
		t.Errorf("the resent steer:\n%s", text)
	}
	if resends == nil || *resends != first || interruptOnly != nil {
		t.Errorf("resends %v, interrupt_only %v: want %s and unset", resends, interruptOnly, first)
	}
	if conductorRun == nil || *conductorRun != "run_cond" {
		t.Errorf("conductor_run_id %v, want run_cond", conductorRun)
	}
	var leaked int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE payload::text LIKE '%Name the flag%'`).Scan(&leaked)
	if leaked != 0 {
		t.Error("the ledger carries the person's words")
	}
}

// A Retry of a conductor's feedback steer that failed unread gives the
// items to the resend, so its reading delivers them; a person's new words
// superseding it are sent as written, and a steer of another Run naming it
// is not given its words.
func TestAResendOfAFailedFeedbackSteerDeliversItsItems(t *testing.T) {
	f := feedbackFixture(t)
	conductor := f.conductor(t)
	f.run(t, "run_fix", "implementer", "running")
	f.run(t, "run_other", "implementer", "running")
	var res map[string]any
	if status := f.postAs(t, conductor, "steer", `{"run":"run_fix","text":"Start with these.","feedbackIds":["fbi_open"]}`, &res); status != 200 {
		t.Fatalf("steer with feedback: %d %v", status, res)
	}
	ctx := context.Background()
	var first, said string
	_ = f.owner.QueryRow(ctx, `SELECT payload->>'directiveId', payload->>'text' FROM events
		WHERE run_id = 'run_fix' AND event_type = 'run.steered'`).Scan(&first, &said)
	steer := func(in delivery.SteerInput) delivery.Steered {
		t.Helper()
		in.Actor = delivery.Writer{ActorType: ledger.ActorHuman, ActorID: "per_ana_" + f.org}
		var out delivery.Steered
		if err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
			var err error
			out, err = delivery.Steer(ctx, tx, f.org, in)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	textOf := func(id string) string {
		var text string
		_ = f.owner.QueryRow(ctx, `SELECT text FROM directives WHERE id = $1`, id).Scan(&text)
		return text
	}

	// Another Run naming this steer is not given its people's words.
	other := steer(delivery.SteerInput{RunID: "run_other", Text: said, Supersedes: first})
	if got := textOf(other.ID); got != said {
		t.Errorf("another Run's steer was given:\n%s", got)
	}
	// New words superseding it are sent as written, with no items.
	fresh := steer(delivery.SteerInput{RunID: "run_fix", Text: "Something else entirely.", Supersedes: first})
	if got := textOf(fresh.ID); got != "Something else entirely." {
		t.Errorf("new words were sent as:\n%s", got)
	}
	var carried bool
	_ = f.owner.QueryRow(ctx, `SELECT payload ? 'feedbackItemIds' FROM events WHERE payload->>'directiveId' = $1`, fresh.ID).Scan(&carried)
	if carried {
		t.Error("new words carry the feedback items")
	}

	// The first send fails unread; the person's Retry delivers the item.
	if err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		return delivery.FeedbackUnheardTx(ctx, tx, first)
	}); err != nil {
		t.Fatal(err)
	}
	again := steer(delivery.SteerInput{RunID: "run_fix", Text: said, Supersedes: first})
	var directive, deliveredRun *string
	_ = f.owner.QueryRow(ctx, `SELECT directive_id, delivered_run_id FROM feedback_items WHERE id = 'fbi_open'`).
		Scan(&directive, &deliveredRun)
	if directive == nil || *directive != again.ID || deliveredRun == nil || *deliveredRun != "run_fix" {
		t.Fatalf("the item after the Retry: directive %v, run %v; want %s on run_fix", directive, deliveredRun, again.ID)
	}
	if err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		return delivery.FeedbackHeardTx(ctx, tx, f.org, again.ID)
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = f.owner.QueryRow(ctx, `SELECT status FROM feedback_items WHERE id = 'fbi_open'`).Scan(&status)
	if status != "delivered" {
		t.Errorf("the item is %s once the Retry is read, want delivered", status)
	}
}
