package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Who may ask to change another repository: the roles that change code.
var writers = []string{"implementer"}

// How many repository requests a Run may have waiting at once.
const maxPendingRequests = 3

// The task agents' tools. Which agent has each is its capabilities'
// (internal/capabilities, docs/design/agent-tools.md).
var tools = []tool{
	define("list_machine_sizes", listMachineSizes).
		describe(machineOffer(false)),
	define("request_machine", requestMachine).describe(machineOffer(true)),
	define("list_tasks", listTasks),
	define("list_epics", listEpics),
	define("ask_person", askPerson),
	define("emit_event", emitEvent).limit(eventsPerRun),
	define("list_repositories", listRepositories),
	define("request_repository", requestRepository).limit(requestsPerRun),
	define("create_task", createTask).limit(createsPerRun),
	define("search_memory", searchMemory).before(embedQuery),
	define("get_memory", getMemory),
	define("remember", remember).limit(remembersPerRun),
	define("run_diff", runDiff),
	define("findings", findings),
	define("pull_requests", pullRequests),
	define("limits", readLimits),
	define("request_limit", requestLimit),
	define("settings", readSettings).before(prepareSettings),
	define("run_chat", runChat).withholdingResult(),
	define("start_phase", startPhase),
	define("start_agent", startAgent),
	define("verify", verify).limit(verifiesPerRun),
	define("steer", steer),
	define("restart_run", restartRun),
	define("decide", decide),
	define("dismiss_finding", dismissFinding),
	define("answer_review_question", answerReviewQuestion),
	define("decide_escalation", decideEscalation),
	define("update_task", updateTask),
	around(define("reply_on_pull_request", replyOnPullRequest).before(forgeFor), postReply, recordReply),
	define("publish", publish),
}

type startAgentIn struct {
	Profile string `json:"profile"`
	Prompt  string `json:"prompt,omitempty"`
	Note    string `json:"note,omitempty"`
	Tier    string `json:"tier,omitempty"`
	Key     string `json:"key,omitempty"`
}

type startAgentOut struct {
	RunID   string `json:"runId"`
	Profile string `json:"profile"`
	Phase   string `json:"phase"`
}

func startAgent(ctx context.Context, tx pgx.Tx, c Caller, in startAgentIn) (startAgentOut, error) {
	phase := delivery.PhaseForPromptRole(in.Profile)
	if phase == "" || in.Profile == "conductor" || in.Profile == "brainstorm" {
		return startAgentOut{}, refuseText(rChildProfileRequired)
	}
	key := in.Key
	if key == "" {
		key = "tool:" + c.CallID
	}
	parentKind := delivery.ChildParentTaskConductor
	if c.Role == delivery.RoleBrainstorm {
		parentKind = delivery.ChildParentSession
	}
	id, err := delivery.StartChildTx(ctx, tx, delivery.StartChildInput{
		Parent: delivery.ChildParent{Kind: parentKind, Run: c.run()}, Profile: in.Profile,
		Prompt: in.Prompt, Note: in.Note, Tier: in.Tier, Key: key,
		Capabilities: delivery.ChildCapabilities{PublishCode: delivery.Publishes[phase]},
	})
	if err != nil {
		if delivery.IsRefusal(err) {
			return startAgentOut{}, refuse("%s", err.Error())
		}
		return startAgentOut{}, err
	}
	return startAgentOut{RunID: id, Profile: in.Profile, Phase: phase}, nil
}

// verifiesPerRun bounds a conductor's verifications: each is a Run on a
// machine running the repository's tests.
const verifiesPerRun = 10

