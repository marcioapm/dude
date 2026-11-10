package delivery

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/advance"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The ready gate: whether a pull request has reached the point where
// merging it is the only act left, item by item. Read only: it writes
// nothing, here or on GitHub. Like a pull request action (CheckPRActionTx,
// DoPRAction), it is read in two parts: what dude knows, in a transaction
// (CheckReadyGateTx), then what GitHub says, with none open (DoReadyGate).
// weighReadiness, the conductor's pull_requests listing and the internal
// API's ready-gate read all decide or show "ready" by it.

// The gate's items, in the order they are judged.
const (
	ReadyOpen       = "open"
	ReadyHead       = "head"
	ReadyReviewed   = "reviewed_through_head"
	ReadySimplified = "simplified_after_last_change"
	ReadyFindings   = "findings"
	ReadyQuestions  = "questions"
	ReadyThreads    = "threads"
	ReadyMergeable  = "mergeable"
	ReadyChecks     = "checks"
)

// ReadyKeys are the gate's items in order.
var ReadyKeys = []string{ReadyOpen, ReadyHead, ReadyReviewed, ReadySimplified, ReadyFindings, ReadyQuestions, ReadyThreads, ReadyMergeable, ReadyChecks}

// GateItem is one item of a pull request's gate: whether it holds, and a
// person's words for what it found.
type GateItem struct {
	Key    string `json:"key"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// PRGate is one pull request's gate: Ready when every item holds. Err is
// GitHub not answering (a timeout, a rate limit): the gate could not be
// judged, which is not a judgement that it fails.
type PRGate struct {
	ID     string     `json:"pullRequestId"`
	Repo   string     `json:"repo"`
	Number int        `json:"number"`
	Head   string     `json:"head"`
	Ready  bool       `json:"ready"`
	Items  []GateItem `json:"items"`
	Err    error      `json:"-"`
}

// Failing is the gate's items that do not hold.
func (g PRGate) Failing() []GateItem {
	var out []GateItem
	for _, it := range g.Items {
		if !it.OK {
			out = append(out, it)
		}
	}
	return out
}

// ReadyGateCheck is the gate's first part, read in a transaction: per pull
// request, what dude knows of it.
type ReadyGateCheck struct {
	PRs []GateFacts
}

// GateFacts is what dude knows of one pull request for its gate.
type GateFacts struct {
	ID, Repo, Slug, BaseBranch string
	Number                     int
	State, Head                string
	// The delivery's policy says to run the simplifier.
	Simplify bool
	// Head is dude's push no review of the attempt covers
	// (unreviewedPRPushesTx).
	UnreviewedPush bool
	// AcceptedHead is the head a person accepted a review round at.
	AcceptedHead string
	// Reviewed is R: the latest head every review Run of the attempt
	// started there completed at, "" for none.
	Reviewed string
	// The policy's reviewers, and per category the head of its latest
	// completed review Run of the attempt over this repository
	// (LensHeads): which lenses the pull request's paths require, and
	// whether each covered what changed since, is judged from GitHub's
	// compares (missingLenses), so a person's push counts as dude's does.
	Policy    Policy
	LensHeads map[string]string
	// The attempt's completed simplify Runs in this repository.
	Simplifies []gateSimplify
	// The items read from dude's own records.
	Findings, Questions GateItem
}

// gateSimplify is a completed simplify Run: where it started in the
// repository, what it produced there ("" for nothing), and the commit it
// made when GitHub merged the base under it (Merged).
type gateSimplify struct {
	Start, Produced, Merged string
	// A completed review of the attempt started at Produced.
	Reviewed bool
}

// ReadyGateFor reads the gate of the task's pull requests prIDs (every
// open one of the task when nil), in both parts.
func (s *Store) ReadyGateFor(ctx context.Context, org string, gh *forge.GitHub, taskID string, prIDs []string) ([]PRGate, error) {
	var c ReadyGateCheck
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		c, err = CheckReadyGateTx(ctx, tx, taskID, prIDs)
		return err
	}); err != nil {
		return nil, err
	}
	return DoReadyGate(ctx, gh, c), nil
}

// CheckReadyGateTx reads what dude knows of the task's pull requests
// prIDs (every open or draft one of the task when nil) for their gate.
func CheckReadyGateTx(ctx context.Context, tx pgx.Tx, taskID string, prIDs []string) (ReadyGateCheck, error) {
	var c ReadyGateCheck
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return c, err
	}
	st := &State{TaskID: taskID, Policy: DefaultPolicy()}
	if d != nil {
		st = &d.State
	}
	attempt := st.Attempt
	if attempt == 0 {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(attempt), 0) FROM runs WHERE task_id = $1`, taskID).Scan(&attempt); err != nil {
			return c, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT pr.id, r.name, r.url, pr.base_branch, pr.number, pr.state::text, COALESCE(pr.head_sha, '')
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND (CASE WHEN $2::text[] IS NULL THEN pr.state IN ('open', 'draft') ELSE pr.id = ANY($2) END)
		ORDER BY pr.created_at, pr.id`, taskID, prIDs)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var p GateFacts
		var url string
		if err := rows.Scan(&p.ID, &p.Repo, &url, &p.BaseBranch, &p.Number, &p.State, &p.Head); err != nil {
			rows.Close()
			return c, err
		}
		p.Slug = forge.SlugFromURL(url)
		p.Simplify = st.Policy.Simplify
		p.AcceptedHead = st.AcceptedHeads[p.Repo]
		c.PRs = append(c.PRs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}
	if len(c.PRs) == 0 {
		return c, nil
	}
	unreviewed := map[string]string{}
	if d != nil {
		pushes, err := unreviewedPRPushesTx(ctx, tx, taskID, st)
		if err != nil {
			return c, err
		}
		for _, p := range pushes {
			unreviewed[p.Repo] = p.Head
		}
	}
	findings, err := gateFindingsTx(ctx, tx, taskID, attempt)
	if err != nil {
		return c, err
	}
	questions, err := gateQuestionsTx(ctx, tx, st, attempt)
	if err != nil {
		return c, err
	}
	for i := range c.PRs {
		p := &c.PRs[i]
		p.UnreviewedPush = p.Head != "" && unreviewed[p.Repo] == p.Head
		p.Findings, p.Questions = findings, questions
		if p.Reviewed, err = reviewedHeadTx(ctx, tx, taskID, attempt, p.Repo); err != nil {
			return c, err
		}
		p.Policy = st.Policy
		if p.LensHeads, err = lensHeadsTx(ctx, tx, taskID, attempt, p.Repo); err != nil {
			return c, err
		}
		if p.Simplifies, err = gateSimplifiesTx(ctx, tx, taskID, attempt, p.Repo); err != nil {
			return c, err
		}
	}
	return c, nil
}

// reviewedFrom (SQL, $1 the task, $2 the attempt, $3 the repository): a
// review Run of the attempt that reviewed the repository, started at
// base_refs->>$3; one over a range reviewed only the repositories its
// range names.
const reviewedFrom = `k.task_id = $1 AND k.attempt = $2 AND k.phase = 'review' AND k.kind = 'agent'
	AND COALESCE(k.base_refs->>$3, '') <> '' AND (k.review_range IS NULL OR k.review_range->'base' ? $3)`

// reviewedHeadTx is R: the latest head of repo every review Run of the
// attempt started at completed at — each category's latest Run there
// completed, none still running nor failed — by when its last one ended;
// "" for none.
func reviewedHeadTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, repo string) (string, error) {
	var head string
	err := tx.QueryRow(ctx, `WITH latest AS (
			SELECT DISTINCT ON (k.base_refs->>$3, COALESCE(k.category, '')) k.base_refs->>$3 AS head, k.status, k.ended_at
			FROM runs k WHERE `+reviewedFrom+`
			ORDER BY k.base_refs->>$3, COALESCE(k.category, ''), k.created_at DESC, k.id DESC)
		SELECT COALESCE((SELECT head FROM latest GROUP BY head HAVING bool_and(status = 'completed')
			ORDER BY max(ended_at) DESC NULLS LAST, head LIMIT 1), '')`, taskID, attempt, repo).Scan(&head)
	return head, err
}

// lensHeadsTx is, per review category, the head of repo its latest
// completed review Run of the attempt started at.
func lensHeadsTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, repo string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (k.category) k.category, k.base_refs->>$3 FROM runs k WHERE `+reviewedFrom+`
		AND k.status = 'completed' AND k.category IS NOT NULL
		ORDER BY k.category, k.ended_at DESC NULLS LAST, k.created_at DESC, k.id DESC`, taskID, attempt, repo)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var c, head string
		if err := rows.Scan(&c, &head); err != nil {
			rows.Close()
			return nil, err
		}
		out[c] = head
	}
	rows.Close()
	return out, rows.Err()
}

