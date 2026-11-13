package delivery

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A review-only task's verdict: dude's GitHub review of the pull request
// at H, the head its lenses reviewed. The gate (ProfileReviewVerdict)
// reads dude's records and GitHub's answers at H; decideVerdict alone
// chooses the event; the claim → submit → read back below posts it at
// most once. Then the delivery waits on the pull request, no agent
// running, until CI or the conductor moves the verdict again.

// The verdict's ledger events, on the task. Payloads carry pullRequestId,
// head and, where one is made, claimId.
const (
	// The review dude is about to submit: {claimId, pullRequestId, head, event}.
	EvReviewClaimed = "pull_request.review_claimed"
	// GitHub holds it: {claimId, reviewId, url, event, head, recovered?}.
	EvReviewSubmitted = "pull_request.review_submitted"
	// Not posted, nothing zombie: {head, why, claimId?}.
	EvReviewAbandoned = "pull_request.review_abandoned"
	// GitHub refused APPROVE/REQUEST_CHANGES as the account's own pull
	// request: posted as a COMMENT instead {claimId, from, why}.
	EvReviewDowngraded = "pull_request.review_downgraded"
	// GitHub refused the review, not retried: {claimId, status, message}.
	EvReviewFailed = "pull_request.review_failed"
	// No verdict yet, no agent running: {head, why, deferrals}.
	EvVerdictDeferred = "pull_request.verdict_deferred"
)

// VerdictState is the review-only verdict's state between its runs.
type VerdictState struct {
	// Due is the head a verdict is still owed at: the wait runs the
	// verdict again on CI's signals while the pull request shows it.
	Due string `json:"due,omitempty"`
	// When the verdict first deferred at Due, and how often (the cap).
	DeferredSince *time.Time `json:"deferredSince,omitempty"`
	Deferrals     int        `json:"deferrals,omitempty"`
	// The head the conductor was woken at for failing checks: past that,
	// failing checks it did not turn into a finding are not the change's.
	CIAsked string `json:"ciAsked,omitempty"`
	// ci_stuck reached the wait for Due.
	Stuck bool `json:"stuck,omitempty"`
	// The checks' state ("passing", "failing", "pending", "unreadable") the
	// verdict last read at Due: failing then pending is a rerun, and the
	// cap and ci_stuck measure it, not the time before it.
	Checks string `json:"checks,omitempty"`
	// A review-only test that failed or was blocked at TestHead, as the
	// verdict names it ("" once one passed there), and its criteria and
	// findings with what the tester observed, for the review's body.
	Test      string   `json:"test,omitempty"`
	TestNotes []string `json:"testNotes,omitempty"`
	TestHead  string   `json:"testHead,omitempty"`
	// The last event posted at Due, and the status comment.
	Posted    string `json:"posted,omitempty"`
	CommentID int64  `json:"commentId,omitempty"`
	Status    string `json:"status,omitempty"` // the status comment's last body hash
	// An approval of dude's GitHub refused to dismiss: the status comment
	// names it under every line from then on (decision 3).
	DismissRefused string `json:"dismissRefused,omitempty"`
	// The claim GitHub's answer was lost for: how often it was submitted
	// with no answer, and looked for since the last of those (P-M2).
	Lost        string `json:"lost,omitempty"`
	LostSubmits int    `json:"lostSubmits,omitempty"`
	LostLookups int    `json:"lostLookups,omitempty"`
}

// The verdict's actions.
const (
	verdictPost    = "post"
	verdictDefer   = "defer"
	verdictAbandon = "abandon"
	verdictRetry   = "retry"
	// Failing checks at H, not yet looked at: the conductor investigates
	// (decision 2) before anything is posted.
	verdictAskCI = "ask_ci"
)

// verdictInput is what decideVerdict weighs: the gate at H and what the
// gate does not see.
type verdictInput struct {
	Gate PRGate
	// Lenses whose latest review Run at H failed or was aborted (the gate
	// reads completed Runs only).
	Unfinished []string
	// The conductor was asked about the failing checks at H already.
	CIAsked bool
	// ci_stuck: checks pending at H past the organization's patience.
	Stuck bool
	// The deferral cap is reached.
	Capped bool
	// A review-only test that failed or was blocked at the head ("" none),
	// and the notes naming what it failed (VerdictState.TestNotes).
	Test      string
	TestNotes []string
}

// The verdict's reasons: part of the claim, so a later verdict at the
// same head and event for another reason is a review of its own.
const (
	reasonLensFailed  = "lens-failed"
	reasonNotReviewed = "not-reviewed"
	reasonFindings    = "findings"
	reasonTestFailed  = "test-failed"
	reasonQuestion    = "question"
	reasonUnrelatedCI = "unrelated-red-ci"
	// CI pending at H past the cap or the organization's patience
	// (ci_stuck): one claim, whichever headline it was posted with, so
	// the cap's COMMENT is not posted again when ci_stuck follows (L-A).
	reasonCIUnconfirmed = "ci-unconfirmed"
	reasonUnreadable    = "ci-unreadable"
	reasonDraft         = "draft"
	reasonApprove       = "approve"
)