type verifyIn struct {
	Repo     string   `json:"repo,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Note     string   `json:"note,omitempty"`
	Key      string   `json:"key,omitempty"`
}

type verifyOut struct {
	RunID  string `json:"runId"`
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	Next   string `json:"next"`
}

// verify (capabilities.Verify) starts a verifier: start_agent's report-only
// child, publishing off, with a brief to run the checks on one commit
// (delivery.VerifyStart). Its result wakes the caller with each command's
// exit code and output.
func verify(ctx context.Context, tx pgx.Tx, c Caller, in verifyIn) (verifyOut, error) {
	key := in.Key
	if key == "" {
		key = "verify:" + c.CallID
	}
	start, err := conducted(delivery.VerifyStart(ctx, tx, c.run(), delivery.VerifyAsk{Repo: in.Repo, Commit: in.Commit,
		Commands: in.Commands, Paths: in.Paths, Note: in.Note}, key))
	if err != nil {
		return verifyOut{}, err
	}
	id, err := delivery.StartChildTx(ctx, tx, start)
	if err != nil {
		if delivery.IsRefusal(err) {
			return verifyOut{}, refuse("%s", err.Error())
		}
		return verifyOut{}, err
	}
	t := start.Capabilities.Verify
	return verifyOut{RunID: id, Repo: t.Repo, Commit: t.Commit, Next: verifyNext}, nil
}

var verifyNext = delivery.MustText("results/verify").Render(nil)

// ---- list_tasks --------------------------------------------------------------

type listTasksIn struct {
	Text      string `json:"text,omitempty"`
	Peers     bool   `json:"peers,omitempty"`
	Startable bool   `json:"startable,omitempty"`
	Archived  bool   `json:"archived,omitempty"`
	Closed    bool   `json:"closed,omitempty"`
}

type taskOut struct {
	ID     string `json:"-"`
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Why a person ended it: wont_do, duplicate, cant_reproduce, or
	// nothing_to_merge (done, nothing merged); empty while it hasn't.
	Resolution string `json:"resolution,omitempty"`
	Archived   bool   `json:"archived,omitempty"`
	Epic       string `json:"epic,omitempty"`
	// "person", or the key of the task whose agent created it.
	CreatedBy string `json:"createdBy"`
	// This task is the caller's own.
	Yours bool `json:"yours,omitempty"`
	// Its relations: blockedBy [{key, status, merged}], blocks, related,
	// startable, startsWhenUnblocked; and the same on one line.
	*delivery.TaskRelationsOut
	Line string `json:"line"`
}

type listTasksOut struct {
	Epics []string  `json:"epics"`
	Tasks []taskOut `json:"tasks"`
}

// listTasks is the project's work, or with peers the caller's peers.
func listTasks(ctx context.Context, tx pgx.Tx, c Caller, in listTasksIn) (any, error) {
	if in.Peers {
		if strings.TrimSpace(in.Text) != "" || in.Startable || in.Archived || in.Closed {
			return nil, refuseText(rPeersListsYourPeersWholeLeave)
		}
		return listPeers(ctx, tx, c)
	}
	return projectTasks(ctx, tx, c, in)
}

func projectTasks(ctx context.Context, tx pgx.Tx, c Caller, in listTasksIn) (listTasksOut, error) {
	out := listTasksOut{Epics: []string{}, Tasks: []taskOut{}}
	rows, err := tx.Query(ctx, `SELECT title FROM epics WHERE project_id = $1 AND archived_at IS NULL ORDER BY position, created_at`, c.ProjectID)
	if err != nil {
		return out, err
	}
	if out.Epics, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `
		SELECT w.id, p.key_prefix || '-' || w.number, w.title, w.status::text, COALESCE(w.resolution::text, ''), w.archived_at IS NOT NULL,
		       COALESCE(e.title, ''),
		       COALESCE((SELECT p.key_prefix || '-' || src.number FROM runs r JOIN tasks src ON src.id = r.task_id
		                 WHERE r.id = w.created_by_run_id), 'person'),
		       w.id = $2
		FROM tasks w JOIN projects p ON p.id = w.project_id LEFT JOIN epics e ON e.id = w.epic_id
		WHERE w.project_id = $1
		  AND ($3 = '' OR w.title ILIKE '%' || $3 || '%' ESCAPE '\' OR w.goal ILIKE '%' || $3 || '%' ESCAPE '\')
		  AND ($4 OR w.archived_at IS NULL)
		  AND (NOT $5 OR task_closed(w))
		ORDER BY w.number`, c.ProjectID, c.TaskID, db.LikeLiteral(strings.TrimSpace(in.Text)), in.Archived, in.Closed)
	if err != nil {
		return out, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (taskOut, error) {
		var t taskOut
		err := r.Scan(&t.ID, &t.Key, &t.Title, &t.Status, &t.Resolution, &t.Archived, &t.Epic, &t.CreatedBy, &t.Yours)
		return t, err
	})
	if err != nil {
		return out, err
	}
	ids := make([]string, len(all))
	for i, t := range all {
		ids[i] = t.ID
	}
	rels, err := delivery.RelationsOf(ctx, tx, ids)
	if err != nil {
		return out, err
	}
	for _, t := range all {
		t.TaskRelationsOut = rels[t.ID]
		if in.Startable && (t.TaskRelationsOut == nil || !t.Startable) {
			continue
		}
		head := t.Key + " · " + t.Status
		if t.Resolution != "" {
			head += " (" + strings.ToLower(delivery.ResolutionLabel[t.Resolution]) + ")"
		}
		if t.Archived {
			head += " · archived"
		}
		t.Line = strings.TrimSuffix(head+" · "+delivery.RelationLine(t.TaskRelationsOut), " · ")
		out.Tasks = append(out.Tasks, t)
	}
	return out, nil
}

// ---- create_task -----------------------------------------------------

// relationWriters may write relations: the conductor (create_task,
// update_task) and the investigator (create_task). An implementer that
// finds it needs other work says so; it never blocks its own task.
var relationWriters = []string{delivery.RoleConductor, "investigator"}

type createTaskIn struct {
	Title              string   `json:"title"`
	Goal               string   `json:"goal"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Epic               string   `json:"epic,omitempty"`
	BlockedBy          []string `json:"blockedBy,omitempty"`
	Blocks             []string `json:"blocks,omitempty"`
	Related            []string `json:"related,omitempty"`
}

type createTaskOut struct {
	Key string `json:"key"`
}

// The control plane's task limits, in UTF-16 code units as its `.length`
// counts them: a goal, and all the criteria together. GoalMin is counted on
// the goal trimmed, and must agree with TASK_GOAL_MIN in
// packages/domain/src/hierarchy.ts: a task an agent makes is one a person
// could save.
const (
	GoalMin     = 16
	GoalMax     = 65_536
	CriteriaMax = 16_384
)

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// isJSSpace is ECMAScript's WhiteSpace and LineTerminator set, what the
// control plane's String.prototype.trim() strips. It differs from
// unicode.IsSpace: it includes U+FEFF and excludes U+0085.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f',
		'\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func criteriaLength(criteria []string) int {
	n := 0
	for _, c := range criteria {
		n += utf16Len(c)
	}
	return n
}

