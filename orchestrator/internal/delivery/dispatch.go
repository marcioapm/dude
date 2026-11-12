package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// run_workflow: the conductor starts a GitHub Actions workflow
// (workflow_dispatch) and dude supervises its run to the end.
//
// Supervision is level-triggered. GitHub answers a dispatch with no run
// id, so a pass binds each dispatch to the one run that can be it, and
// every pass after re-reads that run's status, conclusion and attempt and
// stores what it saw (workflow_dispatches). What the conductor is told is
// decided from that stored state alone, never from an event that might
// have been missed; a restart resumes from the rows. A read that fails is
// "cannot tell", counted, never "not yet": past DispatchCannotSeeReads in
// a row the conductor is told dude cannot see the run. Each terminal
// outcome wakes it once (woken_outcome, and the wake's key).

// WakeWorkflow is the conductor_wakes kind of a dispatched workflow's
// terminal outcome.
const WakeWorkflow = "workflow"

// EvWorkflowDispatched is the ledger event of a dispatch, on the
// conductor's Run: {dispatchId, repo, workflow, ref, inputs, questionId}.
const EvWorkflowDispatched = "workflow.dispatched"

// Bounds of supervision, in code (no settings).
const (
	// DispatchSkew is how far before dispatched_at a run may be created and
	// still be a candidate: GitHub's clock is not dude's.
	DispatchSkew = time.Minute
	// DispatchBindWithin is how long a dispatch waits for its run to
	// appear before it is unknown.
	DispatchBindWithin = 5 * time.Minute
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
	inputs             map[string]string
	questionID         string
	questionOK         bool
	gh                 *forge.GitHub
}

// DispatchDone is what was done on GitHub: the workflow's triggers and
// whether the dispatch needed the person's answer.
type DispatchDone struct {
	Triggers []string
	CI       bool
}

// CheckDispatchTx checks a run_workflow call: the live conductor of a task
// not ended, one of the task's repositories on GitHub, a workflow file
// name, the ref (the task's branch by default), the inputs, and the named
// question, if any: on this task, addressed to a person, answered, and
// naming the workflow.
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
	for _, r := range repos {
		names = append(names, r.Name)
		if strings.EqualFold(r.Name, strings.TrimSpace(in.Repo)) || in.Repo == "" && len(repos) == 1 {
			c.repoID, c.repo, c.slug = r.ID, r.Name, forge.SlugFromURL(r.URL)
		}
	}
	if c.repoID == "" {
		return c, refusef(rDispatchNotTaskRepository, in.Repo, strings.Join(names, ", "))
	}
	if gh == nil || c.slug == "" {
		return c, refusef(rDispatchNotOnGitHub, c.repo)
	}
	c.branch = strings.TrimSpace(in.Ref)
	if c.branch == "" {
		d, err := ReadDelivery(ctx, tx, ref.TaskID)
		if err != nil {
			return c, err
		}
		if d == nil {
			return c, refuseText(rDispatchNameRef)
		}
		c.branch = d.Branch(ref.TaskID)
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
	if c.questionID = strings.TrimSpace(in.QuestionID); c.questionID != "" {
		var prompt string
		err := tx.QueryRow(ctx, `SELECT prompt FROM questions WHERE id = $1 AND task_id = $2 AND addressee = 'person' AND status = 'answered'`,
			c.questionID, ref.TaskID).Scan(&prompt)
		if db.IsNotFound(err) {
			return c, refusef(rDispatchQuestionNotAnswered, c.questionID)
		}
		if err != nil {
			return c, err
		}
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(c.workflow, ".yml"), ".yaml"))) {
			return c, refusef(rDispatchQuestionNotNaming, c.questionID, c.workflow)
		}
		c.questionOK = true
	}
	return c, nil
}

// ciTrigger: a workflow that runs on these is CI the change already runs,
// which the conductor may dispatch on its own.
func ciTrigger(event string) bool {
	return event == "push" || event == "pull_request" || event == "pull_request_target"
}

