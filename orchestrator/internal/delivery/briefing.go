package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The conductor's briefing: what dude tells a task's conductor when someone
// first writes in the task's Chat. Written from what dude already has — the
// task, every Run's outcome, the findings and how each was settled, the
// pull requests — and bounded: ids, counts and one line per thing, capped.
// It never carries a diff, a file list or a finding's text; the conductor
// reads those with its tools (run_diff, findings, pull_requests).
const (
	briefGoalChars       = 4000
	briefCriteria        = 20
	briefCriterionChars  = 300
	briefRunsMax         = 20
	briefRunLineChars    = 160
	briefFindingsMax     = 20
	briefPullRequestsMax = 10
)

// RepoHead is where a task stands in one repository: the commit its
// conductor starts from, "" for the default branch.
type RepoHead struct {
	Repo, SHA, Branch string
}

// TaskHeads is the task's head in each repository it names: its pull
// request's head commit, else the last commit dude published for it — a
// phase's, or its conductor's; a repository with neither has none ("",
// the default branch).
func TaskHeads(ctx context.Context, tx pgx.Tx, taskID string) ([]RepoHead, error) {
	rows, err := tx.Query(ctx, `
		SELECT repo.name,
		  COALESCE(
		    (SELECT pr.head_sha FROM pull_requests pr WHERE pr.task_id = $1 AND pr.repository_id = repo.id
		       AND pr.head_sha IS NOT NULL ORDER BY pr.updated_at DESC LIMIT 1),
		    (SELECT h.sha FROM (
		       SELECT r.heads->repo.name->>'sha' AS sha, r.ended_at AS at FROM runs r WHERE r.task_id = $1 AND r.heads ? repo.name
		       UNION ALL
		       SELECT p.heads->repo.name->>'sha', p.settled_at FROM conductor_publishes p
		         WHERE p.task_id = $1 AND p.status = 'published' AND p.heads ? repo.name) h
		     ORDER BY h.at DESC NULLS LAST LIMIT 1),
		    ''),
		  -- The task branch: a pull request's candidate fix (CandidateBranch) is not on it yet.
		  COALESCE((SELECT r.branch FROM runs r WHERE r.task_id = $1 AND r.heads ? repo.name AND r.branch IS NOT NULL
		       AND `+NotGeneratedCandidateSQL("r")+` ORDER BY r.ended_at DESC NULLS LAST LIMIT 1), '')
		FROM task_repositories tr JOIN repositories repo ON repo.id = tr.repository_id
		WHERE tr.task_id = $1 ORDER BY repo.name`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[RepoHead])
}

// Briefing writes the conductor's first prompt: dude's note on the task,
// then the person's message.
func Briefing(ctx context.Context, tx pgx.Tx, taskID, conductorRunID, person, message string) (string, error) {
	return briefing(ctx, tx, taskID, conductorRunID, person, message, byPerson)
}

// Why a conductor is briefed, which sets the briefing's opening line.
type briefedFor int

const (
	byPerson briefedFor = iota // a person wrote in the Chat
	byWake                     // dude woke it with a note
	rebrief                    // its harness lost the conversation (Rebriefing)
)

// lostConversation is dude's message in a talker's briefing again, after
// its harness restarted without the conversation (Rebriefing).
var lostConversation = render("briefings/lost-conversation", nil)

// Rebriefing is the briefing of a talker whose harness restarted without
// its conversation (lux started a blank session on resume), in dude's own
// words: its briefing as it would be written now, saying the conversation
// is lost, and lux's reason when it gave one, with the conversation so far
// (Replay). A conductor gets the task's (briefing, opened as a briefing
// again), a session's agent the session's (sessionAbout) under a heading of
// dude's before it. The caller wraps it as the first prompt was
// (instructions, checkouts, tools).
func Rebriefing(ctx context.Context, tx pgx.Tx, ref RunRef, reason string) (string, error) {
	message := lostConversation
	if reason != "" {
		message += "\n\n" + render("briefings/lost-conversation-reason", reason)
	}
	if ref.SessionID != "" {
		about, err := sessionAbout(ctx, tx, ref.SessionID)
		if err != nil {
			return "", err
		}
		replay, err := Replay(ctx, tx, Talker{SessionID: ref.SessionID})
		if err != nil {
			return "", err
		}
		if replay != "" {
			about += "\n\n" + replay
		}
		return render("briefings/rebrief-session", message+"\n\n"+about), nil
	}
	return briefing(ctx, tx, ref.TaskID, ref.RunID, "dude", message, rebrief)
}

