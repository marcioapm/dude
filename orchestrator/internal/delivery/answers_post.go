package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// not on the pull request.
	Withheld string
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

// AfterAnswerPost is a test hook: called once GitHub took an answer,
// before it is recorded. An error stands for the poster crashing there.
type AfterAnswerPost func(answerID string) error

// fixerToPost is a published, ended fixer with answers not settled.
type fixerToPost struct {
	ref     RunRef
	commits map[string]string
}

// PostFixerAnswers posts the held answers of the task's fixers whose fix is
// published, in the order they published, each fixer's as the package
// comment says. It returns what it did, by fixer; an error that is
// forge.Transient, or errAnswerBusy, leaves the rest to a later call.
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
		if done.settled() {
			out = append(out, done)
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// fixersToPostTx are the task's ended fix Runs with held answers not
// settled and a fix published, oldest publication first, each with its
// published head by repository (its latest).
func fixersToPostTx(ctx context.Context, tx pgx.Tx, org, taskID string) ([]fixerToPost, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.project_id,
			(SELECT jsonb_object_agg(c.repo, c.head) FROM (SELECT DISTINCT ON (e.payload->>'repo') e.payload->>'repo' AS repo, e.payload->>'headSha' AS head
				FROM events e WHERE e.run_id = r.id AND e.event_type = $2 ORDER BY e.payload->>'repo', e.cursor DESC) c),
			(SELECT min(e.cursor) FROM events e WHERE e.run_id = r.id AND e.event_type = $2) AS published
		FROM runs r WHERE r.task_id = $1 AND r.phase = 'fix' AND r.status IN ('completed', 'failed', 'aborted')
		  AND EXISTS (SELECT 1 FROM events e WHERE e.run_id = r.id AND e.event_type = $2)
		  AND EXISTS (SELECT 1 FROM events h WHERE h.run_id = r.id AND h.event_type = $3 AND NOT `+answerSettled("h")+`)
		ORDER BY published, r.id`, taskID, EvGitCommitCreated, EvAnswerHeld)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fixerToPost
	for rows.Next() {
		var f fixerToPost
		var cursor int64
		f.ref = RunRef{Org: org, TaskID: taskID}
		if err := rows.Scan(&f.ref.RunID, &f.ref.ProjectID, &f.commits, &cursor); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// answerSettled (SQL) says the EvAnswerHeld event h is settled: a record
// on its Run names it (its reply's chat.message, its resolution's
// pull_request.action, or its EvAnswerFailed).
func answerSettled(h string) string {
	return `EXISTS (SELECT 1 FROM events s WHERE s.run_id = ` + h + `.run_id
		AND s.event_type IN ('` + EvChatMessage + `', '` + EvPullRequestAction + `', '` + EvAnswerFailed + `')
		AND (s.payload->>'answerId' = ` + h + `.id OR s.payload->'github'->>'answerId' = ` + h + `.id))`
}

// heldOf is one held answer and its id.
type heldOf struct {
	ID string
	heldAnswer
}

// unsettledTx is Run runID's held answers not settled: replies in the
// order made, then resolutions in the order made.
func unsettledTx(ctx context.Context, tx pgx.Tx, runID string) ([]heldOf, error) {
	rows, err := tx.Query(ctx, `SELECT h.id, h.payload FROM events h WHERE h.run_id = $1 AND h.event_type = $2
		AND NOT `+answerSettled("h")+`
		ORDER BY h.payload->>'kind' = '`+answerResolve+`', h.cursor`, runID, EvAnswerHeld)
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
		return done, err
	} else if why != "" {
		done.Withheld = why
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
	for _, h := range held {
		var err error
		if h.Kind == answerResolve {
			err = s.resolveHeld(ctx, gh, f.ref, h, slugs, &done)
		} else {
			err = s.postHeldReply(ctx, gh, f.ref, h, &done)
		}
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// contained is "" when every pull request the held answers are on is open
// on GitHub and its head contains the fixer's published commit in its
// repository; else why not. An error is GitHub not answering. The head is
// read from the pull request's branch, which GitHub moves at once (the pull
// request's own read trails it by seconds after a push).
func (s *Store) contained(ctx context.Context, gh *forge.GitHub, f fixerToPost, held []heldOf, slugs, branches map[string]string) (string, error) {
	type pr struct {
		repo   string
		number int
	}
	seen := map[pr]bool{}
	for _, h := range held {
		p := pr{h.Repo, h.Number}
		if seen[p] {
			continue
		}
		seen[p] = true
		slug := slugs[h.Repo]
		if gh == nil || slug == "" {
			return fmt.Sprintf("%s#%d is not on a GitHub dude is connected to", h.Repo, h.Number), nil
		}
		ref, _, err := gh.Mergeability(ctx, slug, h.Number)
		if err != nil {
			if forge.Transient(err) {
				return "", err
			}
			return fmt.Sprintf("%s#%d could not be read: %v", h.Repo, h.Number, err), nil
		}
		if ref.State != forge.StateOpen && ref.State != forge.StateDraft {
			return fmt.Sprintf("%s#%d is %s", h.Repo, h.Number, ref.State), nil
		}
		commit := f.commits[h.Repo]
		if commit == "" {
			// No commit of the fixer's there: the pull request being open
			// is all its answer there needs.
			continue
		}
		head := ref.HeadSHA
		if b := branches[h.PRID]; b != "" {
			if head, err = gh.BranchSHA(ctx, slug, b); err != nil {
				if forge.Transient(err) {
					return "", err
				}
				return fmt.Sprintf("%s#%d's branch could not be read: %v", h.Repo, h.Number, err), nil
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
			return "", err
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
			WHERE run_id = $1 AND event_type = $2 AND payload->>'answerId' = $3 ORDER BY cursor DESC LIMIT 1`,
			ref.RunID, EvAnswerClaimed, h.ID).Scan(&at, &released)
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
			return err
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
// (PostFixerAnswers).
// GitHub not answering, or another poster at work, is a Wait: the step runs
// again, nothing it did before this counted twice.
func (w *steps) fixerAnswers(ctx context.Context, sc workflow.StepContext, st *State) error {
	if w.forges == nil {
		return nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return err
	}
	_, err = w.s.PostFixerAnswers(ctx, sc.OrganizationID, st.TaskID, gh)
	switch {
	case errors.Is(err, errAnswerBusy):
		return workflow.Wait{After: answerClaimLease, Why: err.Error()}
	case err != nil:
		return forgeWait(err)
	}
	return nil
}