func createTask(ctx context.Context, tx pgx.Tx, c Caller, in createTaskIn) (createTaskOut, error) {
	title := strings.TrimSpace(in.Title)
	switch {
	case title == "":
		return createTaskOut{}, refuseText(rTitleRequired)
	case utf16Len(strings.TrimFunc(in.Goal, isJSSpace)) < GoalMin:
		return createTaskOut{}, refuse(rTaskNeedsGoalAtLeastCharacters, GoalMin)
	case len(title) > 500 || utf16Len(in.Goal) > GoalMax || len(in.AcceptanceCriteria) > 50:
		return createTaskOut{}, refuse(rTooLongTitleAtMost500, GoalMax)
	case criteriaLength(in.AcceptanceCriteria) > CriteriaMax:
		return createTaskOut{}, refuse(rAcceptanceCriteriaTooLongAtMost, CriteriaMax)
	case len(in.BlockedBy)+len(in.Blocks)+len(in.Related) > 0 && !slices.Contains(relationWriters, c.Role):
		return createTaskOut{}, refuse(rCanTRelateTasksSayWhat, c.Role)
	}
	var epicID *string
	if e := strings.TrimSpace(in.Epic); e != "" {
		id, err := epicByTitle(ctx, tx, c.ProjectID, e)
		if err != nil {
			return createTaskOut{}, err
		}
		if id == "" {
			return createTaskOut{}, refuse(rNoEpicCalledInThisProject, e)
		}
		epicID = &id
	}
	var number int
	var prefix string
	// The project's next number, locked so two creates cannot take the same.
	if err := tx.QueryRow(ctx, `UPDATE projects SET next_task_number = next_task_number + 1
		WHERE id = $1 RETURNING next_task_number - 1, key_prefix`, c.ProjectID).Scan(&number, &prefix); err != nil {
		return createTaskOut{}, err
	}
	criteria, _ := json.Marshal(db.NonNil(in.AcceptanceCriteria))
	id := ids.New(ids.Task)
	// Owned by whoever drives the task the agent is working on: the person
	// who would hear about it, and decide whether it is worth doing.
	if _, err := tx.Exec(ctx, `INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, goal,
			acceptance_criteria, status, created_by_run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, 'received', $9)`,
		id, c.Org, c.ProjectID, number, epicID, title, strings.TrimSpace(in.Goal), criteria, c.RunID); err != nil {
		return createTaskOut{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		SELECT $1, p.id, $2, 0 FROM task_people tp JOIN people p ON p.id = tp.person_id
		WHERE tp.task_id = $3 AND p.removed_at IS NULL
		ORDER BY tp.position, tp.person_id LIMIT 1`, id, c.Org, c.TaskID); err != nil {
		return createTaskOut{}, err
	}
	// The same event a person creating one records, on the new task
	// (its own correlation), so its history starts with who made it.
	created := c.event("task.created", map[string]any{"title": title, "goal": strings.TrimSpace(in.Goal),
		"createdByTaskId": c.TaskID})
	created.TaskID, created.CorrelationID = id, id
	if _, err := ledger.Append(ctx, tx, created); err != nil {
		return createTaskOut{}, err
	}
	key := fmt.Sprintf("%s-%d", prefix, number)
	// Its relations, with it: one refused refuses the whole create.
	here := delivery.RelatedTask{ID: id, Key: key, ProjectID: c.ProjectID}
	for _, side := range []struct {
		name string
		keys []string
	}{{delivery.SideBlockedBy, in.BlockedBy}, {delivery.SideBlocks, in.Blocks}, {delivery.SideRelated, in.Related}} {
		for _, k := range side.keys {
			other, ok, err := delivery.TaskByOrgKey(ctx, tx, k)
			if err != nil {
				return createTaskOut{}, err
			}
			if !ok {
				return createTaskOut{}, refuse(rNoTaskInThisOrganisationNothing, side.name, k)
			}
			if err := delivery.AddRelationTx(ctx, tx, c.Org, delivery.Edge{Here: here, Other: other, Side: side.name, RunID: c.RunID}); err != nil {
				if r, ok := err.(delivery.RelationRefusal); ok {
					return createTaskOut{}, refuse(rNothingWasCreated, strings.ReplaceAll(side.name, "_", " "), k, r.Msg)
				}
				return createTaskOut{}, err
			}
		}
	}
	return createTaskOut{Key: key}, nil
}

// epicByTitle finds an epic as an agent names it: by its title, in its
// project, whatever the case. "" when there is none.
func epicByTitle(ctx context.Context, tx pgx.Tx, project, title string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM epics WHERE project_id = $1 AND lower(title) = lower($2) LIMIT 1`, project, title).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ---- list_epics -------------------------------------------------------------

type listEpicsIn struct{}

type epicOut struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Tasks       int    `json:"tasks"`
	Open        int    `json:"open"`
}

func listEpics(ctx context.Context, tx pgx.Tx, c Caller, _ listEpicsIn) ([]epicOut, error) {
	rows, err := tx.Query(ctx, `SELECT e.title, e.description,
			(SELECT count(*) FROM tasks w WHERE w.epic_id = e.id),
			(SELECT count(*) FROM tasks w WHERE w.epic_id = e.id AND w.status NOT IN ('done', 'aborted', 'failed'))
		FROM epics e WHERE e.project_id = $1 ORDER BY e.position, e.created_at`, c.ProjectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[epicOut])
}

// ---- ask_person -------------------------------------------------------------

// askItemIn is one question of ask_person's questions.
type askItemIn struct {
	Header   string        `json:"header"`
	Question string        `json:"question"`
	Choices  []askChoiceIn `json:"choices,omitempty"`
	Multiple bool          `json:"multiple,omitempty"`
}

type askChoiceIn struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type askIn struct {
	Question  string      `json:"question,omitempty"`
	Choices   []string    `json:"choices,omitempty"`
	Questions []askItemIn `json:"questions,omitempty"`
	Actions   []string    `json:"actions,omitempty"`
}

type askOut struct {
	QuestionID string `json:"questionId"`
	Next       string `json:"next"`
}

// askItems is what an ask_person call asks, either shape, normalized:
// question (and choices) is one item; questions is one to four.
func askItems(question string, choices []string, questions []askItemIn) ([]delivery.QuestionItem, error) {
	var items []delivery.QuestionItem
	switch {
	case strings.TrimSpace(question) != "" && len(questions) > 0:
		return nil, refuseText(rAskWithQuestionOrWithQuestions)
	case len(questions) > 0:
		if len(choices) > 0 {
			return nil, refuseText(rWithQuestionsEachQuestionHasIts)
		}
		for _, q := range questions {
			item := delivery.QuestionItem{Header: q.Header, Question: q.Question, Multiple: q.Multiple}
			for _, c := range q.Choices {
				item.Choices = append(item.Choices, delivery.Choice{Label: c.Label, Description: c.Description, Recommended: c.Recommended})
			}
			items = append(items, item)
		}
	case strings.TrimSpace(question) == "":
		return nil, refuse(rQuestionRequiredQuestionOrQuestions1, delivery.MaxQuestionItems)
	default:
		items = []delivery.QuestionItem{delivery.SingleItem(question, choices)}
	}
	return conducted(delivery.NormalizeItems(items))
}

// askPerson records a question for a person — or up to four, answered
// together. The agent ends its turn; the answer arrives as its next
// message — parked in between if the person takes longer than the
// project's grace period.
func askPerson(ctx context.Context, tx pgx.Tx, c Caller, in askIn) (askOut, error) {
	items, err := askItems(in.Question, in.Choices, in.Questions)
	if err != nil {
		return askOut{}, err
	}
	if len(in.Actions) > 0 && len(items) > 1 {
		return askOut{}, refuseText(rActionsGoWithOneQuestionQuestion)
	}
	open, err := delivery.HasOpenQuestion(ctx, tx, c.RunID)
	if err != nil {
		return askOut{}, err
	}
	if open {
		return askOut{}, refuseText(rYouAlreadyHaveQuestionWaitingFor)
	}
	// A conductor's question while its task's escalation waits on a person
	// is that escalation's question.
	var escalation string
	if c.Role == delivery.RoleConductor {
		if escalation, err = conducted(delivery.EscalationQuestion(ctx, tx, c.run(), items, in.Actions)); err != nil {
			return askOut{}, err
		}
	} else if len(in.Actions) > 0 {
		return askOut{}, refuseText(rActionsAreConductorForQuestionAbout)
	}
	id, err := delivery.AskItemsTx(ctx, tx, c.run(), items)
	if err != nil {
		return askOut{}, err
	}
	if escalation != "" {
		acts, _ := json.Marshal(in.Actions)
		if _, err := tx.Exec(ctx, `UPDATE questions SET escalation = $2, actions = $3::jsonb WHERE id = $1`,
			id, escalation, acts); err != nil {
			return askOut{}, err
		}
		return askOut{QuestionID: id, Next: askNext(true)}, nil
	}
	return askOut{QuestionID: id, Next: askNext(false)}, nil
}

// askNext is what ask_person tells the agent to do next; escalation, for a
// conductor's question about its task's escalation.
func askNext(escalation bool) string {
	return askResult.Render(struct{ Escalation bool }{escalation})
}

// ---- emit_event -------------------------------------------------------------

type emitIn struct {
	Type string `json:"type"`
	// Any JSON value. (Not json.RawMessage: its schema reads as an array of
	// bytes, and a model follows the schema.)
	Data any `json:"data,omitempty"`
}

type emitOut struct {
	EventType string `json:"eventType"`
}

// CustomPrefix is where custom events live: a namespace no one else writes,
// so an agent cannot pass one off as dude's own.
const CustomPrefix = "agent.custom."

var eventType = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$`)

// emitEvent records a custom event on the Run: progress, a milestone,
// something dude reacts to. Shown in the chat and the events unless dude
// handles its type.
func emitEvent(ctx context.Context, tx pgx.Tx, c Caller, in emitIn) (emitOut, error) {
	if !eventType.MatchString(in.Type) || len(in.Type) > 64 {
		return emitOut{}, refuse(rTypeLowercaseWordsJoinedByDots, in.Type)
	}
	data := in.Data
	if data == nil {
		data = map[string]any{}
	}
	// Models often send an object as the JSON text of one: take the object.
	if text, ok := data.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			if _, isObject := decoded.(map[string]any); isObject {
				data = decoded
			}
		}
	}
	if raw, _ := json.Marshal(data); len(raw) > 16<<10 {
		return emitOut{}, refuseText(rDataTooLargeAtMost16)
	}
	typ := CustomPrefix + in.Type
	_, err := ledger.Append(ctx, tx, c.event(typ, map[string]any{"type": in.Type, "data": data}))
	return emitOut{EventType: typ}, err
}

// ---- request_repository ---------------------------------------------------

type requestRepoIn struct {
	Repository string `json:"repository"`
	Write      bool   `json:"write,omitempty"`
	Reason     string `json:"reason"`
	Wait       bool   `json:"wait,omitempty"`
}

type requestRepoOut struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
	Next      string `json:"next"`
}

// requestRepository asks a person for another of the project's
// repositories. The agent carries on; approved, it is given the repository
// without losing its conversation. Only the project's own: reaching into
// another project is a person's decision to make, not an agent's to ask.
func requestRepository(ctx context.Context, tx pgx.Tx, c Caller, in requestRepoIn) (requestRepoOut, error) {
	name := strings.TrimSpace(in.Repository)
	reason := strings.TrimSpace(in.Reason)
	switch {
	case name == "" || reason == "":
		return requestRepoOut{}, refuseText(rNameRepositoryAndSayWhyYou)
	case len(reason) > 2000:
		return requestRepoOut{}, refuseText(rReasonTooLongAtMost2000)
	case in.Write && !slices.Contains(writers, c.Role):
		return requestRepoOut{}, refuse(rMayAskReadRepositoryNotChange, c.Role)
	}
	// The project's repositories, by name or owner/name (the end of its URL).
	var repo struct{ ID, Name string }
	err := tx.QueryRow(ctx, `SELECT id, name FROM repositories
		WHERE project_id = $1
		  AND (lower(name) = lower($2) OR lower(regexp_replace(url, '(\.git)?/*$', '')) LIKE '%/' || lower($3) ESCAPE '\')
		LIMIT 1`, c.ProjectID, name, db.LikeLiteral(name)).Scan(&repo.ID, &repo.Name)
	if err == pgx.ErrNoRows {
		return requestRepoOut{}, refuse(rNoRepositoryInThisProjectPerson, name)
	}
	if err != nil {
		return requestRepoOut{}, err
	}
	var clash, pending int
	if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM task_repositories wr JOIN repositories r ON r.id = wr.repository_id
			 WHERE wr.task_id = $1 AND (wr.repository_id = $2 OR r.name = $3)),
			(SELECT count(*) FROM repository_requests WHERE run_id = $4 AND status = 'pending')`,
		c.TaskID, repo.ID, repo.Name, c.RunID).Scan(&clash, &pending); err != nil {
		return requestRepoOut{}, err
	}
	switch {
	case clash > 0:
		return requestRepoOut{}, refuse(rAlreadyCheckedOutForThisWork, repo.Name)
	case pending >= maxPendingRequests:
		return requestRepoOut{}, refuse(rYouHaveRequestsWaitingForPerson, pending)
	}
	access := "read"
	if in.Write {
		access = "write"
	}
	id := ids.New(ids.RepoRequest)
	tag, err := tx.Exec(ctx, `INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, access, reason, blocking)
		VALUES ($1, $2, $3, $4, $5, $6::repository_access, $7, $8) ON CONFLICT DO NOTHING`,
		id, c.Org, c.TaskID, c.RunID, repo.ID, access, reason, in.Wait)
	if err != nil {
		return requestRepoOut{}, err
	}
	if tag.RowsAffected() == 0 {
		return requestRepoOut{}, refuse(rYouAlreadyAskedForPersonHas, repo.Name)
	}
	if _, err := ledger.Append(ctx, tx, c.event(delivery.EvRepositoryRequested,
		map[string]any{"requestId": id, "repository": repo.Name, "repositoryId": repo.ID, "access": access, "reason": reason})); err != nil {
		return requestRepoOut{}, err
	}
	return requestRepoOut{RequestID: id, Status: "pending", Next: requestRepositoryNext(repo.Name, in.Wait)}, nil
}

