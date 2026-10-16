// The verification gate: what a finished test Run's verdict and the
// tester's findings let the delivery do next (steps.testVerdict).

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// MaxTestFixRounds is how many fixes a failed test may send a delivery's
// attempt back for before a person decides.
const MaxTestFixRounds = 2

// EnvironmentPrefix starts the title of the finding a tester raises when
// it cannot run the application: not a defect for a fixer.
const EnvironmentPrefix = "Test environment:"

// How long the gate waits for a finished test Run's published files to be
// recorded before it judges with what is there, and the bounds of its
// back-off between looks: a tenth of the time since the Run ended.
const (
	artifactsWait       = 15 * time.Minute
	artifactsRecheckMin = 500 * time.Millisecond
	artifactsRecheckMax = 30 * time.Second
)

// acceptanceCategory is the category of the finding dude raises for a
// failed criterion.
const acceptanceCategory = "acceptance"

// Escalation reasons of the gate, and the actions only they offer.
const (
	ReasonTestFailedCriteria = "test_failed_criteria"
	ReasonTestBlocked        = "test_blocked"
	ActionWaive              = "waive"
)

// NeedsReason says an action on an escalation must carry a person's
// reason, and may be taken by an organization admin as well as the
// task's owner: shipping past an unmet or unchecked criterion.
func NeedsReason(reason, action string) bool {
	return reason == ReasonTestFailedCriteria && action == "accept" || reason == ReasonTestBlocked && action == ActionWaive
}

// RunReplyTx is everything a Run's agent said, its messages joined.
func RunReplyTx(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT payload->>'text' FROM events
		WHERE run_id = $1 AND event_type = 'agent.message' ORDER BY cursor`, runID)
	if err != nil {
		return "", err
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[*string])
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range texts {
		// Messages end where the agent stopped to call a tool; a document
		// starting the next one must still begin on a line of its own.
		if t != nil {
			b.WriteString(*t)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// artifactsRecheckTx is how long to wait before judging the test Run
// again because its published files may not all be recorded yet: its
// exit's collection is still due, and it ended under artifactsWait ago.
// Zero when they are all there, or the wait is over.
func artifactsRecheckTx(ctx context.Context, tx pgx.Tx, runID string) (time.Duration, error) {
	var since *float64
	err := tx.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM now() - COALESCE(ended_at, now()))::float8 FROM runs
		WHERE id = $1 AND artifacts_due_at IS NOT NULL AND COALESCE(ended_at, now()) > now() - make_interval(secs => $2)`,
		runID, artifactsWait.Seconds()).Scan(&since)
	if db.IsNotFound(err) || since == nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return min(max(time.Duration(*since*float64(time.Second))/10, artifactsRecheckMin), artifactsRecheckMax), nil
}

// awaitsEvidence says a verdict could still change once more of the Run's
// files are recorded: a pass is unverified for a name it cited that is not
// recorded yet. Nothing else a later file could change.
func awaitsEvidence(verdicts []CriterionVerdict, published map[string]bool) bool {
	return slices.ContainsFunc(verdicts, func(v CriterionVerdict) bool {
		return v.Status == VerdictUnverified && slices.ContainsFunc(v.Evidence, func(n string) bool { return !published[n] })
	})
}

