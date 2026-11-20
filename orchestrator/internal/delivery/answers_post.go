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

// FixerAnswers is what was done with one fixer's answers, for its wake.
type FixerAnswers struct {
	RunID string
	// Its published commit, by repository.
	Commits map[string]string
	// The comments answered and the threads resolved, by feedback id.
	Posted, Resolved []string
	// Answers that failed, each "<label>: <why>".
	Failed []string
	// Why its answers were withheld ("" when they were not), and what kind
	// of reason that is: CauseDropped, CauseEnded or CauseUnverified.
	Withheld, Cause string
	// Why its answers could not be verified this pass: they stay held.
	Unverified string
	// The held answers settled, by event id: the wake's key.
	ids []string
	// A delivery's end (CauseEnded): why it ended, the answers it
	// withheld, and the ones a poster held then (answers_end.go).
	Ended                  string
	WithheldList, InFlight []string
}

// settled says something was done, so the conductor is told.
func (f FixerAnswers) settled() bool {
	return len(f.Posted)+len(f.Resolved)+len(f.Failed) > 0 || f.Withheld != "" || f.Ended != ""
}

// Causes of a fixer's withheld answers (FixerAnswers.Cause).
const (
	CauseDropped    = "dropped"
	CauseEnded      = "ended"
	CauseUnverified = "unverified"
)

// errAnswerBusy: another poster holds a fresh claim on one of the answers.
var errAnswerBusy = errors.New("another poster is posting a fixer's answer")

// errAnswerEnded: the answer's delivery ended while it was claimed: the
// reconciliation settles it, never a new claim.
var errAnswerEnded = errors.New("the delivery ended: the answer is reconciled, not claimed")

// Test hooks on Store, each given the answer's id. BeforeAnswerClaim runs
// in the claim's transaction under the Chat lock, before the claim is
// written: an error fails that transaction, as a database error would. BeforeAnswerWrite runs once
// the claim is committed, before the GitHub write; AfterAnswerPost once
// GitHub took it, before it is recorded: an error from either stands for
// the poster crashing there.
type (
	BeforeAnswerClaim func(answerID string) error
	BeforeAnswerWrite func(answerID string) error
	AfterAnswerPost   func(answerID string) error
)

// crashed is a hook's crash: the poster gone, nothing after it done.
type crashed struct{ err error }

func (c crashed) Error() string { return c.err.Error() }
func (c crashed) Unwrap() error { return c.err }

// crash is hook(id) as a crash, if the hook is set and fails.
func crash(hook func(string) error, id string) error {
	if hook != nil {
		if err := hook(id); err != nil {
			return crashed{err}
		}
	}
	return nil
}

// Unverified is a fixer whose answers dude could not check against the
// pull request: they stay held until answerVerifyBound past its end.
type Unverified struct {
	RunID, Why string
	Err        error
}

func (u *Unverified) Error() string {
	return "fixer " + u.RunID + "'s answers could not be verified on the pull request: " + u.Why
}

func (u *Unverified) Unwrap() error { return u.Err }

// answerVerifyBound is how long answers that could not be verified wait,
// from the fixer's end, before they are withheld.
const answerVerifyBound = 24 * time.Hour

// fixerToPost is a published, ended fixer with answers not settled.
type fixerToPost struct {
	ref     RunRef
	commits map[string]string
	ended   time.Time
}

// PostFixerAnswers posts the held answers of the task's published fixers,
// in the order they published, and returns what it did, by fixer. A fixer
// whose answers could not be verified stops the fixers after it. An error
// stops it: forge.Transient or errAnswerBusy (try again soon), a crash, or
// a database error selecting the fixers.
func (s *Store) PostFixerAnswers(ctx context.Context, org, taskID string, gh *forge.GitHub) ([]FixerAnswers, error) {
	var fixers []fixerToPost
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fixersToPostSQL, taskID)
		if err != nil {
			return err
		}
		f := fixerToPost{ref: RunRef{Org: org, TaskID: taskID}}
		_, err = pgx.ForEachRow(rows, []any{&f.ref.RunID, &f.ref.ProjectID, &f.ended, &f.commits}, func() error {
			fixers = append(fixers, f)
			return nil
		})
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
		if err != nil || done.Unverified != "" && done.Withheld == "" {
			return out, err
		}
	}
	return out, nil
}

