package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Posting a fixer's held answers once its fix is on the pull request: see
// answers.go.

// FixerAnswers is what PostFixerAnswers did for one fixer.
type FixerAnswers struct {
	RunID string
	// Its published commit, by repository.
	Commits map[string]string
	// The comments answered and the threads resolved, by feedback id.
	Posted, Resolved []string
	// Answers that failed, each "<id>: <why>".
	Failed []string
	// Why its answers were withheld ("" when they were not): the fix is
	// not on the pull request, the pull request ended, or dude could not
	// verify it for answerVerifyBound.
	Withheld string
	// Why its answers could not be verified this pass ("" when they
	// were): they stay held, for a later pass.
	Unverified string
	// The held answers settled, by event id: the wake's key.
	ids []string
}

// settled says something was done, so the conductor is told.
func (f FixerAnswers) settled() bool {
	return len(f.Posted)+len(f.Resolved)+len(f.Failed) > 0 || f.Withheld != ""
}

// errAnswerBusy: another poster holds a fresh claim on one of the
// answers; the caller tries again after answerClaimLease.
var errAnswerBusy = errors.New("another poster is posting a fixer's answer")

// BeforeAnswerClaim is a test hook: called in a held reply's claim
// transaction, under the task's Chat lock, once the answer was read
// unsettled and unclaimed, before the claim is written. An error fails the
// claim's transaction (a database failing there).
type BeforeAnswerClaim func(answerID string) error

// AfterAnswerPost is a test hook: called once GitHub took an answer,
// before it is recorded. An error stands for the poster crashing there.
type AfterAnswerPost func(answerID string) error

// crashed is AfterAnswerPost's error: the poster gone, nothing after it
// done.
type crashed struct{ err error }

func (c crashed) Error() string { return c.err.Error() }
func (c crashed) Unwrap() error { return c.err }

// fixerToPost is a published, ended fixer with answers not settled.
type fixerToPost struct {
	ref     RunRef
	commits map[string]string
	ended   time.Time
}

// Unverified is a fixer whose answers dude could not check against the
// pull request (GitHub refused a read, no GitHub connected, no repository
// slug): they stay held, retried on later passes, until
// answerVerifyBound past the fixer's end withholds them.
type Unverified struct {
	RunID, Why string
	// What failed, when it was an error of its own (a posting failure that
	// is neither GitHub refusing nor GitHub down); nil for a reason.
	Err error
}

func (u *Unverified) Error() string {
	return "fixer " + u.RunID + "'s answers could not be verified on the pull request: " + u.Why
}

func (u *Unverified) Unwrap() error { return u.Err }

// answerVerifyBound is how long a fixer's answers that could not be
// verified wait, from its Run's end, before they are withheld: nothing
// held waits forever.
const answerVerifyBound = 24 * time.Hour

// now is the store's clock (Store.Now, else time.Now).
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// PostFixerAnswers posts the held answers of the task's fixers whose fix is
// published, in the order they published, each fixer's as the package
// comment says. It returns what it did, by fixer: a fixer whose answers
// could not be verified (FixerAnswers.Unverified) stays held, and stops
// the fixers after it. An error stops it, the rest left to a later call:
// forge.Transient or errAnswerBusy (try again soon), or a database error
// selecting the fixers.
func (s *Store) PostFixerAnswers(ctx context.Context, org, taskID string, gh *forge.GitHub) ([]FixerAnswers, error) {
	var fixers []fixerToPost
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var err error
		fixers, err = fixersToPostTx(ctx, tx, org, taskID)
		return err
	}); err != nil {
		return nil, err
	}
	var out []FixerAnswers
	for _, f := range fixers {
		done, err := s.postFixer(ctx, gh, f)
		if done.settled() || done.Unverified != "" {
			out = append(out, done)
		}
		if err != nil {
			return out, err
		}
		if done.Unverified != "" && done.Withheld == "" {
			// Fixers post in the order they published: a later one
			// waits for this one's answers to be settled.
			return out, nil
		}
	}
	return out, nil
}