// DoDispatch reads the workflow's on: at the ref, refuses one with no
// workflow_dispatch, and one that is not CI without the person's answered
// question, then dispatches it. No transaction is open.
func DoDispatch(ctx context.Context, c DispatchCheck) (DispatchDone, error) {
	var done DispatchDone
	triggers, err := c.gh.WorkflowTriggersAt(ctx, c.slug, c.workflow, c.branch)
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
	done.CI = slices.ContainsFunc(triggers, ciTrigger)
	if !done.CI && !c.questionOK {
		return done, refusef(rDispatchNeedsPerson, c.workflow, strings.Join(triggers, ", "), c.workflow, c.branch)
	}
	if err := c.gh.DispatchWorkflow(ctx, c.slug, c.workflow, c.branch, inputsAny(c.inputs)); err != nil {
		if forge.Refused(err) || forge.Transient(err) {
			return done, refusef(rGithubDidNotDo, err)
		}
		return done, err
	}
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

// RecordDispatchTx records the dispatch row and its event: the run is
// bound and supervised from here on. Returns its id.
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
	if _, err := tx.Exec(ctx, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, inputs,
			dispatched_by, question_id, dispatched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, NULLIF($9, ''), now())`,
		id, c.ref.Org, c.ref.TaskID, c.repoID, c.workflow, c.branch, raw, c.ref.RunID, c.questionID); err != nil {
		return "", err
	}
	payload := map[string]any{"dispatchId": id, "repo": c.repo, "workflow": c.workflow, "ref": c.branch, "inputs": inputs,
		"triggers": done.Triggers, "ci": done.CI}
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
}

const dispatchColumns = `d.id, r.name, d.workflow, d.ref, d.dispatched_at, d.state, COALESCE(d.reason, ''), COALESCE(d.run_id, 0),
	COALESCE(d.run_attempt, 0), COALESCE(d.run_status, ''), COALESCE(d.run_conclusion, ''), COALESCE(d.run_url, ''), d.last_seen_at,
	d.failed_reads, COALESCE(d.last_error, ''), COALESCE(d.outcome, ''), r.url, d.task_id, d.failing_since, COALESCE(d.woken_outcome, ''),
	d.last_read_at`

func scanDispatch(row pgx.Row) (DispatchRow, error) {
	var d DispatchRow
	var url string
	err := row.Scan(&d.ID, &d.Repo, &d.Workflow, &d.Ref, &d.At, &d.State, &d.Reason, &d.RunID, &d.Attempt, &d.Status, &d.Conclusion,
		&d.URL, &d.LastSeenAt, &d.FailedRead, &d.LastError, &d.Outcome, &url, &d.taskID, &d.failing, &d.woken, &d.readAt)
	d.slug = forge.SlugFromURL(url)
	return d, err
}

// TaskDispatchesTx lists a task's dispatches, oldest first, as stored: no
// GitHub read.
func TaskDispatchesTx(ctx context.Context, tx pgx.Tx, taskID string) ([]DispatchRow, error) {
	rows, err := tx.Query(ctx, `SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.task_id = $1 ORDER BY d.dispatched_at, d.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DispatchRow{}
	for rows.Next() {
		d, err := scanDispatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DispatchTarget is what the supervisor reads on GitHub for one dispatch.
type DispatchTarget struct {
	ID, Slug, Workflow, Ref string
	Since                   time.Time
	// RunID is the bound run; 0 while binding.
	RunID int64
}

// DispatchTargetTx is dispatch id's target, ok false when it is no longer
// supervised (ended, owed no wake).
func DispatchTargetTx(ctx context.Context, tx pgx.Tx, id string) (DispatchTarget, bool, error) {
	d, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.id = $1`, id))
	if db.IsNotFound(err) {
		return DispatchTarget{}, false, nil
	}
	if err != nil {
		return DispatchTarget{}, false, err
	}
	live := d.State == DispatchBinding || d.State == DispatchBound
	return DispatchTarget{ID: d.ID, Slug: d.slug, Workflow: d.Workflow, Ref: d.Ref, Since: d.At.Add(-DispatchSkew), RunID: d.RunID},
		live || d.Outcome != "" && d.Outcome != d.woken, nil
}

// DispatchRead is one pass's read of a dispatch on GitHub: the candidate
// runs while binding, the bound run's state after, or the error the read
// failed with ("cannot tell").
type DispatchRead struct {
	Candidates []forge.RunState
	Run        *forge.RunState
	Err        error
}

// SettleDispatchTx stores one pass's read of dispatch id and decides from
// the stored state: binds the one candidate (never a guess between
// several), stores the run's status, conclusion and attempt it saw, counts
// a failed read, and records the conductor's wake for a terminal outcome
// it has not been woken for. The row is locked; a pass that lost a race
// stores nothing twice and wakes nobody twice.
func SettleDispatchTx(ctx context.Context, tx pgx.Tx, org, id string, read DispatchRead) error {
	d, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM workflow_dispatches d JOIN repositories r ON r.id = d.repository_id
		WHERE d.id = $1 FOR UPDATE OF d`, id))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	live := d.State == DispatchBinding || d.State == DispatchBound
	switch {
	case !live:
	case read.Err != nil:
		d.FailedRead++
		if d.failing == nil {
			d.failing = &now
		}
		d.LastError = clip(oneLine(read.Err.Error()), 1000)
		if d.FailedRead >= DispatchCannotSeeReads && d.Outcome == "" {
			d.Outcome = DispatchCannotSee
		}
	default:
		if d.Outcome == DispatchCannotSee {
			// Seen again: the next "cannot see" is a new one.
			d.Outcome, d.woken = "", ""
		}
		d.FailedRead, d.failing, d.LastError = 0, nil, ""
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
			woken_outcome = NULLIF($14, ''), last_read_at = now()
		WHERE id = $1`, d.ID, d.State, d.Reason, d.RunID, d.Attempt, d.Status, d.Conclusion, d.URL, d.LastSeenAt, d.FailedRead,
		d.failing, d.LastError, d.Outcome, d.woken); err != nil {
		return err
	}
	if d.Outcome == "" || d.Outcome == d.woken {
		return nil
	}
	// An open wait on this dispatch tells the outcome in its own wake,
	// read from the same stored state: one wake, not two.
	carried, err := waitCarriesDispatchTx(ctx, tx, d.taskID, d.ID)
	if err != nil {
		return err
	}
	if carried {
		_, err = tx.Exec(ctx, `UPDATE workflow_dispatches SET woken_outcome = outcome, woken_at = now() WHERE id = $1`, d.ID)
		return err
	}
	key := "workflow:" + d.ID + ":" + d.Outcome
	if d.Outcome == DispatchCannotSee && d.failing != nil {
		key += ":" + strconv.FormatInt(d.failing.Unix(), 10)
	}
	if _, err = recordWakeTx(ctx, tx, org, d.taskID, WakeWorkflow, key, renderWorkflowWake(d), false); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_dispatches SET woken_outcome = outcome, woken_at = now() WHERE id = $1`, d.ID)
	return err
}

