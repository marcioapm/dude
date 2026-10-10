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
	briefGoalChars      = 4000
	briefCriteria       = 20
	briefCriterionChars = 300
	briefRuns           = 20
	briefRunLineChars   = 160
	briefFindings       = 20
	briefPullRequests   = 10
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
		  COALESCE((SELECT r.branch FROM runs r WHERE r.task_id = $1 AND r.heads ? repo.name AND r.branch IS NOT NULL
		       ORDER BY r.ended_at DESC NULLS LAST LIMIT 1), '')
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
	return briefing(ctx, tx, taskID, conductorRunID, person, message, false)
}

// lostConversation is dude's message in a talker's briefing again, after
// its harness restarted without the conversation (Rebriefing).
const lostConversation = "Your agent session restarted without its earlier conversation: what was said before this " +
	"is lost to you. This is your briefing again, as things stand now. Read again with your tools what you need, " +
	"and answer the messages that follow, or that just came before this one, as if they were the first."

// Rebriefing is the briefing of a talker whose harness restarted without
// its conversation (lux started a blank session on resume), in dude's own
// words: its briefing as it would be written now, saying the conversation
// is lost, and lux's reason when it gave one. A conductor gets the task's
// (briefing, as dude waking it), a session's agent the session's
// (sessionAbout) under a heading of dude's before it. The caller wraps it
// as the first prompt was (instructions, checkouts, tools).
func Rebriefing(ctx context.Context, tx pgx.Tx, ref RunRef, reason string) (string, error) {
	message := lostConversation
	if reason != "" {
		message += "\n\n(lux: " + oneLine(reason) + ")"
	}
	// PR B: the conversation replayed from the ledger joins the briefing here.
	if ref.SessionID != "" {
		about, err := sessionAbout(ctx, tx, ref.SessionID)
		if err != nil {
			return "", err
		}
		return "## Your earlier conversation is lost\n\n" + message + "\n\n" + about, nil
	}
	return briefing(ctx, tx, ref.TaskID, ref.RunID, "dude", message, true)
}

// briefing is Briefing, or with woken, a conductor dude starts to hear a
// wake note (message) rather than a person.
func briefing(ctx context.Context, tx pgx.Tx, taskID, conductorRunID, person, message string, woken bool) (string, error) {
	var key, title, goal, status string
	var criteria json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number, t.title, t.goal, t.status::text, t.acceptance_criteria
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).
		Scan(&key, &title, &goal, &status, &criteria); err != nil {
		return "", fmt.Errorf("briefing: the task: %w", err)
	}
	var b strings.Builder
	who := person
	if who == "" {
		who = "Someone"
	}
	if woken {
		who = "dude"
		fmt.Fprintf(&b, "Conductor, dude woke you about %s, %q: a decision is yours. You are this task's conductor. "+
			"Below is what dude knows about the task, in short; read more with your tools.", key, oneLine(title))
	} else {
		fmt.Fprintf(&b, "Conductor, %s wrote in the Chat of %s, %q. You are this task's conductor: answer them. "+
			"Below is what dude knows about the task, in short; read more with your tools.", who, key, oneLine(title))
	}
	fmt.Fprintf(&b, "\n\n## The task\n\n%s · %s · status %s", key, taskID, status)
	fmt.Fprintf(&b, "\n\nGoal:\n\n%s", briefGoal(goal))
	var ac []string
	_ = json.Unmarshal(criteria, &ac)
	if len(ac) > 0 {
		b.WriteString("\n\nAcceptance criteria:")
		for i, c := range ac {
			if i == briefCriteria {
				fmt.Fprintf(&b, "\n- … and %d more", len(ac)-briefCriteria)
				break
			}
			fmt.Fprintf(&b, "\n- %s", clipTaskText(oneLine(c), briefCriterionChars))
		}
	}

	if err := briefRunsSection(ctx, tx, &b, taskID, conductorRunID); err != nil {
		return "", err
	}
	if err := briefFindingsSection(ctx, tx, &b, taskID); err != nil {
		return "", err
	}
	if err := briefPullRequestsSection(ctx, tx, &b, taskID); err != nil {
		return "", err
	}
	heads, err := TaskHeads(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	b.WriteString("\n\n## Head\n")
	if len(heads) == 0 {
		b.WriteString("\nThe task names no repository: it changes no code.")
	}
	for _, h := range heads {
		switch {
		case h.SHA == "":
			fmt.Fprintf(&b, "\n- %s: the default branch (nothing published yet)", h.Repo)
		case h.Branch != "":
			fmt.Fprintf(&b, "\n- %s: %s on %s", h.Repo, short(h.SHA), h.Branch)
		default:
			fmt.Fprintf(&b, "\n- %s: %s", h.Repo, short(h.SHA))
		}
	}
	b.WriteString("\n\nYour checkout is at that head.")
	if err := briefDeciderSection(ctx, tx, &b, taskID); err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "\n\n## %s's message\n\n%s", who, message)
	return b.String(), nil
}

// briefDeciderSection says who takes the task's decisions, and the one
// its delivery waits on.
func briefDeciderSection(ctx context.Context, tx pgx.Tx, b *strings.Builder, taskID string) error {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return err
	}
	b.WriteString("\n\n## Decisions\n\n")
	switch {
	case d == nil || !d.Live() || Ended(d.TaskStatus):
		b.WriteString("No delivery is in progress: you are read-only.")
	case !d.State.conducted():
		b.WriteString("Deliver takes this task's decisions: you are read-only.")
	case d.State.Decision != nil && d.State.Decision.Taken == nil:
		fmt.Fprintf(b, "You take this task's decisions. The delivery waits on one now: %s.", pointLabel[d.State.Decision.Point])
	default:
		fmt.Fprintf(b, "You take this task's decisions. The delivery is at %s; you are woken at its next decision.", d.Step)
	}
	return nil
}

