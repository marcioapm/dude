package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// GitHub actions dude takes on a task's pull request for one of its agents
// (agents never hold a token): ask for reviews, resolve a review thread,
// mark a draft ready, re-run failed checks. Each is one of the task's own
// pull requests; each is recorded as pull_request.action by the Run that
// asked. Like a reply (reply.go), an action is checked in a transaction,
// done on GitHub with none open, then recorded.

// PR actions.
const (
	ActionRequestReview = "request_review"
	ActionResolveThread = "resolve_thread"
	ActionMarkReady     = "mark_ready"
	ActionRerunChecks   = "rerun_failed_checks"
)

// PRAction is an agent's request.
type PRAction struct {
	Action string
	// The pull request: its number, or repo#number.
	PR string
	// request_review: whom to ask, GitHub logins.
	Logins []string
	// resolve_thread: the thread, by GitHub's node id (PRRT_…) or by the
	// feedback id of a comment in it (line-comment-…).
	Thread string
}

// PRActionCheck is an action checked and not yet done.
type PRActionCheck struct {
	ref          RunRef
	action       string
	repo, slug   string
	number       int
	nodeID, head string
	logins       []string
	thread       string
	checks       []forge.Check
	// The line comments a fixer's batch names: the only threads it may
	// resolve. nil for the conductor, which may resolve any of the PR's.
	batch []int64
}

// prActors are who may take which action: the conductor all, a fixer
// answering pull request feedback only resolve_thread, in its own batch.
func prActionAllowed(role, phase, action string) bool {
	switch {
	case role == RoleConductor && phase == "":
		return true
	case phase == PhaseFix:
		return action == ActionResolveThread
	}
	return false
}