// transientReason says a COMMENT for reason holds back an approval only
// until CI passes at the head: the verdict stays due there.
func transientReason(reason string) bool {
	return reason == reasonUnrelatedCI || reason == reasonCIUnconfirmed
}

// verdictDecision is decideVerdict's answer.
type verdictDecision struct {
	Action   string
	Event    string // for verdictPost
	Reason   string // for verdictPost: one of the reason keys above
	Headline string
	// Every failing item the body lists, beyond the headline's.
	Notes []string
	// Why, for a defer, an abandon or a retry.
	Why string
	// Checks failing at H that the conductor judged not the change's:
	// never APPROVE until they pass at H.
	Unrelated bool
}

// checksState reads the checks item's detail: "failing", "pending", or
// "unreadable" (anything else not OK). checksAt writes those prefixes.
func checksState(it GateItem) string {
	switch {
	case it.OK:
		return "passing"
	case strings.HasPrefix(it.Detail, "failing: "):
		return "failing"
	case strings.HasPrefix(it.Detail, "pending: "):
		return "pending"
	}
	return "unreadable"
}

// decideVerdict is the verdict rule (design §4.4, row 5 replaced by the
// owner's decision on red CI): the only place the event is chosen. Pure.
func decideVerdict(in verdictInput) verdictDecision {
	g := in.Gate
	if g.Err != nil {
		return verdictDecision{Action: verdictRetry, Why: "GitHub did not answer: " + g.Err.Error()}
	}
	items := map[string]GateItem{}
	for _, it := range g.Items {
		items[it.Key] = it
	}
	h := short(g.Head)
	// 0. GitHub's head is not H (or the pull request is gone): no verdict.
	if it := items[ReadyHead]; !it.OK {
		return verdictDecision{Action: verdictAbandon, Why: "head_moved: " + it.Detail}
	}
	if it := items[ReadyOpen]; !it.OK && it.Detail != VerdictDraft {
		return verdictDecision{Action: verdictAbandon, Why: "not_open: " + it.Detail}
	}
	var notes []string
	for _, k := range []string{ReadyFindings, ReadyQuestions, ReadyChecks} {
		if it := items[k]; !it.OK {
			notes = append(notes, it.Detail)
		}
	}
	// Informational under this profile (always OK): a conflict or a branch
	// behind is the author's to update, said in the body.
	if it := items[ReadyMergeable]; strings.Contains(it.Detail, "conflicts with") || strings.HasPrefix(it.Detail, "behind ") {
		notes = append(notes, "For the author: "+it.Detail)
	}
	post := func(event, reason, headline string) verdictDecision {
		return verdictDecision{Action: verdictPost, Event: event, Reason: reason, Headline: headline, Notes: notes}
	}
	// 2. Not every lens finished at H: nothing to vouch for.
	if len(in.Unfinished) > 0 {
		return post(forge.EventComment, reasonLensFailed, fmt.Sprintf("Could not complete the review at %s: %s did not finish", h, strings.Join(in.Unfinished, ", ")))
	}
	if it := items[ReadyReviewed]; !it.OK {
		return post(forge.EventComment, reasonNotReviewed, fmt.Sprintf("Could not complete the review at %s: %s", h, it.Detail))
	}
	// 3. An open finding the pull request introduced, at the fix bar.
	if it := items[ReadyFindings]; !it.OK {
		return post(forge.EventRequestChanges, reasonFindings, fmt.Sprintf("Changes requested at %s: %s", h, it.Detail))
	}
	// 3'. The test failed or was blocked at H with no finding of its open
	// at the fix bar (one would be row 3): named, never approved.
	if in.Test != "" {
		d := post(forge.EventComment, reasonTestFailed, fmt.Sprintf("Reviewed at %s, not approved: %s", h, in.Test))
		d.Notes = append(slices.Clone(in.TestNotes), notes...)
		return d
	}
	// 4. A question still open.
	if it := items[ReadyQuestions]; !it.OK {
		return post(forge.EventComment, reasonQuestion, fmt.Sprintf("Waiting on an answer at %s: %s", h, it.Detail))
	}
	checks := items[ReadyChecks]
	switch checksState(checks) {
	case "failing":
		// 5'. The conductor investigates first; related, it starts a lens
		// that files the failure (row 3 then); unrelated, never APPROVE
		// until CI is green at H.
		if !in.CIAsked {
			return verdictDecision{Action: verdictAskCI, Why: checks.Detail}
		}
		d := post(forge.EventComment, reasonUnrelatedCI, fmt.Sprintf("Reviewed at %s, no issue found; not approved while CI fails at %s (%s), which looks unrelated to the change: approval follows once it passes at %s",
			h, h, strings.TrimPrefix(checks.Detail, "failing: "), h))
		d.Unrelated = true
		return d
	case "pending":
		// 6. Deferred, no agent running; bounded (Capped) and ended by CI
		// that never finishes (Stuck).
		switch {
		case in.Stuck:
			return post(forge.EventComment, reasonCIUnconfirmed, fmt.Sprintf("Reviewed at %s, no issue found; CI has been pending at %s past the organization's patience: not approved", h, h))
		case in.Capped:
			return post(forge.EventComment, reasonCIUnconfirmed, fmt.Sprintf("Reviewed at %s, no issue found; could not confirm CI at %s: not approved", h, h))
		}
		return verdictDecision{Action: verdictDefer, Why: checks.Detail}
	case "unreadable":
		// 7.
		return post(forge.EventComment, reasonUnreadable, fmt.Sprintf("Reviewed at %s, no issue found; could not read CI at %s: %s", h, h, checks.Detail))
	}
	// 8. A draft is reviewed, never approved.
	if it := items[ReadyOpen]; !it.OK {
		return post(forge.EventComment, reasonDraft, fmt.Sprintf("Reviewed at %s, no issue found. Draft: no approval until it is marked ready for review", h))
	}
	// 9.
	return post(forge.EventApprove, reasonApprove, fmt.Sprintf("Approved at %s: %s; %s", h, items[ReadyReviewed].Detail, checks.Detail))
}

