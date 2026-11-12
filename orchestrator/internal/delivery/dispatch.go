package delivery

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// run_workflow: the conductor starts a GitHub Actions workflow
// (workflow_dispatch) and dude supervises its run to the end.
//
// Binding never guesses. The call reserves its row (dispatching) before
// anything is sent, so the unique index on question_id refuses a second
// call spending the same answer before it reaches GitHub. The dispatch
// asks GitHub for the run's details, and the run id it answers with (200)
// is bound at once. An older GitHub (or GHES) answers 204 and no id: the
// dispatch went out, and dude cannot know which run it started, so it is
// unknown at once and the conductor is told to look with
// github_branch_runs. A row left dispatching (dude stopped between the
// reserve and GitHub's answer) is unknown past DispatchReservedFor, and is
// never sent again.
//
// Supervision is level-triggered: every pass re-reads the bound run's
// status, conclusion and attempt and stores what it saw
// (workflow_dispatches). What the conductor is told is decided from that
// stored state alone, never from an event that might have been missed; a
// restart resumes from the rows. A read that fails is "cannot tell",
// counted, never "not yet": past
// DispatchCannotSeeReads in a row the conductor is told dude cannot see
// the run.
//
// A terminal outcome is delivered when it reached the conductor, and only
// then (woken_outcome): as the held digest of a wait naming the dispatch,
// or as a wake of its own. While a wait naming it is open the supervisor
// leaves it to the wait; once none is (held, replaced, its conductor
// ended), every later pass that finds it owed and not delivered records
// its wake, keyed so it is recorded once.

// WakeWorkflow is the conductor_wakes kind of a dispatched workflow's
// terminal outcome.
const WakeWorkflow = "workflow"

// EvWorkflowDispatched is the ledger event of a dispatch, on the
// conductor's Run: {dispatchId, repo, workflow, ref, inputs, triggers, ci,
// runId?, questionId?}.
const EvWorkflowDispatched = "workflow.dispatched"

// Bounds of supervision, in code (no settings).
const (
	// DispatchReservedFor is how long a reserved dispatch may wait for
	// GitHub's answer before the supervisor ends it unknown: well past the
	// call's reads and its POST (forge's request timeout).
	DispatchReservedFor = 5 * time.Minute
	// DispatchFollowAfter is how long a completed run is still read: a
	// re-run (GitHub re-runs only a completed run, attempt n+1) reopens it.
	DispatchFollowAfter = 30 * time.Minute
	// DispatchCannotSeeReads is how many failed reads in a row make
	// "dude cannot see this workflow run".
	DispatchCannotSeeReads = 3
	// DispatchInputsMax bounds a dispatch's inputs (GitHub takes 25), and
	// DispatchInputChars each value.
	DispatchInputsMax  = 25
	DispatchInputChars = 1000
	workflowLineChars  = 600
	// workflowItemChars bounds a dispatch's item in a wait's digest, so
	// WaitMaxConditions of them fit its line.
	workflowItemChars = 200
)

// Dispatch states and outcomes (workflow_dispatches.state, outcome).
const (
	DispatchDispatching = "dispatching"
	DispatchBound       = "bound"
	DispatchCompleted   = "completed"
	DispatchUnknown     = "unknown"
	DispatchFailed      = "failed"
	DispatchCannotSee   = "cannot_see"
)

// Why a dispatch is unknown. Stored, so tool-free: where to look
// (github_branch_runs, or a person) is added when it is told, by
// lookForRunTmpl, as the conductor's grant says.
const (
	// GitHub answered 204: the dispatch went out, its run not named.
	dispatchNoRunID = "GitHub did not say which run this dispatch started"
	// The row stayed reserved: dude stopped between the reserve and
	// GitHub's answer, or GitHub's answer was lost.
	dispatchMaybe = "dude may or may not have dispatched this: it never heard GitHub's answer"
)

// DispatchApproval is the choice a person picks to authorize workflow on
// ref: the one answer to a question (ask_person) the gate accepts.
func DispatchApproval(workflow, ref string) string { return "Run " + workflow + " on " + ref }

// DispatchDecline is the other choice the refusal tells the conductor to
// offer.
const DispatchDecline = "Do not run it"

// DispatchAsk is a run_workflow call.
type DispatchAsk struct {
	Repo, Workflow, Ref string
	Inputs              map[string]string
	QuestionID          string
}

// DispatchCheck is a run_workflow call checked in its transaction: what to
// read and dispatch on GitHub outside it.
type DispatchCheck struct {
	ref                RunRef
	repoID, repo, slug string
	workflow, branch   string
	// The task's own branch and the base its pull request goes into: CI
	// is free only for that change.
	taskBranch, base string
	inputs           map[string]string
	questionID       string
	questionOK       bool
	gh               *forge.GitHub
	// id is the dispatch's row, reserved (dispatching) by the check.
	id string
}