// fixersToPostTx are the task's ended fix Runs with held answers not
// settled and a fix published, oldest publication first, each with its
// published head by repository (its latest) and when it ended.
func fixersToPostTx(ctx context.Context, tx pgx.Tx, org, taskID string) ([]fixerToPost, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.project_id, COALESCE(r.ended_at, r.created_at),
			(SELECT jsonb_object_agg(c.repo, c.head) FROM (SELECT DISTINCT ON (e.payload->>'repo') e.payload->>'repo' AS repo, e.payload->>'headSha' AS head
				FROM events e WHERE e.run_id = r.id AND `+isCommitCreated("e")+` ORDER BY e.payload->>'repo', e.cursor DESC) c),
			(SELECT min(e.cursor) FROM events e WHERE e.run_id = r.id AND `+isCommitCreated("e")+`) AS published
		FROM runs r WHERE r.task_id = $1 AND r.phase = 'fix' AND r.status IN ('completed', 'failed', 'aborted')
		  AND EXISTS (SELECT 1 FROM events e WHERE e.run_id = r.id AND `+isCommitCreated("e")+`)
		  AND EXISTS (SELECT 1 FROM events h WHERE h.run_id = r.id AND `+isHeld("h")+` AND NOT `+answerSettled("h")+`)
		ORDER BY published, r.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fixerToPost
	for rows.Next() {
		var f fixerToPost
		var cursor int64
		f.ref = RunRef{Org: org, TaskID: taskID}
		if err := rows.Scan(&f.ref.RunID, &f.ref.ProjectID, &f.ended, &f.commits, &cursor); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// answerSettled (SQL) says the EvAnswerHeld event h is settled: a record
// on its Run names it (its reply's chat.message, its resolution's
// pull_request.action, or its EvAnswerFailed).
// Its event types and the answer's id expression are literally those of
// events_run_answer_settled_idx (migration 269), so a generic plan finds
// the record by (run, answer id) there.
func answerSettled(h string) string {
	return `EXISTS (SELECT 1 FROM events s WHERE s.run_id = ` + h + `.run_id
		AND s.event_type IN ('` + EvChatMessage + `', '` + EvPullRequestAction + `', '` + EvAnswerFailed + `')
		AND ` + settledAnswerID("s") + ` = ` + h + `.id)`
}

// settledAnswerID (SQL) is the held answer a settling record on events e
// names: a pull_request.action's or EvAnswerFailed's answerId, a
// chat.message's github.answerId.
func settledAnswerID(e string) string {
	return `COALESCE(` + e + `.payload->>'answerId', ` + e + `.payload->'github'->>'answerId')`
}

// isHeld and isCommitCreated (SQL) say events e is an EvAnswerHeld or a
// git.commit_created, with the type a literal: the partial indexes on
// them (migrations 268, 270) are proved by a generic plan only so.
func isHeld(e string) string { return e + `.event_type = '` + EvAnswerHeld + `'` }

func isCommitCreated(e string) string { return e + `.event_type = '` + EvGitCommitCreated + `'` }

// heldOf is one held answer and its id.
type heldOf struct {
	ID string
	heldAnswer
}

// unsettledTx is Run runID's held answers not settled: replies in the
// order made, then resolutions in the order made.
func unsettledTx(ctx context.Context, tx pgx.Tx, runID string) ([]heldOf, error) {
	rows, err := tx.Query(ctx, `SELECT h.id, h.payload FROM events h WHERE h.run_id = $1 AND `+isHeld("h")+`
		AND NOT `+answerSettled("h")+`
		ORDER BY h.payload->>'kind' = '`+answerResolve+`', h.cursor`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []heldOf
	for rows.Next() {
		var h heldOf
		var raw []byte
		if err := rows.Scan(&h.ID, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &h.heldAnswer); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// settledTx says held answer id of Run runID is settled.
func settledTx(ctx context.Context, tx pgx.Tx, runID, id string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT `+answerSettled("h")+` FROM (SELECT $1::text AS run_id, $2::text AS id) h`,
		runID, id).Scan(&ok)
	return ok, err
}

// failAnswerTx settles a held answer as failed, once.
func failAnswerTx(ctx context.Context, tx pgx.Tx, ref RunRef, h heldOf, why string, withheld bool) error {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return err
	}
	if done, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || done {
		return err
	}
	p := map[string]any{"answerId": h.ID, "kind": h.Kind, "error": why}
	if withheld {
		p["withheld"] = true
	}
	_, err := ledger.Append(ctx, tx, ref.Event(EvAnswerFailed, ledger.ActorSystem, p))
	return err
}

// postFixer posts one fixer's unsettled answers, if its fix is on every
// pull request they are on.
func (s *Store) postFixer(ctx context.Context, gh *forge.GitHub, f fixerToPost) (FixerAnswers, error) {
	done := FixerAnswers{RunID: f.ref.RunID, Commits: f.commits}
	var held []heldOf
	slugs, branches := map[string]string{}, map[string]string{}
	if err := s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error {
		var err error
		if held, err = unsettledTx(ctx, tx, f.ref.RunID); err != nil || len(held) == 0 {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.name, r.url, pr.id, pr.head_branch FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.task_id = $1`, f.ref.TaskID)
		if err != nil {
			return err
		}
		var name, url, prID, branch string
		_, err = pgx.ForEachRow(rows, []any{&name, &url, &prID, &branch}, func() error {
			slugs[name], branches[prID] = forge.SlugFromURL(url), branch
			return nil
		})
		return err
	}); err != nil || len(held) == 0 {
		return done, err
	}
	if why, err := s.contained(ctx, gh, f, held, slugs, branches); err != nil {
		return s.unverified(ctx, f, done, err)
	} else if why != "" {
		return s.withhold(ctx, f, held, why)
	}
	for _, h := range held {
		var err error
		if h.Kind == answerResolve {
			err = s.resolveHeld(ctx, gh, f.ref, h, slugs, &done)
		} else {
			err = s.postHeldReply(ctx, gh, f.ref, h, &done)
		}
		if err != nil {
			return s.unverified(ctx, f, done, err)
		}
	}
	return done, nil
}

// unverified is postFixer's end on err: GitHub down or limiting, or
// another poster at work, is returned as it is (a later pass tries again
// soon). Anything else — GitHub refusing a read, no GitHub, a database
// error — is no error: the fixer's answers stay held (done.Unverified, the
// reason recorded on its Run as EvAnswerUnverified, once per reason) for
// a later pass, until answerVerifyBound past the fixer's end, when what is
// left is withheld: nothing held waits forever.
func (s *Store) unverified(ctx context.Context, f fixerToPost, done FixerAnswers, err error) (FixerAnswers, error) {
	if forge.Transient(err) || errors.Is(err, errAnswerBusy) || errors.As(err, new(crashed)) {
		return done, err
	}
	var u *Unverified
	if !errors.As(err, &u) {
		u = &Unverified{RunID: f.ref.RunID, Why: err.Error(), Err: err}
	}
	slog.Warn("a fixer's answers are held: they could not be verified", "run", f.ref.RunID, "why", u.Why)
	done.Unverified = u.Why
	if rerr := s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error { return recordUnverifiedTx(ctx, tx, f.ref, u.Why) }); rerr != nil {
		// Not even that recorded: still held, tried again on a later pass.
		slog.Warn("recording why a fixer's answers could not be verified failed", "run", f.ref.RunID, "why", u.Why, "error", rerr)
		return done, nil
	}
	if s.now().Sub(f.ended) < answerVerifyBound {
		return done, nil
	}
	var held []heldOf
	if rerr := s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) (err error) {
		held, err = unsettledTx(ctx, tx, f.ref.RunID)
		return err
	}); rerr != nil || len(held) == 0 {
		return done, nil
	}
	withheld, werr := s.withhold(ctx, f, held, "could not verify the fix is on the pull request: "+u.Why)
	if werr != nil {
		slog.Warn("withholding a fixer's answers past the bound failed", "run", f.ref.RunID, "error", werr)
		return done, nil
	}
	withheld.Posted, withheld.Resolved, withheld.Failed = done.Posted, done.Resolved, done.Failed
	withheld.ids = append(done.ids, withheld.ids...)
	return withheld, nil
}