// missingLenses is each reviewer the policy picks (ReviewersFor) for the
// paths the pull request changes (GitHub's compare of its base with the
// head, whoever pushed them) that has not reviewed what concerns it: one
// with no completed review of the attempt here, or, for a lens chosen by
// path, one whose paths the pull request changed again since its last
// completed review. "" for none; refused says GitHub would not compare.
func missingLenses(ctx context.Context, gh *forge.GitHub, p GateFacts, base string) (missing []string, refused bool, err error) {
	whole, err := gh.CompareCommits(ctx, p.Slug, base, p.Head)
	if forge.Refused(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	ours := map[string]bool{}
	var paths []string
	for _, f := range whole.Files {
		for _, n := range []string{f.Filename, f.PreviousFilename} {
			if n != "" && !ours[n] {
				ours[n] = true
				paths = append(paths, p.Repo+"/"+n)
			}
		}
	}
	for _, lens := range ReviewersFor(p.Policy, paths) {
		at, ran := p.LensHeads[lens]
		if !ran || at == "" {
			missing = append(missing, lens)
			continue
		}
		if at == p.Head || !byPath(p.Policy, lens) {
			continue
		}
		since, err := gh.CompareCommits(ctx, p.Slug, at, p.Head)
		if forge.Refused(err) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		// What the pull request itself changed since that review: a file a
		// pure merge of the base brought in is not the pull request's.
		var changed []string
		for _, f := range since.Files {
			for _, n := range []string{f.Filename, f.PreviousFilename} {
				if n != "" && ours[n] {
					changed = append(changed, p.Repo+"/"+n)
				}
			}
		}
		if slices.Contains(pathReviewers(p.Policy, changed), lens) {
			missing = append(missing, lens+" (changed since its review at "+short(at)+")")
		}
	}
	return missing, false, nil
}

// byPath says the policy picks lens by the paths changed: a conditional
// rule with paths, not a reviewer it always asks for.
func byPath(p Policy, lens string) bool {
	if slices.Contains(p.RequiredReviewers, lens) {
		return false
	}
	return slices.ContainsFunc(p.ConditionalReviewers, func(r ReviewerRule) bool {
		return r.Category == lens && len(r.WhenPathsMatch) > 0
	})
}

// gateSimplifiesTx is the attempt's completed simplify Runs in repo.
func gateSimplifiesTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int, repo string) ([]gateSimplify, error) {
	rows, err := tx.Query(ctx, `SELECT COALESCE(s.heads->$3->>'base', s.base_refs->>$3, ''), COALESCE(s.heads->$3->>'sha', ''),
			COALESCE((SELECT e.payload->'mergedBase'->>'commit' FROM events e WHERE e.run_id = s.id AND e.event_type = $4
				AND e.payload->>'repo' = $3 AND e.payload ? 'mergedBase' ORDER BY e.cursor DESC LIMIT 1), ''),
			EXISTS (SELECT 1 FROM runs k WHERE `+reviewedFrom+` AND k.status = 'completed'
				AND k.base_refs->>$3 = s.heads->$3->>'sha')
		FROM runs s WHERE s.task_id = $1 AND s.attempt = $2 AND s.phase = 'simplify' AND s.status = 'completed'
		ORDER BY s.created_at, s.id`, taskID, attempt, repo, EvGitCommitCreated)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[gateSimplify])
}