// verdictFinding is an open finding of the attempt, as the review shows it.
type verdictFinding struct {
	ID, Severity, Title, Description, Fix, Acceptance, File string
	Line                                                    int
	Trivial                                                 bool
	Introduced                                              *bool
	// The inline comment an earlier review of dude's made for it, if one
	// did: not repeated, listed as still open (design §6.4).
	earlier *inlinePost
}

// blocks says the finding holds APPROVE back: at the fix bar, and not said
// to be on the base already.
func (f verdictFinding) blocks() bool {
	return (f.Introduced == nil || *f.Introduced) && IsBlocking(atFixBar([]FindingState{{Severity: f.Severity, Status: "open", Trivial: f.Trivial}})[0])
}

// maxInline bounds the review's inline comments; the rest go in its body.
const maxInline = 20

// verdictReview is the review's body and inline comments for d (design
// §4.5): the headline at H, the lenses, every failing item, the findings
// on the base ("Already on <base>"), a link. postReview adds the marker. Pure.
func verdictReview(d verdictDecision, head, base string, lenses []string, findings []verdictFinding, link string) (string, []forge.ReviewComment) {
	body, comments, _ := verdictReviewOf(d, head, base, lenses, findings, link)
	return body, comments
}

// verdictReviewOf is verdictReview with, for each inline comment, the
// finding it is for.
func verdictReviewOf(d verdictDecision, head, base string, lenses []string, findings []verdictFinding, link string) (string, []forge.ReviewComment, []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\nReviewed commit: `%s`", d.Headline, head)
	if len(lenses) > 0 {
		fmt.Fprintf(&b, " by %s", strings.Join(lenses, ", "))
	}
	b.WriteString(".\n")
	for _, n := range d.Notes {
		if !strings.Contains(d.Headline, n) {
			b.WriteString("\n- " + n)
		}
	}
	var comments []forge.ReviewComment
	var ids []string
	var onBase, stillOpen []string
	for _, f := range findings {
		if f.Introduced != nil && !*f.Introduced {
			onBase = append(onBase, fmt.Sprintf("- %s (%s)%s", f.Title, f.Severity, where(f)))
			continue
		}
		if e := f.earlier; e != nil {
			line := fmt.Sprintf("- %s (%s)%s", f.Title, f.Severity, where(f))
			if e.URL != "" {
				line += ": " + e.URL
			}
			stillOpen = append(stillOpen, e.Head, line)
			continue
		}
		text := "**" + f.Title + "** (" + f.Severity + ")"
		if !f.blocks() {
			text = "nit: " + text
		}
		for _, s := range []string{f.Description, f.Fix, f.Acceptance} {
			if s != "" {
				text += "\n\n" + s
			}
		}
		if f.File == "" || len(comments) >= maxInline && !f.blocks() {
			b.WriteString("\n- " + strings.ReplaceAll(text, "\n", " ") + where(f))
			continue
		}
		comments = append(comments, forge.ReviewComment{Path: f.File, Line: f.Line, Body: text})
		ids = append(ids, f.ID)
	}
	// Pairs of (head, line), grouped by the head the comment was made at.
	for i, last := 0, ""; i < len(stillOpen); i += 2 {
		if stillOpen[i] != last {
			last = stillOpen[i]
			fmt.Fprintf(&b, "\n\n**Still open from %s**", short(last))
		}
		b.WriteString("\n" + stillOpen[i+1])
	}
	if len(onBase) > 0 {
		fmt.Fprintf(&b, "\n\n**Already on %s (not blocking)**\n%s", base, strings.Join(onBase, "\n"))
	}
	if link != "" {
		b.WriteString("\n\n" + link)
	}
	return b.String(), comments, ids
}

