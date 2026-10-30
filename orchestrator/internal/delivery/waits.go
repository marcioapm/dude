package delivery

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A conductor's wait (wait_for, conductor_waits): what it waits on — Runs
// of its task ending, a pull request's checks becoming final on a head —
// and a deadline every wait has. One open wait per task: a new one
// replaces it. The phase syncer evaluates open waits each sweep
// (EvaluateWaitTx) and, once one holds or its deadline passes, records one
// wake reason (kind WakeWait, key "wait:<id>") and ends it in the same
// transaction. It reads only what dude already keeps: Runs' statuses and
// pull_requests, which webhooks and the reconciler keep fresh. Nothing
// else is woken or held back by it: dude's own wakes come as they do.

// WakeWait is the conductor_wakes kind for a conductor's wait that held or
// reached its deadline.
const WakeWait = "wait"

// Ledger events of a wait, on the conductor's Run. Payloads:
// {waitId, conditions, wake, deadlineAt} and {waitId, by, held, of}.
const (
	EvConductorWaiting   = "conductor.waiting"
	EvConductorWaitEnded = "conductor.wait_ended"
)

// Bounds of a wait.
const (
	WaitMinDeadline   = time.Minute
	WaitMaxDeadline   = 24 * time.Hour
	WaitMaxConditions = 8
	WaitMaxRuns       = 8
	WaitNoteMax       = 500
	waitLineChars     = 2000
)

// How a wait ended (conductor_waits.ended_by).
const (
	WaitHeld           = "held"
	WaitDeadline       = "deadline"
	WaitReplaced       = "replaced"
	WaitTaskEnded      = "task_ended"
	WaitConductorEnded = "conductor_ended"
)

// WaitCondition is one condition as the conductor names it: exactly one
// of its fields.
type WaitCondition struct {
	RunsEnded     []string     `json:"runsEnded,omitempty"`
	PRChecksFinal *PRChecksAsk `json:"prChecksFinal,omitempty"`
}

// PRChecksAsk names a pull request (12, #12, repo#12) and the head its
// checks are waited on at: a full SHA, or "" for its head now.
type PRChecksAsk struct {
	PR   string `json:"pr"`
	Head string `json:"head,omitempty"`
}

// WaitAsk is a wait_for call.
type WaitAsk struct {
	Conditions []WaitCondition
	// "all" (or "") or "any".
	Wake     string
	Deadline string
	Note     string
	// The tool call's id: a retried call is the same wait.
	CallKey string
}

// WaitOpened is what a wait_for call did: opened (or found, for a retried
// call) WaitID, or found it holds already (Holds, with its Digest) and
// opened nothing. DecisionPoint is the decision the delivery waits on from
// the conductor at the call, "" for none.
type WaitOpened struct {
	WaitID        string
	DeadlineAt    time.Time
	Holds         bool
	Digest        string
	DecisionPoint string
}

// waitCond is a condition as stored (conductor_waits.conditions).
type waitCond struct {
	Kind string   `json:"kind"`
	Runs []string `json:"runs,omitempty"`
	// pr_checks_final: the pull_requests row, how the conductor reads it
	// (repo#number), and the head pinned at the call.
	PR     string `json:"pr,omitempty"`
	PRName string `json:"prName,omitempty"`
	Head   string `json:"head,omitempty"`
}

const (
	condRunsEnded     = "runs_ended"
	condPRChecksFinal = "pr_checks_final"
)

// parseWaitDeadline reads a deadline: a Go duration, or an RFC 3339 time,
// from WaitMinDeadline to WaitMaxDeadline ahead of now.
func parseWaitDeadline(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, refuseText(rWaitDeadlineRequired)
	}
	var at time.Time
	if d, err := time.ParseDuration(s); err == nil {
		at = now.Add(d)
	} else if t, err := time.Parse(time.RFC3339, s); err == nil {
		at = t
	} else {
		return time.Time{}, refusef(rWaitDeadlineUnreadable, s)
	}
	if ahead := at.Sub(now); ahead < WaitMinDeadline || ahead > WaitMaxDeadline {
		return time.Time{}, refusef(rWaitDeadlineOutOfRange, s)
	}
	return at, nil
}