// requestRepositoryNext is what request_repository tells the agent to do
// next about repo; wait, when it asked to wait for the decision.
func requestRepositoryNext(repo string, wait bool) string {
	return requestRepositoryResult.Render(struct {
		Repo string
		Wait bool
	}{repo, wait})
}

// ---- list_repositories ----------------------------------------------------

type listReposIn struct{}

type repoOut struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	DefaultBranch string `json:"defaultBranch"`
	// "write" or "read" when this work has it checked out; "" when it could
	// be requested.
	Access string `json:"access,omitempty"`
	// A request for it waiting on a person.
	Requested bool `json:"requested,omitempty"`
}

// listRepositories is what an agent may work on: the project's
// repositories, which this work has, and which it could ask for.
func listRepositories(ctx context.Context, tx pgx.Tx, c Caller, _ listReposIn) ([]repoOut, error) {
	rows, err := tx.Query(ctx, `SELECT r.name, r.url, r.default_branch, COALESCE(wr.access::text, ''),
			EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = $2 AND q.repository_id = r.id AND q.status = 'pending')
		FROM repositories r
		LEFT JOIN task_repositories wr ON wr.repository_id = r.id AND wr.task_id = $3
		WHERE r.project_id = $1 ORDER BY r.name`, c.ProjectID, c.RunID, c.TaskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[repoOut])
}