// RecordVerdict stores the test Run's verdict on each criterion, once:
// the tester's report judged against the task's criteria and what the Run
// published, at the heads it checked out. A criterion that fails with no
// finding of this Run's saying why gets one, so the fixer is told. An
// environment finding of an earlier test of the attempt is superseded:
// this test is the one that tried to run it now. When a pass cites a file
// not recorded yet and the Run's files are still due, it records nothing
// and says how long to wait before judging again.
func (s *Store) RecordVerdict(ctx context.Context, org string, st *State, runID string) (wait time.Duration, err error) {
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM acceptance_verdicts WHERE run_id = $1)`, runID).Scan(&done); err != nil || done {
			return err
		}
		var rawCriteria, rawHeads []byte
		if err := tx.QueryRow(ctx, `SELECT t.acceptance_criteria, r.base_refs FROM runs r JOIN tasks t ON t.id = r.task_id
			WHERE r.id = $1`, runID).Scan(&rawCriteria, &rawHeads); err != nil {
			return err
		}
		var criteria []string
		_ = json.Unmarshal(rawCriteria, &criteria)
		reply, err := RunReplyTx(ctx, tx, runID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT name FROM artifacts WHERE run_id = $1`, runID)
		if err != nil {
			return err
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		published := map[string]bool{}
		for _, n := range names {
			published[n] = true
		}
		verdicts := JudgeAcceptance(criteria, ParseAcceptance(reply), published)
		if awaitsEvidence(verdicts, published) {
			// What it published may still be on its way from lux: a pass
			// is judged against every file, not the ones recorded so far.
			if wait, err = artifactsRecheckTx(ctx, tx, runID); err != nil || wait > 0 {
				return err
			}
		}
		for _, v := range verdicts {
			if _, err := tx.Exec(ctx, `INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text,
					status, evidence, observed, problem, heads)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
				org, st.TaskID, runID, v.Criterion, v.Text, v.Status, db.NonNil(v.Evidence), v.Observed, v.Problem, rawHeads); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE review_findings SET status = 'superseded',
				resolution_note = 'a later test ran the application again', updated_at = now()
			WHERE task_id = $1 AND status = 'open' AND title LIKE $3 || '%' AND run_id <> $4
			  AND `+thisAttempt+` AND NOT `+fromReview, st.TaskID, st.Attempt, EnvironmentPrefix, runID); err != nil {
			return err
		}
		// What dude raised for a failed criterion is settled by the
		// criterion's verdict now.
		for _, v := range verdicts {
			if v.Status != VerdictPass && v.Status != VerdictNotApplicable {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE review_findings SET status = 'resolved', resolved_by_run_id = $4,
					resolution_note = 'the criterion passed the next test', updated_at = now()
				WHERE task_id = $1 AND status = 'open' AND title LIKE $3 AND run_id <> $4 AND `+thisAttempt,
				st.TaskID, st.Attempt, criterionFindingTitle(v.Criterion, "")+"%", runID); err != nil {
				return err
			}
		}
		// A blocking finding this test raised says why already. One an
		// earlier test raised is about what that test saw, not this failure.
		var said bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM review_findings WHERE run_id = $1 AND status = 'open'
			AND severity::text = ANY($2) AND title NOT LIKE $3 || '%')`,
			runID, st.Policy.BlockingSeverities, EnvironmentPrefix).Scan(&said); err != nil || said {
			return err
		}
		severity := "blocking"
		if len(st.Policy.BlockingSeverities) > 0 && !slices.Contains(st.Policy.BlockingSeverities, severity) {
			severity = st.Policy.BlockingSeverities[0]
		}
		for _, v := range verdicts {
			if v.Status != VerdictFail {
				continue
			}
			// One dude raised for this criterion at an earlier test, still
			// open, is the same failure: the fixer is told once.
			if _, err := tx.Exec(ctx, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category,
					severity, title, description)
				SELECT $3, $4, $1, $5, $6, $7::finding_severity, $8, $9
				WHERE NOT EXISTS (SELECT 1 FROM review_findings WHERE task_id = $1 AND status = 'open'
					AND title LIKE $10 AND `+thisAttempt+`)`,
				st.TaskID, st.Attempt, ids.New(ids.Finding), org, runID, acceptanceCategory, severity,
				criterionFindingTitle(v.Criterion, v.Text),
				"Reproduced: "+valueOr(v.Observed, "the tester reported it failing."),
				criterionFindingTitle(v.Criterion, "")+"%"); err != nil {
				return err
			}
		}
		return nil
	})
	return wait, err
}

// criterionFindingTitle is the title of the finding dude raises for a
// failed criterion the tester raised none for.
func criterionFindingTitle(n int, text string) string {
	t := fmt.Sprintf("Acceptance criterion %d fails:", n)
	if text != "" {
		t += " " + clip(oneLine(text), 200)
	}
	return t
}

// TestFinding is an open finding of the tester's, as the gate weighs it.
type TestFinding struct {
	ID, Severity, Title, RunID string
}

// Environment says the tester could not run the application: a blocked
// check, not a product defect.
func (f TestFinding) Environment() bool { return strings.HasPrefix(f.Title, EnvironmentPrefix) }

// gateInput is what the gate decides from: the test Run's verdict, and
// the tester's open findings of this attempt.
type gateInput struct {
	RunID    string
	Verdicts []StoredVerdict
	Findings []TestFinding
}

// StoredVerdict is a recorded criterion verdict and any person's decision.
type StoredVerdict struct {
	Criterion int
	Text      string
	Status    string
	Decision  string
}

func (s *Store) gateInput(ctx context.Context, org string, st *State, runID string) (gateInput, error) {
	in := gateInput{RunID: runID}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT criterion, text, status, COALESCE(decision, '') FROM acceptance_verdicts
			WHERE run_id = $1 ORDER BY criterion`, runID)
		if err != nil {
			return err
		}
		if in.Verdicts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[StoredVerdict]); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id, severity::text, title, run_id FROM review_findings
			WHERE task_id = $1 AND status = 'open' AND `+thisAttempt+` AND NOT `+fromReview+` ORDER BY created_at`,
			st.TaskID, st.Attempt)
		if err != nil {
			return err
		}
		in.Findings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[TestFinding])
		return err
	})
	return in, err
}

// gate is the outcome of a test: "pass", "fail" (something to fix) or
// "blocked" (something nobody could check), and the criteria behind it.
type gate struct {
	Outcome  string
	Criteria []int
	Findings []string
}

// judgeGate decides from the verdict and the tester's open findings. A
// failure is a criterion that fails or a blocking finding of the tester's
// that is not about its environment; failing outranks blocked, since a fix
// is needed whatever else could not be checked. A person's decision on a
// criterion settles it. An earlier test's finding is a failure only when
// this test ran the application — judged some criterion and raised no
// environment finding: one that did not has not judged it, so it is still
// to be checked, not to be fixed.
func judgeGate(p Policy, in gateInput) gate {
	var failed, blocked gate
	ran := slices.ContainsFunc(in.Verdicts, func(v StoredVerdict) bool {
		return v.Status == VerdictPass || v.Status == VerdictFail || v.Status == VerdictNotApplicable
	}) && !slices.ContainsFunc(in.Findings, func(f TestFinding) bool { return f.Environment() && f.RunID == in.RunID })
	for _, v := range in.Verdicts {
		if v.Decision != "" {
			continue
		}
		switch v.Status {
		case VerdictFail:
			failed.Criteria = append(failed.Criteria, v.Criterion)
		case VerdictBlocked, VerdictUnverified:
			blocked.Criteria = append(blocked.Criteria, v.Criterion)
		}
	}
	for _, f := range in.Findings {
		switch {
		case f.Environment():
			blocked.Findings = append(blocked.Findings, f.ID)
		case !slices.Contains(p.BlockingSeverities, f.Severity):
		case ran || f.RunID == in.RunID:
			failed.Findings = append(failed.Findings, f.ID)
		default:
			blocked.Findings = append(blocked.Findings, f.ID)
		}
	}
	switch {
	case len(failed.Criteria)+len(failed.Findings) > 0:
		failed.Outcome = "fail"
		return failed
	case len(blocked.Criteria)+len(blocked.Findings) > 0:
		blocked.Outcome = "blocked"
		return blocked
	}
	return gate{Outcome: "pass"}
}

// testVerdict is the gate after a test Run that finished: open the pull
// request when every criterion passed with evidence, was not applicable
// with a reason, or a person waived or accepted it; send a failure back to
// the fixer, up to MaxTestFixRounds; and stop for a person otherwise.
func (w *steps) testVerdict(ctx context.Context, sc workflow.StepContext) (workflow.Result, error) {
	st, err := load(sc)
	if err != nil {
		return workflow.Result{}, err
	}
	runID := st.TestRunID
	if wait, err := w.s.RecordVerdict(ctx, sc.OrganizationID, st, runID); err != nil {
		return workflow.Result{}, err
	} else if wait > 0 {
		return workflow.Result{Next: "testVerdict", State: st, SleepUntil: time.Now().Add(wait)}, nil
	}
	w.s.CommentVerdict(ctx, sc.OrganizationID, st, w.forges)
	w.s.RefreshVerification(ctx, sc.OrganizationID, st, w.forges)
	in, err := w.s.gateInput(ctx, sc.OrganizationID, st, runID)
	if err != nil {
		return workflow.Result{}, err
	}
	g := judgeGate(st.Policy, in)
	detail := map[string]any{"runId": runID, "criteria": db.NonNil(criteriaOf(in.Verdicts, g.Criteria)),
		"findingIds": db.NonNil(g.Findings), "rounds": st.TestFixRounds}
	switch g.Outcome {
	case "fail":
		if st.TestFixRounds < MaxTestFixRounds {
			st.TestFixRounds++
			st.TestFindingIDs, st.RetestDue = g.Findings, true
			return w.next(ctx, sc, st, PointReviewed, "fix", fmt.Sprintf("test %s failed: criteria %v, %d blocking findings "+
				"of the tester's; test fix round %d of %d.", runID, g.Criteria, len(g.Findings), st.TestFixRounds, MaxTestFixRounds))
		}
		return w.escalate(ctx, sc, st, ReasonTestFailedCriteria, detail)
	case "blocked":
		return w.escalate(ctx, sc, st, ReasonTestBlocked, detail)
	}
	return w.beforePR(ctx, sc, st, fmt.Sprintf("test %s: every criterion passed or was settled by a person.", runID))
}

// criteriaOf names the criteria in a gate's detail: number and text.
func criteriaOf(verdicts []StoredVerdict, numbers []int) []map[string]any {
	var out []map[string]any
	for _, v := range verdicts {
		if slices.Contains(numbers, v.Criterion) {
			out = append(out, map[string]any{"criterion": v.Criterion, "text": v.Text, "status": v.Status})
		}
	}
	return out
}

// TesterFindingsToJudge are the tester's open findings a fix has been
// sent: what the next test is shown to judge, as a re-review is.
func (s *Store) TesterFindingsToJudge(ctx context.Context, org string, st *State) ([]string, error) {
	var out []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM review_findings
			WHERE task_id = $1 AND status = 'open' AND fix_attempts > 0 AND title NOT LIKE $3 || '%'
			  AND `+thisAttempt+` AND NOT `+fromReview+` ORDER BY created_at`, st.TaskID, st.Attempt, EnvironmentPrefix)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return out, err
}

