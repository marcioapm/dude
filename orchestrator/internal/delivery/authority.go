package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// A person's answer that opens the pull request, carried forward: the
// owner picked a choice whose action is open or draft on a question the
// task's conductor asked in this delivery, before the decision that opens
// it. It is standing authority for the opening while what the person was
// asked about still holds (OpenAuthorityValid); the conductor then decides
// open_pull_request citing it rather than asking the same thing again at
// the decision before the pull request. It never opens anything by itself:
// only the conductor's decide does, and only with a person's answer.

// The actions a choice of a conductor's question may carry (DecideChoice.
// Action): the pull request opened ready for review, or as a draft.
const (
	ChoiceOpen  = "open"
	ChoiceDraft = "draft"
)

// EvOpenAuthorized: a person's answer recorded as authority to open the
// pull request. Payload: {questionId, answeredBy, choice, heads,
// criteria, findings, workflowId, attempt}.
const EvOpenAuthorized = "conductor.open_authorized"

// OpenAuthority is a person's answer that authorizes the opening, and what
// it was given at (State.OpenAuthority; owned by the row, which the answer
// writes and the simplifier's commits extend).
type OpenAuthority struct {
	QuestionID string `json:"questionId"`
	// The person who answered (people.id), "" when not known.
	AnsweredBy string `json:"answeredBy,omitempty"`
	// ChoiceOpen or ChoiceDraft, and the label picked.
	Choice string `json:"choice"`
	Label  string `json:"label,omitempty"`
	// The delivery and attempt it was given in.
	WorkflowID string `json:"workflowId"`
	Attempt    int    `json:"attempt"`
	// The task's heads at the answer, and what failed or blocked then: the
	// criteria (by number) of the attempt's latest test, and the open
	// findings at the fix bar.
	Heads    map[string]string `json:"heads"`
	Criteria []int             `json:"criteria"`
	Findings []string          `json:"findings"`
	At       time.Time         `json:"at"`
	// Commits since the answer that keep it: each from a source in
	// AuthorityCommitSources, in order.
	Moves []AuthorityMove `json:"moves,omitempty"`
	// Voided: once found not to hold, or overtaken by the person being
	// asked again or answering since, it never holds again; only a new
	// answer of the person's gives a new authority.
	Voided *AuthorityVoid `json:"voided,omitempty"`
}

// AuthorityVoid is why and when an authority stopped holding, for good.
type AuthorityVoid struct {
	Why string    `json:"why"`
	At  time.Time `json:"at"`
}

// EvOpenAuthorityVoided: a person's open answer no longer authorizes the
// opening, for good. Payload: {questionId, why}. A refused decide commits
// the void and this event under its original decision locks, before returning
// the semantic refusal (InvalidOpenAuthority).
const EvOpenAuthorityVoided = "conductor.open_authority_voided"

