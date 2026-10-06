package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
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
	PublishMoving    = "moving"
	PublishPublished = "published"
	PublishRefused   = "refused"
	PublishStalled   = "stalled"
)

// writerPhases (SQL list): the phases whose Runs push to the task branch.
// A live one is another writer.
const writerPhases = `('implement', 'fix', 'simplify')`

// ErrStepRunning: a step of the delivery holds it; try again after.
var ErrStepRunning = errors.New("a step of the delivery is running")

// stepHolds says whether a step of the delivery wfID holds its lease now;
// lock (" FOR UPDATE", or "") locks its row for the rest of tx.
func stepHolds(ctx context.Context, tx pgx.Tx, wfID, lock string) (bool, error) {
	var held bool
	err := tx.QueryRow(ctx, `SELECT locked_by IS NOT NULL AND locked_until > now() FROM workflow_runs WHERE id = $1`+lock,
		wfID).Scan(&held)
	return held, err
}

// liveWriter (SQL, $1 the task): a phase Run that pushes to the task
// branch, not ended.
const liveWriter = `EXISTS (SELECT 1 FROM runs w WHERE w.task_id = $1 AND w.phase IN ` + writerPhases + `
	AND w.status IN ('pending', 'scheduled', 'starting', 'running', 'paused'))`

// ConductPublish is publish: the task's live conductor asks for what it
// committed in its checkout to be pushed and taken to the task branch.
// Refused, writing nothing, unless it is the live conductor, it takes the
// delivery's decisions, its checkout is writable, and no other writer is
// at work; the limit and whether its checkout is current are checked once
// lux has pushed (the publish worker, phases.SettlePublishes). Returns the
// publish's id.
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
		EXISTS (SELECT 1 FROM conductor_publishes WHERE run_id = $2 AND status IN `+publishLive+`)`,
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
	if _, err := tx.Exec(ctx, `INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, message,
			workflow_run_id, attempt, branch)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, ref.Org, ref.TaskID, ref.RunID, "publish-"+id,
		clip(strings.TrimSpace(message), 2000), d.WorkflowID, d.State.Attempt, d.Branch(ref.TaskID)); err != nil {
		return "", err
	}
	return id, nil
}

// Branch is the delivery's task branch.
func (d *Delivery) Branch(taskID string) string {
	if d.State.Branch != "" {
		return d.State.Branch
	}
	return BranchFor(taskID, d.State.Attempt)
}

// publishLive (SQL list): the statuses of a publish not settled yet.
const publishLive = `('requested', 'asked', 'pushed', 'moving')`

// ErrPublishMoving: the task's conductor's publish is moving its task
// branch now (a reservation of a second or so). Writers wait, and so do
// replacing or ending its conductor, changing its decider and ending the
// task; an API caller is answered 409, a workflow step runs again soon.
var ErrPublishMoving = workflow.Wait{After: time.Second,
	Why: "the conductor's publish is moving the task branch now; try again in a moment"}

