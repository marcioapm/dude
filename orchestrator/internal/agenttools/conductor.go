package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The conductor's read tools: what the delivery workflow recorded about the
// caller's task, read the way the briefing names it — ids, one line each —
// with the text only when asked for by id.
var conductors = []string{"conductor"}

const (
	// Findings listed per call; a task with more says so (total).
	findingsMax = 200
	// Findings whose text one call returns.
	findingTextMax = 20
	// Feedback items per pull request, newest last, and characters of each.
	feedbackMax     = 50
	feedbackExcerpt = 280
)

// ---- findings ------------------------------------------------------------------

type findingsIn struct {
	IDs []string `json:"ids,omitempty" jsonschema:"findings to read in full (fnd_…), with their text; empty lists the task's findings without it"`
}

type findingOut struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Category string `json:"category"`
	Repo     string `json:"repo,omitempty"`
	// file:line, or the file alone.
	Where  string `json:"where,omitempty"`
	Status string `json:"status"`
	// How it was settled: fixed by a Run, accepted by a person, dismissed
	// by the conductor (with its reason), or still open after so many fix
	// attempts.
	Settled    string `json:"settled"`
	RaisedBy   string `json:"raisedBy,omitempty"`
	ResolvedBy string `json:"resolvedBy,omitempty"`
	// With ids only.
	Title          string `json:"title,omitempty"`
	Description    string `json:"description,omitempty"`
	SuggestedFix   string `json:"suggestedFix,omitempty"`
	ResolutionNote string `json:"resolutionNote,omitempty"`
}

type findingsOut struct {
	Total    int          `json:"total"`
	Findings []findingOut `json:"findings"`
	// Asked for by id and not a finding of this task.
	Unknown []string `json:"unknown,omitempty"`
}