// VoidOpenAuthorityTx latches the authority given by questionID as void
// on the task's deliveries (any, when questionID is ""), with why; an
// authority void already, or given by another answer, is left as it is.
// It returns the question of the authority it voided, "" for none.
func VoidOpenAuthorityTx(ctx context.Context, tx pgx.Tx, taskID, questionID, why string) (string, error) {
	raw, err := json.Marshal(AuthorityVoid{Why: why, At: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{openAuthority,voided}', $3::jsonb)
		WHERE task_id = $1 AND jsonb_typeof(state->'openAuthority') = 'object' AND NOT state->'openAuthority' ? 'voided'
		AND ($2 = '' OR state->'openAuthority'->>'questionId' = $2) RETURNING state->'openAuthority'->>'questionId'`, taskID, questionID, raw)
	if err != nil {
		return "", err
	}
	voided, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(voided) == 0 {
		return "", err
	}
	return voided[0], nil
}

// InvalidOpenAuthority is the sole refusal whose invalidation must commit
// before returning it. ConductDecideAsk has only read and acquired Chat and
// delivery locks when producing this outcome: no decision, gate authorization,
// or unrelated tool writes have happened. The caller commits the question-
// specific void and its actor's audit in that same transaction, not an arbitrary
// errored tool. The original question is captured under those locks.
type InvalidOpenAuthority struct {
	QuestionID string
	Why        string
}

// voidRefusal asks the tool transaction owner to commit this invalidation
// before exposing the original refusal. Ordinary Refusal.Event rolls back.
func voidRefusal(msg string, a *OpenAuthority, why string) error {
	return Refusal{Msg: msg, InvalidOpenAuthority: &InvalidOpenAuthority{QuestionID: a.QuestionID, Why: why}}
}

// AuthorityMove is a commit since the answer that does not void it: the
// heads before and after, and the Run and source that made it.
type AuthorityMove struct {
	Source string            `json:"source"`
	RunID  string            `json:"runId"`
	From   map[string]string `json:"from"`
	To     map[string]string `json:"to"`
}

// AuthorityCommitSources are the only commits since a person's answer that
// keep it: this delivery's own remaining steps that, by their contract, do
// not change behaviour. The simplifier's. Any other commit — a fix or
// implement round, a person's push, a base merge — voids it.
var AuthorityCommitSources = []string{PhaseSimplify}

// AuthorityFacts is what an authority is weighed against now.
type AuthorityFacts struct {
	WorkflowID string
	Attempt    int
	Heads      map[string]string
	Criteria   []int
	Findings   []string
}

// OpenAuthorityValid says whether a still authorizes the opening at now,
// and if not why (for the conductor): the same delivery and attempt; what
// fails or blocks now is what the person was asked about, or less; and the
// heads now are the heads at the answer moved only by commits of
// AuthorityCommitSources.
func OpenAuthorityValid(a *OpenAuthority, now AuthorityFacts) (bool, string) {
	switch {
	case a == nil:
		return false, "no answer is recorded"
	case a.Voided != nil:
		return false, a.Voided.Why
	case a.Choice != ChoiceOpen && a.Choice != ChoiceDraft:
		return false, fmt.Sprintf("the answer chose %q, not open or draft", a.Choice)
	case a.WorkflowID != now.WorkflowID:
		return false, "it was given in another delivery"
	case a.Attempt != now.Attempt:
		return false, "it was given in another attempt"
	}
	for _, c := range now.Criteria {
		if !slices.Contains(a.Criteria, c) {
			return false, fmt.Sprintf("criterion %d fails now, which the person was not asked about", c)
		}
	}
	for _, f := range now.Findings {
		if !slices.Contains(a.Findings, f) {
			return false, fmt.Sprintf("finding %s blocks now, which the person was not asked about", f)
		}
	}
	at := nonNilMap(a.Heads)
	for _, m := range a.Moves {
		if !slices.Contains(AuthorityCommitSources, m.Source) {
			continue
		}
		if maps.Equal(nonNilMap(m.From), at) {
			at = nonNilMap(m.To)
		}
	}
	if !maps.Equal(at, nonNilMap(now.Heads)) {
		return false, "there are commits since the answer that are not the simplifier's"
	}
	return true, ""
}

// authorityFactsTx reads what st's authority is weighed against: the
// failing criteria of the attempt's latest finished test (undecided fail,
// blocked or unverified verdicts), and the attempt's open findings at the
// fix bar.
func authorityFactsTx(ctx context.Context, tx pgx.Tx, wfID string, st *State) (AuthorityFacts, error) {
	f := AuthorityFacts{WorkflowID: wfID, Attempt: st.Attempt, Heads: nonNilMap(st.Heads), Criteria: []int{}, Findings: []string{}}
	rows, err := tx.Query(ctx, `SELECT v.criterion FROM acceptance_verdicts v WHERE v.run_id = (
			SELECT r.id FROM runs r WHERE r.task_id = $1 AND r.phase = 'test' AND r.status = 'completed'
			AND ($2 = 0 OR r.attempt = $2) ORDER BY r.created_at DESC, r.id DESC LIMIT 1)
		AND v.decision IS NULL AND v.status IN ('fail', 'blocked', 'unverified') ORDER BY v.criterion`, st.TaskID, st.Attempt)
	if err != nil {
		return f, err
	}
	if f.Criteria, err = pgx.CollectRows(rows, pgx.RowTo[int]); err != nil {
		return f, err
	}
	// A reviewer's finding by its id; a tester's by its title, as each
	// test raises its own for a criterion that fails again.
	rows, err = tx.Query(ctx, `SELECT CASE WHEN `+fromReview+` THEN id ELSE 'test: ' || title END FROM review_findings
		WHERE task_id = $1 AND status = 'open' AND `+thisAttempt+` AND severity::text = ANY($3) ORDER BY 1`,
		st.TaskID, st.Attempt, fixBarSeverities)
	if err != nil {
		return f, err
	}
	f.Findings, err = pgx.CollectRows(rows, pgx.RowTo[string])
	f.Criteria, f.Findings = db.NonNil(f.Criteria), db.NonNil(f.Findings)
	return f, err
}

// authorityOpens is open_pull_request's fallback once the gate's question
// gave no Open or Draft (gateErr, its refusal): the delivery's authority
// opens it — draft or not, and the answer cited — only while it holds and
// nothing newer of the person's overrides it. The person's newest answer
// wins, and nothing opens while they are being asked: a gate question
// asked since the answer leaves gateErr as it is (and the authority void);
// an open question of the conductor's to the person is waited for. An
// authority found not to hold is void for good, committed with its audit
// before releasing the decision locks (InvalidOpenAuthority). err is the
// refusal; fail a failure to read.
func authorityOpens(ctx context.Context, tx pgx.Tx, ref RunRef, wfID string, st *State, gateErr error) (draft bool, cited *CitedAnswer, err, fail error) {
	a := st.OpenAuthority
	if a == nil {
		return false, nil, gateErr, nil
	}
	var gateSince, asking bool
	if fail = tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND asked_at >= $2),
			EXISTS (SELECT 1 FROM questions q JOIN runs r ON r.id = q.run_id AND r.role = 'conductor'
				WHERE q.task_id = $1 AND q.addressee = 'person' AND q.status = 'open')`,
		st.TaskID, a.At).Scan(&gateSince, &asking); fail != nil {
		return false, nil, nil, fail
	}
	switch {
	case gateSince:
		// The gate's question, asked since (which voided it, askGate):
		// its answer, or its wait, is the person's newest word.
		return false, nil, gateErr, nil
	case a.Voided != nil:
		return false, nil, refusef(rAnswerNoLongerAuthorizes, a.QuestionID, a.Voided.Why), nil
	case asking:
		return false, nil, refuseText(rYouAlreadyHaveQuestionWaitingFor), nil
	}
	facts, fail := authorityFactsTx(ctx, tx, wfID, st)
	if fail != nil {
		return false, nil, nil, fail
	}
	if ok, why := OpenAuthorityValid(a, facts); !ok {
		return false, nil, voidRefusal(fmt.Sprintf(rAnswerNoLongerAuthorizes, a.QuestionID, why), a, why), nil
	}
	return a.Choice == ChoiceDraft, &CitedAnswer{QuestionID: a.QuestionID, AnsweredBy: a.AnsweredBy, Choice: a.Choice}, nil, nil
}

// Why an authority is void, besides OpenAuthorityValid's reasons: the
// person was asked the gate's question since, or answered the conductor
// otherwise since.
const (
	authorityAskedAgain    = "the person was asked again since"
	authorityAnsweredLater = "the person answered the conductor otherwise since"
)

// OpenAnswerTx records the owner's answer to the task's conductor's
// question as authority to open the pull request (State.OpenAuthority),
// when the choice picked carries the action open or draft (questions.
// actions), and the question was asked in the task's live delivery, by a
// conductor, before its pull request. The gate's own question (Open or
// Draft at its heads: GateAnswer) and an escalation's are not read here.
// The answer routes took the delivery and the task's row before the
// question (LockEscalationTx); taken again here, they are held already.
func OpenAnswerTx(ctx context.Context, tx pgx.Tx, org, taskID, questionID, person string) error {
	var answer, escalation string
	var gate bool
	var rawOpts, rawActions []byte
	var askedAt time.Time
	err := tx.QueryRow(ctx, `SELECT COALESCE(q.answer, ''), COALESCE(q.escalation, ''), q.pr_gate_heads IS NOT NULL,
			q.options, COALESCE(q.actions, '[]'), q.asked_at
		FROM questions q JOIN runs r ON r.id = q.run_id AND r.role = 'conductor'
		WHERE q.id = $1 AND q.task_id = $2 AND q.status = 'answered'`, questionID, taskID).
		Scan(&answer, &escalation, &gate, &rawOpts, &rawActions, &askedAt)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var options, actions []string
	_ = json.Unmarshal(rawOpts, &options)
	_ = json.Unmarshal(rawActions, &actions)
	i := choiceOf(options, answer)
	if escalation != "" || gate || i < 0 || i >= len(actions) || actions[i] != ChoiceOpen && actions[i] != ChoiceDraft {
		// The person's newest answer to the conductor does not open it:
		// an earlier one that did no longer stands.
		return voidAnsweredTx(ctx, tx, org, taskID, questionID)
	}
	_, d, err := LockEscalationTx(ctx, tx, taskID)
	if err != nil || d == nil || !d.Live() || !d.State.conducted() || len(d.State.PullRequestIDs) > 0 {
		return err
	}
	var created time.Time
	if err := tx.QueryRow(ctx, `SELECT created_at FROM workflow_runs WHERE id = $1`, d.WorkflowID).Scan(&created); err != nil {
		return err
	}
	if askedAt.Before(created) {
		// Asked in an earlier delivery: not this one's authority.
		return nil
	}
	facts, err := authorityFactsTx(ctx, tx, d.WorkflowID, &d.State)
	if err != nil {
		return err
	}
	a := OpenAuthority{QuestionID: questionID, AnsweredBy: person, Choice: actions[i], Label: options[i],
		WorkflowID: d.WorkflowID, Attempt: d.State.Attempt, Heads: facts.Heads, Criteria: facts.Criteria,
		Findings: facts.Findings, At: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{openAuthority}', $2::jsonb) WHERE id = $1`,
		d.WorkflowID, raw); err != nil {
		return err
	}
	return emitTx(ctx, tx, org, &d.State, EvOpenAuthorized, map[string]any{"questionId": questionID, "answeredBy": person,
		"choice": a.Choice, "heads": a.Heads, "criteria": a.Criteria, "findings": a.Findings, "workflowId": a.WorkflowID,
		"attempt": a.Attempt})
}

// voidAnsweredTx voids the task's authority, given by another answer than
// questionID's, once the person answered the conductor's questionID
// otherwise (newest answer wins).
func voidAnsweredTx(ctx context.Context, tx pgx.Tx, org, taskID, questionID string) error {
	var qid string
	err := tx.QueryRow(ctx, `SELECT state->'openAuthority'->>'questionId' FROM workflow_runs
		WHERE task_id = $1 AND jsonb_typeof(state->'openAuthority') = 'object' AND NOT state->'openAuthority' ? 'voided'
		AND state->'openAuthority'->>'questionId' <> $2 ORDER BY created_at DESC LIMIT 1`, taskID, questionID).Scan(&qid)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return voidTx(ctx, tx, org, taskID, qid, authorityAnsweredLater)
}

// voidTx latches the authority questionID gave as void (VoidOpenAuthorityTx)
// and records it.
func voidTx(ctx context.Context, tx pgx.Tx, org, taskID, questionID, why string) error {
	voided, err := VoidOpenAuthorityTx(ctx, tx, taskID, questionID, why)
	if err != nil || voided == "" {
		return err
	}
	var project string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&project); err != nil {
		return err
	}
	return emitTx(ctx, tx, org, &State{TaskID: taskID, ProjectID: project}, EvOpenAuthorityVoided,
		map[string]any{"questionId": voided, "why": why})
}