// RefuseWhileMovingTx is ErrPublishMoving when the task's publish is
// moving. The caller holds the lock the reservation takes for what it
// fences (ReservePublishTx).
func RefuseWhileMovingTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	var moving bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_publishes WHERE task_id = $1 AND status = 'moving')`,
		taskID).Scan(&moving); err != nil {
		return err
	}
	if moving {
		return ErrPublishMoving
	}
	return nil
}

// lockAndRefuseWhileMovingTx is RefuseWhileMovingTx for a caller that
// does not hold the delivery's row: it locks it first, for the rest of tx.
func lockAndRefuseWhileMovingTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	if _, err := LoadDelivery(ctx, tx, taskID); err != nil {
		return err
	}
	return RefuseWhileMovingTx(ctx, tx, taskID)
}

// RefuseUnmovedTx settles a conductor's publishes that have not moved
// anything as refused, saying why, as it ends; ErrPublishMoving while one
// is moving, which must finish first.
func RefuseUnmovedTx(ctx context.Context, tx pgx.Tx, ref RunRef, why string) error {
	rows, err := tx.Query(ctx, `SELECT id, status FROM conductor_publishes WHERE run_id = $1 AND status IN `+publishLive+`
		FOR UPDATE`, ref.RunID)
	if err != nil {
		return err
	}
	live, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Status string }])
	if err != nil {
		return err
	}
	for _, p := range live {
		if p.Status == PublishMoving {
			return ErrPublishMoving
		}
	}
	for _, p := range live {
		if err := PublishRefusedTx(ctx, tx, ref, p.ID, "", why); err != nil {
			return err
		}
	}
	return nil
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

// PublishOf is a publish as its settlement checks it: its conductor, and
// the delivery and attempt it was asked on. Claim is the worker's claim on
// it (conductor_publishes.claim_token); its writes land only while it
// holds.
type PublishOf struct {
	ID, TaskID, RunID, WorkflowID string
	Attempt                       int
	Claim                         string
}

// ErrClaimLost: the publish was settled, or another worker claimed it,
// since this worker did; it writes nothing more.
var ErrClaimLost = errors.New("the publish's claim was lost")

// claimed (SQL, $n the claim): the publish's claim is still the worker's.
func claimed(n int) string { return fmt.Sprintf("claim_token = $%d", n) }

// publishEligibleTx is why the publish may not go on now, "" when it may:
// what ConductPublish checked, again — its conductor live and not ending,
// its checkout writable, it still deciding, no other writer, and the
// delivery and attempt it was asked on still the task's, not ended. With
// lock, the task's Chat lock, then the conductor's Run, then the
// delivery's row are held for the rest of tx: what ending or replacing the
// conductor, changing the decider and starting a writer take.
func publishEligibleTx(ctx context.Context, tx pgx.Tx, p PublishOf, lock bool) (*Delivery, string, error) {
	runLock, load := "", ReadDelivery
	if lock {
		if err := LockChat(ctx, tx, p.TaskID); err != nil {
			return nil, "", err
		}
		runLock, load = " FOR NO KEY UPDATE", LoadDelivery
	}
	var live bool
	var readOnly string
	err := tx.QueryRow(ctx, `SELECT `+LiveConductor+` AND NOT COALESCE(`+Ending+`, false), COALESCE(r.checkout_read_only, '')
		FROM runs r WHERE r.id = $1`+runLock, p.RunID).Scan(&live, &readOnly)
	if err != nil && !db.IsNotFound(err) {
		return nil, "", err
	}
	d, err := load(ctx, tx, p.TaskID)
	if err != nil {
		return nil, "", err
	}
	if lock && d != nil {
		// Read again past the lock, in a statement of its own: the locking
		// one's task status may predate a change it waited for.
		if d, err = ReadDelivery(ctx, tx, p.TaskID); err != nil {
			return nil, "", err
		}
	}
	var writer bool
	if err := tx.QueryRow(ctx, `SELECT `+liveWriter, p.TaskID).Scan(&writer); err != nil {
		return nil, "", err
	}
	switch {
	case !live:
		return d, "you are no longer this task's conductor", nil
	case readOnly != "":
		return d, "your checkout is read-only for this conversation: " + readOnly, nil
	case d == nil || !d.Live():
		return d, "this task has no delivery in progress", nil
	case Ended(d.TaskStatus):
		return d, "this task is " + d.TaskStatus, nil
	case d.WorkflowID != p.WorkflowID || d.State.Attempt != p.Attempt:
		return d, "the task was started over since you published", nil
	case !d.State.conducted():
		return d, refusedPolicy, nil
	case writer:
		return d, refusedWriter, nil
	}
	return d, "", nil
}

// PublishEligibleTx is why the publish may not go on now, "" when it may
// (publishEligibleTx, unlocked).
func PublishEligibleTx(ctx context.Context, tx pgx.Tx, p PublishOf) (string, error) {
	_, why, err := publishEligibleTx(ctx, tx, p, false)
	return why, err
}

// LoadPublishTarget reads a publish's target in tx and whether it may go
// on (PublishEligibleTx).
func LoadPublishTarget(ctx context.Context, tx pgx.Tx, p PublishOf) (PublishTarget, error) {
	var t PublishTarget
	d, why, err := publishEligibleTx(ctx, tx, p, false)
	if err != nil {
		return t, err
	}
	t.Refused = why
	if d == nil {
		return t, nil
	}
	held, err := stepHolds(ctx, tx, d.WorkflowID, "")
	if err != nil {
		return t, err
	}
	t.WorkflowID, t.Branch, t.Heads, t.Settled = d.WorkflowID, d.Branch(p.TaskID), nonNilMap(d.State.Heads), !held
	var projectPolicy, orgPolicy []byte
	if err := tx.QueryRow(ctx, `SELECT p.delivery_policy, o.delivery_policy FROM tasks t JOIN projects p ON p.id = t.project_id
		JOIN organizations o ON o.id = t.organization_id WHERE t.id = $1`, p.TaskID).Scan(&projectPolicy, &orgPolicy); err != nil {
		return t, err
	}
	policy, err := ResolvePolicy(orgPolicy, projectPolicy)
	if err != nil {
		return t, err
	}
	t.MaxLines, t.MaxFiles = policy.ConductorEditLines, policy.ConductorEditFiles
	return t, nil
}

// Move is one repository's part of a moving publish: its task branch from
// From (what the comparison measured, a branch name where the task had no
// head) to Head, and how that went (MoveStatus).
type Move struct {
	RepoID       string   `json:"repoId"`
	Slug         string   `json:"slug"`
	From         string   `json:"from"`
	Base         string   `json:"base"`
	Head         string   `json:"head"`
	ChangedPaths []string `json:"changedPaths"`
	Lines        int      `json:"lines"`
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
	// When its fast-forward was first sent (AttemptMoveTx): from then on
	// the branch may have moved whatever the forge answered.
	AttemptedAt *time.Time `json:"attemptedAt,omitempty"`
}

// A Move's status. Stalled: its fast-forward was sent and whether it
// landed could not be confirmed (MoveUncertainFor).
const (
	MovePending = "pending"
	MoveMoved   = "moved"
	MoveRefused = "refused"
	MoveStalled = "stalled"
)

// MoveUncertainFor is how long a sent fast-forward whose outcome the forge
// will not confirm is reconciled before it is settled as stalled.
const MoveUncertainFor = 30 * time.Minute

// AttemptMoveTx records, before the first fast-forward of repo's task
// branch is sent, that it was; returns when it first was.
func AttemptMoveTx(ctx context.Context, tx pgx.Tx, p PublishOf, repo string) (time.Time, error) {
	var at time.Time
	err := tx.QueryRow(ctx, `UPDATE conductor_publishes
		SET moves = jsonb_set(moves, ARRAY[$2::text, 'attemptedAt'], COALESCE(moves->$2->'attemptedAt', to_jsonb(now())))
		WHERE id = $1 AND status = 'moving' AND moves ? $2 AND `+claimed(3)+`
		RETURNING (moves->$2->>'attemptedAt')::timestamptz`, p.ID, repo, p.Claim).Scan(&at)
	if db.IsNotFound(err) {
		return at, ErrClaimLost
	}
	return at, err
}

// ErrNotReserved: what the comparisons measured is no longer the task's
// (its heads moved, a step holds the delivery, or the publish was settled
// meanwhile); measure again.
var ErrNotReserved = errors.New("the publish could not be reserved as measured")

// ReservePublishTx records the publish as moving with its moves, once
// everything that let it go on still holds, under the locks that fence
// it (publishEligibleTx with lock): while it moves, no writer starts, its
// conductor is not ended or replaced, its decider does not change and its
// task does not end. Returns why it is refused instead, "" once reserved;
// ErrNotReserved to measure again.
func ReservePublishTx(ctx context.Context, tx pgx.Tx, p PublishOf, heads map[string]string, moves map[string]Move) (string, error) {
	d, why, err := publishEligibleTx(ctx, tx, p, true)
	if err != nil || why != "" {
		return why, err
	}
	held, err := stepHolds(ctx, tx, d.WorkflowID, "")
	if err != nil {
		return "", err
	}
	if held || !maps.Equal(nonNilMap(d.State.Heads), nonNilMap(heads)) {
		return "", ErrNotReserved
	}
	raw, _ := json.Marshal(moves)
	tag, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'moving', moves = $2::jsonb, branch = $3, workflow_run_id = $4
		WHERE id = $1 AND status = 'pushed' AND `+claimed(5), p.ID, raw, d.Branch(p.TaskID), d.WorkflowID, p.Claim)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotReserved
	}
	return "", nil
}