// SettleVerdictTx records a person's waiver or acceptance on the test
// Run's verdict: each blocked or unverified criterion waived, or each
// failed one accepted, with who and why; the tester's findings it covers
// are accepted with the same reason. In the decision's transaction, so the
// gate reads it when the workflow carries the decision out.
func SettleVerdictTx(ctx context.Context, tx pgx.Tx, e *Escalation, action, person, note string) error {
	runID := e.RunID()
	if runID == "" {
		return nil
	}
	statuses, decision := []string{VerdictBlocked, VerdictUnverified}, "waived"
	if action == "accept" {
		statuses, decision = []string{VerdictFail}, "accepted"
	}
	if _, err := tx.Exec(ctx, `UPDATE acceptance_verdicts SET decision = $2, decided_by = $3, decision_note = $4, decided_at = now()
		WHERE run_id = $1 AND status = ANY($5) AND decision IS NULL`, runID, decision, db.Nullable(person), note, statuses); err != nil {
		return err
	}
	findingIDs := e.findingIDs()
	if len(findingIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE review_findings SET status = 'accepted', resolution_note = $2, updated_at = now()
		WHERE id = ANY($1) AND status = 'open'`, findingIDs, decision+" by a person: "+note)
	return err
}

// verdictCommentTimeout bounds every GitHub call of one verdict comment
// together: up to a recovery search, an edit and a post, each also under
// forge's own per-request limit.
const verdictCommentTimeout = 30 * time.Second

// CommentVerdict keeps one verdict comment on each adopted pull request.
// Its successful ledger event keeps the comment id across workflow replays
// and attempts; the marker recovers a post whose response or record was
// lost. GitHub failures are recorded, never returned to the delivery.
//
// Two short transactions around the GitHub calls, none open across them:
// what to post, then its record. No lock: the callers are steps of the
// task's one delivery (testVerdict, awaitPRFix), which never run at once,
// and an adopted pull request is one live task's.
func (s *Store) CommentVerdict(ctx context.Context, org string, st *State, forges Forges) {
	if !st.adoptedPR() {
		return
	}
	var prID, repo, repoURL, body string
	var number int
	var commentID int64
	var section, link string
	var err error
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pr.id, r.name, r.url, pr.number
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = ANY($1) AND pr.task_id = $2 AND pr.adopted AND pr.state IN ('open', 'draft')`,
			st.PullRequestIDs, st.TaskID).Scan(&prID, &repo, &repoURL, &number); err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		section, link, err = verificationText(ctx, tx, st)
		if err != nil {
			return err
		}
		if section == "" {
			return nil
		}
		body = "dude's test verdict for this pull request"
		if link != "" {
			body += " — " + link
		}
		body += "\n\n" + section + "\n\n" + forge.VerdictMarker(st.TaskID)
		return tx.QueryRow(ctx, `SELECT COALESCE((SELECT (payload->>'commentId')::bigint FROM events
			WHERE event_type = $1 AND payload->>'pullRequestId' = $2 AND payload ? 'commentId'
			AND NOT payload ? 'error' ORDER BY cursor DESC LIMIT 1), 0)`, EvPullRequestVerdictCommented, prID).Scan(&commentID)
	})
	if err != nil || body == "" {
		if err != nil {
			slog.Warn("reading the adopted pull request's test verdict failed", "task", st.TaskID, "error", err)
		}
		return
	}
	payload := map[string]any{"pullRequestId": prID, "repo": repo, "number": number, "head": st.Heads[repo]}
	posted, edited, postErr := s.postVerdict(ctx, org, forges, forge.SlugFromURL(repoURL), number, st.TaskID, &commentID, body)
	payload["edited"] = edited
	if postErr != nil {
		slog.Warn("posting the adopted pull request's test verdict failed", "pr", prID, "error", postErr)
		payload["error"] = postErr.Error()
		if commentID != 0 {
			payload["commentId"] = commentID
		}
	} else {
		payload["commentId"], payload["url"] = posted.CommentID, posted.URL
	}
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return emitTx(ctx, tx, org, st, EvPullRequestVerdictCommented, payload)
	}); err != nil {
		slog.Warn("recording the adopted pull request's test verdict comment failed", "task", st.TaskID, "error", err)
	}
}