func findings(ctx context.Context, tx pgx.Tx, c Caller, in findingsIn) (any, error) {
	ids := uniquePaths(in.IDs)
	if len(ids) > findingTextMax {
		return nil, refuse("at most %d findings by id at once", findingTextMax)
	}
	out := findingsOut{Findings: []findingOut{}}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM review_findings WHERE task_id = $1`, c.TaskID).Scan(&out.Total); err != nil {
		return nil, err
	}
	full := len(ids) > 0
	// The text only when asked for by id: a list of up to findingsMax reads
	// none of it.
	rows, err := tx.Query(ctx, `SELECT id, severity::text, category, COALESCE(repo, ''), COALESCE(file, ''), COALESCE(line, 0),
			status::text, fix_attempts, COALESCE(run_id, ''), COALESCE(resolved_by_run_id, ''), COALESCE(resolution_note, ''),
			CASE WHEN $2 THEN title ELSE '' END, CASE WHEN $2 THEN description ELSE '' END,
			CASE WHEN $2 THEN suggested_fix ELSE '' END
		FROM review_findings WHERE task_id = $1 AND (NOT $2 OR id = ANY ($3))
		ORDER BY status <> 'open', array_position(ARRAY['blocking','high','medium','low','note'], severity::text), created_at
		LIMIT $4`, c.TaskID, full, ids, findingsMax)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var f findingOut
		var file, note string
		var line, attempts int
		if err := rows.Scan(&f.ID, &f.Severity, &f.Category, &f.Repo, &file, &line, &f.Status, &attempts, &f.RaisedBy, &f.ResolvedBy,
			&note, &f.Title, &f.Description, &f.SuggestedFix); err != nil {
			return nil, err
		}
		if full {
			f.ResolutionNote = note
		}
		f.Where = file
		if file != "" && line > 0 {
			f.Where = fmt.Sprintf("%s:%d", file, line)
		}
		f.Settled = settledAs(f.Status, f.ResolvedBy, attempts, note)
		seen[f.ID] = true
		out.Findings = append(out.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !seen[id] {
			out.Unknown = append(out.Unknown, id)
		}
	}
	return out, nil
}

func settledAs(status, by string, attempts int, note string) string {
	switch status {
	case "resolved":
		if by != "" {
			return "fixed by " + by
		}
		return "fixed"
	case "accepted":
		return delivery.AcceptedBy(note)
	case "superseded":
		return "superseded: the code it described is gone"
	}
	if attempts > 0 {
		return fmt.Sprintf("open after %d fix attempts", attempts)
	}
	return "open"
}

// ---- pull_requests ----------------------------------------------------------

type pullRequestsIn struct{}

type checkOut struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}

type feedbackOut struct {
	Author string `json:"author"`
	// comment, line_comment, review, changes_requested.
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Excerpt string `json:"excerpt"`
	At      string `json:"at"`
	// A fixer Run was sent it, by id; "" for none.
	ActedOnBy string `json:"actedOnBy,omitempty"`
	// Why it woke nobody: its author may not ("not_permitted").
	Ignored string `json:"ignored,omitempty"`
}

type pullRequestOut struct {
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	State      string `json:"state"`
	HeadBranch string `json:"headBranch"`
	Head       string `json:"head"`
	BaseBranch string `json:"baseBranch"`
	// passing, failing, pending or unknown, then each check.
	Checks       string     `json:"checks"`
	CheckDetails []checkOut `json:"checkDetails"`
	// approved, changes_requested or pending, then each reviewer's word.
	Review            string            `json:"review"`
	Reviews           []json.RawMessage `json:"reviews"`
	UnresolvedThreads int               `json:"unresolvedThreads"`
	FeedbackTotal     int               `json:"feedbackTotal"`
	Feedback          []feedbackOut     `json:"feedback"`
}

func pullRequests(ctx context.Context, tx pgx.Tx, c Caller, _ pullRequestsIn) (any, error) {
	rows, err := tx.Query(ctx, `SELECT repo.name, pr.number, pr.url, pr.state::text, pr.head_branch, COALESCE(pr.head_sha, ''),
			pr.base_branch, pr.checks::text, COALESCE(pr.checks_json, '[]'), pr.review::text, COALESCE(pr.reviews_json, '[]'),
			COALESCE(pr.unresolved_threads, 0)
		FROM pull_requests pr JOIN repositories repo ON repo.id = pr.repository_id
		WHERE pr.task_id = $1 ORDER BY pr.created_at`, c.TaskID)
	if err != nil {
		return nil, err
	}
	type row struct {
		pullRequestOut
		checks, reviews json.RawMessage
	}
	var prs []row
	for rows.Next() {
		var p row
		if err := rows.Scan(&p.Repo, &p.Number, &p.URL, &p.State, &p.HeadBranch, &p.Head, &p.BaseBranch, &p.Checks, &p.checks,
			&p.Review, &p.reviews, &p.UnresolvedThreads); err != nil {
			return nil, err
		}
		prs = append(prs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []pullRequestOut{}
	for _, p := range prs {
		pr := p.pullRequestOut
		pr.CheckDetails, pr.Reviews, pr.Feedback = []checkOut{}, []json.RawMessage{}, []feedbackOut{}
		_ = json.Unmarshal(p.checks, &pr.CheckDetails)
		_ = json.Unmarshal(p.reviews, &pr.Reviews)
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
			AND (payload->>'number')::int = $2 AND payload->>'repo' = $3`, c.TaskID, pr.Number, pr.Repo).Scan(&pr.FeedbackTotal); err != nil {
			return nil, err
		}
		// The latest feedbackMax, oldest first. Acted on: a Run of this task
		// was sent the same author's words about the same repository and
		// path. runs.pr_feedback keeps what woke each fixer, not the
		// comment's id, so this is the closest match there is; a fix sent
		// before repo was recorded (a one-repository task) matches by path.
		frows, err := tx.Query(ctx, `SELECT COALESCE(e.payload->>'author', ''), COALESCE(e.payload->>'kind', ''),
				COALESCE(e.payload->>'path', ''), COALESCE(e.payload->>'body', ''), e.occurred_at::text,
				COALESCE((SELECT r.id FROM runs r, jsonb_array_elements(COALESCE(r.pr_feedback, '[]')) f
					WHERE r.task_id = e.task_id AND f->>'source' = 'review' AND f->>'author' = e.payload->>'author'
					  AND f->>'body' = e.payload->>'body'
					  AND COALESCE(f->>'repo', $3) = $3
					  AND COALESCE(f->>'path', '') = COALESCE(e.payload->>'path', '')
					ORDER BY r.created_at LIMIT 1), ''),
				COALESCE(e.payload->>'ignored', '')
			FROM (SELECT * FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
				AND (payload->>'number')::int = $2 AND payload->>'repo' = $3 ORDER BY cursor DESC LIMIT $4) e
			ORDER BY e.cursor`, c.TaskID, pr.Number, pr.Repo, feedbackMax)
		if err != nil {
			return nil, err
		}
		for frows.Next() {
			var f feedbackOut
			var body string
			if err := frows.Scan(&f.Author, &f.Kind, &f.Path, &body, &f.At, &f.ActedOnBy, &f.Ignored); err != nil {
				frows.Close()
				return nil, err
			}
			f.Excerpt = excerpt(body, feedbackExcerpt)
			pr.Feedback = append(pr.Feedback, f)
		}
		if err := frows.Err(); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return map[string]any{"pullRequests": out}, nil
}