// OpenConductorWaitTx is wait_for: the task's live conductor declares what
// it waits on. A retried call (the same call key) is the wait it opened.
// A wait that holds already opens nothing and says so. Otherwise the open
// wait, if any, is ended as replaced and the new one opened, in tx.
func OpenConductorWaitTx(ctx context.Context, tx pgx.Tx, ref RunRef, ask WaitAsk) (WaitOpened, error) {
	var out WaitOpened
	if err := liveConductor(ctx, tx, ref); err != nil {
		return out, err
	}
	var status string
	var attempt int
	if err := tx.QueryRow(ctx, `SELECT t.status::text, r.attempt FROM tasks t JOIN runs r ON r.id = $2 WHERE t.id = $1`,
		ref.TaskID, ref.RunID).Scan(&status, &attempt); err != nil {
		return out, err
	}
	if Ended(status) {
		return out, refusef(rThisTaskYouAreReadOnly, status)
	}
	err := tx.QueryRow(ctx, `SELECT id, deadline_at FROM conductor_waits WHERE conductor_run_id = $1 AND call_key = $2`,
		ref.RunID, ask.CallKey).Scan(&out.WaitID, &out.DeadlineAt)
	if err == nil {
		out.DecisionPoint, err = decisionWaitingTx(ctx, tx, ref.TaskID)
		return out, err
	}
	if !db.IsNotFound(err) {
		return out, err
	}
	all := true
	switch strings.TrimSpace(ask.Wake) {
	case "", "all":
	case "any":
		all = false
	default:
		return out, refusef(rWaitWakeAllOrAny, ask.Wake)
	}
	now := time.Now()
	deadline, err := parseWaitDeadline(ask.Deadline, now)
	if err != nil {
		return out, err
	}
	if utf8.RuneCountInString(ask.Note) > WaitNoteMax {
		return out, refusef(rWaitNoteTooLong, WaitNoteMax)
	}
	conds, err := normalizeWaitTx(ctx, tx, ref.TaskID, ask.Conditions)
	if err != nil {
		return out, err
	}
	if len(conds) > 0 {
		ev, err := evaluateConditionsTx(ctx, tx, conds, all)
		if err != nil {
			return out, err
		}
		if ev.holds {
			out.Holds = true
			out.Digest = renderWaitDigest(ev, "", false, true, nil, ask.Note)
			return out, nil
		}
	}
	if err := endOpenWaitsTx(ctx, tx, ref.TaskID, WaitReplaced); err != nil {
		return out, err
	}
	raw, err := json.Marshal(conds)
	if err != nil {
		return out, err
	}
	out.WaitID = ids.New("cwt")
	if err := tx.QueryRow(ctx, `INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions,
			wake_all, note, deadline_at, since_cursor, call_key)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, NULLIF($8, ''), $9,
			(SELECT COALESCE(max(cursor), 0) FROM events WHERE organization_id = $2), $10)
		RETURNING deadline_at`,
		out.WaitID, ref.Org, ref.TaskID, ref.RunID, attempt, raw, all, ask.Note, deadline, ask.CallKey).Scan(&out.DeadlineAt); err != nil {
		return out, err
	}
	wake := "all"
	if !all {
		wake = "any"
	}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvConductorWaiting, ledger.ActorAgent, map[string]any{
		"waitId": out.WaitID, "conditions": conds, "wake": wake, "deadlineAt": out.DeadlineAt})); err != nil {
		return out, err
	}
	out.DecisionPoint, err = decisionWaitingTx(ctx, tx, ref.TaskID)
	return out, err
}