func briefRunsSection(ctx context.Context, tx pgx.Tx, b *strings.Builder, taskID, self string) error {
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND id <> $2`,
		taskID, self).Scan(&total); err != nil {
		return err
	}
	// The newest, then shown oldest first; each with its outcome in a line:
	// its error, or the first line of its last message.
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(phase::text, ''), COALESCE(role::text, ''), COALESCE(category, ''),
			status::text, COALESCE(error, ''),
			COALESCE((SELECT e.payload->>'text' FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.message'
				ORDER BY e.cursor DESC LIMIT 1), '')
		FROM (SELECT * FROM runs WHERE task_id = $1 AND kind = 'agent' AND id <> $2 ORDER BY created_at DESC LIMIT $3) r
		ORDER BY created_at`, taskID, self, briefRuns)
	if err != nil {
		return err
	}
	type run struct{ ID, Phase, Role, Category, Status, Error, Said string }
	runs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[run])
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "\n\n## Runs (%d)\n", total)
	if total == 0 {
		b.WriteString("\nNone yet: the task has not been delivered.")
		return nil
	}
	if total > len(runs) {
		fmt.Fprintf(b, "\nThe latest %d; the rest are older.", len(runs))
	}
	for _, r := range runs {
		what := r.Phase
		if r.Category != "" {
			what += " · " + r.Category
		}
		if what == "" {
			what = "agent"
		}
		outcome := r.Error
		if outcome == "" {
			outcome = firstLine(r.Said)
		}
		line := fmt.Sprintf("%s · %s · %s · %s", r.ID, what, r.Role, r.Status)
		if outcome != "" {
			line += " · " + outcome
		}
		fmt.Fprintf(b, "\n- %s", clip(line, briefRunLineChars))
	}
	return nil
}

func briefFindingsSection(ctx context.Context, tx pgx.Tx, b *strings.Builder, taskID string) error {
	var total int
	var byStatus string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(n), 0)::int, COALESCE(string_agg(n || ' ' || status, ', ' ORDER BY status), '')
		FROM (SELECT status::text, count(*) AS n FROM review_findings WHERE task_id = $1 GROUP BY status) s`,
		taskID).Scan(&total, &byStatus); err != nil {
		return err
	}
	fmt.Fprintf(b, "\n\n## Findings (%d", total)
	if byStatus != "" {
		fmt.Fprintf(b, ": %s", byStatus)
	}
	b.WriteString(")\n")
	if total == 0 {
		b.WriteString("\nNone.")
		return nil
	}
	// Open first, most severe first: what a person most likely asks about.
	rows, err := tx.Query(ctx, `SELECT id, severity::text, category, COALESCE(file, ''), COALESCE(line, 0), status::text,
			COALESCE(resolved_by_run_id, ''), fix_attempts, COALESCE(resolution_note, '')
		FROM review_findings WHERE task_id = $1
		ORDER BY status <> 'open', array_position(ARRAY['blocking','high','medium','low','note'], severity::text), created_at
		LIMIT $2`, taskID, briefFindings)
	if err != nil {
		return err
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
		return err
	}
	if total > len(fs) {
		fmt.Fprintf(b, "\nThe first %d, open and most severe first.", len(fs))
	}
	for _, f := range fs {
		where := f.File
		if where != "" && f.Line > 0 {
			where = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		line := fmt.Sprintf("%s · %s · %s", f.ID, f.Severity, f.Category)
		if where != "" {
			line += " · " + clip(where, 80)
		}
		line += " · " + settled(f.Status, f.ResolvedBy, f.Attempts, f.Note)
		fmt.Fprintf(b, "\n- %s", line)
	}
	return nil
}

// settled says how a finding stands, in a few words.
func settled(status, by string, attempts int, note string) string {
	switch status {
	case "resolved":
		if by != "" {
			return "fixed, by " + by
		}
		return "fixed"
	case "accepted":
		return AcceptedBy(note)
	case "superseded":
		return "superseded"
	}
	if attempts > 0 {
		return fmt.Sprintf("open, %d fix attempts", attempts)
	}
	return "open"
}

func briefPullRequestsSection(ctx context.Context, tx pgx.Tx, b *strings.Builder, taskID string) error {
	rows, err := tx.Query(ctx, `SELECT repo.name, pr.number, pr.state::text, COALESCE(pr.head_sha, ''), pr.checks::text, pr.review::text,
			(SELECT count(*) FROM events e WHERE e.task_id = pr.task_id AND e.event_type = 'pull_request.commented'
				AND (e.payload->>'number')::int = pr.number AND e.payload->>'repo' = repo.name)
		FROM pull_requests pr JOIN repositories repo ON repo.id = pr.repository_id
		WHERE pr.task_id = $1 ORDER BY pr.created_at LIMIT $2`, taskID, briefPullRequests)
	if err != nil {
		return err
	}
	type pr struct {
		Repo                        string
		Number                      int
		State, Head, Checks, Review string
		Feedback                    int
	}
	prs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pr])
	if err != nil {
		return err
	}
	b.WriteString("\n\n## Pull requests\n")
	if len(prs) == 0 {
		b.WriteString("\nNone.")
	}
	for _, p := range prs {
		fmt.Fprintf(b, "\n- %s#%d · %s · head %s · checks %s · review %s · %d feedback items",
			p.Repo, p.Number, p.State, short(p.Head), p.Checks, p.Review, p.Feedback)
	}
	return nil
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