// publishedHeads (SQL) is Run r's latest published head by repository, a
// jsonb object: its git.commit_created on the task's index (migration 234).
func publishedHeads(r string) string {
	return `COALESCE((SELECT jsonb_object_agg(c.repo, c.head) FROM (SELECT DISTINCT ON (e.payload->>'repo') e.payload->>'repo' AS repo,
		e.payload->>'headSha' AS head FROM events e WHERE e.task_id = ` + r + `.task_id AND e.run_id = ` + r + `.id
		AND e.event_type = '` + EvGitCommitCreated + `' ORDER BY e.payload->>'repo', e.cursor DESC) c), '{}'::jsonb)`
}

// fixersToPostSQL are the task's ended fix Runs with held answers not
// settled and a fix published, oldest publication first ($1 the task).
var fixersToPostSQL = `SELECT r.id, r.project_id, COALESCE(r.ended_at, r.created_at), ` + publishedHeads("r") + `
	FROM runs r CROSS JOIN LATERAL (SELECT min(e.cursor) AS first FROM events e WHERE e.task_id = r.task_id AND e.run_id = r.id
		AND e.event_type = '` + EvGitCommitCreated + `') p
	WHERE r.task_id = $1 AND r.phase = 'fix' AND r.status IN ('completed', 'failed', 'aborted') AND p.first IS NOT NULL
	  AND EXISTS (SELECT 1 FROM events h WHERE h.run_id = r.id AND ` + isHeld("h") + ` AND NOT ` + answerSettled("h") + ` AND NOT ` + reconciling("h") + `)
	ORDER BY p.first, r.id`

// unsettledSQL is a Run's held answers not settled nor reconciling ($1):
// replies in the order made, then resolutions.
var unsettledSQL = `SELECT h.id, h.payload FROM events h WHERE h.run_id = $1 AND ` + isHeld("h") + `
	AND NOT ` + answerSettled("h") + ` AND NOT ` + reconciling("h") + `
	ORDER BY h.payload->>'kind' = '` + answerResolve + `', h.cursor`

func unsettledTx(ctx context.Context, tx pgx.Tx, runID string) ([]heldOf, error) {
	rows, err := tx.Query(ctx, unsettledSQL, runID)
	if err != nil {
		return nil, err
	}
	var out []heldOf
	var h heldOf
	var raw []byte
	_, err = pgx.ForEachRow(rows, []any{&h.ID, &raw}, func() error {
		h.heldAnswer = heldAnswer{}
		out = append(out, h)
		return json.Unmarshal(raw, &out[len(out)-1].heldAnswer)
	})
	return out, err
}