// recordUnverifiedTx records on the fixer's Run why its answers could not
// be verified, unless that is already the last reason recorded.
func recordUnverifiedTx(ctx context.Context, tx pgx.Tx, ref RunRef, why string) error {
	var last string
	err := tx.QueryRow(ctx, `SELECT payload->>'why' FROM events WHERE run_id = $1 AND event_type = '`+EvAnswerUnverified+`'
		ORDER BY cursor DESC LIMIT 1`, ref.RunID).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if last == why {
		return nil
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvAnswerUnverified, ledger.ActorSystem, map[string]any{"why": why}))
	return err
}

// withhold settles held, the fixer's unsettled answers, as withheld for
// why (EvAnswerFailed, withheld: final), and reports it.
func (s *Store) withhold(ctx context.Context, f fixerToPost, held []heldOf, why string) (FixerAnswers, error) {
	done := FixerAnswers{RunID: f.ref.RunID, Commits: f.commits, Withheld: why}
	for _, h := range held {
		done.ids = append(done.ids, h.ID)
	}
	return done, s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error {
		for _, h := range held {
			if err := failAnswerTx(ctx, tx, f.ref, h, why, true); err != nil {
				return err
			}
		}
		return nil
	})
}

// contained is "" when every pull request the held answers are on is open
// on GitHub and its head contains the fixer's published commit in its
// repository. It is why not (the answers are withheld, for good) only on
// positive evidence the fix is absent: the pull request not open, the
// fixer having published nothing to its repository, its branch gone, or
// GitHub's compare saying the head does not contain the commit (behind, or
// a 404 or 422: no common history). Anything else is an error, the
// answers left held: forge.Transient (GitHub down or limiting), or
// *Unverified (GitHub refusing a read, no GitHub, no repository slug).
// The head is read from the pull request's branch, which GitHub moves at
// once (the pull request's own read trails it by seconds after a push).
// Each pull request is read once, however many answers are on it.
func (s *Store) contained(ctx context.Context, gh *forge.GitHub, f fixerToPost, held []heldOf, slugs, branches map[string]string) (string, error) {
	type pr struct {
		repo   string
		number int
	}
	unverified := func(format string, args ...any) error {
		return &Unverified{RunID: f.ref.RunID, Why: fmt.Sprintf(format, args...)}
	}
	// refusedOr is err as the caller returns it: GitHub not answering
	// stays as it is, anything else could not be verified.
	refusedOr := func(err error, what string, h heldOf) error {
		if forge.Transient(err) {
			return err
		}
		return unverified("%s#%d's %s could not be read: %v", h.Repo, h.Number, what, err)
	}
	seen := map[pr]bool{}
	for _, h := range held {
		p := pr{h.Repo, h.Number}
		if seen[p] {
			continue
		}
		seen[p] = true
		slug := slugs[h.Repo]
		if gh == nil {
			return "", unverified("no GitHub is connected for %s#%d", h.Repo, h.Number)
		}
		if slug == "" {
			return "", unverified("%s is not a GitHub repository dude knows", h.Repo)
		}
		ref, _, err := gh.Mergeability(ctx, slug, h.Number)
		if err != nil {
			return "", refusedOr(err, "pull request", h)
		}
		if ref.State != forge.StateOpen && ref.State != forge.StateDraft {
			return fmt.Sprintf("%s#%d is %s", h.Repo, h.Number, ref.State), nil
		}
		commit := f.commits[h.Repo]
		if commit == "" {
			// No commit of the fixer's there: nothing of its fix is on
			// this pull request, so a "fixed" answer there would be false.
			return fmt.Sprintf("its fix did not change %s", h.Repo), nil
		}
		head := ref.HeadSHA
		if b := branches[h.PRID]; b != "" {
			if head, err = gh.BranchSHA(ctx, slug, b); err != nil {
				return "", refusedOr(err, "branch", h)
			}
			if head == "" {
				return fmt.Sprintf("%s#%d's branch %s is gone", h.Repo, h.Number, b), nil
			}
		}
		if head == commit {
			continue
		}
		cmp, err := gh.CompareLineage(ctx, slug, commit, head)
		var fe *forge.Error
		switch {
		case errors.As(err, &fe) && (fe.Status == 404 || fe.Status == 422):
			return fmt.Sprintf("%s#%d's head %s does not contain the published fix %s", h.Repo, h.Number, short(head), short(commit)), nil
		case err != nil:
			return "", refusedOr(err, "comparison with the fix", h)
		case cmp.BehindBy > 0:
			return fmt.Sprintf("%s#%d's head %s does not contain the published fix %s (a force-push dropped it)", h.Repo, h.Number, short(head), short(commit)), nil
		}
	}
	return "", nil
}

