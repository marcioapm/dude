package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// GitHub actions dude takes on a task's pull request for one of its agents
// (agents never hold a token): ask for reviews, resolve a review thread,
// mark a draft ready, re-run failed checks, write the conductor's section
// of the description. Each is one of the task's own pull requests; each is
// recorded as pull_request.action by the Run that asked. Like a reply
// (reply.go), an action is checked in a transaction, done on GitHub with
// none open, then recorded.

// PR actions.
const (
	ActionRequestReview = "request_review"
	ActionResolveThread = "resolve_thread"
	ActionMarkReady     = "mark_ready"
	ActionRerunChecks   = "rerun_failed_checks"
	ActionNote          = "pull_request_note"
)

// PRAction is an agent's request.
type PRAction struct {
	Action string
	// The pull request: its number, repo#number, owner/repo#number or URL.
	PR string
	// request_review: whom to ask, GitHub logins.
	Logins []string
	// resolve_thread: the thread, by GitHub's node id (PRRT_…) or by the
	// feedback id of a comment in it (line-comment-…).
	Thread string
	// pull_request_note: the conductor's section of the description.
	Text string
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
	note         string
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
	// A review-only task leaves the author's pull request as it is: only a
	// rerun of failed checks, which changes no code. Its own threads
	// (resolve_thread) come with its GitHub review.
	if in.Action != ActionRerunChecks {
		if err := RefuseReviewOnlyPRActionTx(ctx, tx, ref.TaskID, in.Action); err != nil {
			return c, err
		}
	}
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
	owner, repo, number, err := parsePR(in.PR)
	if err != nil {
		return c, err
	}
	var url, state string
	var nodeID, head *string
	var checks []byte
	var adopted bool
	prID, err := taskPullRequest(ctx, tx, ref.TaskID, number, owner, repo)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT pr.id, r.name, r.url, pr.state::text, pr.node_id, pr.head_sha, pr.checks_json, pr.adopted
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = $1`, prID).Scan(&c.prID, &repo, &url, &state, &nodeID, &head, &checks, &adopted)
	}
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
		again, err := RerunClaimedTx(ctx, tx, ref.TaskID, repo, number, c.head)
		if err != nil {
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
	case ActionNote:
		// dude writes its sections only into a description it opened, as
		// the Verification and Review sections (refreshBodies): a pull
		// request it took over is its author's.
		if adopted {
			return c, refusef(rNoteNotDudesPullRequest, repo, number)
		}
		if c.note, err = conductorNote(in.Text); err != nil {
			return c, err
		}
	default:
		return c, refusef(rNoPullRequestAction, in.Action)
	}
	return c, nil
}

func (c PRActionCheck) PullRequestID() string { return c.prID }

// conductorNote is the conductor's text as its section holds it, or a
// Refusal: not empty, at most pullRequestNoteMax bytes, and no HTML
// comment in it, since dude's markers are HTML comments (one could end the
// section or pass for dude's own, and one left open hides the rest of the
// description); a fence it leaves open is closed, so the section's end
// marker stays a marker.
func conductorNote(text string) (string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	switch {
	case text == "":
		return "", refuseText(rNoteTextRequired)
	case len(text) > pullRequestNoteMax:
		return "", refusef(rNoteAtMostBytes, pullRequestNoteMax)
	case strings.Contains(text, "<!--"):
		return "", refuseText(rNoteNoMarkers)
	}
	if fence := UnclosedFence(text); fence != "" {
		text += "\n" + fence
	}
	return text, nil
}

// pullRequestNoteMax bounds the conductor's section of a description.
const pullRequestNoteMax = 4000

// The hidden lines around the conductor's section of a description it
// writes (pull_request_note): what lies between them is the conductor's.
// conductorStart is forge.ReplyMarker's literal on purpose: both say the
// conductor wrote what they mark. A description is no feedback, and its
// section ends in conductorEnd, so forge.Own never reads it as a reply.
const (
	conductorStart = forge.ReplyMarker
	conductorEnd   = "<!-- /dude:conductor -->"
)

// replaceConductorNote puts note between the conductor's markers in body
// (replaceMarked): a person's text and dude's other sections are kept.
func replaceConductorNote(body, note string) string {
	return replaceMarked(body, conductorStart, conductorEnd, conductorStart+"\n"+note+"\n"+conductorEnd)
}

// maxReviewLogins bounds one request_review.
const maxReviewLogins = 10

// PRActionDone is what GitHub did.
type PRActionDone struct {
	Asked, Refused []string
	// Rerun: failed checks GitHub was asked to run again; Running: those
	// whose workflow run GitHub said was already running.
	Rerun, Running int
	Thread         string
	// pull_request_note: the description as GitHub has it now.
	Body string
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
		if c.batch != nil {
			// A fixer's thread, its fix not published yet: the conductor
			// resolves once the published fix is verified (#485).
			return done, refusef(rFixNotOnPullRequestYet2, c.thread)
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
		n, running, err := gh.RerunFailed(ctx, c.slug, c.checks)
		var unasked *forge.NothingAsked
		if err != nil && n == 0 && (forge.Refused(err) || errors.As(err, &unasked)) {
			// GitHub answered no and ran nothing (a workflow still running,
			// a job too old), or could not be read before anything was
			// asked: the head's rerun is not spent. The release is recorded
			// by the caller as the refusal's event.
			msg := fmt.Sprintf(rGithubRefusedToRunFailedChecksOn, c.repo, c.number, err)
			if !forge.Refused(err) {
				msg = fmt.Sprintf(rGithubCouldNotBeReadNothingRerun, c.repo, c.number, err)
			}
			return done, Refusal{Msg: msg,
				Event: &RefusalEvent{Type: EvPullRequestAction, Payload: map[string]any{"action": ActionRerunChecks,
					"repo": c.repo, "number": c.number, "head": c.head, "released": c.claim}}}
		}
		if err != nil {
			return done, refusef(rGithubReRunFailedChecksOn, c.repo, c.number, err)
		}
		done.Rerun, done.Running = n, running
	case ActionNote:
		// A note is refused rather than taking the room dude's
		// Verification section needs: past prBodyMax GitHub refuses the
		// description whole, every later refresh with it.
		var room int
		body, err := refreshBody(ctx, gh, c.slug, c.number, func(body string) string {
			merged := replaceConductorNote(body, c.note)
			if room = prBodyMax - prBodyMargin - utf8.RuneCountInString(merged); room < 0 {
				return body
			}
			return merged
		})
		if errors.Is(err, errBodyChanged) {
			return done, refusef(rNoteBodyKeepsChanging, c.repo, c.number)
		}
		if err != nil {
			return done, ghRefusal(err)
		}
		if room < 0 {
			return done, refusef(rNoteNoRoom, c.repo, c.number, max(0, utf8.RuneCountInString(c.note)+room))
		}
		done.Body = body
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
		if done.Running > 0 {
			payload["alreadyRunning"] = done.Running
		}
		// The claim written before GitHub was called is this head's record.
		return payload, nil
	case ActionNote:
		payload["note"] = c.note
		// The description as GitHub has it now, a person's edits included.
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET body = $2, updated_at = now() WHERE id = $1`, c.prID, done.Body); err != nil {
			return payload, err
		}
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: c.ref.Org, ProjectID: c.ref.ProjectID,
		TaskID: c.ref.TaskID, RunID: c.ref.RunID, ActorType: ledger.ActorAgent, ActorID: c.ref.RunID,
		Source: ledger.SourceOrchestrator, CorrelationID: c.ref.TaskID, Payload: payload})
	return payload, err
}