// DispatchDone is what was done on GitHub: the workflow's triggers,
// whether it was free CI, whether the dispatch was sent (Posted), and the
// run GitHub answered with (0 for none: an older GitHub).
type DispatchDone struct {
	Triggers []string
	CI       bool
	Posted   bool
	Run      forge.DispatchedRun
}

// dispatchRef is a ref as GitHub's dispatch takes it: a branch or tag
// name, refs/heads/ and refs/tags/ stripped.
func dispatchRef(ref string) string {
	ref = strings.TrimSpace(ref)
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}

// CheckDispatchTx checks a run_workflow call: the live conductor of a task
// not ended, one of the task's repositories on GitHub, a workflow file
// name, the ref (the task's branch by default), the inputs, and the named
// question, if any: on this task, addressed to a person, offering the
// approving choice for this workflow and ref, answered with it, and not
// spent on another dispatch. It then reserves the dispatch's row
// (dispatching), before anything is sent: a second call with the same
// answer is refused by the row's unique question_id, here, never after a
// second POST.
func CheckDispatchTx(ctx context.Context, tx pgx.Tx, ref RunRef, gh *forge.GitHub, in DispatchAsk) (DispatchCheck, error) {
	c := DispatchCheck{ref: ref, gh: gh}
	if err := liveConductorOr(ctx, tx, ref, rYouAreNoLongerThisTask4); err != nil {
		return c, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, ref.TaskID).Scan(&status); err != nil {
		return c, err
	}
	if Ended(status) {
		return c, refusef(rThisTaskYouAreReadOnly, status)
	}
	c.workflow = strings.TrimSpace(in.Workflow)
	if !forge.WorkflowFile.MatchString(c.workflow) {
		return c, refusef(rDispatchWorkflowFile, in.Workflow)
	}
	repos, err := TaskRepositories(ctx, tx, ref.TaskID)
	if err != nil {
		return c, err
	}
	var names []string
	var repo Repository
	for _, r := range repos {
		names = append(names, r.Name)
		if strings.EqualFold(r.Name, strings.TrimSpace(in.Repo)) || in.Repo == "" && len(repos) == 1 {
			c.repoID, c.repo, c.slug, repo = r.ID, r.Name, forge.SlugFromURL(r.URL), r
		}
	}
	if c.repoID == "" {
		return c, refusef(rDispatchNotTaskRepository, in.Repo, strings.Join(names, ", "))
	}
	if gh == nil || c.slug == "" {
		return c, refusef(rDispatchNotOnGitHub, c.repo)
	}
	d, err := ReadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return c, err
	}
	if d != nil {
		c.taskBranch = d.Branch(ref.TaskID)
		c.base = cmp.Or(d.State.Bases[repo.Name], repo.DefaultBranch)
	}
	var prBase string
	if err := tx.QueryRow(ctx, `SELECT base_branch FROM pull_requests WHERE task_id = $1 AND repository_id = $2
		ORDER BY created_at DESC LIMIT 1`, ref.TaskID, c.repoID).Scan(&prBase); err == nil && prBase != "" {
		c.base = prBase
	} else if err != nil && !db.IsNotFound(err) {
		return c, err
	}
	c.branch = dispatchRef(in.Ref)
	if c.branch == "" {
		if c.taskBranch == "" {
			return c, refuseText(rDispatchNameRef)
		}
		c.branch = c.taskBranch
	}
	if len(c.branch) > 255 || strings.ContainsAny(c.branch, " \t\n~^:?*[\\") || strings.HasPrefix(c.branch, "-") {
		return c, refusef(rDispatchRefUnreadable, c.branch)
	}
	if len(in.Inputs) > DispatchInputsMax {
		return c, refusef(rDispatchTooManyInputs, DispatchInputsMax)
	}
	for k, v := range in.Inputs {
		if len(v) > DispatchInputChars || k == "" || len(k) > 100 {
			return c, refusef(rDispatchInputTooLong, k, DispatchInputChars)
		}
	}
	c.inputs = in.Inputs
	if c.inputs == nil {
		c.inputs = map[string]string{}
	}
	if c.questionID = strings.TrimSpace(in.QuestionID); c.questionID != "" {
		if err := checkDispatchQuestionTx(ctx, tx, &c); err != nil {
			return c, err
		}
	}
	return c, reserveDispatchTx(ctx, tx, &c)
}