// decisionWaitingTx is the decision the task's delivery waits on from its
// conductor now, "" for none: waiting decides nothing.
func decisionWaitingTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil {
		return "", err
	}
	if d.Status == "waiting" && d.Step == "conductorDecision" && d.State.Decision != nil && d.State.Decision.Taken == nil {
		return d.State.Decision.Point, nil
	}
	return "", nil
}

// normalizeWaitTx checks the conditions and stores them as the evaluator
// reads them: Runs of this task, a pull request of this task by its row,
// its head pinned.
func normalizeWaitTx(ctx context.Context, tx pgx.Tx, taskID string, in []WaitCondition) ([]waitCond, error) {
	if len(in) > WaitMaxConditions {
		return nil, refusef(rWaitTooManyConditions, WaitMaxConditions)
	}
	out := make([]waitCond, 0, len(in))
	for _, c := range in {
		switch {
		case c.RunsEnded != nil && c.PRChecksFinal != nil, c.RunsEnded == nil && c.PRChecksFinal == nil:
			return nil, refuseText(rWaitOneKindPerCondition)
		case c.RunsEnded != nil:
			var runs []string
			for _, id := range c.RunsEnded {
				if id = strings.TrimSpace(id); id != "" && !slices.Contains(runs, id) {
					runs = append(runs, id)
				}
			}
			if len(runs) == 0 || len(runs) > WaitMaxRuns {
				return nil, refusef(rWaitRunsCount, WaitMaxRuns)
			}
			for _, id := range runs {
				var ours bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1 AND task_id = $2)`, id, taskID).Scan(&ours); err != nil {
					return nil, err
				}
				if !ours {
					return nil, refusef(rWaitNotRunOfThisTask, id)
				}
			}
			out = append(out, waitCond{Kind: condRunsEnded, Runs: runs})
		default:
			repo, number, err := parsePR(c.PRChecksFinal.PR)
			if err != nil {
				return nil, err
			}
			head := strings.TrimSpace(c.PRChecksFinal.Head)
			if head != "" && !fullSHA.MatchString(head) {
				return nil, refusef(rWaitHeadNotFullSHA, head)
			}
			var prID, name, now string
			err = tx.QueryRow(ctx, `SELECT pr.id, r.name, pr.head_sha FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
				WHERE pr.task_id = $1 AND pr.number = $2 AND ($3 = '' OR r.name = $3) ORDER BY pr.created_at DESC LIMIT 1`,
				taskID, number, repo).Scan(&prID, &name, &now)
			if db.IsNotFound(err) {
				return nil, refusef(rWaitNotPullRequestOfThisTask, c.PRChecksFinal.PR)
			}
			if err != nil {
				return nil, err
			}
			if head == "" {
				head = now
			}
			out = append(out, waitCond{Kind: condPRChecksFinal, PR: prID, PRName: name + "#" + strconv.Itoa(number), Head: head})
		}
	}
	return out, nil
}

// waitItem is one fact a wait's digest tells: a Run (Kind "run") or a pull
// request (Kind "pr"). rank orders them worst first.
type waitItem struct {
	Kind string
	// A Run: whose it is (its phase or profile), its id and, once
	// restarted, the id the wait named; its status and error.
	Who, RunID, Was, Status, Error string
	Ended                          bool
	// A pull request: repo#number, the head waited on, its state, its
	// checks there and the failing ones' names; MovedTo, its head now
	// when it moved; Polled, when dude last read it.
	PR, Head, State, Checks, MovedTo, Polled string
	Failing                                  []string
	rank                                     int
}

// waitEval is a wait's conditions as they read now.
type waitEval struct {
	holds     bool
	held, of  int
	items     []waitItem
	waitedAll bool
}

const (
	rankBad = iota
	rankWaiting
	rankGood
)

// evaluateConditionsTx reads each condition from what dude keeps, with no
// locks: a Run's latest replacement (replaced_by) ended; a pull request's
// checks final on the head, its head moved, or it ended. Checks unknown are
// not final: the deadline covers a repository with no CI.
func evaluateConditionsTx(ctx context.Context, tx pgx.Tx, conds []waitCond, all bool) (waitEval, error) {
	ev := waitEval{of: len(conds), waitedAll: all}
	for _, c := range conds {
		held := true
		switch c.Kind {
		case condRunsEnded:
			for _, id := range c.Runs {
				it, err := runItemTx(ctx, tx, id)
				if err != nil {
					return ev, err
				}
				held = held && it.Ended
				ev.items = append(ev.items, it)
			}
		case condPRChecksFinal:
			it, err := prItemTx(ctx, tx, c)
			if err != nil {
				return ev, err
			}
			held = it.rank != rankWaiting
			ev.items = append(ev.items, it)
		}
		if held {
			ev.held++
		}
	}
	if all {
		ev.holds = ev.held == ev.of
	} else {
		ev.holds = ev.held > 0
	}
	slices.SortStableFunc(ev.items, func(a, b waitItem) int { return cmp.Compare(a.rank, b.rank) })
	return ev, nil
}

// maxReplacements bounds following a Run's replacements.
const maxReplacements = 50

func runItemTx(ctx context.Context, tx pgx.Tx, id string) (waitItem, error) {
	it := waitItem{Kind: "run", RunID: id}
	for range maxReplacements {
		var replacedBy *string
		if err := tx.QueryRow(ctx, `SELECT status::text, replaced_by, COALESCE(error, ''),
				COALESCE(phase::text, child_profile, role::text, 'agent') FROM runs WHERE id = $1`, it.RunID).
			Scan(&it.Status, &replacedBy, &it.Error, &it.Who); err != nil {
			return it, err
		}
		if replacedBy == nil {
			break
		}
		it.Was, it.RunID = id, *replacedBy
	}
	switch it.Status {
	case "completed":
		it.Ended, it.rank = true, rankGood
	case "failed", "aborted":
		it.Ended, it.rank = true, rankBad
	default:
		it.rank, it.Error = rankWaiting, ""
	}
	return it, nil
}

func prItemTx(ctx context.Context, tx pgx.Tx, c waitCond) (waitItem, error) {
	it := waitItem{Kind: "pr", PR: c.PRName, Head: c.Head}
	var head string
	var checksJSON []byte
	var polled *time.Time
	if err := tx.QueryRow(ctx, `SELECT state::text, head_sha, checks::text, checks_json, last_polled_at FROM pull_requests WHERE id = $1`,
		c.PR).Scan(&it.State, &head, &it.Checks, &checksJSON, &polled); err != nil {
		return it, err
	}
	if polled != nil {
		it.Polled = polled.UTC().Format(time.RFC3339)
	}
	switch {
	case it.State == forge.StateMerged:
		it.rank = rankGood
	case it.State == forge.StateClosed:
		it.rank = rankBad
	case head != c.Head:
		it.MovedTo, it.rank = head, rankBad
	case it.Checks == "failing":
		var checks []forge.Check
		_ = json.Unmarshal(checksJSON, &checks)
		for _, ch := range checks {
			if ch.Failed() {
				it.Failing = append(it.Failing, ch.Name)
			}
		}
		it.rank = rankBad
	case it.Checks == "passing":
		it.rank = rankGood
	default:
		it.rank = rankWaiting
	}
	return it, nil
}

// waitCount is one kind of thing that happened meanwhile, counted.
type waitCount struct {
	What string
	N    int
}

// meanwhileTx counts what happened on the task since the wait was opened
// that its conditions do not cover: people's comments and reviews on its
// pull requests, feedback submitted, and people's messages in Chat. Counts
// only, no text.
func meanwhileTx(ctx context.Context, tx pgx.Tx, taskID string, since int64) ([]waitCount, error) {
	var commented, reviewed, feedback, messages int
	// A person in Chat: signed in ('person') or by API key ('human').
	if err := tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE event_type = $3),
			count(*) FILTER (WHERE event_type = $4),
			count(*) FILTER (WHERE event_type = $5),
			count(*) FILTER (WHERE event_type = $6 AND actor_type IN ('person', 'human'))
		FROM events WHERE task_id = $1 AND cursor > $2 AND event_type IN ($3, $4, $5, $6)`,
		taskID, since, EvPullRequestCommented, EvPullRequestReviewed, EvFeedbackSubmitted, EvChatMessage).
		Scan(&commented, &reviewed, &feedback, &messages); err != nil {
		return nil, err
	}
	var out []waitCount
	for _, c := range []waitCount{{"commented", commented}, {"reviewed", reviewed}, {"feedback", feedback}, {"messages", messages}} {
		if c.N > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}

// renderWaitDigest is a wait's one wake line (wakes/wait): how it ended,
// its facts worst first, what happened meanwhile and the conductor's note.
func renderWaitDigest(ev waitEval, id string, deadline, now bool, meanwhile []waitCount, note string) string {
	return render(waitTmpl, struct {
		ID                 string
		Deadline, Now, Any bool
		Held, Of           int
		Items              []waitItem
		Meanwhile          []waitCount
		Note               string
	}{id, deadline, now, !ev.waitedAll, ev.held, ev.of, ev.items, meanwhile, strings.TrimSpace(note)})
}

// EvaluateWaitTx evaluates one open wait: once it holds or its deadline
// passed, its wake reason is recorded and it ends, in tx, so a crash
// before the commit evaluates it again and one after leaves the reason
// pending. A wait on a task that ended (closed, not stopped for a pick-up)
// ends with no wake. It is first read with no locks, and only one that
// would end is read again under the task's Chat lock, its row locked, and
// ended from that reading. Returns whether it ended.
func EvaluateWaitTx(ctx context.Context, tx pgx.Tx, org, taskID, waitID string) (bool, error) {
	if v, err := readWaitTx(ctx, tx, taskID, waitID, false); err != nil || v.by == "" {
		return false, err
	}
	if err := LockChat(ctx, tx, taskID); err != nil {
		return false, err
	}
	v, err := readWaitTx(ctx, tx, taskID, waitID, true)
	if err != nil || v.by == "" {
		return false, err
	}
	if v.by == WaitTaskEnded {
		return true, endWaitTx(ctx, tx, org, taskID, waitID, v.conductor, WaitTaskEnded, waitEval{}, "")
	}
	meanwhile, err := meanwhileTx(ctx, tx, taskID, v.since)
	if err != nil {
		return false, err
	}
	digest := renderWaitDigest(v.ev, waitID, v.by == WaitDeadline, false, meanwhile, v.note)
	if _, err := RecordWakeTx(ctx, tx, org, taskID, WakeWait, "wait:"+waitID, digest); err != nil {
		return false, err
	}
	return true, endWaitTx(ctx, tx, org, taskID, waitID, v.conductor, v.by, v.ev, digest)
}

// waitReading is an open wait as read now: how it would end (by, "" for
// not now) and what ending it needs.
type waitReading struct {
	conductor, note, by string
	since               int64
	ev                  waitEval
}

// readWaitTx reads one open wait and how it would end now, its row locked
// (FOR UPDATE) when lock is set. A wait that is not open reads as not now.
func readWaitTx(ctx context.Context, tx pgx.Tx, taskID, waitID string, lock bool) (waitReading, error) {
	var v waitReading
	var raw []byte
	var all, due bool
	q := `SELECT conductor_run_id, COALESCE(note, ''), conditions, wake_all, deadline_at <= now(), since_cursor
		FROM conductor_waits WHERE id = $1 AND ended_at IS NULL`
	if lock {
		q += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, q, waitID).Scan(&v.conductor, &v.note, &raw, &all, &due, &v.since)
	if db.IsNotFound(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, taskID).Scan(&status); err != nil {
		return v, err
	}
	// A task a person stopped (Halt) ends its wait at the deadline alone:
	// the Runs its Stop kept read aborted until a Resume brings them back.
	// Its conditions are still read then, for the digest.
	halted := false
	if Ended(status) {
		var err error
		if halted, err = HaltedTx(ctx, tx, taskID); err != nil {
			return v, err
		}
		if !halted {
			v.by = WaitTaskEnded
			return v, nil
		}
		if !due {
			return v, nil
		}
	}
	var conds []waitCond
	if err := json.Unmarshal(raw, &conds); err != nil {
		return v, err
	}
	v.ev = waitEval{waitedAll: all}
	if len(conds) > 0 {
		if v.ev, err = evaluateConditionsTx(ctx, tx, conds, all); err != nil {
			return v, err
		}
	}
	switch {
	case len(conds) > 0 && v.ev.holds && !halted:
		v.by = WaitHeld
	case due:
		v.by = WaitDeadline
	}
	return v, nil
}

// endWaitTx ends one open wait, recording how.
func endWaitTx(ctx context.Context, tx pgx.Tx, org, taskID, waitID, conductor, by string, ev waitEval, digest string) error {
	if _, err := tx.Exec(ctx, `UPDATE conductor_waits SET ended_at = now(), ended_by = $2, digest = NULLIF($3, '')
		WHERE id = $1 AND ended_at IS NULL`, waitID, by, digest); err != nil {
		return err
	}
	return waitEndedEvent(ctx, tx, org, taskID, waitID, conductor, by, ev.held, ev.of)
}

func waitEndedEvent(ctx context.Context, tx pgx.Tx, org, taskID, waitID, conductor, by string, held, of int) error {
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvConductorWaitEnded, OrganizationID: org,
		ProjectID: projectID, TaskID: taskID, RunID: conductor, ActorType: ledger.ActorSystem, ActorID: "dude",
		Source: ledger.SourceOrchestrator, CorrelationID: taskID,
		Payload: map[string]any{"waitId": waitID, "by": by, "held": held, "of": of}})
	return err
}