func where(f verdictFinding) string {
	if f.File == "" {
		return ""
	}
	if f.Line > 0 {
		return fmt.Sprintf(" `%s:%d`", f.File, f.Line)
	}
	return " `" + f.File + "`"
}

// claimID is the review's claim: deterministic in the task, attempt, head,
// round and event, so a replayed step finds its own claim, and the same
// verdict at the same head is never posted twice.
// The reason is part of it: a COMMENT at H for one reason (unrelated red
// CI) is not the COMMENT for another (the cap).
func claimID(st *State, head, event, reason string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%d\x00%s\x00%s", st.TaskID, st.Attempt, head, st.Round, event, reason))
	return hex.EncodeToString(sum[:8])
}

// verdictDeferCap bounds how long a verdict defers at one head: the
// organization's patience with pending CI (Settings.CIStuck), the same
// the pull request wait gives CI before calling it stuck. Past it, a
// COMMENT "could not confirm CI". It also bounds a pin the sync never
// catches up with, whose CI grace never runs out (doGate).
func (s *Store) verdictDeferCap(gh *forge.GitHub) time.Duration {
	if s.VerdictDeferCap > 0 {
		return s.VerdictDeferCap
	}
	if gh != nil && gh.Settings.CIStuck() > 0 {
		return gh.Settings.CIStuck()
	}
	return time.Hour
}

// After an ambiguous answer the review is looked for (FindOwnReview) at
// increasing delays, reviewLookups times its base, and resubmitted only
// after the last miss. Each further ambiguous answer at the claim doubles
// them, up to reviewRetryCeiling times the base; every wait is at least
// GitHub's Retry-After. The base is reviewRecoverDelay (5s, 30s, 2m; 15m).
const reviewRecoverDelay = 5 * time.Second

var reviewLookups = []time.Duration{1, 6, 24}

const reviewRetryCeiling = 180

// reviewRetryDelay is the wait before the next lookup of a claim whose
// answer was lost submits times, after lookups misses since.
func reviewRetryDelay(base time.Duration, submits, lookups int) time.Duration {
	d := base * reviewLookups[min(lookups, len(reviewLookups)-1)] << min(max(submits-1, 0), 16)
	return min(d, base*reviewRetryCeiling)
}

// reviewRetry is postReview's wait before it runs again: the verdict
// state (the lost claim's counts) is kept across it, unlike a Wait's.
type reviewRetry struct {
	After time.Duration
	Why   string
}

func (r reviewRetry) Error() string { return r.Why }

func (s *Store) reviewRecoverDelay() time.Duration {
	return cmp.Or(s.ReviewRecoverDelay, reviewRecoverDelay)
}