// reserveDispatchTx inserts c's row, dispatching. Another call that
// reserved the same answer and committed first makes the insert wait for
// it and fail on workflow_dispatches_question_idx: refused as spent.
func reserveDispatchTx(ctx context.Context, tx pgx.Tx, c *DispatchCheck) error {
	raw, err := json.Marshal(c.inputs)
	if err != nil {
		return err
	}
	c.id = ids.New("wfd")
	_, err = tx.Exec(ctx, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, inputs,
			dispatched_by, question_id, dispatched_at, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, NULLIF($9, ''), now(), $10)`,
		c.id, c.ref.Org, c.ref.TaskID, c.repoID, c.workflow, c.branch, raw, c.ref.RunID, c.questionID, DispatchDispatching)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "workflow_dispatches_question_idx" {
		return refusef(rDispatchQuestionReserved, c.questionID)
	}
	return err
}

// checkDispatchQuestionTx: c's question is this task's, addressed to a
// person, answered; offers DispatchApproval of c's workflow and ref among
// its choices (the workflow's file name exactly: deploy-docs.yml does not
// name deploy.yml); was answered with that choice (a "no" never passes);
// and authorized no dispatch before (one answer, one dispatch).
func checkDispatchQuestionTx(ctx context.Context, tx pgx.Tx, c *DispatchCheck) error {
	var answer string
	var rawOpts []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(answer, ''), COALESCE(options, '[]') FROM questions
		WHERE id = $1 AND task_id = $2 AND addressee = 'person' AND status = 'answered'`, c.questionID, c.ref.TaskID).Scan(&answer, &rawOpts)
	if db.IsNotFound(err) {
		return refusef(rDispatchQuestionNotAnswered, c.questionID, DispatchApproval(c.workflow, c.branch), DispatchDecline)
	}
	if err != nil {
		return err
	}
	var options []string
	_ = json.Unmarshal(rawOpts, &options)
	approve := DispatchApproval(c.workflow, c.branch)
	i := choiceOf(options, approve)
	if i < 0 {
		return refusef(rDispatchQuestionNotNaming, c.questionID, approve, approve, DispatchDecline)
	}
	if choiceOf(options, answer) != i {
		return refusef(rDispatchQuestionNotApproved, c.questionID, clip(oneLine(answer), 120), approve)
	}
	// A failed dispatch (GitHub refused it: no workflow ran) does not
	// spend the answer.
	var spent string
	err = tx.QueryRow(ctx, `SELECT id FROM workflow_dispatches WHERE question_id = $1 AND state <> $2`, c.questionID,
		DispatchFailed).Scan(&spent)
	if err == nil {
		return refusef(rDispatchQuestionSpent, c.questionID, spent)
	}
	if !db.IsNotFound(err) {
		return err
	}
	c.questionOK = true
	return nil
}

// DoDispatch reads the workflow's on: at the ref, refuses one with no
// workflow_dispatch, and one that is not free CI without the person's
// approving answer, then dispatches it (Posted). An error with Posted
// false: nothing was sent; with Posted set, the POST failed (EndDispatchTx
// tells which way).
// Free CI is a workflow that runs for the task's own change (a push to its
// branch, a pull request into its base, as its filters say) dispatched on
// the task's branch; a filter dude cannot evaluate is not free. No
// transaction is open.
func DoDispatch(ctx context.Context, c DispatchCheck) (DispatchDone, error) {
	var done DispatchDone
	triggers, raw, err := c.gh.WorkflowTriggersAt(ctx, c.slug, c.workflow, c.branch)
	if err != nil {
		var e *forge.Error
		if errors.As(err, &e) && e.NotFound() {
			return done, refusef(rDispatchNoSuchWorkflow, c.workflow, c.branch, c.repo)
		}
		if forge.Refused(err) || forge.Transient(err) {
			return done, refusef(rGithubDidNotDo, err)
		}
		return done, refusef(rDispatchTriggersUnreadable, c.workflow, c.branch, err)
	}
	done.Triggers = triggers
	if !slices.Contains(triggers, "workflow_dispatch") {
		return done, refusef(rDispatchNoTrigger, c.workflow, c.branch, strings.Join(triggers, ", "))
	}
	done.CI = c.taskBranch != "" && c.branch == c.taskBranch && c.base != "" &&
		forge.WorkflowRunsForChange(raw, c.taskBranch, c.base) == forge.ActionsExpected
	if !done.CI && !c.questionOK {
		why := fmt.Sprintf("it does not run for this task's own change (a push to %s, a pull request into %s), or dude cannot tell from its filters", c.taskBranch, c.base)
		if c.taskBranch == "" || c.branch != c.taskBranch {
			why = fmt.Sprintf("%s is not this task's branch", c.branch)
		}
		approve := DispatchApproval(c.workflow, c.branch)
		return done, refusef(rDispatchNeedsPerson, c.workflow, strings.Join(triggers, ", "), why, approve, DispatchDecline, approve)
	}
	done.Posted = true
	run, err := c.gh.DispatchWorkflow(ctx, c.slug, c.workflow, c.branch, inputsAny(c.inputs))
	if err != nil {
		return done, err
	}
	done.Run = run
	return done, nil
}