// RecheckMovingTx is why a moving publish's repositories not moved yet
// may no longer move, "" when they may (publishEligibleTx with lock).
func RecheckMovingTx(ctx context.Context, tx pgx.Tx, p PublishOf) (string, error) {
	_, why, err := publishEligibleTx(ctx, tx, p, true)
	return why, err
}

// MovedTx records one repository's move as it happens.
func MovedTx(ctx context.Context, tx pgx.Tx, p PublishOf, repo string, m Move) error {
	raw, _ := json.Marshal(m)
	tag, err := tx.Exec(ctx, `UPDATE conductor_publishes SET moves = jsonb_set(moves, ARRAY[$2::text], $3::jsonb)
		WHERE id = $1 AND status = 'moving' AND `+claimed(4), p.ID, repo, raw, p.Claim)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrClaimLost
	}
	return err
}

// LockRecordingTx begins recording a moving publish, before anything is
// written for it: the delivery's row, then the publish's, locked for the
// rest of tx. ErrStepRunning when a step of the delivery holds it now;
// ErrClaimLost when the publish is settled or no longer the worker's.
func LockRecordingTx(ctx context.Context, tx pgx.Tx, p PublishOf) error {
	held, err := stepHolds(ctx, tx, p.WorkflowID, " FOR UPDATE")
	if err != nil {
		return err
	}
	if held {
		return ErrStepRunning
	}
	var ours bool
	err = tx.QueryRow(ctx, `SELECT status = 'moving' AND `+claimed(2)+` FROM conductor_publishes WHERE id = $1 FOR UPDATE`,
		p.ID, p.Claim).Scan(&ours)
	if err == nil && !ours || db.IsNotFound(err) {
		return ErrClaimLost
	}
	return err
}