func seen(d *DispatchRow, r forge.RunState, now time.Time) {
	d.RunID, d.Attempt, d.Status, d.Conclusion, d.LastSeenAt = r.ID, r.Attempt, r.Status, r.Conclusion, &now
	if r.URL != "" {
		d.URL = r.URL
	}
	if r.Status == "completed" {
		d.State, d.Outcome = DispatchCompleted, DispatchCompleted
	} else {
		d.State, d.Conclusion = DispatchBound, ""
	}
}

// bindDispatchTx binds d to its one candidate not bound to another
// dispatch; several are unknown (no guess), none past DispatchBindWithin
// too.
func bindDispatchTx(ctx context.Context, tx pgx.Tx, d *DispatchRow, candidates []forge.RunState, now time.Time) error {
	var free []forge.RunState
	for _, r := range candidates {
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

func renderWorkflowWake(d DispatchRow) string {
	return render(workflowWakeTmpl, workflowView(d))
}

type workflowFacts struct {
	DispatchRow
	Seen, Failing string
}

func workflowView(d DispatchRow) workflowFacts {
	v := workflowFacts{DispatchRow: d}
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
	v := workflowView(d)
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

// waitCarriesDispatchTx: an open wait of the task names dispatch id, so
// its outcome reaches the conductor through the wait, not a wake of its
// own (one wake per outcome).
func waitCarriesDispatchTx(ctx context.Context, tx pgx.Tx, taskID, id string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_waits w, jsonb_array_elements(w.conditions) c
		WHERE w.task_id = $1 AND w.ended_at IS NULL AND c->>'kind' = $2 AND c->>'dispatch' = $3)`, taskID, condWorkflowEnded, id).Scan(&open)
	return open, err
}