func inputsAny(in map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// postRefused: GitHub answered the POST with one of the refusals it gives
// before it creates a run (a bad request, no auth, forbidden or rate
// limited, no such workflow, a conflict, invalid inputs, too many
// requests), so no workflow ran. Any other status (a 5xx, or a 4xx such
// as 408 or 499 a proxy in front of GitHub may answer after forwarding
// the request), no answer, or the call cut off: it may have started one.
func postRefused(err error) bool {
	var e *forge.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Status {
	case 400, 401, 403, 404, 409, 422, 429:
		return true
	}
	return false
}

// EndDispatchTx ends c's reserved row when DoDispatch failed with err, and
// returns what the call answers (and the error of ending it). Nothing
// sent: the row goes, the answer with it. GitHub refused the POST:
// failed, with its error; no workflow ran, so the person's answer is not
// spent and may authorize a retry. Any other failure of the POST:
// unknown, "may or may not", never sent again; the call's refusal is what
// tells the conductor (its outcome is marked told, no wake besides).
func EndDispatchTx(ctx context.Context, tx pgx.Tx, c DispatchCheck, done DispatchDone, err error) (error, error) {
	if !done.Posted {
		_, derr := tx.Exec(ctx, `DELETE FROM workflow_dispatches WHERE id = $1 AND state = $2`, c.id, DispatchDispatching)
		return err, derr
	}
	said := clip(oneLine(err.Error()), 1000)
	if postRefused(err) {
		_, uerr := tx.Exec(ctx, `UPDATE workflow_dispatches SET state = $2, last_error = $3, reason = 'GitHub refused the dispatch'
			WHERE id = $1 AND state = $4`, c.id, DispatchFailed, said, DispatchDispatching)
		return refusef(rGithubDidNotDo, err), uerr
	}
	_, uerr := tx.Exec(ctx, `UPDATE workflow_dispatches SET state = $2, outcome = $2, woken_outcome = $2,
			reason = $3, last_error = $4
		WHERE id = $1 AND state = $5`, c.id, DispatchUnknown, dispatchMaybe, said, DispatchDispatching)
	if uerr != nil {
		return err, uerr
	}
	branchRuns, gerr := conductorMayReadBranchRunsTx(ctx, tx, c.ref.Org, c.ref.TaskID)
	return refusef(rDispatchMaybe, c.workflow, c.branch, said, c.id, render(lookForRunTmpl, branchRuns)), gerr
}

// DispatchRecorded is a dispatch as RecordDispatchTx left it: its id,
// target, state and reason, and Late when the supervisor ended it before
// GitHub's answer was recorded (the call then reports this state, not
// "Dispatched").
type DispatchRecorded struct {
	ID, Repo, Workflow, Ref, State, Reason string
	Late                                   bool
}

// RecordDispatchTx ends the reserved row with GitHub's answer and records
// the event: bound to the run GitHub answered with (200), or unknown (204:
// no run id, never bound by guess), whose wake is recorded now.
//
// A row the supervisor already ended "may or may not" (the call outlived
// DispatchReservedFor) is Late, and GitHub's answer still says what
// happened: a 200 binds it to its run (its "may or may not" wake goes if
// not yet delivered, and the run's end is woken for), a 204 makes it
// unknown for want of a run id. Any other late row is left as it is.
func RecordDispatchTx(ctx context.Context, tx pgx.Tx, org string, c DispatchCheck, done DispatchDone) (DispatchRecorded, error) {
	d, err := dispatchTx(ctx, tx, c.id, true)
	if err != nil {
		return DispatchRecorded{}, err
	}
	late := d.State != DispatchDispatching
	switch {
	case late && (d.State != DispatchUnknown || d.Reason != dispatchMaybe || d.RunID != 0):
	case done.Run.ID != 0:
		// Late, its undelivered "may or may not" wake no longer holds, and
		// nothing is owed until the run ends.
		if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET state = $2, run_id = $3, run_url = NULLIF($4, ''), run_status = 'queued',
				reason = NULL, outcome = NULL, woken_outcome = NULL, woken_wait = NULL, last_error = NULL
			WHERE id = $1`, d.ID, DispatchBound, done.Run.ID, done.Run.URL); err != nil {
			return DispatchRecorded{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM conductor_wakes WHERE task_id = $1 AND key = $2 AND delivered_at IS NULL`,
			d.taskID, dispatchWakeKey(DispatchRow{ID: d.ID, Outcome: DispatchUnknown})); err != nil {
			return DispatchRecorded{}, err
		}
		d.State, d.Reason = DispatchBound, ""
	default:
		// Late, it is already unknown and told: only the reason changes.
		if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET state = $2, outcome = $2, reason = $3 WHERE id = $1`,
			d.ID, DispatchUnknown, dispatchNoRunID); err != nil {
			return DispatchRecorded{}, err
		}
		d.State, d.Outcome, d.Reason = DispatchUnknown, DispatchUnknown, dispatchNoRunID
		if !late {
			if err := deliverDispatchTx(ctx, tx, org, d); err != nil {
				return DispatchRecorded{}, err
			}
		}
	}
	payload := map[string]any{"dispatchId": d.ID, "repo": c.repo, "workflow": c.workflow, "ref": c.branch, "inputs": c.inputs,
		"triggers": done.Triggers, "ci": done.CI}
	if done.Run.ID != 0 {
		payload["runId"] = done.Run.ID
	}
	if c.questionID != "" {
		payload["questionId"] = c.questionID
	}
	_, err = ledger.Append(ctx, tx, c.ref.Event(EvWorkflowDispatched, ledger.ActorAgent, payload))
	return DispatchRecorded{ID: d.ID, Repo: d.Repo, Workflow: d.Workflow, Ref: d.Ref, State: d.State, Reason: d.Reason, Late: late}, err
}

// DispatchRow is a dispatch as stored: what the supervisor last saw.
type DispatchRow struct {
	ID         string     `json:"dispatchId"`
	Repo       string     `json:"repo"`
	Workflow   string     `json:"workflow"`
	Ref        string     `json:"ref"`
	At         time.Time  `json:"dispatchedAt"`
	State      string     `json:"state"`
	Reason     string     `json:"reason,omitempty"`
	RunID      int64      `json:"runId,omitempty"`
	Attempt    int        `json:"attempt,omitempty"`
	Status     string     `json:"status,omitempty"`
	Conclusion string     `json:"conclusion,omitempty"`
	URL        string     `json:"url,omitempty"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	FailedRead int        `json:"failedReads,omitempty"`
	LastError  string     `json:"lastError,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	slug       string
	taskID     string
	failing    *time.Time
	woken      string
	org        string
	completed  *time.Time
	readAfter  *time.Time
}

const dispatchColumns = `d.id, r.name, d.workflow, d.ref, d.dispatched_at, d.state, COALESCE(d.reason, ''), COALESCE(d.run_id, 0),
	COALESCE(d.run_attempt, 0), COALESCE(d.run_status, ''), COALESCE(d.run_conclusion, ''), COALESCE(d.run_url, ''), d.last_seen_at,
	d.failed_reads, COALESCE(d.last_error, ''), COALESCE(d.outcome, ''), r.url, d.task_id, d.failing_since, COALESCE(d.woken_outcome, ''),
	d.completed_at, d.read_after, d.organization_id`

// dispatchTx reads dispatch id as stored; lock takes its row for update.
func dispatchTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (DispatchRow, error) {
	q := `SELECT ` + dispatchColumns + ` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id WHERE d.id = $1`
	if lock {
		q += ` FOR UPDATE OF d`
	}
	return scanDispatch(tx.QueryRow(ctx, q, id))
}

func scanDispatch(row pgx.Row) (DispatchRow, error) {
	var d DispatchRow
	var url string
	err := row.Scan(&d.ID, &d.Repo, &d.Workflow, &d.Ref, &d.At, &d.State, &d.Reason, &d.RunID, &d.Attempt, &d.Status, &d.Conclusion,
		&d.URL, &d.LastSeenAt, &d.FailedRead, &d.LastError, &d.Outcome, &url, &d.taskID, &d.failing, &d.woken,
		&d.completed, &d.readAfter, &d.org)
	d.slug = forge.SlugFromURL(url)
	return d, err
}

// DispatchesListed bounds the dispatches pull_requests lists: the newest.
const DispatchesListed = 10

// TaskDispatchesTx lists a task's newest DispatchesListed dispatches,
// oldest first, as stored (no GitHub read), and how many it has in all.
func TaskDispatchesTx(ctx context.Context, tx pgx.Tx, taskID string) ([]DispatchRow, int, error) {
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM workflow_dispatches WHERE task_id = $1`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT * FROM (SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.task_id = $1 ORDER BY d.dispatched_at DESC, d.id DESC LIMIT $2) l ORDER BY l.dispatched_at, l.id`, taskID, DispatchesListed)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []DispatchRow{}
	for rows.Next() {
		d, err := scanDispatch(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// DispatchTarget is what the supervisor reads on GitHub for one dispatch.
type DispatchTarget struct {
	ID, Slug string
	// RunID is the bound run; 0 while dispatching (nothing to read).
	RunID int64
	// Read is false while GitHub's Retry-After has not passed: the pass
	// settles what is stored (a wake owed) and reads nothing.
	Read bool
}

// following: d is supervised this pass: dispatching (to end it once
// past DispatchReservedFor), bound, or completed within
// DispatchFollowAfter (a re-run reopens it).
func (d DispatchRow) following(now time.Time) bool {
	switch d.State {
	case DispatchDispatching, DispatchBound:
		return true
	case DispatchCompleted:
		return d.completed != nil && now.Sub(*d.completed) < DispatchFollowAfter
	}
	return false
}

// DispatchTargetTx is dispatch id's target, ok false when it is no longer
// supervised (not followed, owed no wake).
func DispatchTargetTx(ctx context.Context, tx pgx.Tx, id string) (DispatchTarget, bool, error) {
	d, err := dispatchTx(ctx, tx, id, false)
	if db.IsNotFound(err) {
		return DispatchTarget{}, false, nil
	}
	if err != nil {
		return DispatchTarget{}, false, err
	}
	now := time.Now()
	follow := d.following(now)
	return DispatchTarget{ID: d.ID, Slug: d.slug, RunID: d.RunID,
			Read: follow && d.RunID != 0 && (d.readAfter == nil || !d.readAfter.After(now))},
		follow || d.Outcome != "" && d.Outcome != d.woken, nil
}

// DispatchRead is one pass's read of a dispatch on GitHub: the bound
// run's state, or the error the read failed with ("cannot tell"). Neither:
// nothing was read this pass.
type DispatchRead struct {
	Run *forge.RunState
	Err error
}

// SettleDispatchTx stores one pass's read of dispatch id and decides from
// the stored state: ends a dispatch reserved longer than
// DispatchReservedFor as unknown (never sent again), stores the run's
// status, conclusion and attempt it saw (a new attempt of a completed run
// reopens it), counts a failed read ("cannot tell"), and records the
// conductor's wake for a terminal outcome not delivered while no open
// wait names the dispatch. The row is locked; a pass that lost a race
// stores nothing twice and wakes nobody twice.
func SettleDispatchTx(ctx context.Context, tx pgx.Tx, org, id string, read DispatchRead) error {
	d, err := dispatchTx(ctx, tx, id, true)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	switch {
	case d.State == DispatchDispatching:
		if now.Sub(d.At) > DispatchReservedFor {
			d.State, d.Outcome, d.Reason = DispatchUnknown, DispatchUnknown, dispatchMaybe
		}
	case !d.following(now) || read.Err == nil && read.Run == nil:
	case d.State == DispatchCompleted:
		// Still read for a re-run: a failed read here is not "cannot see"
		// of a run already told.
		if read.Err == nil && read.Run != nil && read.Run.Attempt > d.Attempt {
			seen(&d, *read.Run, now)
		}
	case read.Err != nil:
		d.failedRead(read.Err, now)
	default:
		if d.Outcome == DispatchCannotSee {
			// Seen again: the next "cannot see" is a new one.
			d.Outcome, d.woken = "", ""
		}
		d.FailedRead, d.failing, d.LastError, d.readAfter = 0, nil, "", nil
		if read.Run != nil {
			seen(&d, *read.Run, now)
		}
	}
	// $4 is typed: untyped, NULLIF($4, 0) takes int4 from the literal, and
	// a GitHub run id (4.3e10 today) does not fit it (#465).
	if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET state = $2, reason = NULLIF($3, ''), run_id = NULLIF($4::bigint, 0),
			run_attempt = NULLIF($5, 0), run_status = NULLIF($6, ''), run_conclusion = NULLIF($7, ''), run_url = NULLIF($8, ''),
			last_seen_at = $9, failed_reads = $10, failing_since = $11, last_error = NULLIF($12, ''), outcome = NULLIF($13, ''),
			woken_outcome = NULLIF($14, ''), woken_wait = CASE WHEN $14 = '' THEN NULL ELSE woken_wait END,
			completed_at = $15, read_after = $16, last_read_at = now()
		WHERE id = $1`, d.ID, d.State, d.Reason, d.RunID, d.Attempt, d.Status, d.Conclusion, d.URL, d.LastSeenAt, d.FailedRead,
		d.failing, d.LastError, d.Outcome, d.woken, d.completed, d.readAfter); err != nil {
		return err
	}
	return deliverDispatchTx(ctx, tx, org, d)
}