// recordAuthorityMoveTx records a commit of source (one of
// AuthorityCommitSources) since the person's answer, from heads from to
// heads to, on the row's authority; nothing when there is none.
func recordAuthorityMoveTx(ctx context.Context, tx pgx.Tx, wfID, source, runID string, from, to map[string]string) error {
	if !slices.Contains(AuthorityCommitSources, source) || maps.Equal(nonNilMap(from), nonNilMap(to)) {
		return nil
	}
	raw, err := json.Marshal([]AuthorityMove{{Source: source, RunID: runID, From: nonNilMap(from), To: nonNilMap(to)}})
	if err != nil {
		return err
	}
	// Once per Run: a step replayed records its commit once.
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{openAuthority,moves}',
			COALESCE(state->'openAuthority'->'moves', '[]'::jsonb) || $2::jsonb)
		WHERE id = $1 AND state ? 'openAuthority' AND jsonb_typeof(state->'openAuthority') = 'object'
		AND NOT COALESCE(state->'openAuthority'->'moves', '[]'::jsonb) @> jsonb_build_array(jsonb_build_object('runId', $3::text))`,
		wfID, raw, runID)
	return err
}

// authorityLine is the fact the decision before the pull request says of a
// person's answer that still authorizes the opening.
func authorityLine(a *OpenAuthority) string {
	return render(authorityLineTmpl, struct{ QuestionID, Label, Choice string }{a.QuestionID, a.Label, a.Choice})
}

// CitedAnswer is the person's answer an open_pull_request cites: which
// question, who answered it, and what they chose.
type CitedAnswer struct {
	QuestionID string `json:"questionId"`
	AnsweredBy string `json:"answeredBy,omitempty"`
	Choice     string `json:"choice"`
}

// DecideChoice is a choice of decide ask_person's question: its label, a
// line on it, the action it carries (ChoiceOpen, ChoiceDraft or none), and
// whether the conductor recommends it.
type DecideChoice struct {
	Label       string
	Description string
	Action      string
	Recommended bool
}

// DecideAsk is what decide ask_person asks besides its note: the choices
// (none before the pull request, whose are fixed), and the label the
// conductor recommends.
type DecideAsk struct {
	Choices   []DecideChoice
	Recommend string
}

// gateRecommended is the gate's fixed choices with the conductor's
// recommendation marked (Open when none is said), and each one's action.
func gateRecommended(recommend string) ([]Choice, []string, error) {
	rec := strings.TrimSpace(recommend)
	if rec == "" {
		rec = GateOpen
	}
	i := choiceOf(GateChoices, rec)
	if i < 0 {
		return nil, nil, refusef(rRecommendIsOneOf, rec, listOr(GateChoices))
	}
	choices := make([]Choice, len(GateChoices))
	actions := make([]string, len(GateChoices))
	for j, label := range GateChoices {
		choices[j] = Choice{Label: label, Recommended: j == i}
	}
	actions[0], actions[1] = ChoiceOpen, ChoiceDraft
	return choices, actions, nil
}

// proseOptions says a note lists the answers to choose from as prose: a
// question line with two or more " / " separating words (not a fraction
// or a path: no digit and no "/" in the word on either side), or two or
// more list items after the note's last line ending in "?" (options follow
// the question; a list before it is context) — or anywhere, in a note with
// no such line. Conservative: it reads only these two shapes, and is
// asked only of a question with no choices at a decision point whose
// answers have actions.
func proseOptions(note string) (string, bool) {
	lines := strings.Split(note, "\n")
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "?") && alternatives(l) >= 2 {
			return clip(oneLine(l), 120), true
		}
	}
	after := 0
	for i, line := range lines {
		if strings.HasSuffix(strings.TrimSpace(line), "?") {
			after = i + 1
		}
	}
	items := 0
	for _, line := range lines[after:] {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") || len(l) > 2 && l[0] >= '1' && l[0] <= '9' && (l[1] == '.' || l[1] == ')') {
			items++
		}
	}
	if items >= 2 {
		return clip(oneLine(note), 120), true
	}
	return "", false
}

// alternatives is how many " / " in line separate two words, neither with
// a digit or a "/" of its own (a fraction's, a path's).
func alternatives(line string) int {
	parts := strings.Split(line, " / ")
	n := 0
	for i := 1; i < len(parts); i++ {
		before, after := strings.Fields(parts[i-1]), strings.Fields(parts[i])
		if len(before) == 0 || len(after) == 0 {
			continue
		}
		if plainWord(before[len(before)-1]) && plainWord(after[0]) {
			n++
		}
	}
	return n
}

func plainWord(w string) bool {
	return !strings.ContainsAny(w, "0123456789/\\")
}

// decideItems is decide ask_person's question away from the gate: the
// note, and the choices given, each one's action checked. None asks for the
// person's own words — refused when the note lists answers in prose.
func decideItems(note string, in DecideAsk) ([]QuestionItem, []string, error) {
	if len(in.Choices) == 0 {
		if s, prose := proseOptions(note); prose {
			return nil, nil, refusef(rOptionsOnlyInProse, s)
		}
		if strings.TrimSpace(in.Recommend) != "" {
			return nil, nil, refusef(rRecommendIsOneOf, in.Recommend, "your choices; you gave none")
		}
		return []QuestionItem{SingleItem(clip(note, noteChars), nil)}, nil, nil
	}
	if len(in.Choices) < 2 {
		return nil, nil, refuseText(rChoicesNeedTwo)
	}
	item := QuestionItem{Question: clip(note, noteChars)}
	actions := make([]string, len(in.Choices))
	carries := false
	for i, c := range in.Choices {
		a := strings.TrimSpace(c.Action)
		switch a {
		case "", ChoiceOpen, ChoiceDraft:
		default:
			return nil, nil, refusef(rChoiceActionIsOneOf, c.Label, a)
		}
		actions[i], carries = a, carries || a != ""
		item.Choices = append(item.Choices, Choice{Label: c.Label, Description: c.Description, Recommended: c.Recommended})
	}
	if rec := strings.TrimSpace(in.Recommend); rec != "" {
		i := choiceOf(item.Labels(), rec)
		if i < 0 {
			return nil, nil, refusef(rRecommendIsOneOf, rec, listOr(item.Labels()))
		}
		item.Choices[i].Recommended = true
	}
	items, err := NormalizeItems([]QuestionItem{item})
	if err != nil {
		return nil, nil, err
	}
	if !carries {
		actions = nil
	}
	return items, actions, nil
}

// setQuestionActionsTx keeps the action each choice of question id carries
// (questions.actions), for the answer to be read by (OpenAnswerTx).
func setQuestionActionsTx(ctx context.Context, tx pgx.Tx, id string, actions []string) error {
	if len(actions) == 0 {
		return nil
	}
	raw, _ := json.Marshal(actions)
	_, err := tx.Exec(ctx, `UPDATE questions SET actions = $2::jsonb WHERE id = $1`, id, raw)
	return err
}