// settledTx says held answer id of Run runID is settled.
func settledTx(ctx context.Context, tx pgx.Tx, runID, id string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT `+answerSettled("h")+` FROM (SELECT $1::text AS run_id, $2::text AS id) h`, runID, id).Scan(&ok)
	return ok, err
}

// failAnswerTx settles a held answer as failed (or withheld), once. A
// withholding never settles an answer a poster may be writing to GitHub:
// one with a fresh claim, or one in reconciliation. Nor one ever claimed:
// its lapsed or released claim's write may have reached GitHub, so, as at
// a delivery's end (settleHeldOnEndTx), it is put in answer_reconciliations
// for why, and reconciled says so.
func failAnswerTx(ctx context.Context, tx pgx.Tx, ref RunRef, h heldOf, why string, withheld bool, now time.Time) (reconciled bool, err error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return false, err
	}
	if done, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || done {
		return false, err
	}
	p := map[string]any{"answerId": h.ID, "kind": h.Kind, "error": why}
	if withheld {
		if busy, err := inFlightTx(ctx, tx, ref.RunID, h.ID, now); err != nil || busy {
			return false, err
		}
		var at time.Time
		var released bool
		switch err := tx.QueryRow(ctx, lastClaimSQL, ref.RunID, h.ID).Scan(&at, &released); {
		case err == nil:
			_, err = tx.Exec(ctx, `INSERT INTO answer_reconciliations (answer_id, organization_id, ended, claimed_at, due_at)
				VALUES ($1, $2, $3, $4, $5)`, h.ID, ref.Org, clip(oneLine(why), 300), at, at.Add(answerReconcileAfter))
			return err == nil, err
		case !errors.Is(err, pgx.ErrNoRows):
			return false, err
		}
		p["withheld"] = true
	}
	_, err = ledger.Append(ctx, tx, answerEvent(ref, EvAnswerFailed, h.ID, p))
	return false, err
}

// inFlightTx says held answer id may be being written to GitHub: its latest
// claim is unreleased and inside its lease at now, or it is reconciling.
func inFlightTx(ctx context.Context, tx pgx.Tx, runID, id string, now time.Time) (bool, error) {
	var x bool
	if err := tx.QueryRow(ctx, `SELECT `+reconciling("h")+` FROM (SELECT $1::text AS id) h`, id).Scan(&x); err != nil || x {
		return x, err
	}
	var at time.Time
	var released bool
	err := tx.QueryRow(ctx, lastClaimSQL, runID, id).Scan(&at, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && !released && now.Sub(at) < answerClaimLease, err
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
	if why, cause, err := s.contained(ctx, gh, f, held, slugs, branches); err != nil {
		return s.unverified(ctx, f, done, err)
	} else if why != "" {
		return s.withhold(ctx, f, held, why, cause)
	}
	threads := map[string][]*forge.ReviewThread{}
	for _, h := range held {
		err := s.postHeld(ctx, gh, f.ref, h, slugs[h.Repo], threads, &done)
		if errors.Is(err, errAnswerEnded) {
			continue
		}
		if err != nil {
			return s.unverified(ctx, f, done, err)
		}
	}
	return done, nil
}

// unverified is postFixer's end on err. GitHub not answering, another
// poster at work, or a crash, is returned as it is. Anything else leaves
// the answers held, the reason recorded (EvAnswerUnverified), until
// answerVerifyBound past the fixer's end withholds them — unless the
// marker search was incomplete: the answer may be on GitHub, so it is
// never withheld, and past the bound a person is asked to reconcile it.
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
	past := s.now().Sub(f.ended) >= answerVerifyBound
	why := u.Why
	if past && errors.Is(err, forge.ErrSearchIncomplete) {
		why = "manual reconciliation required: " + why
	}
	if rerr := s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error { return recordUnverifiedTx(ctx, tx, f.ref, why) }); rerr != nil {
		slog.Warn("recording why a fixer's answers could not be verified failed", "run", f.ref.RunID, "error", rerr)
		return done, nil
	}
	if !past || errors.Is(err, forge.ErrSearchIncomplete) {
		return done, nil
	}
	var held []heldOf
	if rerr := s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) (err error) {
		held, err = unsettledTx(ctx, tx, f.ref.RunID)
		return err
	}); rerr != nil || len(held) == 0 {
		return done, nil
	}
	withheld, werr := s.withhold(ctx, f, held, "could not verify the fix is on the pull request: "+u.Why, CauseUnverified)
	if werr != nil {
		slog.Warn("withholding a fixer's answers past the bound failed", "run", f.ref.RunID, "error", werr)
		return done, nil
	}
	withheld.Posted, withheld.Resolved, withheld.Failed = done.Posted, done.Resolved, done.Failed
	withheld.ids = append(done.ids, withheld.ids...)
	return withheld, nil
}

// recordUnverifiedTx records why a fixer's answers could not be verified,
// unless that is already the last reason recorded.
func recordUnverifiedTx(ctx context.Context, tx pgx.Tx, ref RunRef, why string) error {
	var last string
	err := tx.QueryRow(ctx, `SELECT payload->>'why' FROM events WHERE run_id = $1 AND event_type = '`+EvAnswerUnverified+`'
		ORDER BY cursor DESC LIMIT 1`, ref.RunID).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) || last == why {
		return err
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvAnswerUnverified, ledger.ActorSystem, map[string]any{"why": why}))
	return err
}

// withhold settles held as withheld for why, and reports it: an answer
// ever claimed is reconciled instead (failAnswerTx), reported in flight,
// not withheld.
func (s *Store) withhold(ctx context.Context, f fixerToPost, held []heldOf, why, cause string) (FixerAnswers, error) {
	done := FixerAnswers{RunID: f.ref.RunID, Commits: f.commits, Withheld: why, Cause: cause}
	now := s.now()
	err := s.DB.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error {
		done.ids, done.InFlight = nil, nil
		for _, h := range held {
			done.ids = append(done.ids, h.ID)
			reconciled, err := failAnswerTx(ctx, tx, f.ref, h, why, true, now)
			if err != nil {
				return err
			}
			if reconciled {
				done.InFlight = append(done.InFlight, h.label())
			}
		}
		return nil
	})
	return done, err
}

// contained is "" when every pull request the held answers are on is open
// and its branch head contains the fixer's published commit. It is why
// not only on positive evidence the fix is absent: the pull request not
// open, nothing published to its repository, its branch gone, or GitHub's
// compare saying the head does not contain the commit. Anything else is an
// error: forge.Transient, or *Unverified. Each pull request is read once.
func (s *Store) contained(ctx context.Context, gh *forge.GitHub, f fixerToPost, held []heldOf, slugs, branches map[string]string) (why, cause string, err error) {
	unverified := func(format string, args ...any) error {
		return &Unverified{RunID: f.ref.RunID, Why: fmt.Sprintf(format, args...)}
	}
	refusedOr := func(err error, what string, h heldOf) error {
		if forge.Transient(err) {
			return err
		}
		return unverified("%s#%d's %s could not be read: %v", h.Repo, h.Number, what, err)
	}
	seen := map[string]bool{}
	for _, h := range held {
		pr := fmt.Sprintf("%s#%d", h.Repo, h.Number)
		if seen[pr] {
			continue
		}
		seen[pr] = true
		slug := slugs[h.Repo]
		if gh == nil {
			return "", "", unverified("no GitHub is connected for %s", pr)
		}
		if slug == "" {
			return "", "", unverified("%s is not a GitHub repository dude knows", h.Repo)
		}
		ref, _, rerr := gh.Mergeability(ctx, slug, h.Number)
		if rerr != nil {
			return "", "", refusedOr(rerr, "pull request", h)
		}
		if ref.State != forge.StateOpen && ref.State != forge.StateDraft {
			return fmt.Sprintf("%s is %s", pr, ref.State), CauseEnded, nil
		}
		commit := f.commits[h.Repo]
		if commit == "" {
			return fmt.Sprintf("its fix did not change %s", h.Repo), CauseDropped, nil
		}
		// The branch, which GitHub moves at once: the pull request's own
		// read trails a push by seconds.
		head := ref.HeadSHA
		if b := branches[h.PRID]; b != "" {
			var berr error
			if head, berr = gh.BranchSHA(ctx, slug, b); berr != nil {
				return "", "", refusedOr(berr, "branch", h)
			}
			if head == "" {
				return fmt.Sprintf("%s's branch %s is gone", pr, b), CauseDropped, nil
			}
		}
		if head == commit {
			continue
		}
		cmp, cerr := gh.CompareLineage(ctx, slug, commit, head)
		var fe *forge.Error
		switch {
		case errors.As(cerr, &fe) && (fe.Status == 404 || fe.Status == 422):
			return fmt.Sprintf("%s's head %s does not contain the published fix %s", pr, short(head), short(commit)), CauseDropped, nil
		case cerr != nil:
			return "", "", refusedOr(cerr, "comparison with the fix", h)
		case cmp.BehindBy > 0:
			return fmt.Sprintf("%s's head %s does not contain the published fix %s (a force-push dropped it)", pr, short(head), short(commit)), CauseDropped, nil
		}
	}
	return "", "", nil
}

// claimTx says whether held answer h may be claimed for a GitHub write,
// under the task's Chat lock: skip when it is settled; errAnswerEnded when it is reconciling;
// errAnswerBusy when another poster's claim is fresh; recovering when an
// earlier claim lapsed or was released (its write may have happened).
func (s *Store) claimTx(ctx context.Context, tx pgx.Tx, ref RunRef, h heldOf) (skip, recovering bool, err error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return false, false, err
	}
	if ok, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || ok {
		return ok, false, err
	}
	var x bool
	if err := tx.QueryRow(ctx, `SELECT `+reconciling("h")+` FROM (SELECT $1::text AS id) h`, h.ID).Scan(&x); err != nil {
		return false, false, err
	} else if x {
		return false, false, errAnswerEnded
	}
	var at time.Time
	var released bool
	switch err := tx.QueryRow(ctx, lastClaimSQL, ref.RunID, h.ID).Scan(&at, &released); {
	case err == nil:
		if !released && s.now().Sub(at) < answerClaimLease {
			return false, false, errAnswerBusy
		}
		recovering = true
	case !errors.Is(err, pgx.ErrNoRows):
		return false, false, err
	}
	return false, recovering, nil
}

// appendClaimTx records a claim on answer id, at now, or its release.
func appendClaimTx(ctx context.Context, tx pgx.Tx, ref RunRef, id string, released bool, now time.Time) error {
	p := map[string]any{"answerId": id}
	if released {
		p["released"] = true
	}
	e := answerEvent(ref, EvAnswerClaimed, id, p)
	e.OccurredAt = now
	_, err := ledger.Append(ctx, tx, e)
	return err
}

// postHeld posts one held answer: claimed (claimTx), then its GitHub write
// with no transaction open and bounded by the claim's lease, then its
// record (recordPostedTx). A reply that was refused before its write, or
// a thread GitHub no longer has, fails.
func (s *Store) postHeld(ctx context.Context, gh *forge.GitHub, ref RunRef, h heldOf, slug string,
	threads map[string][]*forge.ReviewThread, done *FixerAnswers) error {
	var p ReplyPost
	var skip, recovering bool
	var refused error
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		var err error
		if skip, recovering, err = s.claimTx(ctx, tx, ref, h); err != nil || skip {
			return err
		}
		if h.Kind == answerReply {
			p, err = replyPostTx(ctx, tx, ref, gh, Reply{PR: fmt.Sprintf("%s#%d", h.Repo, h.Number), Text: h.Text, InReplyTo: h.InReplyTo},
				forge.AnswerMarker(h.ID))
			if IsRefusal(err) {
				// Nothing to claim: failed below, in a transaction of its own.
				refused = err
				return nil
			} else if err != nil {
				return err
			}
		}
		if s.BeforeAnswerClaim != nil {
			if err := s.BeforeAnswerClaim(h.ID); err != nil {
				return err
			}
		}
		return appendClaimTx(ctx, tx, ref, h.ID, false, s.now())
	}); err != nil || skip {
		return err
	}
	if refused != nil {
		return s.failed(ctx, ref, h, refused.Error(), done)
	}
	claimed, cancel := context.WithTimeout(ctx, answerClaimLease)
	defer cancel()
	var posted forge.Posted
	var thread *forge.ReviewThread
	var err error
	if h.Kind == answerReply {
		if recovering {
			// An earlier poster may have posted it: found by its marker. A
			// search cut at its bound is no evidence of absence.
			if posted, err = gh.FindAnswer(claimed, p.slug, p.number, h.ID, p.thread != 0); errors.Is(err, forge.ErrSearchIncomplete) {
				return &Unverified{RunID: ref.RunID, Why: "answer marker search was inconclusive after the bounded page limit", Err: err}
			}
		}
		if err == nil && posted.ID == "" {
			if err = crash(s.BeforeAnswerWrite, h.ID); err != nil {
				return err
			}
			posted, err = postReply(claimed, gh, p)
		}
	} else {
		// One read of a pull request's threads per pass; resolving a
		// resolved thread is a no-op, so a recovering poster needs no search.
		key := fmt.Sprintf("%s#%d", h.Repo, h.Number)
		all, read := threads[key]
		if !read {
			if all, err = gh.ReviewThreads(claimed, slug, h.Number); err == nil {
				threads[key] = all
			}
		}
		if err == nil {
			if thread = forge.ThreadOf(all, forge.LineCommentID(h.Thread)); thread == nil {
				return s.failed(ctx, ref, h, fmt.Sprintf(rNotReviewThread, h.Thread, h.Repo, h.Number), done)
			}
			if !thread.Resolved {
				if err = crash(s.BeforeAnswerWrite, h.ID); err != nil {
					return err
				}
				err = gh.ResolveReviewThread(claimed, slug, thread.ID)
			}
		}
	}
	switch {
	case err != nil && forge.Transient(err):
		_ = s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error { return appendClaimTx(ctx, tx, ref, h.ID, true, s.now()) })
		return err
	case err != nil && forge.Refused(err):
		return s.failed(ctx, ref, h, err.Error(), done)
	case err != nil:
		return err
	}
	if err := crash(s.AfterAnswerPost, h.ID); err != nil {
		return err
	}
	// Recorded whether or not the delivery ended meanwhile: GitHub took it.
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		return recordPostedTx(ctx, tx, ref, h, slug, posted, thread)
	}); err != nil {
		return err
	}
	if h.Kind == answerReply {
		done.Posted = append(done.Posted, h.InReplyTo)
	} else {
		done.Resolved = append(done.Resolved, h.Thread)
	}
	done.ids = append(done.ids, h.ID)
	return nil
}

// recordPostedTx records held answer h as GitHub took it — a reply as its
// chat.message, a resolution as its pull_request.action — under the Chat
// lock, unless it is settled already, and settles its reconciliation, if
// the delivery's end opened one. Posters and the reconciliation alike.
func recordPostedTx(ctx context.Context, tx pgx.Tx, ref RunRef, h heldOf, slug string, posted forge.Posted, thread *forge.ReviewThread) error {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return err
	}
	if ok, err := settledTx(ctx, tx, ref.RunID, h.ID); err != nil || ok {
		return err
	}
	if h.Kind == answerReply {
		p := ReplyPost{ref: ref, repo: h.Repo, number: h.Number, prID: h.PRID, slug: slug, text: h.Text, inReplyTo: h.InReplyTo,
			by: RoleFixer, answerID: h.ID}
		if _, err := RecordReplyTx(ctx, tx, p, posted); err != nil {
			return err
		}
	} else {
		c := PRActionCheck{ref: ref, action: ActionResolveThread, prID: h.PRID, repo: h.Repo, slug: slug, number: h.Number, answerID: h.ID}
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT head_sha FROM pull_requests WHERE id = $1), '')`, h.PRID).Scan(&c.head); err != nil {
			return err
		}
		if _, err := RecordPRActionTx(ctx, tx, c, PRActionDone{Thread: thread.ID}); err != nil {
			return err
		}
	}
	return settleReconciliationTx(ctx, tx, ref, h.ID, reconciledPosted, "")
}