// failedRead counts one read that could not tell ("cannot tell", never
// "not yet"): the stretch's start, the latest error, GitHub's Retry-After,
// and "cannot see" once DispatchCannotSeeReads have failed in a row.
func (d *DispatchRow) failedRead(err error, now time.Time) {
	d.FailedRead++
	if d.failing == nil {
		d.failing = &now
	}
	d.LastError = clip(oneLine(err.Error()), 1000)
	if after := forge.RetryAfter(err); after > 0 {
		t := now.Add(after)
		d.readAfter = &t
	}
	if d.FailedRead >= DispatchCannotSeeReads && d.Outcome == "" {
		d.Outcome = DispatchCannotSee
	}
}

// DispatchFailedTx records that a supervisor pass over dispatch id failed
// with err (its settle, not GitHub's read): a failed read like any other,
// so a pass that keeps failing reaches the conductor as "cannot see"
// instead of a log line. It writes only the failed-read fields, not the
// run's, so whatever made the settle fail does not fail this too.
func DispatchFailedTx(ctx context.Context, tx pgx.Tx, org, id string, err error) error {
	d, rerr := dispatchTx(ctx, tx, id, true)
	if db.IsNotFound(rerr) {
		return nil
	}
	if rerr != nil {
		return rerr
	}
	// Only a dispatch still followed toward its outcome (reserved, or bound
	// to a run) is one dude cannot tell about; a completed one was told.
	if d.State != DispatchDispatching && d.State != DispatchBound {
		return nil
	}
	d.failedRead(err, time.Now().UTC())
	if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET failed_reads = $2, failing_since = $3, last_error = NULLIF($4, ''),
			outcome = NULLIF($5, ''), read_after = $6, last_read_at = now()
		WHERE id = $1`, d.ID, d.FailedRead, d.failing, d.LastError, d.Outcome, d.readAfter); err != nil {
		return err
	}
	return deliverDispatchTx(ctx, tx, org, d)
}

// deliverDispatchTx records the wake of d's terminal outcome when it has
// not reached the conductor and no open wait names d (that wait's held
// digest will carry it; once it ends any other way, the next pass lands
// here with none open). The wake's key is the outcome's own, so racing
// passes record one.
func deliverDispatchTx(ctx context.Context, tx pgx.Tx, org string, d DispatchRow) error {
	if d.Outcome == "" || d.Outcome == d.woken {
		return nil
	}
	open, err := waitNamesDispatchTx(ctx, tx, d.taskID, d.ID)
	if err != nil || open {
		return err
	}
	branchRuns, err := conductorMayReadBranchRunsTx(ctx, tx, org, d.taskID)
	if err != nil {
		return err
	}
	if _, err = recordWakeTx(ctx, tx, org, d.taskID, WakeWorkflow, dispatchWakeKey(d), render(workflowWakeTmpl, workflowView(d, branchRuns)),
		false); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_dispatches SET woken_outcome = outcome, woken_wait = NULL WHERE id = $1`, d.ID)
	return err
}