// verdict ends a review-only task's review round, and runs again from its
// wait on CI's signals while one is due.
func (w *steps) verdict(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	if len(st.PullRequestIDs) == 0 {
		return workflow.Result{}, fmt.Errorf("a review-only task with no pull request")
	}
	prID := st.PullRequestIDs[0]
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, []string{prID})
	if err != nil || len(states) == 0 {
		return workflow.Result{}, cmp.Or(err, fmt.Errorf("pull request %s not recorded", prID))
	}
	pr := states[0]
	// A conductor's "next" at the checks decision brings no direction here.
	st.Directed = nil
	head := cmp.Or(st.RoundHeads[pr.Repo], st.Heads[pr.Repo])
	v := st.Verdict
	if v == nil || v.Due != head {
		old := cmp.Or(v, &VerdictState{})
		v = &VerdictState{Due: head, CommentID: old.CommentID, Status: old.Status, Test: old.Test, TestNotes: old.TestNotes, TestHead: old.TestHead,
			DismissRefused: old.DismissRefused}
		st.Verdict = v
	}
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil {
		return workflow.Result{}, err
	}
	var c ReadyGateCheck
	var in verdictInput
	var lenses []string
	var findings []verdictFinding
	var link string
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		if c, err = CheckProfileGateTx(ctx, tx, st.TaskID, []string{prID}, ProfileReviewVerdict, map[string]string{prID: head}); err != nil {
			return err
		}
		if in.Unfinished, lenses, err = lensOutcomesTx(ctx, tx, st, pr.Repo, head); err != nil {
			return err
		}
		if findings, err = verdictFindingsTx(ctx, tx, st, pr.Repo); err != nil {
			return err
		}
		link, err = taskLink(ctx, tx, st.TaskID)
		return err
	}); err != nil {
		return workflow.Result{}, err
	}
	// Never stored (StoreGatesTx): a stored gate has no profile, and would
	// be served as the merge gate.
	gates := DoReadyGate(ctx, gh, c)
	if len(gates) == 0 {
		return workflow.Result{}, fmt.Errorf("no gate read for %s", prID)
	}
	now := time.Now()
	in.Gate, in.CIAsked = gates[0], v.CIAsked == head
	if v.TestHead == head {
		in.Test, in.TestNotes = v.Test, v.TestNotes
	}
	if in.Gate.Err == nil {
		checks := ""
		for _, it := range in.Gate.Items {
			if it.Key == ReadyChecks {
				checks = checksState(it)
			}
		}
		// Failing, then pending: CI was rerun at H. The cap and ci_stuck
		// measure the rerun, not the time before the failure.
		if v.Checks == "failing" && checks == "pending" {
			v.DeferredSince, v.Deferrals, v.Stuck = nil, 0, false
		}
		v.Checks = checks
	}
	in.Stuck = v.Stuck
	in.Capped = v.DeferredSince != nil && now.Sub(*v.DeferredSince) >= w.s.verdictDeferCap(gh)
	d := decideVerdict(in)
	base := cmp.Or(st.Source.BaseBranch, "the base")
	switch d.Action {
	case verdictRetry:
		return workflow.Result{}, workflow.Wait{After: max(forge.RetryAfter(in.Gate.Err), readyGateRetry), Why: "the verdict could not read GitHub: " + d.Why}
	case verdictAbandon:
		v.Due = ""
		if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewAbandoned, map[string]any{"pullRequestId": prID, "head": head, "why": d.Why}); err != nil {
			return workflow.Result{}, err
		}
		w.statusComment(ctx, sc, st, pr, fmt.Sprintf("No review posted for %s: %s", short(head), d.Why))
		// The wait looks at the new head at once: the push that moved it may
		// have been signalled while the round ran.
		st.reweighNow()
		return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "the pull request moved past the reviewed head")
	case verdictAskCI:
		// As a writing delivery's conductor hears failing checks at its
		// pull request wait (PointPRFeedback); its "next" comes back here.
		v.CIAsked = head
		return w.next(ctx, sc, st, PointPRFeedback, "verdict", render(verdictChecksTmpl, struct{ Repo, Head, Checks string }{
			pr.Repo, short(head), strings.TrimPrefix(d.Why, "failing: ")}))
	case verdictDefer:
		if v.DeferredSince == nil {
			v.DeferredSince = &now
		}
		v.Deferrals++
		if err := w.s.Emit(ctx, sc.OrganizationID, st, EvVerdictDeferred, map[string]any{"pullRequestId": prID, "head": head,
			"why": d.Why, "deferrals": v.Deferrals}); err != nil {
			return workflow.Result{}, err
		}
		w.statusComment(ctx, sc, st, pr, fmt.Sprintf("Reviewed %s; waiting for CI at %s (%s)", short(head), short(head), d.Why))
		// Woken by the sync's checks signal; by the timer at the grace's
		// end, and at the cap, whichever comes first.
		at := v.DeferredSince.Add(w.s.verdictDeferCap(gh))
		if g := in.Gate.GraceLeft; g > 0 && now.Add(g+graceMargin).Before(at) {
			at = now.Add(g + graceMargin)
		}
		st.ReweighAt = &at
		return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "waiting for CI at the reviewed head")
	}
	// A finding dude already commented on inline, still open, is listed in
	// the body, not commented on again (design §6.4).
	var earlier map[string]inlinePost
	if err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) (err error) {
		earlier, err = earlierPostsTx(ctx, tx, st.TaskID, prID)
		return err
	}); err != nil {
		return workflow.Result{}, err
	}
	for i := range findings {
		if p, ok := earlier[findings[i].ID]; ok {
			findings[i].earlier = &p
		}
	}
	body, comments, ids := verdictReviewOf(d, head, base, lenses, findings, link)
	posted, err := w.postReview(ctx, sc, st, gh, pr, head, d.Event, d.Reason, body, comments, ids)
	if r := (reviewRetry{}); errors.As(err, &r) {
		_ = w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", r.Why)
		return workflow.Result{Next: "verdict", State: st, SleepUntil: time.Now().Add(r.After)}, nil
	}
	if err != nil {
		return workflow.Result{}, err
	}
	st.ReweighAt = nil
	if posted != "" {
		v.Posted = posted
		// After the verdict, best effort: dude's threads in step, and an
		// approval of dude's at another commit still standing dismissed.
		w.syncThreads(ctx, sc, st, gh, pr, head)
		if err := w.dismissApprovals(ctx, sc, st, gh, pr, head, "superseded"); err != nil {
			return workflow.Result{}, err
		}
	}
	if !transientReason(d.Reason) || posted == "" {
		// Only an approval held back by CI at H (unrelated red CI, CI not
		// confirmed at the cap or stuck) is still owed at H (decision 2):
		// CI's signals run the verdict again, and green at H approves.
		v.Due = ""
	}
	w.statusComment(ctx, sc, st, pr, fmt.Sprintf("%s (%s)", d.Headline, cmp.Or(reviewWord[v.Posted], "not posted")))
	return waitForPR(st), w.s.SetTaskStatus(ctx, sc.OrganizationID, st, "review", "dude's review at "+short(head)+": "+cmp.Or(reviewWord[v.Posted], "not posted"))
}

var reviewWord = map[string]string{forge.EventApprove: "approved", forge.EventRequestChanges: "changes requested", forge.EventComment: "commented"}