// gateFindingsTx is the findings item: the attempt's open findings at the
// fix bar (atFixBar, IsBlocking), a reviewer's or the tester's.
func gateFindingsTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (GateItem, error) {
	rows, err := tx.Query(ctx, `SELECT id, severity::text, status::text, COALESCE(trivial, false), title FROM review_findings
		WHERE task_id = $1 AND status = 'open' AND `+thisAttempt+` ORDER BY created_at, id`, taskID, attempt)
	if err != nil {
		return GateItem{}, err
	}
	type open struct {
		FindingState
		Title string
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (open, error) {
		var f open
		err := r.Scan(&f.ID, &f.Severity, &f.Status, &f.Trivial, &f.Title)
		return f, err
	})
	if err != nil {
		return GateItem{}, err
	}
	states := make([]FindingState, len(found))
	for i, f := range found {
		states[i] = f.FindingState
	}
	var blocking []string
	for i, f := range atFixBar(states) {
		if IsBlocking(f) {
			blocking = append(blocking, fmt.Sprintf("%s (%s) %s", f.ID, found[i].Severity, found[i].Title))
		}
	}
	if len(blocking) == 0 {
		return GateItem{Key: ReadyFindings, OK: true, Detail: "no open finding at the fix bar"}, nil
	}
	return GateItem{Key: ReadyFindings, Detail: fmt.Sprintf("%d open finding(s) at the fix bar: %s",
		len(blocking), strings.Join(blocking, "; "))}, nil
}