// briefing is the conductor's briefing; message is from person, or from
// dude when it wakes the conductor or briefs it again.
func briefing(ctx context.Context, tx pgx.Tx, taskID, conductorRunID, person, message string, why briefedFor) (string, error) {
	var key, title, goal, status string
	var criteria json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number, t.title, t.goal, t.status::text, t.acceptance_criteria
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).
		Scan(&key, &title, &goal, &status, &criteria); err != nil {
		return "", fmt.Errorf("briefing: the task: %w", err)
	}
	b := conductorBrief{Key: key, TaskID: taskID, Title: oneLine(title), Status: status, Goal: briefGoal(goal),
		Who: person, Message: message, Why: map[briefedFor]string{byPerson: "person", byWake: "wake", rebrief: "rebrief"}[why]}
	if b.Who == "" {
		b.Who = "Someone"
	}
	if why != byPerson {
		b.Who = "dude"
	}
	var ac []string
	_ = json.Unmarshal(criteria, &ac)
	for i, c := range ac {
		if i == briefCriteria {
			b.MoreCriteria = len(ac) - briefCriteria
			break
		}
		b.Criteria = append(b.Criteria, clipTaskText(oneLine(c), briefCriterionChars))
	}
	var err error
	if b.Runs, err = briefRuns(ctx, tx, taskID, conductorRunID); err != nil {
		return "", err
	}
	if b.Findings, err = briefFindings(ctx, tx, taskID); err != nil {
		return "", err
	}
	// The epic it is on, and what its peers did since it last looked:
	// told here, never a wake of its own.
	var org string
	if err := tx.QueryRow(ctx, `SELECT organization_id FROM tasks WHERE id = $1`, taskID).Scan(&org); err != nil {
		return "", err
	}
	if b.Digest, err = ConductorDigestTx(ctx, tx, org, taskID, conductorRunID, "briefing"); err != nil {
		return "", err
	}
	if b.PullRequests, err = briefPullRequests(ctx, tx, taskID); err != nil {
		return "", err
	}
	if b.Heads, err = TaskHeads(ctx, tx, taskID); err != nil {
		return "", err
	}
	if err := briefDecider(ctx, tx, &b, taskID); err != nil {
		return "", err
	}
	if b.Epic, err = EpicBriefing(ctx, tx, org, taskID, "", RoleConductor); err != nil {
		return "", err
	}
	// What the task's conductors were told and said before: none before
	// its first. A message handed over is not in it (never read): it is
	// this briefing's message.
	if b.Replay, err = Replay(ctx, tx, Talker{TaskID: taskID}); err != nil {
		return "", err
	}
	return render("briefings/conductor", b), nil
}

// conductorBrief is what briefings/conductor.md is written from.
type conductorBrief struct {
	Why, Who, Message          string
	Key, TaskID, Title, Status string
	Goal                       string
	Criteria                   []string
	MoreCriteria               int
	Runs, Findings             briefLines
	Digest, Epic, Replay       string
	PullRequests               []briefPullRequest
	Heads                      []RepoHead
	Decider, Waiting, Step     string
}

// briefLines is a bounded list: its total, and the lines shown.
type briefLines struct {
	Total    int
	ByStatus string
	Lines    []string
}

type briefPullRequest struct {
	Repo                        string
	Number                      int
	State, Head, Checks, Review string
	Feedback                    int
}

// briefDecider says who takes the task's decisions, and the one its
// delivery waits on.
func briefDecider(ctx context.Context, tx pgx.Tx, b *conductorBrief, taskID string) error {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return err
	}
	switch {
	case d == nil || !d.Live() || Ended(d.TaskStatus):
		b.Decider = "none"
	case !d.State.conducted():
		b.Decider = "deliver"
	case d.State.Decision != nil && d.State.Decision.Taken == nil:
		b.Decider, b.Waiting = "conductor", pointLabel[d.State.Decision.Point]
	default:
		b.Decider, b.Step = "conductor", d.Step
	}
	return nil
}

func briefRuns(ctx context.Context, tx pgx.Tx, taskID, self string) (briefLines, error) {
	var out briefLines
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND id <> $2`,
		taskID, self).Scan(&out.Total); err != nil {
		return out, err
	}
	// The newest, then shown oldest first; each with its outcome in a line:
	// its error, or the first line of its last message.
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(phase::text, ''), COALESCE(role::text, ''), COALESCE(category, ''),
			status::text, COALESCE(error, ''),
			COALESCE((SELECT e.payload->>'text' FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.message'
				ORDER BY e.cursor DESC LIMIT 1), '')
		FROM (SELECT * FROM runs WHERE task_id = $1 AND kind = 'agent' AND id <> $2 ORDER BY created_at DESC LIMIT $3) r
		ORDER BY created_at`, taskID, self, briefRunsMax)
	if err != nil {
		return out, err
	}
	type run struct{ ID, Phase, Role, Category, Status, Error, Said string }
	runs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[run])
	if err != nil {
		return out, err
	}
	for _, r := range runs {
		what := r.Phase
		if r.Category != "" {
			what += " · " + r.Category
		}
		outcome := r.Error
		if outcome == "" {
			outcome = firstLine(r.Said)
		}
		line := render("briefings/run-line", struct{ ID, What, Role, Status, Outcome string }{r.ID, what, r.Role, r.Status, outcome})
		out.Lines = append(out.Lines, clip(line, briefRunLineChars))
	}
	return out, nil
}