// excerpt is s on one line, cut to n characters.
func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- the conductor's decisions ----------------------------------------------

// conducted maps a refusal of the delivery's to the tools' own.
func conducted[T any](out T, err error) (T, error) {
	var r delivery.Refusal
	if errors.As(err, &r) {
		return out, refuse("%s", r.Msg)
	}
	return out, err
}

type startPhaseIn struct {
	Phase      string   `json:"phase" jsonschema:"implement, review, fix, simplify or test"`
	Categories []string `json:"categories,omitempty" jsonschema:"review: the reviewers to run (correctness, security, database, api, frontend, performance); none runs those the change warrants"`
	Findings   []string `json:"findings,omitempty" jsonschema:"fix: the open findings to fix (fnd_…); none fixes every open one"`
	Note       string   `json:"note,omitempty" jsonschema:"what you ask of the Run, added to its prompt"`
}

func startPhase(ctx context.Context, tx pgx.Tx, c Caller, in startPhaseIn) (map[string]any, error) {
	next, err := delivery.ConductStartPhase(ctx, tx, c.run(), delivery.StartPhase{Phase: strings.TrimSpace(in.Phase),
		Categories: in.Categories, FindingIDs: in.Findings, Note: in.Note})
	return conducted(map[string]any{"started": in.Phase, "next": next}, err)
}

type decideIn struct {
	Action string `json:"action" jsonschema:"next, ask_person, wait or open_pull_request"`
	Note   string `json:"note,omitempty" jsonschema:"why; for ask_person, the question"`
}

func decide(ctx context.Context, tx pgx.Tx, c Caller, in decideIn) (map[string]any, error) {
	return conducted(delivery.ConductDecide(ctx, tx, c.run(), strings.TrimSpace(in.Action), in.Note))
}

type dismissIn struct {
	ID     string `json:"id" jsonschema:"the finding (fnd_…)"`
	Reason string `json:"reason" jsonschema:"why it is left as it is, shown with the finding"`
}

func dismissFinding(ctx context.Context, tx pgx.Tx, c Caller, in dismissIn) (map[string]any, error) {
	err := delivery.ConductDismiss(ctx, tx, c.run(), strings.TrimSpace(in.ID), in.Reason)
	return conducted(map[string]any{"dismissed": in.ID}, err)
}

type steerIn struct {
	Run       string `json:"run" jsonschema:"the phase Run to steer (run_…), one of this task's current attempt, still running"`
	Text      string `json:"text" jsonschema:"what to tell it: read at its next step, in the turn it is in"`
	Interrupt bool   `json:"interrupt,omitempty" jsonschema:"stop its current turn so it hears this now; only when the work it is doing is wasted"`
}

func steer(ctx context.Context, tx pgx.Tx, c Caller, in steerIn) (map[string]any, error) {
	st, lands, err := delivery.ConductSteer(ctx, tx, c.run(), strings.TrimSpace(in.Run), in.Text, in.Interrupt)
	if err != nil {
		return conducted[map[string]any](nil, err)
	}
	if c.env.kick != nil {
		c.env.kick()
	}
	out := map[string]any{"directiveId": st.ID, "run": in.Run,
		"next": "Queued for the Run. You are woken when it has read it, or if it never will."}
	if lands != "" {
		out["lands"] = lands
	}
	return out, nil
}

type updateTaskIn struct {
	Goal               *string   `json:"goal,omitempty" jsonschema:"the task's goal, whole, as agreed"`
	AcceptanceCriteria *[]string `json:"acceptanceCriteria,omitempty" jsonschema:"the task's acceptance criteria, the whole list, as agreed"`
}

func updateTask(ctx context.Context, tx pgx.Tx, c Caller, in updateTaskIn) (map[string]any, error) {
	spec := delivery.TaskSpec{Goal: in.Goal}
	if in.AcceptanceCriteria != nil {
		spec.Criteria, spec.HasCriteria = *in.AcceptanceCriteria, true
	}
	switch {
	case in.Goal != nil && utf16Len(strings.TrimFunc(*in.Goal, isJSSpace)) < GoalMin:
		return nil, refuse("a goal of at least %d characters", GoalMin)
	case in.Goal != nil && utf16Len(*in.Goal) > GoalMax || len(spec.Criteria) > 50:
		return nil, refuse("too long: a goal of at most %d characters, at most 50 criteria", GoalMax)
	case criteriaLength(spec.Criteria) > CriteriaMax:
		return nil, refuse("acceptance criteria too long: at most %d characters in all", CriteriaMax)
	}
	err := delivery.ConductUpdateTask(ctx, tx, c.run(), spec)
	return conducted(map[string]any{"updated": true, "next": "The implementer's prompt will have it."}, err)
}