// dispatchWakeKey names one terminal outcome: a completion by its attempt
// (a re-run's is another), a "cannot see" by the stretch of failed reads.
func dispatchWakeKey(d DispatchRow) string {
	key := "workflow:" + d.ID + ":" + d.Outcome
	switch {
	case d.Outcome == DispatchCannotSee && d.failing != nil:
		key += ":" + strconv.FormatInt(d.failing.Unix(), 10)
	case d.Outcome == DispatchCompleted && d.Attempt > 1:
		key += ":" + strconv.Itoa(d.Attempt)
	}
	return key
}

func seen(d *DispatchRow, r forge.RunState, now time.Time) {
	rerun := r.Attempt > d.Attempt && d.Attempt > 0
	if rerun {
		// A re-run: what the conductor was told of the attempt before is
		// not this one's.
		d.woken = ""
	}
	d.RunID, d.Attempt, d.Status, d.Conclusion, d.LastSeenAt = r.ID, r.Attempt, r.Status, r.Conclusion, &now
	if r.URL != "" {
		d.URL = r.URL
	}
	if r.Status == "completed" {
		d.State, d.Outcome = DispatchCompleted, DispatchCompleted
		if d.completed == nil || rerun {
			d.completed = &now
		}
	} else {
		d.State, d.Conclusion, d.Outcome, d.completed = DispatchBound, "", "", nil
	}
}