func briefFindings(ctx context.Context, tx pgx.Tx, taskID string) (briefLines, error) {
	var out briefLines
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(n), 0)::int, COALESCE(string_agg(n || ' ' || status, ', ' ORDER BY status), '')
		FROM (SELECT status::text, count(*) AS n FROM review_findings WHERE task_id = $1 GROUP BY status) s`,
		taskID).Scan(&out.Total, &out.ByStatus); err != nil {
		return out, err
	}
	if out.Total == 0 {
		return out, nil
	}
	// Open first, most severe first: what a person most likely asks about.
	rows, err := tx.Query(ctx, `SELECT id, severity::text, category, COALESCE(file, ''), COALESCE(line, 0), status::text,
			COALESCE(resolved_by_run_id, ''), fix_attempts, COALESCE(resolution_note, '')
		FROM review_findings WHERE task_id = $1
		ORDER BY status <> 'open', array_position(ARRAY['blocking','high','medium','low','note'], severity::text), created_at
		LIMIT $2`, taskID, briefFindingsMax)
	if err != nil {
		return out, err
	}
	type finding struct {
		ID, Severity, Category, File string
		Line                         int
		Status, ResolvedBy           string
		Attempts                     int
		Note                         string
	}
	fs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[finding])
	if err != nil {
		return out, err
	}
	for _, f := range fs {
		where := f.File
		if where != "" && f.Line > 0 {
			where = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		out.Lines = append(out.Lines, render("briefings/finding-line", struct{ ID, Severity, Category, Where, Settled string }{
			f.ID, f.Severity, f.Category, where, settled(f.Status, f.ResolvedBy, f.Attempts, f.Note)}))
	}
	return out, nil
}

// settled says how a finding stands, in a few words.
func settled(status, by string, attempts int, note string) string {
	if status == "accepted" {
		return AcceptedBy(note)
	}
	return render("briefings/finding-settled", struct {
		Status, By string
		Attempts   int
	}{status, by, attempts})
}

func briefPullRequests(ctx context.Context, tx pgx.Tx, taskID string) ([]briefPullRequest, error) {
	rows, err := tx.Query(ctx, `SELECT repo.name, pr.number, pr.state::text, COALESCE(pr.head_sha, ''), pr.checks::text, pr.review::text,
			(SELECT count(*) FROM events e WHERE e.task_id = pr.task_id AND e.event_type = 'pull_request.commented'
				AND (e.payload->>'number')::int = pr.number AND e.payload->>'repo' = repo.name)
		FROM pull_requests pr JOIN repositories repo ON repo.id = pr.repository_id
		WHERE pr.task_id = $1 ORDER BY pr.created_at LIMIT $2`, taskID, briefPullRequestsMax)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[briefPullRequest])
}

// clip cuts s to n characters, saying so.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// clipTaskText is clip for a task's goal or criterion, which
// ConductorPrompt reads image references in: a cut never splits a
// reference, and never makes one out of a code span whose closer it cut
// off; either way it cuts before that reference instead.
func clipTaskText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := len(string([]rune(s)[:n-1]))
	whole := map[int]bool{}
	for _, r := range ImageRefs(s) {
		whole[r.From] = true
		if r.From < cut && cut < r.To {
			cut = r.From
		}
	}
	for _, r := range ImageRefs(s[:cut]) {
		if !whole[r.From] {
			cut = r.From
			break
		}
	}
	return s[:cut] + "…"
}

// briefGoal is the task's goal as the briefing shows it: clipped, with a
// fence it leaves open closed.
func briefGoal(goal string) string {
	return closeFence(clipTaskText(strings.TrimSpace(goal), briefGoalChars))
}

// closeFence closes a fence the goal leaves open, cut or not, so the
// briefing after it is not read as code. Only the goal keeps its lines: a
// criterion is one line after "- ", which never opens a fence.
func closeFence(s string) string {
	if f := UnclosedFence(s); f != "" {
		return s + "\n" + f
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return "—"
	}
	return sha
}
