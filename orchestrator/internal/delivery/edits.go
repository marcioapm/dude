package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The conductor's edits: it commits in its own checkout, kept current by
// lux, and publish asks dude to take what it committed to the task branch,
// as a phase's work is taken. One writer at a time, small, and never
// untested at the pull request.

// Refusals a publish can meet, in the words the conductor is told.
const (
	refusedWriter  = "an implementer is working on this task; wait for it, or steer it"
	refusedPolicy  = "Deliver decides this task: steer its agents, or ask the person to hand it to you."
	RefusedBehind  = "your checkout is behind the task branch; `git merge lux/%s` first"
	RefusedNothing = "nothing to publish: you have no commits on top of the task branch's head"
	// RefusedTooBig takes the lines, files, and the limits.
	RefusedTooBig = "this is %d lines in %d files, past the conductor's limit of %d lines / %d files: " +
		"delegate this (start_phase implement)"
	// UntestedHead is the pull request gate refusing a head that is a
	// conductor's commit no review or test Run has run on.
	UntestedHead = "the last commit is the conductor's; run a review first"
)

// Publish statuses (conductor_publishes.status).
const (
	PublishRequested = "requested"
	PublishAsked     = "asked"
	PublishPushed    = "pushed"
	PublishPublished = "published"
	PublishRefused   = "refused"
)

// writerPhases (SQL list): the phases whose Runs push to the task branch.
// A live one is another writer.
const writerPhases = `('implement', 'fix', 'simplify')`

// ErrStepRunning: a step of the delivery holds it; try again after.
var ErrStepRunning = errors.New("a step of the delivery is running")

// liveWriter (SQL, $1 the task): a phase Run that pushes to the task
// branch, not ended.
const liveWriter = `EXISTS (SELECT 1 FROM runs w WHERE w.task_id = $1 AND w.phase IN ` + writerPhases + `
	AND w.status IN ('pending', 'scheduled', 'starting', 'running', 'paused'))`