// OverLimit is the refusal for a publish of lines in files past the
// target's limits, "" within them.
func (t PublishTarget) OverLimit(lines, files int) string {
	if lines > t.MaxLines || files > t.MaxFiles {
		return fmt.Sprintf(RefusedTooBig, lines, files, t.MaxLines, t.MaxFiles)
	}
	return ""
}

// PublishedTx records a moving publish once each repository's move is
// settled and at least one moved: the delivery's heads advance as a
// phase's would (so the next phase starts from them and the gate's answer
// at the old heads no longer holds), and the conductor is woken saying
// what moved and, by repository, what was refused and why (refused), and
// what could not be confirmed (stalled: stalledNoticeTx). The
// caller moved the branches, and began with LockRecordingTx in tx.
// ErrClaimLost, rolling tx back, when the publish is no longer the
// worker's to record.
func PublishedTx(ctx context.Context, tx pgx.Tx, ref RunRef, p PublishOf, heads map[string]PublishHead,
	refused, stalled map[string]string) error {
	next := map[string]string{}
	var paths []string
	lines := 0
	for repo, h := range heads {
		next[repo] = h.SHA
		for _, path := range h.ChangedPaths {
			paths = append(paths, repo+"/"+path)
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
		WHERE id = $1`, p.WorkflowID, rawHeads, rawPaths); err != nil {
		return err
	}
	var notMoved []string
	for _, repo := range slices.Sorted(maps.Keys(refused)) {
		notMoved = append(notMoved, fmt.Sprintf("%s was refused: %s", repo, clip(oneLine(refused[repo]), 160)))
	}
	for _, repo := range slices.Sorted(maps.Keys(stalled)) {
		notMoved = append(notMoved, fmt.Sprintf("%s could not be confirmed at %s", repo, short(stalled[repo])))
	}
	raw, _ := json.Marshal(heads)
	tag, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'published', heads = $2::jsonb, settled_at = now(),
			error = NULLIF($3, ''), next_attempt_at = NULL
		WHERE id = $1 AND status = 'moving' AND `+claimed(4), p.ID, raw, clip(strings.Join(notMoved, "; "), 1000), p.Claim)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrClaimLost
	}
	var commits []string
	for _, repo := range slices.Sorted(maps.Keys(heads)) {
		commits = append(commits, repo+"@"+short(heads[repo].SHA))
	}
	line := fmt.Sprintf("Your publish %s is on the task branch: %s, %d lines in %d files. Run a review before the pull request.",
		p.ID, strings.Join(commits, " "), lines, len(paths))
	if len(notMoved) > 0 {
		line = fmt.Sprintf("Your publish %s is on the task branch in part: published %s; %s. Run a review before the pull request.",
			p.ID, strings.Join(commits, " "), strings.Join(notMoved, "; "))
	}
	if _, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, "published", "published:"+p.ID, line); err != nil {
		return err
	}
	return stalledNoticeTx(ctx, tx, ref, p.ID, stalled)
}