// postReview claims, submits and reads back the review (design §2.5): the
// event posted ("" when it was abandoned or refused). An ambiguous answer
// keeps the claim, and the step runs again after reviewRecoverDelay to
// look for the review before anything is resubmitted.
func (w *steps) postReview(ctx context.Context, sc workflow.StepContext, st *State, gh *forge.GitHub, pr PullRequestState,
	head, event, reason, body string, comments []forge.ReviewComment, ids []string) (string, error) {
	if gh == nil {
		return "", fmt.Errorf("no forge credential to post the review with")
	}
	org := sc.OrganizationID
	for range 2 { // the second pass is a downgrade of the account's own pull request
		claim := claimID(st, head, event, reason)
		marked := body + "\n\n" + forge.ReviewMarker(st.TaskID, claim)
		base := map[string]any{"claimId": claim, "pullRequestId": pr.ID, "head": head, "event": event}
		claimed, submitted, downgraded, err := w.claimState(ctx, org, st.TaskID, claim)
		if err != nil {
			return "", err
		}
		if submitted {
			return event, nil
		}
		if downgraded {
			body, event = ownPullRequestBody(event, body), forge.EventComment
			continue
		}
		v := st.Verdict
		if v.Lost != claim {
			v.Lost, v.LostSubmits, v.LostLookups = claim, 0, 0
		}
		delay := w.s.reviewRecoverDelay()
		if claimed {
			// An earlier run's answer was lost, or it stopped short: find
			// the review before posting anything, at increasing delays.
			found, ok, err := gh.FindOwnReview(ctx, pr.Slug, pr.Number, st.TaskID, claim)
			if err != nil {
				return "", reviewRetry{After: max(forge.RetryAfter(err), reviewRetryDelay(delay, v.LostSubmits, v.LostLookups)),
					Why: "looking for dude's review: " + err.Error()}
			}
			if ok {
				v.Lost, v.LostSubmits, v.LostLookups = "", 0, 0
				return event, w.submitted(ctx, sc, st, pr, base, found, true, w.reviewPosts(ctx, gh, pr, found.ID, comments, ids))
			}
			if v.LostSubmits > 0 && v.LostLookups+1 < len(reviewLookups) {
				v.LostLookups++
				return "", reviewRetry{After: reviewRetryDelay(delay, v.LostSubmits, v.LostLookups),
					Why: fmt.Sprintf("dude's review not found yet (lookup %d of %d): looking again before resubmitting", v.LostLookups, len(reviewLookups))}
			}
		} else if err := w.s.Emit(ctx, org, st, EvReviewClaimed, base); err != nil {
			return "", err
		}
		// /pulls/files has no ref: the head must be H as the diff is read.
		now, err := gh.Head(ctx, pr.Slug, pr.Number)
		if err != nil {
			return "", workflow.Wait{After: readyGateRetry, Why: "reading the head before the review: " + err.Error()}
		}
		if now != head {
			return "", w.s.Emit(ctx, org, st, EvReviewAbandoned, map[string]any{"claimId": claim, "pullRequestId": pr.ID,
				"head": head, "why": "head_moved", "githubHead": now})
		}
		diff, err := gh.PullDiffLines(ctx, pr.Slug, pr.Number)
		if err != nil {
			return "", workflow.Wait{After: readyGateRetry, Why: "reading the diff before the review: " + err.Error()}
		}
		inline, folded := forge.PlaceComments(comments, diff)
		in := forge.SubmitReview{Slug: pr.Slug, Number: pr.Number, CommitID: head, Event: event,
			Body: forge.FoldComments(marked, folded), Comments: inline}
		got, err := gh.SubmitReview(ctx, in)
		if forge.LineNotInDiff(err) {
			// Once, everything folded into the body.
			in.Body, in.Comments = forge.FoldComments(marked, append(inline, folded...)), nil
			got, err = gh.SubmitReview(ctx, in)
		}
		switch {
		case err == nil:
			v.Lost, v.LostSubmits, v.LostLookups = "", 0, 0
			return event, w.submitted(ctx, sc, st, pr, base, got, false, w.reviewPosts(ctx, gh, pr, got.ID, comments, ids))
		case forge.OwnPullRequest(err) && event != forge.EventComment:
			if err := w.s.Emit(ctx, org, st, EvReviewDowngraded, map[string]any{"claimId": claim, "pullRequestId": pr.ID,
				"head": head, "from": event, "why": forge.RefusalOwnPullRequest}); err != nil {
				return "", err
			}
			body, event = ownPullRequestBody(event, body), forge.EventComment
			continue
		case forge.Refused(err) && !forge.LineNotInDiff(err):
			var e *forge.Error
			status, msg := 0, err.Error()
			if errors.As(err, &e) {
				status = e.Status
			}
			return "", w.s.Emit(ctx, org, st, EvReviewFailed, map[string]any{"claimId": claim, "pullRequestId": pr.ID,
				"head": head, "status": status, "message": msg})
		}
		// Ambiguous: GitHub may hold it. The claim stays; never resubmitted
		// blindly: looked for first, at increasing delays.
		v.LostSubmits++
		v.LostLookups = 0
		return "", reviewRetry{After: max(forge.RetryAfter(err), reviewRetryDelay(delay, v.LostSubmits, 0)),
			Why: "GitHub's answer to dude's review was lost: " + err.Error()}
	}
	return "", nil
}