// gateQuestionsTx is the questions item: no question of the conductor's
// open to a person, no reviewer's question open, no escalation waiting on
// a decision, and no person's feedback still to handle
// (BlockingFeedbackTx).
func gateQuestionsTx(ctx context.Context, tx pgx.Tx, st *State, attempt int) (GateItem, error) {
	var asked, reviewing, feedback int
	if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM questions q JOIN runs r ON r.id = q.run_id AND r.role = 'conductor'
				WHERE q.task_id = $1 AND q.status = 'open' AND q.addressee = 'person'),
			(SELECT count(*) FROM review_questions rq WHERE rq.task_id = $1 AND rq.status = 'open'
				AND (rq.run_id IS NULL OR EXISTS (SELECT 1 FROM runs k WHERE k.id = rq.run_id AND k.attempt = $2)))`,
		st.TaskID, attempt).Scan(&asked, &reviewing); err != nil {
		return GateItem{}, err
	}
	feedback, err := BlockingFeedbackTx(ctx, tx, st.TaskID, attempt)
	if err != nil {
		return GateItem{}, err
	}
	var open []string
	if asked > 0 {
		open = append(open, fmt.Sprintf("%d question(s) to a person open", asked))
	}
	if reviewing > 0 {
		open = append(open, fmt.Sprintf("%d review question(s) open", reviewing))
	}
	if e := st.Escalation; e != nil && e.Decided == nil {
		open = append(open, fmt.Sprintf("escalation %s waits on a decision", e.Reason))
	}
	if feedback > 0 {
		open = append(open, fmt.Sprintf("%d feedback item(s) of a person's still to handle", feedback))
	}
	if len(open) == 0 {
		return GateItem{Key: ReadyQuestions, OK: true, Detail: "nothing open to a person"}, nil
	}
	return GateItem{Key: ReadyQuestions, Detail: strings.Join(open, "; ")}, nil
}

// maxGateCommits bounds the commits since R the gate explains, and
// maxGateMerges the merges it walks past to find the last change: one
// GitHub read or more each.
const (
	maxGateCommits = 50
	maxGateMerges  = 20
)

// DoReadyGate is the gate's second part: GitHub's answers, with no
// transaction open, judged with what c read. A pull request GitHub did not
// answer for has Err set and is not Ready; gh nil (no GitHub connected)
// judges every GitHub item unreadable.
func DoReadyGate(ctx context.Context, gh *forge.GitHub, c ReadyGateCheck) []PRGate {
	out := make([]PRGate, 0, len(c.PRs))
	for _, p := range c.PRs {
		g, err := doGate(ctx, gh, p)
		g.Err = err
		g.Ready = err == nil && len(g.Failing()) == 0 && len(g.Items) == len(ReadyKeys)
		out = append(out, g)
	}
	return out
}

func doGate(ctx context.Context, gh *forge.GitHub, p GateFacts) (PRGate, error) {
	g := PRGate{ID: p.ID, Repo: p.Repo, Number: p.Number, Head: p.Head}
	add := func(key string, ok bool, format string, args ...any) {
		g.Items = append(g.Items, GateItem{Key: key, OK: ok, Detail: fmt.Sprintf(format, args...)})
	}
	unread := func(why string) (PRGate, error) {
		for _, k := range ReadyKeys {
			switch k {
			case ReadyFindings:
				g.Items = append(g.Items, p.Findings)
			case ReadyQuestions:
				g.Items = append(g.Items, p.Questions)
			default:
				add(k, false, "%s", why)
			}
		}
		return g, nil
	}
	if gh == nil || p.Slug == "" {
		return unread("GitHub is not connected for this repository: unreadable")
	}
	f, err := gh.Readiness(ctx, p.Slug, p.Number)
	if forge.Refused(err) {
		return unread("GitHub refused the read: " + err.Error())
	}
	if err != nil {
		g, _ = unread("GitHub did not answer: " + err.Error())
		return g, err
	}
	// 1. open
	switch {
	case f.State == forge.StateMerged || f.State == forge.StateClosed:
		add(ReadyOpen, false, "it is %s", f.State)
	case f.Draft || f.State == forge.StateDraft:
		add(ReadyOpen, false, "it is a draft: mark it ready for review")
	default:
		add(ReadyOpen, true, "open")
	}
	// 2. head
	headOK := f.HeadSHA != "" && f.HeadSHA == p.Head
	if headOK {
		add(ReadyHead, true, "at %s", short(p.Head))
	} else {
		add(ReadyHead, false, "GitHub shows %s, dude last recorded %s: not ready, re-evaluate once it is synced", short(f.HeadSHA), short(p.Head))
	}
	// 3. reviewed_through_head
	reviewed, detail, err := gateReviewed(ctx, gh, p, f)
	if err != nil {
		return g, err
	}
	add(ReadyReviewed, reviewed, "%s", detail)
	// 4. simplified_after_last_change
	simplified, detail, err := gateSimplified(ctx, gh, p, reviewed)
	if err != nil {
		return g, err
	}
	add(ReadySimplified, simplified, "%s", detail)
	// 5, 6: dude's own.
	g.Items = append(g.Items, p.Findings, p.Questions)
	// 7. threads
	switch {
	case f.ThreadsUnknown:
		add(ReadyThreads, false, "GitHub would not count the review threads: unreadable")
	case f.UnresolvedThreads > 0:
		add(ReadyThreads, false, "%d review thread(s) unresolved: each is a person's open feedback", f.UnresolvedThreads)
	default:
		add(ReadyThreads, true, "no unresolved review thread")
	}
	// 8. mergeable
	ok, detail := gateMergeable(f)
	add(ReadyMergeable, ok, "%s", detail)
	// 9. checks
	ok, detail = gateChecks(f)
	add(ReadyChecks, ok, "%s", detail)
	return g, nil
}

// gateReviewed is item 3: R (the latest head reviewed through) is the head,
// or every commit since it is a simplify Run's whose own review
// completed, or GitHub's pure merge of the base (advance.PureBaseMerge).
func gateReviewed(ctx context.Context, gh *forge.GitHub, p GateFacts, f forge.ReadinessFacts) (bool, string, error) {
	h := p.Head
	switch {
	case h == "":
		return false, "no head recorded", nil
	case p.UnreviewedPush:
		return false, fmt.Sprintf("unreviewed: %s, dude's push, has no completed review", short(h)), nil
	case p.AcceptedHead != "" && p.AcceptedHead == h:
		return true, fmt.Sprintf("a person accepted the review round at %s", short(h)), nil
	case p.Reviewed == "":
		return false, "no review of this attempt completed over this repository", nil
	}
	base := cmp.Or(f.BaseBranch, p.BaseBranch)
	missing, refused, err := missingLenses(ctx, gh, p, base)
	switch {
	case err != nil:
		return false, "", err
	case refused:
		return false, fmt.Sprintf("GitHub would not compare %s with %s to say which lenses the changed paths require", base, short(h)), nil
	case len(missing) > 0:
		return false, "no completed review by " + strings.Join(missing, ", ") + ", which the changed paths require", nil
	case p.Reviewed == h:
		return true, fmt.Sprintf("reviewed at %s", short(h)), nil
	}
	since, err := gh.CompareCommits(ctx, p.Slug, p.Reviewed, h)
	switch {
	case forge.Refused(err):
		return false, fmt.Sprintf("GitHub would not compare the reviewed %s with %s: history rewritten?", short(p.Reviewed), short(h)), nil
	case err != nil:
		return false, "", err
	case since.BehindBy > 0:
		return false, fmt.Sprintf("the head no longer descends from the reviewed %s: history rewritten since the review", short(p.Reviewed)), nil
	case len(since.Commits) < since.TotalCommits:
		return false, fmt.Sprintf("%d commits since the review at %s: too many to explain", since.TotalCommits, short(p.Reviewed)), nil
	}
	parents := map[string][]string{}
	for _, c := range since.Commits {
		parents[c.SHA] = []string{}
		for _, x := range c.Parents {
			parents[c.SHA] = append(parents[c.SHA], x.SHA)
		}
	}
	simplified, err := simplifiedCommits(ctx, gh, p)
	if err != nil {
		return false, "", err
	}
	// The branch's own line, head down to R by first parents: what a merge
	// brings in under its second parent is the base's, judged with the
	// merge.
	var unreviewed []string
	at := h
	for n := 0; at != p.Reviewed; n++ {
		ps, listed := parents[at]
		if !listed || len(ps) == 0 || n == maxGateCommits {
			unreviewed = append(unreviewed, fmt.Sprintf("the commits below %s, which do not lead back to the review", short(at)))
			break
		}
		switch {
		case simplified[at]:
		case len(ps) == 2:
			pure, err := advance.PureBaseMerge(ctx, gh, p.Slug, base, at, ps)
			if err != nil {
				return false, "", err
			}
			if !pure {
				unreviewed = append(unreviewed, short(at)+" (a merge that is not GitHub's pure merge of "+base+")")
			}
		default:
			unreviewed = append(unreviewed, short(at))
		}
		at = ps[0]
	}
	if len(unreviewed) > 0 {
		slices.Reverse(unreviewed)
		return false, fmt.Sprintf("reviewed at %s; unreviewed since: %s", short(p.Reviewed), strings.Join(unreviewed, ", ")), nil
	}
	return true, fmt.Sprintf("reviewed at %s; since then only reviewed simplifications and pure merges of %s", short(p.Reviewed), base), nil
}

// simplifiedCommits is every commit of a simplify Run whose own review
// completed: start..produced.
func simplifiedCommits(ctx context.Context, gh *forge.GitHub, p GateFacts) (map[string]bool, error) {
	out := map[string]bool{}
	for _, s := range p.Simplifies {
		if s.Produced == "" || !s.Reviewed {
			continue
		}
		out[s.Produced] = true
		if s.Merged != "" {
			out[s.Merged] = true
		}
		if s.Start == "" {
			continue
		}
		cmp, err := gh.CompareCommits(ctx, p.Slug, s.Start, s.Produced)
		if forge.Refused(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, c := range cmp.Commits {
			out[c.SHA] = true
		}
	}
	return out, nil
}

// gateSimplified is item 4. C is the last change: the head's last commit,
// by first parents, that is not GitHub's pure merge of the base. A
// completed simplify Run counts when it started anywhere on the chain from
// C up to the head and committed nothing, or when what it produced (or the
// commit it made, the base merged under it) is on that chain and was
// reviewed (item 3).
func gateSimplified(ctx context.Context, gh *forge.GitHub, p GateFacts, reviewed bool) (bool, string, error) {
	if !p.Simplify {
		return true, "simplifier off", nil
	}
	if p.Head == "" {
		return false, "no head recorded", nil
	}
	last := p.Head
	chain := map[string]bool{last: true}
	for range maxGateMerges {
		parents, err := gh.CommitParents(ctx, p.Slug, last)
		if forge.Refused(err) {
			return false, fmt.Sprintf("GitHub would not read %s", short(last)), nil
		}
		if err != nil {
			return false, "", err
		}
		if len(parents) < 2 {
			break
		}
		// A merge that is not the base's alone (dude's merge of a writer's
		// commit, a person's resolution) is a change of its own.
		pure, err := advance.PureBaseMerge(ctx, gh, p.Slug, p.BaseBranch, last, parents)
		if err != nil {
			return false, "", err
		}
		if !pure {
			break
		}
		last = parents[0]
		chain[last] = true
	}
	for _, s := range slices.Backward(p.Simplifies) {
		switch {
		case s.Produced == "" && chain[s.Start]:
			return true, fmt.Sprintf("the simplifier ran from %s, after the last change %s, and found nothing to simplify", short(s.Start), short(last)), nil
		case s.Produced != "" && (chain[s.Produced] || s.Merged != "" && chain[s.Merged]):
			last := s.Produced
			if !chain[last] {
				last = s.Merged
			}
			if !reviewed {
				return false, fmt.Sprintf("the simplifier made %s, the last change; its review does not cover the head yet", short(last)), nil
			}
			return true, fmt.Sprintf("the simplifier made %s, the last change, and it was reviewed", short(last)), nil
		}
	}
	return false, fmt.Sprintf("the simplifier has not run since %s, the last change: start the simplifier", short(last)), nil
}

// gateMergeable is item 8, from GitHub's mergeable_state at the head. No
// approval is asked for beyond what branch protection asks.
func gateMergeable(f forge.ReadinessFacts) (bool, string) {
	base := cmp.Or(f.BaseBranch, "its base")
	switch f.MergeableState {
	case "clean", "has_hooks", "unstable":
		if f.BehindBy > 0 {
			return false, fmt.Sprintf("behind %s by %d commit(s): update the branch", base, f.BehindBy)
		}
		if f.MergeableState == "unstable" {
			return true, "mergeable; some checks are not passing (see checks)"
		}
		return true, "clean"
	case "behind":
		return false, fmt.Sprintf("behind %s: update the branch", base)
	case "dirty":
		return false, fmt.Sprintf("it conflicts with %s: resolve the conflict", base)
	case "draft":
		return false, "it is a draft: mark it ready for review"
	case "blocked":
		why := "GitHub branch protection blocks it"
		switch {
		case f.RequiredApprovals > f.Approvals:
			why += fmt.Sprintf(": it requires %d approving review(s), and has %d", f.RequiredApprovals, f.Approvals)
		case len(f.RequiredChecks) > 0:
			why += ": it requires the checks " + strings.Join(f.RequiredChecks, ", ")
		}
		return false, why
	}
	if f.Mergeable != nil && !*f.Mergeable {
		return false, fmt.Sprintf("it conflicts with %s: resolve the conflict", base)
	}
	return false, "GitHub has not worked out whether it merges yet: retry later"
}

// gateChecks is item 9: every check and status at the exact head
// completed as success, neutral or skipped. None at all is no CI only
// when the head has no workflow file and protection requires no check.
func gateChecks(f forge.ReadinessFacts) (bool, string) {
	if forge.CheckDiagnostic(f.CheckList) == forge.CheckRunsForbidden {
		return false, "checks unreadable: GitHub refused the check-runs read"
	}
	var failing, pending []string
	passed := 0
	for _, c := range f.CheckList {
		switch forge.CheckOutcome(c) {
		case forge.ChecksFailing:
			failing = append(failing, c.Name)
		case forge.ChecksPending:
			pending = append(pending, c.Name)
		default:
			passed++
		}
	}
	switch {
	case len(failing) > 0:
		return false, "failing: " + strings.Join(failing, ", ")
	case f.Checks == forge.ChecksFailing:
		return false, "failing: GitHub's combined status at the head is failing"
	case len(pending) > 0:
		return false, "pending: " + strings.Join(pending, ", ")
	case f.Checks == forge.ChecksPending:
		return false, "pending: GitHub's combined status at the head is pending"
	case passed > 0 || f.Checks == forge.ChecksPassing:
		return true, fmt.Sprintf("%d check(s) passed at the head", max(passed, 1))
	case !f.WorkflowsKnown:
		return false, "no checks at the head, and GitHub would not list .github/workflows: unreadable"
	case len(f.Workflows) > 0:
		return false, "pending: no checks have reported at the head yet, though .github/workflows has " + strings.Join(f.Workflows, ", ")
	case len(f.RequiredChecks) > 0:
		return false, "pending: branch protection requires " + strings.Join(f.RequiredChecks, ", ") + ", and none has reported at the head"
	}
	return true, "no CI configured"
}