// ConductPublish is publish: the task's live conductor asks for what it
// committed in its checkout to be pushed and taken to the task branch.
// Refused, writing nothing, unless it is the live conductor, it takes the
// delivery's decisions, its checkout is writable, and no other writer is
// at work; the limit and whether its checkout is current are checked once
// lux has pushed (SettlePublish). Returns the publish's id.
func ConductPublish(ctx context.Context, tx pgx.Tx, ref RunRef, message string) (string, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return "", err
	}
	var live bool
	var readOnly, pushBranch string
	err := tx.QueryRow(ctx, `SELECT `+LiveConductor+` AND NOT COALESCE(`+Ending+`, false),
			COALESCE(r.checkout_read_only, ''), COALESCE(r.push_branch, '')
		FROM runs r WHERE r.id = $1 AND r.task_id = $2 FOR NO KEY UPDATE`, ref.RunID, ref.TaskID).Scan(&live, &readOnly, &pushBranch)
	if err != nil && !db.IsNotFound(err) {
		return "", err
	}
	if !live {
		return "", refusef("you are no longer this task's conductor: another took over from you. Publish nothing")
	}
	d, err := ReadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	status := ""
	if d != nil {
		status = d.TaskStatus
	} else if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, ref.TaskID).Scan(&status); err != nil {
		return "", err
	}
	switch {
	case Ended(status):
		return "", refusef("this task is %s: you are read-only now and publish nothing. Offer a follow-up task (create_task)", status)
	case d == nil || !d.Live():
		return "", refusef("this task has no delivery in progress: there is no task branch to publish to")
	case !d.State.conducted():
		return "", refusef("%s", refusedPolicy)
	case readOnly != "":
		return "", refusef("your checkout is read-only for this conversation: %s", readOnly)
	case pushBranch == "":
		return "", refusef("your checkout has no repository you may change: nothing can be published")
	}
	var writer, inFlight bool
	if err := tx.QueryRow(ctx, `SELECT `+liveWriter+`,
		EXISTS (SELECT 1 FROM conductor_publishes WHERE run_id = $2 AND status IN ('requested', 'asked', 'pushed'))`,
		ref.TaskID, ref.RunID).Scan(&writer, &inFlight); err != nil {
		return "", err
	}
	if writer {
		return "", refusef("%s", refusedWriter)
	}
	if inFlight {
		return "", refusef("a publish of yours is under way: you are woken when it is done")
	}
	id := ids.New("pub")
	if _, err := tx.Exec(ctx, `INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, message)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, ref.Org, ref.TaskID, ref.RunID, "publish-"+id, clip(strings.TrimSpace(message), 2000)); err != nil {
		return "", err
	}
	return id, nil
}

// PublishHead is what a publish took to one repository's task branch.
type PublishHead struct {
	SHA          string   `json:"sha"`
	Base         string   `json:"base"`
	ChangedPaths []string `json:"changedPaths"`
	Lines        int      `json:"lines"`
}

// PublishTarget is where a publish is measured from and goes to: the
// delivery's task branch and heads, and the project's limits.
type PublishTarget struct {
	WorkflowID string
	Branch     string
	// The task branch's head per repository; a repository missing has
	// none yet, and starts from its default branch.
	Heads              map[string]string
	MaxLines, MaxFiles int
	// Why it may not be published now, for good (a Refusal's words), or
	// "" to go on.
	Refused string
	// The delivery is between steps: its state can take the new heads.
	Settled bool
}

// LoadPublishTarget reads a publish's target in tx, re-checking what
// ConductPublish checked: someone may have handed the decisions back, or
// started a writer, since.
func LoadPublishTarget(ctx context.Context, tx pgx.Tx, taskID, runID string) (PublishTarget, error) {
	var t PublishTarget
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return t, err
	}
	var live, writer bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $2 AND `+LiveConductor+`), `+liveWriter,
		taskID, runID).Scan(&live, &writer); err != nil {
		return t, err
	}
	switch {
	case !live:
		t.Refused = "you are no longer this task's conductor"
	case d == nil || !d.Live() || Ended(d.TaskStatus):
		t.Refused = "this task has no delivery in progress"
	case !d.State.conducted():
		t.Refused = refusedPolicy
	case writer:
		t.Refused = refusedWriter
	}
	if d == nil {
		return t, nil
	}
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT locked_by IS NOT NULL AND locked_until > now() FROM workflow_runs WHERE id = $1`,
		d.WorkflowID).Scan(&locked); err != nil {
		return t, err
	}
	t.WorkflowID, t.Branch, t.Heads, t.Settled = d.WorkflowID, d.State.Branch, nonNilMap(d.State.Heads), !locked
	if t.Branch == "" {
		t.Branch = BranchFor(taskID, d.State.Attempt)
	}
	var projectPolicy, orgPolicy []byte
	if err := tx.QueryRow(ctx, `SELECT p.delivery_policy, o.delivery_policy FROM tasks t JOIN projects p ON p.id = t.project_id
		JOIN organizations o ON o.id = t.organization_id WHERE t.id = $1`, taskID).Scan(&projectPolicy, &orgPolicy); err != nil {
		return t, err
	}
	policy, err := ResolvePolicy(orgPolicy, projectPolicy)
	if err != nil {
		return t, err
	}
	t.MaxLines, t.MaxFiles = policy.ConductorEditLines, policy.ConductorEditFiles
	return t, nil
}

// OverLimit is the refusal for a publish of lines in files past the
// target's limits, "" within them.
func (t PublishTarget) OverLimit(lines, files int) string {
	if lines > t.MaxLines || files > t.MaxFiles {
		return fmt.Sprintf(RefusedTooBig, lines, files, t.MaxLines, t.MaxFiles)
	}
	return ""
}

// PublishedTx records a publish that moved the task branch: the delivery's
// heads advance as a phase's would (so the next phase starts from them and
// the gate's answer at the old heads no longer holds), and the conductor
// is woken saying so. The caller moved the branch. ErrStepRunning when a
// step of the delivery holds it now, whose transition would write over
// the heads: tried again after it (moving the branch again is a no-op).
func PublishedTx(ctx context.Context, tx pgx.Tx, ref RunRef, pubID, workflowID string, heads map[string]PublishHead) error {
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT locked_by IS NOT NULL AND locked_until > now() FROM workflow_runs WHERE id = $1 FOR UPDATE`,
		workflowID).Scan(&locked); err != nil {
		return err
	}
	if locked {
		return ErrStepRunning
	}
	next := map[string]string{}
	var paths []string
	lines := 0
	for repo, h := range heads {
		next[repo] = h.SHA
		for _, p := range h.ChangedPaths {
			paths = append(paths, repo+"/"+p)
		}
		lines += h.Lines
	}
	slices.Sort(paths)
	rawHeads, _ := json.Marshal(next)
	rawPaths, _ := json.Marshal(paths)
	// The new heads over the old; what changed is added to what picks the
	// next review's reviewers.
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(jsonb_set(state, '{heads}',
			COALESCE(state->'heads', '{}'::jsonb) || $2::jsonb),
			'{changedPaths}', (SELECT COALESCE(jsonb_agg(DISTINCT p ORDER BY p), '[]'::jsonb)
				FROM jsonb_array_elements_text(COALESCE(state->'changedPaths', '[]'::jsonb) || $3::jsonb) p))
		WHERE id = $1`, workflowID, rawHeads, rawPaths); err != nil {
		return err
	}
	raw, _ := json.Marshal(heads)
	if _, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'published', heads = $2::jsonb, settled_at = now(), error = NULL
		WHERE id = $1`, pubID, raw); err != nil {
		return err
	}
	var commits []string
	for _, repo := range slices.Sorted(maps.Keys(heads)) {
		commits = append(commits, repo+"@"+short(heads[repo].SHA))
	}
	_, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, "published", "published:"+pubID,
		fmt.Sprintf("Your publish %s is on the task branch: %s, %d lines in %d files. Run a review before the pull request.",
			pubID, strings.Join(commits, " "), lines, len(paths)))
	return err
}

