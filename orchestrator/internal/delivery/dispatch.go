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

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// run_workflow: the conductor starts a GitHub Actions workflow
// (workflow_dispatch) and dude supervises its run to the end.
//
// Binding never guesses. The dispatch asks GitHub for the run's details,
// and the run id it answers with (200) is bound at once. Only an older
// GitHub (or GHES), which answers 204 and no id, leaves a dispatch to bind
// afterwards: from the runs of that workflow and ref that were not there
// just before the dispatch (prior_runs); a second dispatch of the same
// workflow and ref is refused while one is unbound, and more than one new
// run is unknown.
//
// Supervision is level-triggered: every pass re-reads the bound run's
// status, conclusion and attempt and stores what it saw
// (workflow_dispatches). What the conductor is told is decided from that
// stored state alone, never from an event that might have been missed; a
// restart resumes from the rows. A read that fails is "cannot tell",
// counted, never "not yet" (nor "no candidates" while binding): past
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
	// DispatchSkew is how far before dispatched_at a run may be created and
	// still be a candidate (204 only): GitHub's clock is not dude's.
	DispatchSkew = time.Minute
	// DispatchBindWithin is how long a dispatch waits for its run to
	// appear before it is unknown (204 only).
	DispatchBindWithin = 5 * time.Minute
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
)

// Dispatch states and outcomes (workflow_dispatches.state, outcome).
const (
	DispatchBinding   = "binding"
	DispatchBound     = "bound"
	DispatchCompleted = "completed"
	DispatchUnknown   = "unknown"
	DispatchCannotSee = "cannot_see"
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
}