// reviewPosts is the review's inline comments, each with the finding it
// is for, read back from GitHub (the submit's answer has no comments).
// Unreadable, none: the threads are then not kept in step.
func (w *steps) reviewPosts(ctx context.Context, gh *forge.GitHub, pr PullRequestState, reviewID int64, comments []forge.ReviewComment, ids []string) []inlinePost {
	if len(comments) == 0 {
		return nil
	}
	got, err := gh.ReviewComments(ctx, pr.Slug, pr.Number, reviewID)
	if err != nil {
		slog.Warn("reading dude's review comments back failed", "pr", pr.ID, "review", reviewID, "error", err)
		return nil
	}
	return postedComments(got, comments, ids)
}

// submitted records the review GitHub holds, then reads the pull request
// back so its reviews show dude's.
func (w *steps) submitted(ctx context.Context, sc workflow.StepContext, st *State, pr PullRequestState, base map[string]any,
	got forge.SubmittedReview, recovered bool, posts []inlinePost) error {
	payload := map[string]any{"reviewId": got.ID, "url": got.URL}
	if len(posts) > 0 {
		payload["comments"] = posts
	}
	for k, v := range base {
		payload[k] = v
	}
	if recovered {
		payload["recovered"] = true
	}
	if err := w.s.Emit(ctx, sc.OrganizationID, st, EvReviewSubmitted, payload); err != nil {
		return err
	}
	if w.s.SyncPullRequest != nil {
		if err := w.s.SyncPullRequest(ctx, sc.OrganizationID, pr.ID); err != nil {
			slog.Warn("reading the pull request back after dude's review failed", "pr", pr.ID, "error", err)
		}
	}
	return nil
}

// ownPullRequestBody is the verdict's body posted as a COMMENT because
// GitHub refused event on the account's own pull request.
func ownPullRequestBody(event, body string) string {
	return "dude cannot " + strings.ToLower(strings.ReplaceAll(event, "_", " ")) +
		" a pull request its own account opened: its verdict, as a comment.\n\n" + body
}

// claimState says whether claim was made, whether its review was
// submitted (or the claim closed: refused), and whether GitHub refused it
// as the account's own pull request (posted as a COMMENT's claim instead).
func (w *steps) claimState(ctx context.Context, org, taskID, claim string) (claimed, done, downgraded bool, err error) {
	err = w.s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'claimId' = $6),
			EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type IN ($3, $4) AND payload->>'claimId' = $6),
			EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $5 AND payload->>'claimId' = $6)`,
			taskID, EvReviewClaimed, EvReviewSubmitted, EvReviewFailed, EvReviewDowngraded, claim).Scan(&claimed, &done, &downgraded)
	})
	return claimed, done, downgraded, err
}

// statusComment keeps the one status comment on the pull request (design
// §4.5): created once, then edited, only when its words change. Failures
// are logged; the review is the record.
func (w *steps) statusComment(ctx context.Context, sc workflow.StepContext, st *State, pr PullRequestState, line string) {
	v := st.Verdict
	body := "dude's review of this pull request — " + line + "\n\n"
	if v.DismissRefused != "" {
		body += v.DismissRefused + "\n\n"
	}
	body += forge.VerdictMarker(st.TaskID)
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:8])
	if v.Status == hash {
		return
	}
	id := v.CommentID
	posted, edited, err := w.s.postVerdict(ctx, sc.OrganizationID, w.forges, pr.Slug, pr.Number, st.TaskID, &id, body)
	payload := map[string]any{"pullRequestId": pr.ID, "repo": pr.Repo, "number": pr.Number, "head": v.Due, "edited": edited}
	if err != nil {
		slog.Warn("dude's review status comment failed", "pr", pr.ID, "error", err)
		payload["error"] = err.Error()
	} else {
		v.CommentID, v.Status = posted.CommentID, hash
		payload["commentId"], payload["url"] = posted.CommentID, posted.URL
	}
	_ = w.s.Emit(ctx, sc.OrganizationID, st, EvPullRequestVerdictCommented, payload)
}

// lensOutcomesTx is, at head in repo, the lenses whose latest review Run
// of the attempt failed or was aborted (unfinished), and those whose latest
// completed (lenses).
func lensOutcomesTx(ctx context.Context, tx pgx.Tx, st *State, repo, head string) (unfinished, lenses []string, err error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (k.category) k.category, k.status::text FROM runs k WHERE `+reviewedFrom+`
		AND k.category IS NOT NULL AND k.base_refs->>$3 = $4 ORDER BY k.category, k.created_at DESC, k.id DESC`,
		st.TaskID, st.Attempt, repo, head)
	if err != nil {
		return nil, nil, err
	}
	var category, status string
	_, err = pgx.ForEachRow(rows, []any{&category, &status}, func() error {
		switch status {
		case "failed", "aborted":
			unfinished = append(unfinished, category)
		case "completed":
			lenses = append(lenses, category)
		}
		return nil
	})
	return unfinished, lenses, err
}