// CheckPRActionTx is the check, under the task's Chat lock: the caller may
// take it, the pull request is one of its task's and open, and what it
// names is that pull request's.
func CheckPRActionTx(ctx context.Context, tx pgx.Tx, ref RunRef, gh *forge.GitHub, in PRAction) (PRActionCheck, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return PRActionCheck{}, err
	}
	var role, phase, status string
	var feedback []byte
	if err := tx.QueryRow(ctx, `SELECT COALESCE(role::text, ''), COALESCE(phase::text, ''), status::text, COALESCE(pr_feedback, '[]')
		FROM runs WHERE id = $1 AND task_id = $2`, ref.RunID, ref.TaskID).Scan(&role, &phase, &status, &feedback); err != nil {
		return PRActionCheck{}, err
	}
	if !prActionAllowed(role, phase, in.Action) {
		return PRActionCheck{}, refusef("%s is not yours to do: a task's conductor acts on its pull requests", in.Action)
	}
	c := PRActionCheck{ref: ref, action: in.Action}
	if role == RoleConductor {
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
			AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
			return c, err
		}
		if !live {
			return c, refusef("you are no longer this task's conductor: another took over from you. Do nothing on GitHub")
		}
	} else {
		if status != "running" {
			return c, refusef("your Run is %s: it acts on nothing", status)
		}
		var items []forge.ActionableFeedback
		_ = json.Unmarshal(feedback, &items)
		for _, f := range items {
			if id := forge.LineCommentID(f.ID); id != 0 && f.Source != forge.SourceBot {
				c.batch = append(c.batch, id)
			}
		}
		if len(c.batch) == 0 {
			return c, refusef("you resolve only the review threads in your feedback, and it has none")
		}
	}
	repo, number, err := parsePR(in.PR)
	if err != nil {
		return c, err
	}
	var url, state string
	var nodeID, head *string
	var checks []byte
	err = tx.QueryRow(ctx, `SELECT r.name, r.url, pr.state::text, pr.node_id, pr.head_sha, pr.checks_json
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.number = $2 AND ($3 = '' OR r.name = $3) ORDER BY pr.created_at DESC LIMIT 1`,
		ref.TaskID, number, repo).Scan(&repo, &url, &state, &nodeID, &head, &checks)
	if db.IsNotFound(err) {
		return c, refusef("%s is not a pull request of your task: you act only on your task's (pull_requests lists them)", in.PR)
	}
	if err != nil {
		return c, err
	}
	if state != forge.StateOpen && state != forge.StateDraft {
		return c, refusef("%s#%d is %s: there is nothing to do on it", repo, number, state)
	}
	c.repo, c.number, c.slug = repo, number, forge.SlugFromURL(url)
	if gh == nil || c.slug == "" {
		return c, refusef("%s#%d is not on a GitHub dude is connected to", repo, number)
	}
	if nodeID != nil {
		c.nodeID = *nodeID
	}
	if head != nil {
		c.head = *head
	}
	_ = json.Unmarshal(checks, &c.checks)
	switch in.Action {
	case ActionRequestReview:
		for _, l := range in.Logins {
			if l = strings.TrimPrefix(strings.TrimSpace(l), "@"); l != "" && !slices.Contains(c.logins, l) {
				c.logins = append(c.logins, l)
			}
		}
		switch {
		case len(c.logins) == 0:
			return c, refusef("name whom to ask: logins")
		case len(c.logins) > maxReviewLogins:
			return c, refusef("ask at most %d people at once", maxReviewLogins)
		}
	case ActionResolveThread:
		c.thread = strings.TrimSpace(in.Thread)
		if c.thread == "" {
			return c, refusef("name the thread: its id (PRRT_…), or a line comment in it (line-comment-…)")
		}
	case ActionMarkReady:
		if state != forge.StateDraft {
			return c, refusef("%s#%d is not a draft", repo, number)
		}
		if c.nodeID == "" {
			return c, refusef("dude has no GraphQL id for %s#%d yet; try again after its next sync", repo, number)
		}
	case ActionRerunChecks:
		failed := 0
		for _, ch := range c.checks {
			if ch.Failed() && ch.RunID != 0 {
				failed++
			}
		}
		if failed == 0 {
			return c, refusef("no failed check on %s#%d's head that GitHub can run again", repo, number)
		}
		// Once per head, whoever asked: CI that fails again on the same
		// commit is a fix's to answer, not another run's.
		var again bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
			AND payload->>'action' = $3 AND payload->>'repo' = $4 AND (payload->>'number')::int = $5 AND payload->>'head' = $6)`,
			ref.TaskID, EvPullRequestAction, ActionRerunChecks, repo, number, c.head).Scan(&again); err != nil {
			return c, err
		}
		if again {
			return c, refusef("the failed checks on %s#%d were run again on this head already: a fix answers them now", repo, number)
		}
	default:
		return c, refusef("no pull request action %q", in.Action)
	}
	return c, nil
}

// maxReviewLogins bounds one request_review.
const maxReviewLogins = 10

// PRActionDone is what GitHub did.
type PRActionDone struct {
	Asked, Refused []string
	Rerun          int
	Thread         string
}

// DoPRAction does it on GitHub, with no transaction open. A refusal of
// GitHub's is the agent's to hear; nothing is recorded.
func DoPRAction(ctx context.Context, gh *forge.GitHub, c PRActionCheck) (PRActionDone, error) {
	var done PRActionDone
	switch c.action {
	case ActionRequestReview:
		// Collaborators only: GitHub refuses a review request of anyone
		// else, and asking first says who.
		for _, l := range c.logins {
			perm, err := gh.Permission(ctx, c.slug, l)
			if err != nil {
				return done, ghRefusal(err)
			}
			if perm == "none" || perm == "" {
				done.Refused = append(done.Refused, l)
				continue
			}
			done.Asked = append(done.Asked, l)
		}
		if len(done.Asked) == 0 {
			return done, refusef("%s may not review %s: not collaborators on it", strings.Join(done.Refused, ", "), c.slug)
		}
		if err := gh.RequestReviewers(ctx, c.slug, c.number, done.Asked); err != nil {
			return done, ghRefusal(err)
		}
	case ActionResolveThread:
		var t *forge.ReviewThread
		var err error
		if lc := forge.LineCommentID(c.thread); lc != 0 {
			t, err = gh.ThreadOfComment(ctx, c.slug, c.number, lc)
		} else {
			t, err = gh.ReviewThread(ctx, c.slug, c.thread)
		}
		if err != nil {
			return done, ghRefusal(err)
		}
		if t == nil || !strings.EqualFold(t.Repo, c.slug) || t.Number != c.number {
			return done, refusef("%s is not a review thread of %s#%d", c.thread, c.repo, c.number)
		}
		if c.batch != nil && !slices.ContainsFunc(t.CommentIDs, func(id int64) bool { return slices.Contains(c.batch, id) }) {
			return done, refusef("%s is not a thread in your feedback: you resolve only those", c.thread)
		}
		if !t.Resolved {
			if err := gh.ResolveReviewThread(ctx, c.slug, t.ID); err != nil {
				return done, ghRefusal(err)
			}
		}
		done.Thread = t.ID
	case ActionMarkReady:
		if err := gh.MarkReadyForReview(ctx, c.slug, c.nodeID); err != nil {
			return done, ghRefusal(err)
		}
	case ActionRerunChecks:
		n, err := gh.RerunFailed(ctx, c.slug, c.checks)
		if err != nil {
			return done, ghRefusal(err)
		}
		done.Rerun = n
	}
	return done, nil
}

func ghRefusal(err error) error {
	var e *forge.Error
	if errors.As(err, &e) && (forge.Refused(err) || forge.Transient(err)) {
		return refusef("GitHub did not do it: %v", err)
	}
	return err
}

// RecordPRActionTx records what was done, by the Run that asked.
func RecordPRActionTx(ctx context.Context, tx pgx.Tx, c PRActionCheck, done PRActionDone) (map[string]any, error) {
	payload := map[string]any{"action": c.action, "repo": c.repo, "number": c.number, "head": c.head}
	switch c.action {
	case ActionRequestReview:
		payload["logins"], payload["refused"] = done.Asked, db.NonNil(done.Refused)
	case ActionResolveThread:
		payload["thread"] = done.Thread
	case ActionRerunChecks:
		payload["rerun"] = done.Rerun
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: c.ref.Org, ProjectID: c.ref.ProjectID,
		TaskID: c.ref.TaskID, RunID: c.ref.RunID, ActorType: ledger.ActorAgent, ActorID: c.ref.RunID,
		Source: ledger.SourceOrchestrator, CorrelationID: c.ref.TaskID, Payload: payload})
	return payload, err
}