// postHeldReply posts one held reply through the reply path's steps, its
// claim and recovery as the package comment says.
func (s *Store) postHeldReply(ctx context.Context, gh *forge.GitHub, ref RunRef, h heldOf, done *FixerAnswers) error {
	var p ReplyPost
	var skip, recovering bool
	var refused error
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		if err := LockChat(ctx, tx, ref.TaskID); err != nil {
			return err
		}
		if ok, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || ok {
			skip = ok
			return err
		}
		var at time.Time
		var released bool
		err := tx.QueryRow(ctx, `SELECT occurred_at, COALESCE((payload->>'released')::bool, false) FROM events
			WHERE run_id = $1 AND event_type = '`+EvAnswerClaimed+`' AND payload->>'answerId' = $2 ORDER BY cursor DESC LIMIT 1`,
			ref.RunID, h.ID).Scan(&at, &released)
		switch {
		case err == nil:
			if !released && time.Since(at) < answerClaimLease {
				return errAnswerBusy
			}
			recovering = true
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		p, err = replyPostTx(ctx, tx, ref, gh, Reply{PR: fmt.Sprintf("%s#%d", h.Repo, h.Number), Text: h.Text, InReplyTo: h.InReplyTo},
			forge.AnswerMarker(h.ID))
		if IsRefusal(err) {
			// Nothing to claim: it is failed below, in a transaction of
			// its own.
			refused = err
			return nil
		}
		if err != nil {
			return err
		}
		p.by, p.answerID = RoleFixer, h.ID
		if s.BeforeAnswerClaim != nil {
			if err := s.BeforeAnswerClaim(h.ID); err != nil {
				return err
			}
		}
		_, err = ledger.Append(ctx, tx, ref.Event(EvAnswerClaimed, ledger.ActorSystem, map[string]any{"answerId": h.ID}))
		return err
	}); err != nil || skip {
		return err
	}
	if refused != nil {
		return s.failed(ctx, ref, h, refused.Error(), done)
	}
	var posted forge.Posted
	var err error
	if recovering {
		// An earlier poster may have posted it: its comment is found by
		// the answer's marker.
		posted, err = gh.FindAnswer(ctx, p.slug, p.number, h.ID, p.thread != 0)
	}
	if err == nil && posted.ID == "" {
		posted, err = postReply(ctx, gh, p)
	}
	if err != nil {
		if forge.Transient(err) {
			_ = s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
				_, err := ledger.Append(ctx, tx, ref.Event(EvAnswerClaimed, ledger.ActorSystem, map[string]any{"answerId": h.ID, "released": true}))
				return err
			})
			return err
		}
		if forge.Refused(err) {
			return s.failed(ctx, ref, h, err.Error(), done)
		}
		return err
	}
	if s.AfterAnswerPost != nil {
		if err := s.AfterAnswerPost(h.ID); err != nil {
			return crashed{err}
		}
	}
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		if err := LockChat(ctx, tx, ref.TaskID); err != nil {
			return err
		}
		if ok, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || ok {
			return err
		}
		_, err := RecordReplyTx(ctx, tx, p, posted)
		return err
	}); err != nil {
		return err
	}
	done.Posted, done.ids = append(done.Posted, h.InReplyTo), append(done.ids, h.ID)
	return nil
}