// failed settles a held answer GitHub refused as failed, and its
// reconciliation, if any.
func (s *Store) failed(ctx context.Context, ref RunRef, h heldOf, why string, done *FixerAnswers) error {
	if err := s.DB.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		if _, err := failAnswerTx(ctx, tx, ref, h, why, false, s.now()); err != nil {
			return err
		}
		return settleReconciliationTx(ctx, tx, ref, h.ID, reconciledFailed, why)
	}); err != nil {
		return err
	}
	done.Failed, done.ids = append(done.Failed, h.label()+": "+clip(oneLine(why), 120)), append(done.ids, h.ID)
	return nil
}

// fixerAnswers posts the task's published fixers' held answers and tells
// the conductor. It never fails the host step: GitHub not answering, or
// another poster at work, is a Wait; anything else is logged, the answers
// left held for a later pass.
func (w *steps) fixerAnswers(ctx context.Context, sc workflow.StepContext, st *State) error {
	if w.forges == nil {
		return nil
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		if forge.Transient(err) {
			return forgeWait(err)
		}
		slog.Warn("a fixer's answers wait: no GitHub to post them with", "task", st.TaskID, "error", err)
		gh = nil
	}
	done, err := w.s.PostFixerAnswers(ctx, sc.OrganizationID, st.TaskID, gh)
	if werr := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		for _, f := range done {
			if err := wakeAnswersTx(ctx, tx, sc.OrganizationID, st.TaskID, f); err != nil {
				return err
			}
		}
		return nil
	}); werr != nil {
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

// FixerAnswersStep is fixerAnswers for task's delivery, with GitHub from
// forges: what the step would fail or wait with.
func (s *Store) FixerAnswersStep(ctx context.Context, org, taskID string, forges Forges) error {
	return (&steps{s: s, forges: forges}).fixerAnswers(ctx, workflow.StepContext{OrganizationID: org}, &State{TaskID: taskID})
}