// DispatchDone is what was done on GitHub: the workflow's triggers,
// whether it was free CI, the run GitHub answered with (0 for none: an
// older GitHub), and the runs there before the dispatch (nil when that
// read failed).
type DispatchDone struct {
	Triggers []string
	CI       bool
	Run      forge.DispatchedRun
	Prior    []int64
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
// name, the ref (the task's branch by default), the inputs, no other
// dispatch of the workflow and ref still unbound, and the named question,
// if any: on this task, addressed to a person, offering the approving
// choice for this workflow and ref, answered with it, and not spent on
// another dispatch.
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
	// An older GitHub answers a dispatch with no run id: while one of this
	// workflow and ref is unbound, a second would make its run ambiguous.
	var unbound string
	err = tx.QueryRow(ctx, `SELECT id FROM workflow_dispatches WHERE repository_id = $1 AND workflow = $2 AND ref = $3
		AND state = 'binding' ORDER BY dispatched_at LIMIT 1`, c.repoID, c.workflow, c.branch).Scan(&unbound)
	if err == nil {
		return c, refusef(rDispatchUnbound, c.workflow, c.branch, unbound)
	}
	if !db.IsNotFound(err) {
		return c, err
	}
	if c.questionID = strings.TrimSpace(in.QuestionID); c.questionID != "" {
		if err := checkDispatchQuestionTx(ctx, tx, &c); err != nil {
			return c, err
		}
	}
	return c, nil
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
	var spent string
	err = tx.QueryRow(ctx, `SELECT id FROM workflow_dispatches WHERE question_id = $1`, c.questionID).Scan(&spent)
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
// approving answer, snapshots the runs already there, then dispatches it.
// Free CI is a workflow that runs for the task's own change (a push to its
// branch, a pull request into its base, as its filters say) dispatched on
// the task's branch; a filter dude cannot evaluate is not free. No
// transaction is open.
func DoDispatch(ctx context.Context, c DispatchCheck) (DispatchDone, error) {
	var done DispatchDone
	triggers, raw, err := c.gh.WorkflowTriggersAt(ctx, c.slug, c.workflow, c.branch)
	if err != nil {
		var e *forge.Error
		if asForgeError(err, &e) && e.NotFound() {
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
	// The runs there before: never this dispatch's (read for an older
	// GitHub's 204). A read that fails, or a full page, is "cannot tell":
	// nil, and such a dispatch is not bound by guess.
	if prior, err := c.gh.DispatchedRuns(ctx, c.slug, c.workflow, c.branch, time.Now().Add(-DispatchSkew)); err == nil &&
		len(prior) < forge.DispatchedRunsPage {
		done.Prior = []int64{}
		for _, r := range prior {
			done.Prior = append(done.Prior, r.ID)
		}
	}
	run, err := c.gh.DispatchWorkflow(ctx, c.slug, c.workflow, c.branch, inputsAny(c.inputs))
	if err != nil {
		if forge.Refused(err) || forge.Transient(err) {
			return done, refusef(rGithubDidNotDo, err)
		}
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

func asForgeError(err error, e **forge.Error) bool {
	return err != nil && errors.As(err, e)
}

// RecordDispatchTx records the dispatch row and its event: bound to the
// run GitHub answered with, or binding (204) with the runs there before.
// Returns its id.
func RecordDispatchTx(ctx context.Context, tx pgx.Tx, c DispatchCheck, done DispatchDone) (string, error) {
	id := ids.New("wfd")
	inputs := c.inputs
	if inputs == nil {
		inputs = map[string]string{}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	state := DispatchBinding
	if done.Run.ID != 0 {
		state = DispatchBound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, inputs,
			dispatched_by, question_id, dispatched_at, state, run_id, run_url, run_status, prior_runs)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, NULLIF($9, ''), now(), $10, NULLIF($11, 0), NULLIF($12, ''),
			CASE WHEN $11 <> 0 THEN 'queued' END, $13)`,
		id, c.ref.Org, c.ref.TaskID, c.repoID, c.workflow, c.branch, raw, c.ref.RunID, c.questionID, state, done.Run.ID, done.Run.URL,
		done.Prior); err != nil {
		return "", err
	}
	payload := map[string]any{"dispatchId": id, "repo": c.repo, "workflow": c.workflow, "ref": c.branch, "inputs": inputs,
		"triggers": done.Triggers, "ci": done.CI}
	if done.Run.ID != 0 {
		payload["runId"] = done.Run.ID
	}
	if c.questionID != "" {
		payload["questionId"] = c.questionID
	}
	_, err = ledger.Append(ctx, tx, c.ref.Event(EvWorkflowDispatched, ledger.ActorAgent, payload))
	return id, err
}

// Dispatched is the target of a dispatch.
func (c DispatchCheck) Dispatched() (repo, workflow, ref string) { return c.repo, c.workflow, c.branch }

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
	readAt     *time.Time
	prior      []int64
	completed  *time.Time
	readAfter  *time.Time
}

const dispatchColumns = `d.id, r.name, d.workflow, d.ref, d.dispatched_at, d.state, COALESCE(d.reason, ''), COALESCE(d.run_id, 0),
	COALESCE(d.run_attempt, 0), COALESCE(d.run_status, ''), COALESCE(d.run_conclusion, ''), COALESCE(d.run_url, ''), d.last_seen_at,
	d.failed_reads, COALESCE(d.last_error, ''), COALESCE(d.outcome, ''), r.url, d.task_id, d.failing_since, COALESCE(d.woken_outcome, ''),
	d.last_read_at, d.prior_runs, d.completed_at, d.read_after`

func scanDispatch(row pgx.Row) (DispatchRow, error) {
	var d DispatchRow
	var url string
	err := row.Scan(&d.ID, &d.Repo, &d.Workflow, &d.Ref, &d.At, &d.State, &d.Reason, &d.RunID, &d.Attempt, &d.Status, &d.Conclusion,
		&d.URL, &d.LastSeenAt, &d.FailedRead, &d.LastError, &d.Outcome, &url, &d.taskID, &d.failing, &d.woken, &d.readAt, &d.prior,
		&d.completed, &d.readAfter)
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
	ID, Slug, Workflow, Ref string
	Since                   time.Time
	// RunID is the bound run; 0 while binding.
	RunID int64
	// Read is false while GitHub's Retry-After has not passed: the pass
	// settles what is stored (a wake owed) and reads nothing.
	Read bool
}

// following: d is read on GitHub this pass: binding, bound, or completed
// within DispatchFollowAfter (a re-run reopens it).
func (d DispatchRow) following(now time.Time) bool {
	switch d.State {
	case DispatchBinding, DispatchBound:
		return true
	case DispatchCompleted:
		return d.completed != nil && now.Sub(*d.completed) < DispatchFollowAfter
	}
	return false
}

// DispatchTargetTx is dispatch id's target, ok false when it is no longer
// supervised (not followed, owed no wake).
func DispatchTargetTx(ctx context.Context, tx pgx.Tx, id string) (DispatchTarget, bool, error) {
	d, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.id = $1`, id))
	if db.IsNotFound(err) {
		return DispatchTarget{}, false, nil
	}
	if err != nil {
		return DispatchTarget{}, false, err
	}
	now := time.Now()
	follow := d.following(now)
	return DispatchTarget{ID: d.ID, Slug: d.slug, Workflow: d.Workflow, Ref: d.Ref, Since: d.At.Add(-DispatchSkew), RunID: d.RunID,
			Read: follow && (d.readAfter == nil || !d.readAfter.After(now))},
		follow || d.Outcome != "" && d.Outcome != d.woken, nil
}

// DispatchRead is one pass's read of a dispatch on GitHub: the candidate
// runs while binding, the bound run's state after, or the error the read
// failed with ("cannot tell"). None of them: nothing was read this pass.
type DispatchRead struct {
	Candidates []forge.RunState
	Run        *forge.RunState
	Err        error
}

func (r DispatchRead) none() bool { return r.Err == nil && r.Run == nil && r.Candidates == nil }

// SettleDispatchTx stores one pass's read of dispatch id and decides from
// the stored state: binds the one new candidate (never a guess between
// several, never a run that was there before the dispatch or is another
// dispatch's), stores the run's status, conclusion and attempt it saw
// (a new attempt of a completed run reopens it), counts a failed read
// (binding too: "cannot tell", not "no run yet"), and records the
// conductor's wake for a terminal outcome not delivered while no open
// wait names the dispatch. The row is locked; a pass that lost a race
// stores nothing twice and wakes nobody twice.
func SettleDispatchTx(ctx context.Context, tx pgx.Tx, org, id string, read DispatchRead) error {
	d, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.id = $1 FOR UPDATE OF d`, id))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	switch {
	case !d.following(now) || read.none():
	case d.State == DispatchCompleted:
		// Still read for a re-run: a failed read here is not "cannot see"
		// of a run already told.
		if read.Err == nil && read.Run != nil && read.Run.Attempt > d.Attempt {
			seen(&d, *read.Run, now)
		}
	case read.Err != nil:
		d.FailedRead++
		if d.failing == nil {
			d.failing = &now
		}
		d.LastError = clip(oneLine(read.Err.Error()), 1000)
		if after := forge.RetryAfter(read.Err); after > 0 {
			t := now.Add(after)
			d.readAfter = &t
		}
		if d.FailedRead >= DispatchCannotSeeReads && d.Outcome == "" {
			d.Outcome = DispatchCannotSee
		}
	default:
		if d.Outcome == DispatchCannotSee {
			// Seen again: the next "cannot see" is a new one.
			d.Outcome, d.woken = "", ""
		}
		d.FailedRead, d.failing, d.LastError, d.readAfter = 0, nil, "", nil
		if d.State == DispatchBinding {
			if err := bindDispatchTx(ctx, tx, &d, read.Candidates, now); err != nil {
				return err
			}
		} else if read.Run != nil {
			seen(&d, *read.Run, now)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET state = $2, reason = NULLIF($3, ''), run_id = NULLIF($4, 0),
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
	_, err = tx.Exec(ctx, `UPDATE workflow_dispatches SET woken_outcome = outcome, woken_wait = NULL, woken_at = now() WHERE id = $1`, d.ID)
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

// bindDispatchTx binds d (an older GitHub answered its dispatch with no
// run id) to its one candidate that was not there before the dispatch and
// is not bound to another dispatch. Several are unknown (no guess); so is
// any when the runs before could not be read; none past
// DispatchBindWithin too.
func bindDispatchTx(ctx context.Context, tx pgx.Tx, d *DispatchRow, candidates []forge.RunState, now time.Time) error {
	var free []forge.RunState
	for _, r := range candidates {
		if slices.Contains(d.prior, r.ID) {
			continue
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workflow_dispatches o WHERE o.run_id = $1 AND o.id <> $2
			AND o.repository_id = (SELECT repository_id FROM workflow_dispatches WHERE id = $2))`, r.ID, d.ID).Scan(&taken); err != nil {
			return err
		}
		if !taken {
			free = append(free, r)
		}
	}
	switch {
	case len(free) > 0 && d.prior == nil:
		d.State, d.Outcome = DispatchUnknown, DispatchUnknown
		d.Reason = fmt.Sprintf("GitHub answered the dispatch with no run id, and dude could not read the runs of %s on %s there before it: "+
			"it cannot tell whether run %d is this one, and does not guess", d.Workflow, d.Ref, free[0].ID)
	case len(free) == 1:
		seen(d, free[0], now)
	case len(free) > 1:
		var runs []string
		for _, r := range free {
			runs = append(runs, strconv.FormatInt(r.ID, 10))
		}
		d.State, d.Outcome = DispatchUnknown, DispatchUnknown
		d.Reason = fmt.Sprintf("%d runs of %s on %s started by workflow_dispatch since the dispatch (%s): dude does not guess which is this one",
			len(free), d.Workflow, d.Ref, strings.Join(runs, ", "))
	case now.Sub(d.At) > DispatchBindWithin:
		d.State, d.Outcome = DispatchUnknown, DispatchUnknown
		d.Reason = fmt.Sprintf("no run of %s on %s started by workflow_dispatch appeared within %s of the dispatch", d.Workflow, d.Ref,
			DispatchBindWithin)
	}
	return nil
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
// completed (good when it succeeded), unknown, or cannot see.
func workflowItemTx(ctx context.Context, tx pgx.Tx, id string) (waitItem, error) {
	d, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.id = $1`, id))
	if err != nil {
		return waitItem{}, err
	}
	v := workflowView(d, true)
	it := waitItem{Kind: "workflow", Workflow: &v}
	switch {
	case d.Outcome == DispatchCompleted && d.Conclusion == "success":
		it.rank = rankGood
	case d.Outcome != "":
		it.rank = rankBad
	default:
		it.rank = rankWaiting
	}
	return it, nil
}

// deliveredByWaitTx marks the terminal outcomes a wait's digest told the
// conductor as delivered: each workflow item of ev with an outcome, as it
// was read (an outcome changed since is not marked).
func deliveredByWaitTx(ctx context.Context, tx pgx.Tx, waitID string, ev waitEval) error {
	for _, it := range ev.items {
		if it.Workflow == nil || it.Workflow.Outcome == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_dispatches SET woken_outcome = outcome, woken_wait = NULLIF($4, ''), woken_at = now()
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
	_, err := tx.Exec(ctx, `UPDATE workflow_dispatches d SET woken_outcome = NULL, woken_wait = NULL, woken_at = NULL
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