// resolveHeld resolves one held thread through the action path's steps.
func (s *Store) resolveHeld(ctx context.Context, gh *forge.GitHub, ref RunRef, h heldOf, slugs map[string]string, done *FixerAnswers) error {
	slug := slugs[h.Repo]
	lc := forge.LineCommentID(h.Thread)
	t, err := gh.ThreadOfComment(ctx, slug, h.Number, lc)
	if err == nil && t == nil {
		return s.failed(ctx, ref, h, fmt.Sprintf(rNotReviewThread, h.Thread, h.Repo, h.Number), done)
	}
	if err == nil && !t.Resolved {
		err = gh.ResolveReviewThread(ctx, slug, t.ID)
	}
	if err != nil {
		if forge.Refused(err) {
			return s.failed(ctx, ref, h, err.Error(), done)
		}
		return err
	}
	c := PRActionCheck{ref: ref, action: ActionResolveThread, prID: h.PRID, repo: h.Repo, slug: slug, number: h.Number, answerID: h.ID}
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		if err := LockChat(ctx, tx, ref.TaskID); err != nil {
			return err
		}
		if ok, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || ok {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(head_sha, '') FROM pull_requests WHERE id = $1`, h.PRID).Scan(&c.head); err != nil {
			return err
		}
		_, err := RecordPRActionTx(ctx, tx, c, PRActionDone{Thread: t.ID})
		return err
	}); err != nil {
		return err
	}
	done.Resolved, done.ids = append(done.Resolved, h.Thread), append(done.ids, h.ID)
	return nil
}

// failed settles a held answer GitHub refused (or dude's records no longer
// allow) as failed, and reports it.
func (s *Store) failed(ctx context.Context, ref RunRef, h heldOf, why string, done *FixerAnswers) error {
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error { return failAnswerTx(ctx, tx, ref, h, why, false) }); err != nil {
		return err
	}
	id := h.InReplyTo
	if h.Kind == answerResolve {
		id = "resolving " + h.Thread
	}
	done.Failed, done.ids = append(done.Failed, id+": "+clip(oneLine(why), 120)), append(done.ids, h.ID)
	return nil
}

// fixerAnswers posts the task's published fixers' held answers
// (PostFixerAnswers) and tells the conductor (wakeOnAnswers). Posting
// never fails the host step: GitHub not answering, or another poster at
// work, is a Wait (the step runs again, nothing it did before counted
// twice); anything else — answers that could not be verified, a database
// error — is logged (and, for the former, recorded on the fixer's Run) and
// the step goes on, the answers left held for a later pass.
func (w *steps) fixerAnswers(ctx context.Context, sc workflow.StepContext, st *State) error {
	if w.forges == nil {
		return nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		if forge.Transient(err) {
			return forgeWait(err)
		}
		// No GitHub to post with now: the answers stay held, and the
		// bound is counted when one is (contained, Unverified).
		slog.Warn("a fixer's answers wait: no GitHub to post them with", "task", st.TaskID, "error", err)
		gh = nil
	}
	done, err := w.s.PostFixerAnswers(ctx, sc.OrganizationID, st.TaskID, gh)
	if werr := w.wakeOnAnswers(ctx, sc, st, done); werr != nil {
		slog.Warn("waking the conductor with a fixer's answers failed", "task", st.TaskID, "error", werr)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errAnswerBusy):
		return workflow.Wait{After: answerClaimLease, Why: err.Error()}
	case forge.Transient(err):
		return forgeWait(err)
	}
	slog.Warn("a fixer's answers are held for a later pass", "task", st.TaskID, "error", err)
	return nil
}

// FixerAnswersStep is the posting a delivery step does first (awaitPRFix,
// movedOnPR): fixerAnswers, for task's delivery, with GitHub from forges.
// Its error is what the step would fail or wait with.
func (s *Store) FixerAnswersStep(ctx context.Context, org, taskID string, forges Forges) error {
	return (&steps{s: s, forges: forges}).fixerAnswers(ctx, workflow.StepContext{OrganizationID: org}, &State{TaskID: taskID})
}