// postVerdict is the GitHub side of CommentVerdict, within its time limit:
// the comment *commentID names edited, else one found by its marker, else
// a new one. *commentID ends as the comment tried.
func (s *Store) postVerdict(ctx context.Context, org string, forges Forges, slug string, number int, taskID string,
	commentID *int64, body string) (posted forge.Posted, edited bool, err error) {
	timeout := s.CommentTimeout
	if timeout == 0 {
		timeout = verdictCommentTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	gh, err := forges.For(ctx, org)
	if err == nil && gh == nil {
		err = fmt.Errorf("no forge credential to post the verdict with")
	}
	if err != nil {
		return posted, false, err
	}
	if *commentID == 0 {
		found, err := gh.FindVerdictComment(ctx, slug, number, taskID)
		if err != nil {
			return posted, false, err
		}
		*commentID = found.CommentID
	}
	if *commentID != 0 {
		posted, err = gh.EditComment(ctx, slug, *commentID, body)
		var refusal *forge.Error
		if !errors.As(err, &refusal) || !refusal.NotFound() {
			return posted, true, err
		}
	}
	posted, err = gh.Comment(ctx, slug, number, body)
	return posted, false, err
}

// RefreshVerification updates only dude's section in the current GitHub body
// of each open pull request dude opened, at most refreshWorkers at once.
// Database reads and ledger writes finish in separate transactions around GitHub.
func (s *Store) RefreshVerification(ctx context.Context, org string, st *State, forges Forges) {
	if st.adoptedPR() || len(st.PullRequestIDs) == 0 {
		return
	}
	type target struct {
		ID, Repo, URL string
		Number        int
	}
	var prs []target
	var section string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT pr.id, r.name, r.url, pr.number
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = ANY($1) AND pr.task_id = $2 AND NOT pr.adopted AND pr.state IN ('open', 'draft')`,
			st.PullRequestIDs, st.TaskID)
		if err != nil {
			return err
		}
		prs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[target])
		if err != nil || len(prs) == 0 {
			return err
		}
		section, _, err = verificationText(ctx, tx, st)
		return err
	})
	if err != nil {
		slog.Warn("reading pull request verification failed", "task", st.TaskID, "error", err)
		return
	}
	if section == "" {
		return
	}
	// One budget for every pull request together, so a task over many
	// repositories ends within it; the ledger writes keep the step's ctx and
	// record a pull request the budget ran out on.
	timeout := s.CommentTimeout
	if timeout == 0 {
		timeout = verdictCommentTimeout
	}
	budget, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	gh, ghErr := forges.For(budget, org)
	if ghErr == nil && gh == nil {
		ghErr = fmt.Errorf("no forge credential to update verification with")
	}
	jobs := make(chan target)
	var wg sync.WaitGroup
	for range min(refreshWorkers, len(prs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pr := range jobs {
				updateErr := ghErr
				if updateErr == nil {
					updateErr = refreshBody(budget, gh, forge.SlugFromURL(pr.URL), pr.Number, section)
				}
				payload := map[string]any{"pullRequestId": pr.ID, "repo": pr.Repo, "number": pr.Number, "head": st.Heads[pr.Repo]}
				if updateErr != nil {
					slog.Warn("updating pull request verification failed", "pr", pr.ID, "error", updateErr)
					payload["error"] = updateErr.Error()
				}
				if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
					return emitTx(ctx, tx, org, st, "pull_request.verification_updated", payload)
				}); err != nil {
					slog.Warn("recording pull request verification update failed", "pr", pr.ID, "error", err)
				}
			}
		}()
	}
	for _, pr := range prs {
		jobs <- pr
	}
	close(jobs)
	wg.Wait()
}

// refreshWorkers bounds how many pull requests' bodies one refresh asks
// GitHub about at once.
const refreshWorkers = 4

// refreshAttempts bounds how many times one refresh merges its section into
// a body a person keeps editing before it leaves it to the next verdict.
const refreshAttempts = 3

var errBodyChanged = errors.New("body changed during update")

// refreshBody writes section into the pull request's current body. GitHub
// has no conditional update for an issue body, so the body is read again
// just before the PATCH and, if a person changed it since, merged again
// from theirs. A person's edit landing between that last read and the PATCH
// itself, about one round-trip, is still overwritten.
func refreshBody(ctx context.Context, gh *forge.GitHub, slug string, number int, section string) error {
	body, err := gh.PullRequestBody(ctx, slug, number)
	if err != nil {
		return err
	}
	for range refreshAttempts {
		merged := replaceVerification(body, section)
		if merged == body {
			return nil
		}
		fresh, err := gh.PullRequestBody(ctx, slug, number)
		if err != nil {
			return err
		}
		if fresh == body {
			return gh.EditPullRequest(ctx, slug, number, merged)
		}
		body = fresh
	}
	return errBodyChanged
}

// The hidden lines around the verification section dude writes into a pull
// request body it opened: what lies between them is dude's, nothing else is.
const (
	verificationStart = "<!-- dude:verification -->"
	verificationEnd   = "<!-- /dude:verification -->"
)

func markedVerification(section string) string {
	return verificationStart + "\n" + section + "\n" + verificationEnd
}

func verificationText(ctx context.Context, tx pgx.Tx, st *State) (string, string, error) {
	v, err := latestVerification(ctx, tx, st)
	if err != nil {
		return "", "", err
	}
	link, err := taskLink(ctx, tx, st.TaskID)
	if err != nil {
		return "", "", err
	}
	return verificationSection(v, st.Heads, link), link, nil
}

// replaceVerification puts section between dude's markers in body, keeping
// every other byte. A marker counts only as a whole line at column 0 and
// outside a fenced code block; any other is text. With
// no marked section it appends one: a `## Verification` without markers is a
// person's, or one dude wrote before it marked its sections, and either way
// is left as it is.
func replaceVerification(body, section string) string {
	nl := "\n"
	if strings.Contains(body, "\r\n") {
		nl = "\r\n"
	}
	marked := strings.ReplaceAll(markedVerification(section), "\n", nl)
	start, fence, offset := -1, "", 0
	for line := range strings.SplitAfterSeq(body, "\n") {
		content := strings.TrimRight(line, "\r\n")
		var code bool
		fence, code = fenceAfter(fence, content)
		// An indented marker is a person's, e.g. in a list item's fenced
		// example, whose fence fenceAfter does not see.
		switch content {
		case verificationStart:
			if !code {
				start = offset
			}
		case verificationEnd:
			if !code && start >= 0 {
				return body[:start] + marked + body[offset+len(content):]
			}
		}
		offset += len(line)
	}
	if fence != "" {
		// A fence the person left open would swallow the section.
		if !strings.HasSuffix(body, nl) {
			body += nl
		}
		body += fence
	}
	separator := nl + nl
	switch {
	case body == "" || strings.HasSuffix(body, nl+nl):
		separator = ""
	case strings.HasSuffix(body, nl):
		separator = nl
	}
	return body + separator + marked
}

// verificationSection is the pull request's account of the test: each
// criterion's status, any waiver or acceptance and its reason, the heads
// it tested and whether the branch has moved since. "" when no test ran.
func verificationSection(v *prVerification, heads map[string]string, taskLink string) string {
	if v == nil || len(v.Criteria) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Verification\n")
	var at []string
	for _, repo := range slices.Sorted(maps.Keys(v.Heads)) {
		head := fmt.Sprintf("`%s`", short(v.Heads[repo]))
		if len(v.Heads) > 1 {
			head = repo + " " + head
		}
		at = append(at, head)
	}
	if len(at) > 0 {
		fmt.Fprintf(&b, "Tested at %s.", strings.Join(at, ", "))
	}
	if stale(v.Heads, heads) {
		b.WriteString(" **Stale:** the branch has changed since; this verdict is about the commits above.")
	}
	b.WriteString("\n\n")
	for _, c := range v.Criteria {
		status := c.Status
		if c.Decision != "" {
			status = fmt.Sprintf("%s, %s by %s: %s", c.Status, c.Decision, valueOr(c.DecidedBy, "a person"), oneLine(c.Note))
		}
		fmt.Fprintf(&b, "- **%s** — %s\n", status, oneLine(CriteriaImagesAsText([]string{c.Text})[0]))
	}
	b.WriteString("\nThe evidence (video, screenshots, `acceptance.md`) is on the task in dude")
	if taskLink != "" {
		b.WriteString(": " + taskLink)
	} else {
		b.WriteString(".")
	}
	return strings.TrimRight(b.String(), "\n")
}

// stale says the work has moved past the heads a verdict covers: a
// repository tested at another commit, or changed and not tested at all.
func stale(tested, now map[string]string) bool {
	for repo, sha := range now {
		if tested[repo] != sha {
			return true
		}
	}
	return false
}

// prVerification is the latest test of the attempt, for the pull request.
type prVerification struct {
	Heads    map[string]string
	Criteria []prCriterion
}

type prCriterion struct {
	Text, Status, Decision, DecidedBy, Note string
}

// latestVerification reads the attempt's latest recorded verdict.
func latestVerification(ctx context.Context, tx pgx.Tx, st *State) (*prVerification, error) {
	rows, err := tx.Query(ctx, `SELECT v.text, v.status, COALESCE(v.decision, ''), COALESCE(p.name, ''),
			COALESCE(v.decision_note, ''), v.heads
		FROM acceptance_verdicts v LEFT JOIN people p ON p.id = v.decided_by
		WHERE v.run_id = (SELECT r.id FROM runs r JOIN acceptance_verdicts av ON av.run_id = r.id
			WHERE r.task_id = $1 AND ($2 = 0 OR r.attempt = $2) ORDER BY av.created_at DESC, r.created_at DESC LIMIT 1)
		ORDER BY v.criterion`, st.TaskID, st.Attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var v *prVerification
	for rows.Next() {
		var c prCriterion
		var heads []byte
		if err := rows.Scan(&c.Text, &c.Status, &c.Decision, &c.DecidedBy, &c.Note, &heads); err != nil {
			return nil, err
		}
		if v == nil {
			v = &prVerification{}
			_ = json.Unmarshal(heads, &v.Heads)
		}
		v.Criteria = append(v.Criteria, c)
	}
	return v, rows.Err()
}