// conductorMayReadBranchRunsTx: the task's conductor (its newest Run, or
// a conductor's defaults when it has none) is granted github_branch_runs,
// which the wake of an unknown run points to.
func conductorMayReadBranchRunsTx(ctx context.Context, tx pgx.Tx, org, taskID string) (bool, error) {
	a := capabilities.Agent{Role: RoleConductor, Org: org}
	err := tx.QueryRow(ctx, `SELECT COALESCE(r.id, ''), t.project_id FROM tasks t LEFT JOIN LATERAL (SELECT id FROM runs
		WHERE task_id = t.id AND role = $2 ORDER BY created_at DESC LIMIT 1) r ON true WHERE t.id = $1`, taskID, RoleConductor).
		Scan(&a.Run, &a.Project)
	if err != nil {
		return false, err
	}
	grants, err := capabilities.Resolve(ctx, tx, a)
	return err == nil && grants.Tool("github_branch_runs"), err
}

type workflowFacts struct {
	DispatchRow
	Seen, Failing string
	// BranchRuns: the conductor may read a branch's runs
	// (github_branch_runs).
	BranchRuns bool
	// Line is its item in a wait's digest, up to workflowItemChars.
	Line string
}

func workflowView(d DispatchRow, branchRuns bool) workflowFacts {
	v := workflowFacts{DispatchRow: d, BranchRuns: branchRuns}
	if d.LastSeenAt != nil {
		v.Seen = d.LastSeenAt.UTC().Format(time.RFC3339)
	}
	if d.failing != nil {
		v.Failing = d.failing.UTC().Format(time.RFC3339)
	}
	return v
}