// PublishRefusedTx records a publish refused once lux had pushed: nothing
// moved, and the conductor is woken with why.
func PublishRefusedTx(ctx context.Context, tx pgx.Tx, ref RunRef, pubID, why string) error {
	tag, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'refused', error = $2, settled_at = now()
		WHERE id = $1 AND status IN ('requested', 'asked', 'pushed')`, pubID, clip(why, 1000))
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, "publish_refused", "publish_refused:"+pubID,
		fmt.Sprintf("Your publish %s was refused, nothing moved: %s", pubID, clip(oneLine(why), 220)))
	return err
}

// UntestedConductorHeadTx says whether the heads are, in some repository,
// a commit the conductor published that no review or test Run has
// completed on since: what the pull request gate refuses.
func UntestedConductorHeadTx(ctx context.Context, tx pgx.Tx, taskID string, heads map[string]string) (bool, error) {
	if len(heads) == 0 {
		return false, nil
	}
	raw, _ := json.Marshal(heads)
	var untested bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM conductor_publishes p, jsonb_each_text($2::jsonb) h(repo, sha)
		WHERE p.task_id = $1 AND p.status = 'published' AND p.heads->h.repo->>'sha' = h.sha
		  AND NOT EXISTS (SELECT 1 FROM runs k WHERE k.task_id = $1 AND k.phase IN ('review', 'test')
		    AND k.status = 'completed' AND k.base_refs->>h.repo = h.sha))`, taskID, raw).Scan(&untested)
	return untested, err
}

// untestedRefusal is UntestedConductorHeadTx as the gate's refusal.
func untestedRefusal(ctx context.Context, tx pgx.Tx, st *State) error {
	untested, err := UntestedConductorHeadTx(ctx, tx, st.TaskID, st.Heads)
	if err != nil {
		return err
	}
	if untested {
		return refusef("%s", UntestedHead)
	}
	return nil
}

// reviewUntested keeps a head that is the conductor's untested commit from
// the pull request: under the conductor, the decision before it is the
// conductor's again (its gate tools refuse until a review ran); under the
// policy, a review round starts. Chat says so once per head. diverted:
// the step goes there instead.
func (w *steps) reviewUntested(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, bool, error) {
	var untested bool
	err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		var err error
		if untested, err = UntestedConductorHeadTx(ctx, tx, st.TaskID, st.Heads); err != nil || !untested {
			return err
		}
		heads, _ := json.Marshal(nonNilMap(st.Heads))
		var said bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
			AND payload->>'about' = 'untested_head' AND payload->'heads' = $3::jsonb)`, st.TaskID, EvChatNotice, heads).Scan(&said); err != nil || said {
			return err
		}
		return emitTx(ctx, tx, sc.OrganizationID, st, EvChatNotice, map[string]any{"about": "untested_head",
			"text": "Not opening the pull request yet: " + UntestedHead + ".", "heads": json.RawMessage(heads)})
	})
	if err != nil || !untested {
		return workflow.Result{}, false, err
	}
	if st.conducted() {
		res, err := w.next(ctx, sc, st, PointBeforePR, "openPullRequest", UntestedHead+".")
		return res, true, err
	}
	return workflow.Result{Next: "review", State: st}, true, nil
}

// CheckoutLine is the line a conductor is told about one repository's
// sync to the task branch, when it did not simply bring the checkout
// current: "" for up-to-date or fast-forward.
func CheckoutLine(repo, ref, status, errText string, ahead, behind int, dirty, diverged bool) string {
	merge := fmt.Sprintf("`git merge lux/%s`", ref)
	switch status {
	case "", "up-to-date", "fast-forward":
		return ""
	case "kept":
		if !dirty && !diverged && ahead == 0 {
			// Nothing of its own in the way: HEAD is not on the task branch,
			// and lux never switches it.
			return fmt.Sprintf("%s: your checkout is %d behind and not on the task branch; `git switch -C %s lux/%s` to take it.",
				repo, behind, ref, ref)
		}
		var why []string
		if dirty {
			why = append(why, "has local changes")
		}
		if diverged || ahead > 0 {
			why = append(why, fmt.Sprintf("%d commits of its own", ahead))
		}
		if len(why) == 0 {
			why = append(why, "was kept as it is")
		}
		return fmt.Sprintf("%s: your checkout is %d behind and %s; %s to take them in.", repo, behind, strings.Join(why, " and "), merge)
	case "ahead":
		return fmt.Sprintf("%s: your checkout is %d ahead of the task branch and not behind: publish, or keep working.", repo, ahead)
	case "failed":
		return fmt.Sprintf("%s: your checkout could not be brought current (%s); it is as you left it.", repo, clip(oneLine(errText), 120))
	}
	return fmt.Sprintf("%s: your checkout is %d behind and %d ahead (%s); %s to take the task branch in.", repo, behind, ahead, status, merge)
}
