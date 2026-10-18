package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	prID         string
	repo, slug   string
	number       int
	nodeID, head string
	logins       []string
	thread       string
	checks       []forge.Check
	// rerun_failed_checks: the event claiming the head.
	claim string
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
	caller, err := actingRunTx(ctx, tx, ref)
	if err != nil {
		return PRActionCheck{}, err
	}
	if !prActionAllowed(caller.role, caller.phase, in.Action) {
		return PRActionCheck{}, refusef(rNotYoursDoTaskConductorActs, in.Action)
	}
	c := PRActionCheck{ref: ref, action: in.Action}
	if caller.role == RoleConductor {
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.id = $1 AND r.task_id = $2 AND `+LiveConductor+`
			AND NOT COALESCE(`+Ending+`, false))`, ref.RunID, ref.TaskID).Scan(&live); err != nil {
			return c, err
		}
		if !live {
			return c, refuseText(rYouAreNoLongerThisTask4)
		}
	} else {
		if caller.ended() {
			return c, refusef(rYourRunActsOnNothing, caller.status)
		}
		for _, id := range caller.batch {
			if n := forge.LineCommentID(id); n != 0 {
				c.batch = append(c.batch, n)
			}
		}
		if len(c.batch) == 0 {
			return c, refuseText(rYouResolveOnlyReviewThreadsIn)
		}
	}
	repo, number, err := parsePR(in.PR)
	if err != nil {
		return c, err
	}
	var url, state string
	var nodeID, head *string
	var checks []byte
	err = tx.QueryRow(ctx, `SELECT pr.id, r.name, r.url, pr.state::text, pr.node_id, pr.head_sha, pr.checks_json
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.number = $2 AND ($3 = '' OR r.name = $3) ORDER BY pr.created_at DESC LIMIT 1`,
		ref.TaskID, number, repo).Scan(&c.prID, &repo, &url, &state, &nodeID, &head, &checks)
	if db.IsNotFound(err) {
		return c, refusef(rNotPullRequestYourTaskYou, in.PR)
	}
	if err != nil {
		return c, err
	}
	if state != forge.StateOpen && state != forge.StateDraft {
		return c, refusef(rThereNothingDoOn, repo, number, state)
	}
	c.repo, c.number, c.slug = repo, number, forge.SlugFromURL(url)
	if gh == nil || c.slug == "" {
		return c, refusef(rNotOnGithubDudeConnected, repo, number)
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
			return c, refuseText(rNameWhomAskLogins)
		case len(c.logins) > maxReviewLogins:
			return c, refusef(rAskAtMostPeopleAtOnce, maxReviewLogins)
		}
	case ActionResolveThread:
		c.thread = strings.TrimSpace(in.Thread)
		if c.thread == "" {
			return c, refuseText(rNameThreadItsIdPrrtOr)
		}
	case ActionMarkReady:
		if state != forge.StateDraft {
			return c, refusef(rNotDraft, repo, number)
		}
		if c.nodeID == "" {
			return c, refusef(rDudeHasNoGraphqlIdFor, repo, number)
		}
	case ActionRerunChecks:
		failed := 0
		for _, ch := range c.checks {
			if ch.Failed() && ch.RunID != 0 {
				failed++
			}
		}
		if failed == 0 {
			return c, refusef(rNoFailedCheckOnHeadThat, repo, number)
		}
		// Once per head, whoever asked: CI that fails again on the same
		// commit is a fix's to answer, not another run's. A claim GitHub
		// refused outright is released (DoPRAction) and counts no more.
		var again bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events e WHERE e.task_id = $1 AND e.event_type = $2
			AND e.payload->>'action' = $3 AND e.payload->>'repo' = $4 AND (e.payload->>'number')::int = $5 AND e.payload->>'head' = $6
			AND e.payload->>'released' IS NULL
			AND NOT EXISTS (SELECT 1 FROM events r WHERE r.task_id = e.task_id AND r.event_type = e.event_type
				AND r.payload->>'released' = e.id))`,
			ref.TaskID, EvPullRequestAction, ActionRerunChecks, repo, number, c.head).Scan(&again); err != nil {
			return c, err
		}
		if again {
			return c, refusef(rFailedChecksOnWereRunAgain, repo, number)
		}
		// The claim commits before GitHub is called; Chat's lock serializes this check.
		claim, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: ref.Org, ProjectID: ref.ProjectID,
			TaskID: ref.TaskID, RunID: ref.RunID, ActorType: ledger.ActorAgent, ActorID: ref.RunID,
			Source: ledger.SourceOrchestrator, CorrelationID: ref.TaskID, Payload: map[string]any{"action": ActionRerunChecks,
				"repo": repo, "number": number, "head": c.head, "claimed": true}})
		if err != nil {
			return c, err
		}
		c.claim = claim
	default:
		return c, refusef(rNoPullRequestAction, in.Action)
	}
	return c, nil
}

func (c PRActionCheck) PullRequestID() string { return c.prID }

// maxReviewLogins bounds one request_review.
const maxReviewLogins = 10

// PRActionDone is what GitHub did.
type PRActionDone struct {
	Asked, Refused []string
	Rerun          int
	Thread         string
}

// DoPRAction does it on GitHub, with no transaction open. A refusal of
// GitHub's is the agent's to hear; nothing is recorded, but for a rerun
// GitHub refused outright, whose claim the refusal's event releases.
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
			return done, refusef(rMayNotReviewNotCollaboratorsOn, strings.Join(done.Refused, ", "), c.slug)
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
			return done, refusef(rNotReviewThread, c.thread, c.repo, c.number)
		}
		if c.batch != nil && !slices.ContainsFunc(t.CommentIDs, func(id int64) bool { return slices.Contains(c.batch, id) }) {
			return done, refusef(rNotThreadInYourFeedbackYou, c.thread)
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
		if err != nil && n == 0 && forge.Refused(err) {
			// GitHub answered no and ran nothing (a workflow still running,
			// a job too old): the head's rerun is not spent. The release is
			// recorded by the caller as the refusal's event.
			return done, Refusal{Msg: fmt.Sprintf(rGithubRefusedToRunFailedChecksOn, c.repo, c.number, err),
				Event: &RefusalEvent{Type: EvPullRequestAction, Payload: map[string]any{"action": ActionRerunChecks,
					"repo": c.repo, "number": c.number, "head": c.head, "released": c.claim}}}
		}
		if err != nil {
			return done, refusef(rGithubReRunFailedChecksOn, c.repo, c.number, err)
		}
		done.Rerun = n
	}
	return done, nil
}

func ghRefusal(err error) error {
	var e *forge.Error
	if errors.As(err, &e) && (forge.Refused(err) || forge.Transient(err)) {
		return refusef(rGithubDidNotDo, err)
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
		// The claim written before GitHub was called is this head's record.
		return payload, nil
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: c.ref.Org, ProjectID: c.ref.ProjectID,
		TaskID: c.ref.TaskID, RunID: c.ref.RunID, ActorType: ledger.ActorAgent, ActorID: c.ref.RunID,
		Source: ledger.SourceOrchestrator, CorrelationID: c.ref.TaskID, Payload: payload})
	return payload, err
}