// verdictFindingsTx is the attempt's open findings in repo, for the review.
func verdictFindingsTx(ctx context.Context, tx pgx.Tx, st *State, repo string) ([]verdictFinding, error) {
	rows, err := tx.Query(ctx, `SELECT id, severity::text, title, description, suggested_fix, COALESCE(acceptance, ''),
			COALESCE(file, ''), COALESCE(line, 0), COALESCE(trivial, false), introduced
		FROM review_findings WHERE task_id = $1 AND status = 'open' AND `+thisAttempt+` AND COALESCE(repo, $3) = $3
		ORDER BY created_at, id`, st.TaskID, st.Attempt, repo)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[verdictFinding])
	slices.SortStableFunc(out, func(a, b verdictFinding) int {
		return cmp.Compare(severityRank[a.Severity], severityRank[b.Severity])
	})
	return out, err
}

// awaitReviewOnly is a review-only task's pull request wait: no fixer.
// The pull request's end ends the task; CI's signals run a verdict still
// due at the head GitHub shows; everything else is context for the next
// round (PR 5), and wakes nobody here.
func (w *steps) awaitReviewOnly(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, error) {
	ended, rerun, recheck := false, false, false
	for _, sig := range sc.Signals {
		if sig.Name != SignalPRFeedback {
			continue
		}
		var s forge.Signal
		if json.Unmarshal(sig.Payload, &s) != nil {
			continue
		}
		switch {
		case s.Kind == "terminal":
			ended = true
		case s.Kind == SignalKindRecheck:
			recheck = true
		case s.Kind == "ci_stuck":
			if st.Verdict != nil {
				st.Verdict.Stuck = true
			}
			rerun = true
		case s.Kind == "readiness", slices.ContainsFunc(s.Feedback, isChecks):
			rerun = true
		}
	}
	if st.ReweighAt != nil && (len(sc.Signals) == 0 || !time.Now().Before(*st.ReweighAt)) {
		rerun = true
	}
	if ended {
		if res, finished, err := w.pullRequestEnded(ctx, sc, st); err != nil || finished {
			return res, err
		}
	}
	// The author's push (design §6.1): every head dude did not review is
	// theirs here. Its stale approval goes at once; the next round after
	// the debounce. (A re-request of dude's review at the same head reaches
	// no wait yet: the sync does not surface review_requested; PR 7.)
	if len(st.PullRequestIDs) > 0 {
		states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs[:1])
		if err != nil {
			return workflow.Result{}, err
		}
		var pr PullRequestState
		if len(states) > 0 {
			pr = states[0]
		}
		// A draft is a live pull request too: its push dismisses the stale
		// approval at once, and its round runs (the verdict comments on a
		// draft rather than approving).
		live := (pr.State == forge.StateOpen || pr.State == forge.StateDraft) && pr.HeadSHA != ""
		if st.RoundDueAt != nil && (!live || pr.HeadSHA != st.RoundDueHead) {
			// The head the round was owed at is gone (a revert, the pull
			// request closed): no round there, and no wake on its past due
			// time. A newer head arms its own below.
			st.RoundDueAt, st.RoundDueHead = nil, ""
		}
		if live {
			if pr.HeadSHA != st.reviewedHead(pr.Repo) {
				gh, err := w.forges.For(ctx, sc.OrganizationID)
				if err != nil {
					return workflow.Result{}, err
				}
				var settings forge.Settings
				if gh != nil {
					settings = gh.Settings
				}
				if err := w.newHead(ctx, sc, st, gh, pr, settings); err != nil {
					return workflow.Result{}, err
				}
				if st.RoundDueAt != nil && st.RoundDueHead == pr.HeadSHA && !time.Now().Before(*st.RoundDueAt) {
					return w.startRound(ctx, sc, st, pr)
				}
			} else if recheck && st.Verdict != nil && st.RoundDueAt == nil {
				// The conductor withdrew a finding: the verdict again at the
				// head reviewed, no new round (its claim names the event).
				st.Verdict.Due, rerun = pr.HeadSHA, true
			}
		}
	}
	if rerun && st.Verdict != nil && st.Verdict.Due != "" {
		return workflow.Result{Next: "verdict", State: st}, nil
	}
	// A verdict still owed keeps its timer (the grace's end, the cap):
	// a signal that did not run it never leaves it waiting on signals alone.
	if st.Verdict == nil || st.Verdict.Due == "" {
		st.ReweighAt = nil
	}
	res := waitForPR(st)
	if st.RoundDueAt != nil && (res.SleepUntil.IsZero() || st.RoundDueAt.Before(res.SleepUntil)) {
		// The debounce's end wakes the wait, signal or none.
		res.SleepUntil = *st.RoundDueAt
	}
	return res, nil
}