// EndWaitsTx ends the task's open wait with no wake: by WaitTaskEnded as
// the task closes or starts over (EndWakesTx), WaitConductorEnded as a
// person ends its conductor (its abort, a pick-up's Try again). The
// conductor's end also settles the wait reasons recorded and not yet told
// (held mid-turn or for a pick-up), as EndWakesTx settles its reasons:
// nothing tells the next conductor of a wait it did not open.
func EndWaitsTx(ctx context.Context, tx pgx.Tx, taskID, by string) error {
	if by == WaitConductorEnded {
		if _, err := tx.Exec(ctx, `DELETE FROM conductor_wake_attempts o USING conductor_wakes c
			WHERE o.wake_id = c.id AND c.task_id = $1 AND c.kind = $2 AND c.delivered_at IS NULL`, taskID, WakeWait); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conductor_wakes SET delivered_at = now()
			WHERE task_id = $1 AND kind = $2 AND delivered_at IS NULL`, taskID, WakeWait); err != nil {
			return err
		}
	}
	return endOpenWaitsTx(ctx, tx, taskID, by)
}

func endOpenWaitsTx(ctx context.Context, tx pgx.Tx, taskID, by string) error {
	rows, err := tx.Query(ctx, `UPDATE conductor_waits SET ended_at = now(), ended_by = $2
		WHERE task_id = $1 AND ended_at IS NULL RETURNING id, organization_id, conductor_run_id`, taskID, by)
	if err != nil {
		return err
	}
	type ended struct{ ID, Org, Conductor string }
	waits, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ended])
	if err != nil {
		return err
	}
	for _, w := range waits {
		if err := waitEndedEvent(ctx, tx, w.Org, taskID, w.ID, w.Conductor, by, 0, 0); err != nil {
			return err
		}
	}
	return nil
}