// PublishRefusedTx records a publish refused before anything moved, and
// wakes the conductor with why. claim is the worker's claim on it, ""
// for a caller holding the publish's row lock (RefuseUnmovedTx), to whom
// a publish settled already is no error.
func PublishRefusedTx(ctx context.Context, tx pgx.Tx, ref RunRef, pubID, claim, why string) error {
	tag, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'refused', error = $2, settled_at = now(), next_attempt_at = NULL
		WHERE id = $1 AND status IN `+publishLive+` AND ($3 = '' OR claim_token = $3)
		  AND NOT EXISTS (SELECT 1 FROM jsonb_each(moves) m WHERE m.value->>'status' = 'moved')`, pubID, clip(why, 1000), claim)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if claim != "" {
			return ErrClaimLost
		}
		return nil
	}
	_, err = RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, "publish_refused", "publish_refused:"+pubID,
		fmt.Sprintf("Your publish %s was refused, nothing moved: %s", pubID, clip(oneLine(why), 220)))
	return err
}

// PublishStalledTx settles a moving publish none of whose moves was
// confirmed and some of which may have landed (stalled, by repository the
// head it was sent to move to): nothing is recorded as published, the
// fence is released, and Chat and the conductor are told to check.
func PublishStalledTx(ctx context.Context, tx pgx.Tx, ref RunRef, p PublishOf, stalled map[string]string) error {
	tag, err := tx.Exec(ctx, `UPDATE conductor_publishes SET status = 'stalled', settled_at = now(), next_attempt_at = NULL,
			error = 'the move could not be confirmed'
		WHERE id = $1 AND status = 'moving' AND `+claimed(2)+`
		  AND NOT EXISTS (SELECT 1 FROM jsonb_each(moves) m WHERE m.value->>'status' IN ('moved', 'pending'))`, p.ID, p.Claim)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrClaimLost
	}
	return stalledNoticeTx(ctx, tx, ref, p.ID, stalled)
}

// stalledNoticeTx says in Chat, for each repository whose move could not
// be confirmed, that its task branch is to be checked, and wakes the
// conductor once for the publish.
func stalledNoticeTx(ctx context.Context, tx pgx.Tx, ref RunRef, pubID string, stalled map[string]string) error {
	if len(stalled) == 0 {
		return nil
	}
	var said []string
	for _, repo := range slices.Sorted(maps.Keys(stalled)) {
		text := fmt.Sprintf("dude could not confirm whether %s's task branch moved to %s; check the branch", repo, stalled[repo])
		said = append(said, fmt.Sprintf("%s to %s", repo, short(stalled[repo])))
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: ref.Org, ProjectID: ref.ProjectID,
			TaskID: ref.TaskID, RunID: ref.RunID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
			CorrelationID: ref.TaskID, Payload: map[string]any{"about": "publish_stalled", "publishId": pubID, "repo": repo,
				"text": text}}); err != nil {
			return err
		}
	}
	_, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, "publish_stalled", "publish_stalled:"+pubID,
		fmt.Sprintf("Your publish %s could not be confirmed on the task branch (%s): check the branch before you publish again.",
			pubID, strings.Join(said, ", ")))
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
// current: "" for up-to-date or fast-forward. An operation in progress
// is told whatever the status, with only the commands git accepts in it.
func CheckoutLine(s lux.SyncResult) string {
	repo, ref, ahead, behind := s.Repo, s.Ref, s.Ahead, s.Behind
	merge := fmt.Sprintf("`git merge lux/%s`", ref)
	if s.Operation != "" {
		return operationLine(repo, ref, s.Operation, behind)
	}
	switch s.Status {
	case "", "up-to-date", "fast-forward":
		return ""
	case "kept":
		if !s.Dirty && !s.Diverged && ahead == 0 {
			// Nothing of its own in the way: HEAD is not on the task branch,
			// and lux never switches it.
			return fmt.Sprintf("%s: your checkout is %d behind and not on the task branch; `git switch -C %s lux/%s` to take it.",
				repo, behind, ref, ref)
		}
		var why []string
		if s.Dirty {
			why = append(why, "has local changes")
		}
		if s.Diverged || ahead > 0 {
			why = append(why, fmt.Sprintf("%d commits of its own", ahead))
		}
		if len(why) == 0 {
			why = append(why, "was kept as it is")
		}
		return fmt.Sprintf("%s: your checkout is %d behind and %s; %s to take them in.", repo, behind, strings.Join(why, " and "), merge)
	case "ahead":
		return fmt.Sprintf("%s: your checkout is %d ahead of the task branch and not behind: publish, or keep working.", repo, ahead)
	case "failed":
		return fmt.Sprintf("%s: your checkout could not be brought current (%s); it is as you left it.", repo, clip(oneLine(s.Error), 120))
	}
	return fmt.Sprintf("%s: your checkout is %d behind and %d ahead (%s); %s to take the task branch in.", repo, behind, ahead, s.Status, merge)
}

// operationLine tells a checkout stopped mid-operation how to finish or
// abort it. Switching branches or merging is refused by git until then,
// so neither is advised before it.
func operationLine(repo, ref, op string, behind int) string {
	var finish string
	switch op {
	case lux.OperationRebase, lux.OperationAm, lux.OperationMerge, lux.OperationCherryPick, lux.OperationRevert:
		finish = fmt.Sprintf("resolve and `git %s --continue`, or `git %s --abort`", op, op)
	case lux.OperationSequencer:
		finish = "resolve and `git cherry-pick --continue` (or `git revert --continue`), or `git cherry-pick --abort` (or `git revert --abort`)"
	default:
		finish = "finish or abort it"
	}
	then := ""
	if behind > 0 {
		then = fmt.Sprintf("; then `git merge lux/%s`", ref)
	}
	return fmt.Sprintf("%s: %s is in progress in your checkout (%d behind the task branch): %s%s.",
		repo, operationName(op), behind, finish, then)
}

// operationName is a git operation lux reported, as the conductor reads it.
func operationName(op string) string {
	switch op {
	case lux.OperationSequencer:
		return "a cherry-pick or revert of several commits"
	case lux.OperationAm:
		return "a `git am`"
	case "":
		return "a git operation"
	}
	return "a " + op
}

// MidOperationRefusal is why a publish is refused when lux would not push
// repo, an operation (lux.Operation*) being in progress in its checkout.
func MidOperationRefusal(repo, op string) string {
	return fmt.Sprintf("%s: %s is in progress in your checkout: finish or abort it, then publish.", repo, operationName(op))
}