// workflowItemTx is a dispatch as a wait reads it: from what the
// supervisor stored, no GitHub read. It holds at a terminal outcome:
// completed (good when it succeeded), unknown, or cannot see; and at
// failed (GitHub refused the dispatch: nothing ran, nothing will end), a
// known answer, never "not answered yet".
func workflowItemTx(ctx context.Context, tx pgx.Tx, id string) (waitItem, error) {
	d, err := dispatchTx(ctx, tx, id, false)
	if err != nil {
		return waitItem{}, err
	}
	branchRuns := false
	if d.Outcome == DispatchUnknown {
		// Only an unknown item says where to look.
		if branchRuns, err = conductorMayReadBranchRunsTx(ctx, tx, d.org, d.taskID); err != nil {
			return waitItem{}, err
		}
	}
	v := workflowView(d, branchRuns)
	v.Line = clip(oneLine(render(waitWorkflowTmpl, v)), workflowItemChars)
	it := waitItem{Kind: "workflow", Workflow: &v}
	switch {
	case d.Outcome == DispatchCompleted && d.Conclusion == "success":
		it.rank = rankGood
	case d.Outcome != "" || d.State == DispatchFailed:
		it.rank = rankBad
	default:
		it.rank = rankWaiting
	}
	return it, nil
}

// deliveredByWaitTx marks the terminal outcomes a wait's digest told the
// conductor as delivered: each workflow item of ev with an outcome, as it
// was read (an outcome changed since is not marked), whose whole line is
// in told, the digest as stored (clipped to its bound). An item cut from
// it reached no one: it stays owed, and the supervisor's next pass, with
// the wait ended, wakes for it.
func deliveredByWaitTx(ctx context.Context, tx pgx.Tx, waitID, told string, ev waitEval) error {
	for _, it := range ev.items {
		if it.Workflow == nil || it.Workflow.Outcome == "" || !strings.Contains(told, it.Workflow.Line) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET woken_outcome = outcome, woken_wait = NULLIF($4, '')
			WHERE id = $1 AND outcome = $2 AND COALESCE(run_attempt, 0) = $3 AND outcome IS DISTINCT FROM woken_outcome`,
			it.Workflow.ID, it.Workflow.Outcome, it.Workflow.Attempt, waitID); err != nil {
			return err
		}
	}
	return nil
}

// undeliverWaitsTx: the task's wait digests recorded and not told (their
// conductor ended first) did not reach anyone, so the outcomes they
// carried are owed again; the supervisor's next pass wakes for them.
func undeliverWaitsTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `UPDATE workflow_dispatches d SET woken_outcome = NULL, woken_wait = NULL
		WHERE d.task_id = $1 AND d.woken_wait IS NOT NULL AND EXISTS (SELECT 1 FROM conductor_wakes c
			WHERE c.task_id = $1 AND c.kind = $2 AND c.key = 'wait:' || d.woken_wait AND c.delivered_at IS NULL)`, taskID, WakeWait)
	return err
}

// waitNamesDispatchTx: an open wait of the task names dispatch id, so its
// outcome reaches the conductor through the wait's held digest, not a
// wake of its own (one per outcome). Once that wait ends without holding
// on it, the outcome is still owed and the supervisor wakes for it.
func waitNamesDispatchTx(ctx context.Context, tx pgx.Tx, taskID, id string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_waits w, jsonb_array_elements(w.conditions) c
		WHERE w.task_id = $1 AND w.ended_at IS NULL AND c->>'kind' = $2 AND c->>'dispatch' = $3)`, taskID, condWorkflowEnded, id).Scan(&open)
	return open, err
}
